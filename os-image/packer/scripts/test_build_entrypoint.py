#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Exercise Make's Packer argv and early failures without downloading or booting."""
import json
import os
import shutil
import signal
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
        self.script("uname", '#!/bin/sh\ncase "$1" in -m) echo aarch64;; -s) echo Linux;; esac\n')
        # nke_qemu_env resolves the QEMU binary by name off PATH; check-deps
        # looks for qemu-img, which the macOS CI runner lacks.
        self.script("qemu-system-aarch64", '#!/bin/sh\nif [ "$1" = -accel ]; then echo tcg; fi\n')
        self.script("qemu-img", '#!/bin/sh\nexit 0\n')
        self.script("packer", '''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
Path(os.environ['PACKER_ARGS']).write_text(json.dumps(sys.argv[1:]))
values = dict(arg.split('=', 1) for arg in sys.argv[1:] if '=' in arg)
variant = 'non-fde' if 'non-fde' in values['-only'] else 'fde'
suffix = '-development' if values.get('image_profile') == 'development' else ''
output = Path(values['output_directory']) / (variant + suffix)
output.mkdir(parents=True, exist_ok=True)
(output / 'nke-node.qcow2').touch()
''')

    def script(self, name, text):
        p = self.root / name
        p.write_text(text)
        p.chmod(0o755)

    def make(self, target, *extra, skip_build_setup=False, directory=None):
        args = [os.environ.get("MAKE", "make"), "--no-print-directory", "-C", str(directory or self.packer_dir)]
        if skip_build_setup:
            # Each invocation represents a fresh build in this test-owned project.
            for output in self.packer_dir.glob("output-*"):
                shutil.rmtree(output)
            args += ["-o", "ssh-key", "-o", "init-fde", "-o", "init-non-fde", "-o", "prepare-installer-iso", "-o", "prepare-artifacts", "-o", "prepare-packer-inputs"]
            target = "_" + target if target.startswith("os-image-") else target
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
                self.assertEqual(args[-1], str(self.packer_dir))
                for value in ("accelerator=tcg", "cpu_model=max,pauth-impdef=on", "ssh_timeout=180m",
                              "root_fs_label=cloudimg-rootfs", f"material_manifest={self.packer_dir / '.packer-inputs/material-manifest.json'}",
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
        recipe = subprocess.run(
            [os.environ.get("MAKE", "make"), "--dry-run", "-C", str(PACKER_DIR.parent),
             "image-native", "MAKE=echo"],
            env=self.env, text=True, capture_output=True, check=True,
        )
        self.assertIn("ACCELERATOR=kvm REQUIRE_ACCELERATION=1", recipe.stdout)
        result = self.make("check-acceleration", "ACCELERATOR=kvm", "REQUIRE_ACCELERATION=1")
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
            ("Linux", ["build", "--no-link", f"{self.packer_dir.parent}#builder-arm64"]),
            ("Darwin", ["run", f"{self.packer_dir.parent}#build-packages-arm64", "--", "--check"]),
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
        self.run_builder()

    @unittest.skipUnless(Path("/bin/bash").exists(), "requires system Bash")
    def test_system_bash_completes_artifact_sync(self):
        self.run_builder(shell="/bin/bash")

    def test_missing_client_public_key_is_reconstructed(self):
        self.run_builder(missing_public_key=True)

    def test_signal_after_launch_reaps_builder(self):
        self.run_builder(signal_after_launch=True)

    def run_builder(self, shell="bash", missing_public_key=False, signal_after_launch=False):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        root = Path(tmp.name)
        guest, out, bin_dir, state, repo = (root / n for n in
                                            ("guest", "out", "bin", "state", "repo"))
        for d in (guest / "debs", out / "debs", bin_dir, state, repo):
            d.mkdir(parents=True)
        real_rsync = shutil.which("rsync")
        if real_rsync is None:
            self.skipTest("rsync is required for the artifact sync test")
        if missing_public_key:
            subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "",
                            "-f", str(state / "client-key")], check=True)
            (state / "client-key.pub").unlink()
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
        script("launcher", '#!/bin/sh\ntest -s "$NKE_BUILDER_SSH_KEY" || exit 1\nwhile [ ! -e "$NKE_BUILDER_STATE_DIR/down" ]; '
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
        source = (PACKER_DIR.parent / "nix/build-packages.sh").read_text()
        if signal_after_launch:
            # Interrupt exactly between starting the launcher and storing $!.
            source = source.replace("\nvm_pid=$!\n", '\nprintf "%s" "$!" >"$NKE_BUILDER_STATE_DIR/launched-pid"\nkill -TERM "$$"\nvm_pid=$!\n')
        driver.write_text(f"arch=arm64\nlauncher={bin_dir / 'launcher'}\n"
                          f"guest_script={PACKER_DIR.parent / 'nix/build-guest.sh'}\n"
                          + source)
        result = subprocess.run(
            [shell, str(driver), "--source", str(repo), "--output", str(out)],
            env=os.environ | {"PATH": f"{bin_dir}:{os.environ['PATH']}",
                              "NKE_BUILDER_STATE_DIR": str(state), "VERSION": "agent/v0.0.0",
                              "NKE_BUILDER_BOOT_TIMEOUT": "10",
                              "REAL_RSYNC": real_rsync, "FAKE_GUEST": str(guest)},
            stdin=subprocess.DEVNULL, text=True, capture_output=True,
        )
        if signal_after_launch:
            pid = int((state / "launched-pid").read_text())
            try:
                self.assertEqual(result.returncode, 143, result.stdout + result.stderr)
                with self.assertRaises(ProcessLookupError):
                    os.kill(pid, 0)
            finally:
                try:
                    os.killpg(pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
            return
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue((state / "client-key.pub").is_file())
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
