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
            OVMF_CODE=str(self.root / "code.fd"),
            OVMF_VARS_TEMPLATE=str(self.root / "vars.fd"),
            PACKER_ARGS=str(self.root / "args.json"),
        )
        for name in ("code.fd", "vars.fd"):
            (self.root / name).touch()
        self.script("uname", '#!/bin/sh\ncase "$1" in -m) echo aarch64;; -s) echo Linux;; esac\n')
        # nke_qemu_env resolves the QEMU binary by name off PATH; check-deps
        # looks for qemu-img, which the macOS CI runner lacks.
        self.script("qemu-system-aarch64", '#!/bin/sh\nif [ "$1" = -accel ]; then echo tcg; fi\n')
        self.script("qemu-img", '#!/bin/sh\nexit 0\n')
        self.script("packer", '''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
Path(os.environ['PACKER_ARGS']).write_text(json.dumps(sys.argv[1:]))
''')

    def script(self, name, text):
        p = self.root / name
        p.write_text(text)
        p.chmod(0o755)

    def make(self, target, *extra, skip_build_setup=False, directory=PACKER_DIR):
        args = [os.environ.get("MAKE", "make"), "--no-print-directory", "-C", str(directory)]
        if skip_build_setup:
            args += ["-o", "check-deps", "-o", "ssh-key", "-o", "init-fde", "-o", "init-non-fde", "-o", "prepare-installer-iso", "-o", "prepare-artifacts"]
        args += [target, "ARCH=arm64", f"PACKER={self.root / 'packer'}",
                 f"ARTIFACTS_DIR={self.root / 'artifacts'}", f"BUILD_LOGS={self.root / 'logs'}", *extra]
        return subprocess.run(args, env=self.env, text=True, capture_output=True, check=False)

    def test_macos_uses_hvf_and_preserves_explicit_overrides(self):
        self.script("uname", '#!/bin/sh\ncase "$1" in -m) echo arm64;; -s) echo Darwin;; esac\n')
        self.script("sysctl", '#!/bin/sh\necho 1\n')
        self.script("qemu-system-aarch64", '#!/bin/sh\nif [ "$1" = -accel ]; then printf "hvf\\ntcg\\n"; fi\n')
        for variant in ("fde", "non-fde"):
            for overrides, expected in (
                ([], ["accelerator=hvf", "cpu_model=host", "ssh_timeout=30m", "boot_wait=10s"]),
                (["ACCELERATOR=tcg"], ["accelerator=tcg", "cpu_model=max,pauth-impdef=on",
                                      "ssh_timeout=180m", "boot_wait=30s"]),
                (["CPU_MODEL=cortex-a72", "SSH_TIMEOUT=90m", "BOOT_WAIT=45s"],
                 ["accelerator=hvf", "cpu_model=cortex-a72", "ssh_timeout=90m", "boot_wait=45s"]),
            ):
                with self.subTest(variant=variant, overrides=overrides):
                    result = self.make(f"os-image-{variant}", *overrides, skip_build_setup=True)
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    args = json.loads(Path(self.env['PACKER_ARGS']).read_text())
                    for value in expected:
                        self.assertIn(value, args)

    def test_both_variants_pass_firmware_before_template_and_keep_current_defaults(self):
        for variant in ("fde", "non-fde"):
            with self.subTest(variant=variant):
                result = self.make(f"os-image-{variant}", skip_build_setup=True)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                args = json.loads(Path(self.env['PACKER_ARGS']).read_text())
                self.assertEqual(args[0], "build")
                self.assertEqual(args[-1], str(PACKER_DIR / f"nke-node-{variant}.pkr.hcl"))
                for value in ("accelerator=tcg", "cpu_model=max,pauth-impdef=on", "ssh_timeout=180m",
                              "root_fs_label=cloudimg-rootfs", f"artifacts_dir={PACKER_DIR / 'artifacts'}",
                              f"efi_firmware_code={self.env['OVMF_CODE']}",
                              f"efi_firmware_vars={self.env['OVMF_VARS_TEMPLATE']}"):
                    self.assertIn(value, args)

    def test_image_profiles_select_safe_defaults_and_reject_incompatible_requests(self):
        self.script("sysctl", '#!/bin/sh\necho 1\n')
        self.script("qemu-system-aarch64", '#!/bin/sh\nif [ "$1" = -accel ]; then printf "hvf\\ntcg\\n"; fi\n')
        cases = (
            ("Linux", [], "production", "tcg", None),
            ("Darwin", [], "development", "hvf", None),
            ("Darwin", ["IMAGE_PROFILE=production"], "production", "tcg", None),
            ("Darwin", ["IMAGE_PROFILE=production", "ACCELERATOR=hvf"], None, None, "64 KiB"),
            ("Darwin", ["IMAGE_PROFILE=production", "REQUIRE_ACCELERATION=1"], None, None,
             "cannot be hardware accelerated"),
            ("Linux", ["IMAGE_PROFILE=development"], "development", "tcg", None),
            ("Linux", ["IMAGE_PROFILE=invalid"], None, None, "IMAGE_PROFILE"),
        )
        for variant in ("fde", "non-fde"):
            for system, overrides, profile, accelerator, error in cases:
                with self.subTest(variant=variant, system=system, overrides=overrides):
                    self.script("uname", f'#!/bin/sh\ncase "$1" in -m) echo arm64;; -s) echo {system};; esac\n')
                    Path(self.env['PACKER_ARGS']).unlink(missing_ok=True)
                    result = self.make(f"os-image-{variant}", *overrides, skip_build_setup=True)
                    if error:
                        self.assertNotEqual(result.returncode, 0)
                        self.assertIn(error, result.stderr)
                        self.assertFalse(Path(self.env['PACKER_ARGS']).exists())
                    else:
                        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                        args = json.loads(Path(self.env['PACKER_ARGS']).read_text())
                        self.assertIn(f"image_profile={profile}", args)
                        self.assertIn(f"accelerator={accelerator}", args)
                        if accelerator == "tcg":
                            self.assertIn("cpu_model=max,pauth-impdef=on", args)

    def test_native_requirement_rejects_tcg_before_packer_build(self):
        result = self.make("os-image-non-fde", "REQUIRE_ACCELERATION=1", skip_build_setup=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("cannot be hardware accelerated", result.stderr)
        self.assertFalse(Path(self.env['PACKER_ARGS']).exists())

    def test_acceleration_preflight_rejects_mismatched_iso_arch(self):
        cases = (
            (["ISO_URL=https://example.invalid/ubuntu-26.04.1-live-server-amd64.iso"],
             "does not match ARCH=arm64"),
        )
        for extra, expected in cases:
            with self.subTest(extra=extra):
                result = self.make("check-acceleration", *extra)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(expected, result.stderr)

    def test_unavailable_hardware_accelerator_fails_before_packer(self):
        for accelerator in ("hvf", "kvm"):
            with self.subTest(accelerator=accelerator):
                result = self.make("os-image-non-fde", f"ACCELERATOR={accelerator}",
                                   skip_build_setup=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("unavailable", result.stderr)
                self.assertFalse(Path(self.env['PACKER_ARGS']).exists())

    def test_image_native_still_requires_linux_kvm(self):
        self.script("nix", "#!/bin/sh\nexit 0\n")
        self.script("uname", '#!/bin/sh\ncase "$1" in -m) echo arm64;; -s) echo Darwin;; esac\n')
        self.script("sysctl", '#!/bin/sh\necho 1\n')
        self.script("qemu-system-aarch64", '#!/bin/sh\nif [ "$1" = -accel ]; then printf "hvf\\ntcg\\n"; fi\n')
        result = self.make("image-native", skip_build_setup=True, directory=PACKER_DIR.parent)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("requested accelerator kvm is unavailable", result.stderr)
        self.assertFalse(Path(self.env['PACKER_ARGS']).exists())

    def test_macos_cross_architecture_uses_tcg(self):
        self.script("uname", '#!/bin/sh\ncase "$1" in -m) echo x86_64;; -s) echo Darwin;; esac\n')
        self.script("qemu-system-aarch64", '#!/bin/sh\nif [ "$1" = -accel ]; then printf "hvf\\ntcg\\n"; fi\n')
        result = self.make("os-image-non-fde", skip_build_setup=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        args = json.loads(Path(self.env['PACKER_ARGS']).read_text())
        self.assertIn("accelerator=tcg", args)
        self.assertIn("cpu_model=max,pauth-impdef=on", args)

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
sys.exit(1 if sys.argv[1] in ("build", "run") else 0)
''')
        # macOS defers to the entrypoint, which can bootstrap a Linux builder.
        cases = (
            ("Linux", ["build", "--no-link", f"{PACKER_DIR.parent}#builder-arm64"]),
            ("Darwin", ["run", f"{PACKER_DIR.parent}#build-packages-arm64", "--", "--check"]),
        )
        for system, expected in cases:
            with self.subTest(system=system):
                machine = "arm64" if system == "Darwin" else "aarch64"
                self.script("uname", f'#!/bin/sh\ncase "$1" in -m) echo {machine};; -s) echo {system};; esac\n')
                result = self.make("check-deps", "NKE_PACKAGE_BUILDER=nix")
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(json.loads(Path(self.env["PACKER_ARGS"]).read_text()), expected)

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


class BuildPackagesTest(unittest.TestCase):
    """Drive nix/build-packages.sh with the VM, SSH and rsync transport stubbed out."""

    def test_artifact_sync_replaces_debs_and_keeps_unrelated_output(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        root = Path(tmp.name)
        guest, out, bin_dir, state, repo = (root / n for n in
                                            ("guest", "out", "bin", "state", "repo"))
        for d in (guest / "debs", out / "debs", bin_dir, state, repo):
            d.mkdir(parents=True)
        (guest / "nke-agent.deb").write_text("agent")
        (guest / "debs" / "Packages").write_text("index")
        (out / "keep-me.txt").write_text("caller's file")
        (out / "debs" / "stale.deb").write_text("stale")
        subprocess.run(["git", "init", "-q", str(repo)], check=True)

        def script(name, text):
            p = bin_dir / name
            p.write_text(text)
            p.chmod(0o755)

        script("flock", "#!/bin/sh\nexit 0\n")
        script("timeout", '#!/bin/sh\nshift\nexec "$@"\n')
        script("launcher", '#!/bin/sh\nwhile [ ! -e "$NKE_BUILDER_STATE_DIR/down" ]; '
                           'do sleep 0.2; done\n')
        script("nke-builder-ssh", '#!/bin/sh\nset -eu\n'
                                  'test -s "$NKE_BUILDER_HOST_KEY"\n'
                                  'expected="nke-package-builder $(ssh-keygen -y -f "$NKE_BUILDER_HOST_KEY")"\n'
                                  'test "$(cat "$NKE_BUILDER_STATE_DIR/known_hosts")" = "$expected"\n'
                                  'cat >/dev/null\n'
                                  'case "$*" in *poweroff*) touch "$NKE_BUILDER_STATE_DIR/down";; '
                                  'esac\nexit 0\n')
        # Rewrite the guest's artifacts path to a local fixture; the source upload
        # has no local equivalent, so it is answered without copying anything.
        script("rsync", '''#!/usr/bin/env python3
import os, re, subprocess, sys
args, skip = [], False
for a in sys.argv[1:]:
    if skip or a == "-e":
        skip = not skip
        continue
    args.append(re.sub(r"root@127\\.0\\.0\\.1:/root/nke-builds/[^/]+/artifacts",
                       os.environ["FAKE_GUEST"], a))
sys.exit(0 if any("root@" in a for a in args)
         else subprocess.call([os.environ["REAL_RSYNC"], *args]))
''')
        driver = root / "build-packages"
        driver.write_text(f"arch=arm64\nlauncher={bin_dir / 'launcher'}\n"
                          f"guest_script={PACKER_DIR.parent / 'nix/build-guest.sh'}\n"
                          + (PACKER_DIR.parent / "nix/build-packages.sh").read_text())
        result = subprocess.run(
            ["bash", str(driver), "--source", str(repo), "--output", str(out)],
            env=os.environ | {"PATH": f"{bin_dir}:{os.environ['PATH']}",
                              "NKE_BUILDER_STATE_DIR": str(state), "VERSION": "agent/v0.0.0",
                              "NKE_BUILDER_BOOT_TIMEOUT": "10",
                              "REAL_RSYNC": shutil.which("rsync"), "FAKE_GUEST": str(guest)},
            stdin=subprocess.DEVNULL, text=True, capture_output=True,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue((out / "keep-me.txt").exists())
        self.assertTrue((out / "nke-agent.deb").exists())
        self.assertTrue((out / "debs/Packages").exists())
        self.assertFalse((out / "debs/stale.deb").exists())


class GuestUpdaterTest(unittest.TestCase):
    """The guest must reject absent or unreadable updater build outputs."""

    def test_updater_validation(self):
        for kind in ("missing", "invalid", "dynamic", "static"):
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                tools = root / "bin"
                tools.mkdir()
                # Skip package compilation and cache setup; exercise the real
                # guest script's artifact validation and shell error handling.
                for name in ("mkdir", "ln", "bash", "check-ubuntu-elf"):
                    tool = tools / name
                    tool.write_text("#!/bin/sh\nexit 0\n")
                    tool.chmod(0o755)
                readelf = tools / "readelf"
                readelf.write_text('''#!/bin/sh
test -f "$2" || exit 1
case "$(cat "$2")" in
  invalid) exit 1;;
  dynamic) echo INTERP;;
  static) echo LOAD;;
esac
''')
                readelf.chmod(0o755)
                updater = root / "agent/bin/linux_arm64/nke-agent-updater"
                updater.parent.mkdir(parents=True)
                if kind != "missing":
                    updater.write_text(kind)
                result = subprocess.run(
                    [shutil.which("bash"), str(PACKER_DIR.parent / "nix/build-guest.sh")],
                    env=os.environ | {"PATH": f"{tools}:{os.environ['PATH']}",
                                      "REPO_ROOT": str(root), "ARCH": "arm64"},
                    text=True, capture_output=True,
                )
                if kind == "static":
                    self.assertEqual(result.returncode, 0, result.stderr)
                else:
                    self.assertNotEqual(result.returncode, 0, result.stdout)
                    self.assertNotIn("PASS:", result.stdout)


if __name__ == "__main__":
    unittest.main()
