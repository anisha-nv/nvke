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
        self.root = Path(self.tmp.name).resolve()
        self.packer_dir = self.root / "os-image/packer"
        self.packer_dir.mkdir(parents=True)
        shutil.copy(PACKER_DIR / "Makefile", self.packer_dir)
        shutil.copytree(PACKER_DIR / "scripts", self.packer_dir / "scripts")
        self.env = os.environ.copy()
        self.env.update(
            PATH=f"{self.root}:{self.env['PATH']}",
            OVMF_CODE=str(self.root / "code.fd"),
            OVMF_VARS_TEMPLATE=str(self.root / "vars.fd"),
            PACKER_ARGS=str(self.root / "args.json"),
        )
        for name in ("code.fd", "vars.fd"):
            (self.root / name).touch()
        # Real prepare-installer-iso.sh downloads and needs xorriso/7z;
        # prepare-installer-manifest (install stage's lean manifest) now
        # depends on it directly, so fake it here too.
        (self.packer_dir / "scripts/prepare-installer-iso.sh").write_text(
            '#!/bin/sh\nset -eu\nmkdir -p "${OUT_DIR:?}"\n'
            'printf "patched installer iso\\n" > "${OUT_DIR}/${ISO_URL##*/}.patched.iso"\n'
        )
        self.script("uname", '#!/bin/sh\ncase "$1" in -m) echo aarch64;; -s) echo Linux;; esac\n')
        # nke_qemu_env resolves the QEMU binary by name off PATH; check-deps
        # looks for qemu-img, which the macOS CI runner lacks.
        self.script("qemu-system-aarch64", '#!/bin/sh\nif [ "$1" = -accel ]; then echo tcg; fi\n')
        self.script("qemu-img", '#!/bin/sh\nexit 0\n')
        # Appends one {"stage": "install"|"provision", "argv": [...]} record
        # per invocation, since a build now issues two: install, then
        # provision. Tests that only care about the final published disk can
        # still read the last record; test_serial_console_... needs the
        # install-stage one specifically, since only autoinstall consumes it.
        self.script("packer", '''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
if sys.argv[1] != 'build':
    sys.exit(0)
values = dict(arg.split('=', 1) for arg in sys.argv[1:] if '=' in arg)
only = values.get('-only', '')
stage = 'install' if only.endswith('-install') else 'provision'
path = Path(os.environ['PACKER_ARGS'])
records = json.loads(path.read_text()) if path.exists() else []
records.append({"stage": stage, "argv": sys.argv[1:]})
path.write_text(json.dumps(records))
variant = 'non-fde' if 'non-fde' in only else 'fde'
output = Path(values['output_directory']) / variant
output.mkdir(parents=True, exist_ok=True)
if stage == 'install':
    (output / 'nke-node-installed.qcow2').touch()
    (output / 'efivars.fd').touch()
else:
    (output / 'nke-node.qcow2').touch()
''')
        # The real script hashes install-stage inputs this fixture does not
        # create; a fixed (but sha256-shaped, per H5's fail-loudly check) key
        # per variant is enough since nothing here asserts on its value.
        (self.packer_dir / "scripts/print-installed-disk-cache-key.sh").write_text(
            "#!/bin/sh\nprintf 'argv-test-key-%s' \"${VARIANT:?}\" | sha256sum | cut -d' ' -f1\n"
        )
        (self.packer_dir / "scripts/print-installed-disk-cache-key.sh").chmod(0o755)
        # skip_build_setup below bypasses prepare-image-build (via `make -o`),
        # so nothing else generates the SSH keypair or the material manifest
        # -- this suite only exercises argv construction, not real key
        # generation or material staging, so fake both directly.
        ssh_dir = self.root / ".ssh"
        ssh_dir.mkdir()
        (ssh_dir / "id_ed25519").write_text("fixture\n")
        (ssh_dir / "id_ed25519.pub").write_text("fixture\n")
        self.env["SSH_PRIVATE_KEY_FILE"] = str(ssh_dir / "id_ed25519")
        self.env["SSH_PUBLIC_KEY_FILE"] = str(ssh_dir / "id_ed25519.pub")
        inputs = self.packer_dir / ".packer-inputs"
        inputs.mkdir()
        (inputs / "material-manifest.json").write_text(json.dumps({
            "schemaVersion": "nke.dev/os-image-materials/v1",
            "materials": [
                {"binding": "installer-iso", "component": "fixture", "kind": "disk-image",
                 "architecture": "arm64", "digest": "sha256:" + "0" * 64, "path": "materials/installer-iso"},
                {"binding": "image-inputs", "component": "fixture", "kind": "archive",
                 "architecture": "arm64", "digest": "sha256:" + "1" * 64, "path": "materials/image-inputs"},
            ],
        }))

    @staticmethod
    def install_argv(records):
        # PACKER_ARGS accumulates across every make() call in a test method
        # (e.g. one per variant in a subTest loop); the most recent record is
        # the one that matters.
        return [r["argv"] for r in records if r["stage"] == "install"][-1]

    @staticmethod
    def provision_argv(records):
        return [r["argv"] for r in records if r["stage"] == "provision"][-1]

    def script(self, name, text):
        p = self.root / name
        p.write_text(text)
        p.chmod(0o755)

    def make(self, target, *extra, skip_build_setup=False):
        args = [os.environ.get("MAKE", "make"), "--no-print-directory", "-C", str(self.packer_dir)]
        if skip_build_setup:
            # prepare-installed-disk-* depends on prepare-ssh-key and
            # preflight directly (not only through prepare-image-build), so
            # all three need -o: this suite fakes a keypair's paths and a
            # fixture checkout, not real key bytes or a real git repo.
            args += ["-o", "prepare-image-build", "-o", "prepare-ssh-key", "-o", "preflight"]
            target = "_" + target
        args += [target, "ARCH=arm64", "ACCELERATOR=tcg", f"PACKER={self.root / 'packer'}",
                 f"ARTIFACTS_DIR={self.root / 'artifacts'}", f"BUILD_LOGS={self.root / 'logs'}", *extra]
        return subprocess.run(args, env=self.env, text=True, capture_output=True, check=False)

    def test_both_variants_pass_firmware_before_template_and_keep_current_defaults(self):
        for variant in ("fde", "non-fde"):
            with self.subTest(variant=variant):
                result = self.make(f"os-image-{variant}", skip_build_setup=True)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                records = json.loads(Path(self.env['PACKER_ARGS']).read_text())
                install = self.install_argv(records)
                provision = self.provision_argv(records)
                for args in (install, provision):
                    self.assertEqual(args[0], "build")
                    self.assertEqual(args[-1], str(self.packer_dir))
                    for value in (
                        "accelerator=tcg", "cpu_model=max,pauth-impdef=on", "ssh_timeout=180m",
                        f"material_manifest={self.packer_dir / '.packer-inputs/material-manifest.json'}",
                    ):
                        self.assertIn(value, args)
                # Only the install stage renders the autoinstall template, so
                # only it uses the firmware Make discovered (OVMF_CODE /
                # OVMF_VARS_TEMPLATE); the provision stage boots with the
                # installed-disk cache entry's own efivars.fd instead, with the
                # same firmware code (see EFI_VARS in the Makefile).
                self.assertIn(f"efi_firmware_code={self.env['OVMF_CODE']}", install)
                self.assertIn(f"efi_firmware_vars={self.env['OVMF_VARS_TEMPLATE']}", install)
                self.assertIn(f"efi_firmware_code={self.env['OVMF_CODE']}", provision)
                self.assertTrue(
                    any(a.startswith("efi_firmware_vars=") and a.endswith("/efivars.fd") for a in provision),
                    provision,
                )
                # Only the provision stage's provisioners consume root_fs_label.
                self.assertIn("root_fs_label=cloudimg-rootfs", provision)
                self.assertNotIn("root_fs_label=cloudimg-rootfs", install)

    def test_serial_console_defaults_to_empty_and_passes_an_override_through(self):
        # Empty means "use the architecture default", resolved in the template
        # rather than in Make. A variant each, not the same one twice: the build
        # target refuses a pre-existing output directory. Only the install
        # stage renders autoinstall-lvm.pkrtpl, so this checks its argv.
        for variant, extra, expected in (
            ("non-fde", [], "serial_console="),
            ("fde", ["SERIAL_CONSOLE=ttyS1"], "serial_console=ttyS1"),
        ):
            with self.subTest(variant=variant, extra=extra):
                result = self.make(f"os-image-{variant}", *extra, skip_build_setup=True)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                records = json.loads(Path(self.env["PACKER_ARGS"]).read_text())
                self.assertIn(expected, self.install_argv(records))

    def test_native_requirement_rejects_tcg_before_packer_build(self):
        result = self.make("os-image-non-fde", "REQUIRE_ACCELERATION=1", skip_build_setup=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("cannot be hardware accelerated", result.stderr)
        self.assertFalse(Path(self.env['PACKER_ARGS']).exists())

    def test_acceleration_preflight_rejects_hvf_and_mismatched_iso_arch(self):
        cases = (
            (["ACCELERATOR=hvf"], "not supported for image builds"),
            (["ISO_URL=https://example.invalid/ubuntu-26.04.1-live-server-amd64.iso"],
             "does not match ARCH=arm64"),
        )
        for extra, expected in cases:
            with self.subTest(extra=extra):
                result = self.make("check-acceleration", *extra)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(expected, result.stderr)

    def test_package_preflight_selects_vm_for_macos_and_mismatched_linux(self):
        self.script("nix", "#!/bin/sh\nexit 0\n")
        for system, arch in (("Darwin", "arm64"), ("Linux", "x86_64")):
            with self.subTest(system=system, arch=arch):
                self.script("uname", f'#!/bin/sh\ncase "$1" in -m) echo {arch};; -s) echo {system};; esac\n')
                result = self.make("check-deps")
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn("package builder: nix", result.stdout)

    def test_host_newer_than_the_target_release_cannot_build_packages(self):
        # The agent .deb declares a bare `Depends: libc6`, so an agent built on a
        # newer Ubuntu installs into the image and then fails to exec.
        self.script("go", "#!/bin/sh\nexit 0\n")
        self.script("nix", "#!/bin/sh\nexit 0\n")
        for host, mode, expected in (
            ("26.04", "auto", "package builder: nix"),
            ("24.04", "auto", "package builder: host"),
            ("22.04", "auto", "package builder: host"),
            ("24.04", "host", "package builder: host"),
        ):
            with self.subTest(host=host, mode=mode):
                self.env["NKE_HOST_UBUNTU_SERIES"] = host
                result = self.make(
                    "check-deps", f"NKE_PACKAGE_BUILDER={mode}", "UBUNTU_RELEASE=24.04.5"
                )
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn(expected, result.stdout)

        self.env["NKE_HOST_UBUNTU_SERIES"] = "26.04"
        result = self.make("check-deps", "NKE_PACKAGE_BUILDER=host", "UBUNTU_RELEASE=24.04.5")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("cannot build packages for an Ubuntu 24.04 image", result.stderr)
        del self.env["NKE_HOST_UBUNTU_SERIES"]

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
                    # /usr/bin and /bin supply dirname; the stubs still come first.
                    env=self.env | {"PATH": f"{self.root}:/usr/bin:/bin", "ARCH": "arm64",
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

    def test_check_deps_fails_when_the_guest_runner_cannot_be_realized(self):
        self.script("nix", '''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
Path(os.environ['PACKER_ARGS']).write_text(json.dumps(sys.argv[1:]))
sys.exit(1 if sys.argv[1] == "build" else 0)
''')
        result = self.make("check-deps", "NKE_PACKAGE_BUILDER=nix")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(json.loads(Path(self.env["PACKER_ARGS"]).read_text()),
                         ["build", "--no-link", f"{self.packer_dir.parent}#builder-arm64"])

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


class GuestUpdaterTest(unittest.TestCase):
    """The guest must reject absent or unreadable updater build outputs."""

    def test_guest_fetch_uses_guest_checkout(self):
        for source_root in (None, "/host/checkout"):
            with self.subTest(source_root=source_root), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp).resolve()
                repo = root / "guest checkout"
                scripts = repo / "os-image/packer/scripts"
                scripts.mkdir(parents=True)
                shutil.copy(PACKER_DIR / "scripts/fetch-artifacts.sh", scripts)
                files = repo / "os-image/files"
                files.mkdir()
                for name in ("nke-agent-spire.conf", "wait-spiffe-socket.sh"):
                    shutil.copy(PACKER_DIR.parent / "files" / name, files)
                updater = repo / "agent/bin/linux_arm64/nke-agent-updater"
                updater.parent.mkdir(parents=True)
                updater.touch()
                artifacts = root / "artifacts"
                artifacts.mkdir()
                tools = root / "tools"
                tools.mkdir()
                # Keep the real fetch script and shell; replace compilation and
                # guest-only cache setup, which cannot run on the test host.
                for name in ("mkdir", "ln", "go", "check-ubuntu-elf", "readelf"):
                    tool = tools / name
                    tool.write_text("#!/bin/sh\nexit 0\n")
                    tool.chmod(0o755)
                make = tools / "make"
                make.write_text('#!/bin/sh\nprintf "%s\\n" "$*" >> "$TEST_MAKE_LOG"\n')
                make.chmod(0o755)
                log = root / "make.log"
                env = os.environ | {"PATH": f"{tools}:{os.environ['PATH']}",
                                    "REPO_ROOT": str(repo), "ARCH": "arm64",
                                    "ARTIFACTS_DIR": str(artifacts), "TEST_MAKE_LOG": str(log)}
                env.pop("SOURCE_ROOT", None)
                if source_root is not None:
                    env["SOURCE_ROOT"] = source_root
                result = subprocess.run(
                    [shutil.which("bash"), str(PACKER_DIR.parent / "nix/build-guest.sh")],
                    env=env, text=True, capture_output=True, timeout=15,
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                calls = log.read_text().splitlines()
                self.assertEqual(len(calls), 5, calls)
                self.assertIn(f"-C {repo}/agent deb-arm64", calls[0])
                self.assertTrue((artifacts / "nke-agent-spire.conf").is_file())


if __name__ == "__main__":
    unittest.main()
