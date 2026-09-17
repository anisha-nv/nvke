#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Exercise Make's Packer argv and early failures without downloading or booting."""
import json
import os
import shutil
from pathlib import Path
import subprocess
import tempfile
import unittest

PACKER_DIR = Path(__file__).resolve().parents[1]


class BuildEntrypointTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.env = os.environ.copy()
        self.env.update(
            PATH=f"{self.root}:{self.env['PATH']}",
            QEMU_BIN=str(self.root / "qemu"),
            OVMF_CODE=str(self.root / "code.fd"),
            OVMF_VARS_TEMPLATE=str(self.root / "vars.fd"),
            PACKER_ARGS=str(self.root / "args.json"),
        )
        for name in ("code.fd", "vars.fd"):
            (self.root / name).touch()
        self.script("uname", '#!/bin/sh\ncase "$1" in -m) echo aarch64;; -s) echo Linux;; esac\n')
        self.script("qemu", '#!/bin/sh\nif [ "$1" = -accel ]; then echo tcg; fi\n')
        self.script("packer", '''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
Path(os.environ['PACKER_ARGS']).write_text(json.dumps(sys.argv[1:]))
''')

    def script(self, name, text):
        p = self.root / name
        p.write_text(text)
        p.chmod(0o755)

    def make(self, target, *extra, skip_build_setup=False):
        args = [os.environ.get("MAKE", "make"), "--no-print-directory", "-C", str(PACKER_DIR)]
        if skip_build_setup:
            args += ["-o", "ssh-key", "-o", "init-fde", "-o", "init-non-fde", "-o", "prepare-installer-iso"]
        args += [target, "ARCH=arm64", "ACCELERATOR=tcg", f"PACKER={self.root / 'packer'}",
                 f"ARTIFACTS_DIR={self.root / 'artifacts'}", f"BUILD_LOGS={self.root / 'logs'}", *extra]
        return subprocess.run(args, env=self.env, text=True, capture_output=True, check=False)

    def test_both_variants_pass_firmware_before_template_and_keep_current_defaults(self):
        for variant in ("fde", "non-fde"):
            with self.subTest(variant=variant):
                result = self.make(f"os-image-{variant}", skip_build_setup=True)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                args = json.loads(Path(self.env['PACKER_ARGS']).read_text())
                self.assertEqual(args[0], "build")
                self.assertEqual(args[-1], str(PACKER_DIR / f"nke-node-{variant}.pkr.hcl"))
                for value in ("accelerator=tcg", "cpu_model=max,pauth-impdef=on", "ssh_timeout=180m",
                              "root_fs_label=cloudimg-rootfs", f"artifacts_dir={self.root / 'artifacts'}",
                              f"efi_firmware_code={self.env['OVMF_CODE']}",
                              f"efi_firmware_vars={self.env['OVMF_VARS_TEMPLATE']}"):
                    self.assertIn(value, args)

    def test_native_requirement_rejects_tcg_before_packer_build(self):
        result = self.make("os-image-non-fde", "REQUIRE_ACCELERATION=1", skip_build_setup=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("cannot be hardware accelerated", result.stderr)
        self.assertFalse(Path(self.env['PACKER_ARGS']).exists())

    def test_package_preflight_selects_vm_for_macos_and_mismatched_linux(self):
        self.script("nix", "#!/bin/sh\nexit 0\n")
        for system, arch in (("Darwin", "arm64"), ("Linux", "x86_64")):
            with self.subTest(system=system, arch=arch):
                self.script("uname", f'#!/bin/sh\ncase "$1" in -m) echo {arch};; -s) echo {system};; esac\n')
                result = self.make("check-deps")
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn("package builder: nix", result.stdout)

    def test_explicit_host_builder_rejects_mismatched_linux(self):
        self.script("uname", '#!/bin/sh\ncase "$1" in -m) echo x86_64;; -s) echo Linux;; esac\n')
        result = self.make("check-deps", "NKE_PACKAGE_BUILDER=host")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("matching Linux host", result.stderr)

    def test_missing_nix_explains_setup_but_native_build_does_not_require_it(self):
        bash = shutil.which("bash")
        self.script("go", "#!/bin/sh\nexit 0\n")
        for mode, expected_status in (("nix", 1), ("auto", 0)):
            with self.subTest(mode=mode):
                result = subprocess.run(
                    [bash, str(PACKER_DIR / "scripts/package-builder.sh"), "--check"],
                    env=self.env | {"PATH": str(self.root), "ARCH": "arm64",
                                    "NKE_PACKAGE_BUILDER": mode},
                    text=True, capture_output=True,
                )
                self.assertEqual(result.returncode, expected_status, result.stderr)
                if mode == "nix":
                    self.assertIn("nix is required", result.stderr)
                    self.assertIn("Install Nix", result.stderr)
                    self.assertIn("os-image/nix/README.md", result.stderr)
                else:
                    self.assertIn("package builder: host", result.stdout)

    def test_unknown_package_builder_fails_before_packer(self):
        result = self.make("check-deps", "NKE_PACKAGE_BUILDER=invalid")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("NKE_PACKAGE_BUILDER", result.stderr)

    def test_vm_adapter_invokes_nix_app_with_checkout_and_output(self):
        self.script("nix", '''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
Path(os.environ['PACKER_ARGS']).write_text(json.dumps(sys.argv[1:]))
''')
        artifacts = self.root / "artifacts with spaces"
        result = subprocess.run(
            ["bash", str(PACKER_DIR / "scripts/package-builder.sh")],
            env=self.env | {"ARCH": "arm64", "NKE_PACKAGE_BUILDER": "nix",
                            "ARTIFACTS_DIR": str(artifacts)},
            text=True, capture_output=True,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(Path(self.env["PACKER_ARGS"]).read_text()), [
            "run", f"{PACKER_DIR.parent}#build-packages-arm64", "--",
            "--source", str(PACKER_DIR.parent.parent), "--output", str(artifacts),
        ])


if __name__ == "__main__":
    unittest.main()
