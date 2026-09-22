#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Check kernel preservation and the actual Packer profile expressions."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

PACKER_DIR = Path(__file__).resolve().parents[1]


class KernelProvisionTest(unittest.TestCase):
    def test_kernel_cleanup_and_profile_marker(self):
        cases = (
            ("development", "7.0.0-14-generic", True, False),
            ("production", "7.0.0-14-nvidia-64k", True, True),
            ("production", "7.0.0-14-generic", False, False),
            ("development", "7.0.0-14-nvidia-64k", False, False),
            ("invalid", "7.0.0-14-generic", False, False),
        )
        for profile, kernel, success, purge in cases:
            with self.subTest(profile=profile, kernel=kernel), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                scripts = {
                    "uname": '#!/bin/sh\necho "$TEST_KERNEL"\n',
                    "sudo": '''#!/bin/sh
printf '%s\\n' "$*" >> "$TEST_COMMANDS"
if [ "$1" = tee ]; then cat > "$TEST_MARKER"; fi
''',
                    "dpkg-query": '''#!/bin/sh
if [ "$#" = 2 ]; then
    printf 'linux-image-7.0.0-13-generic\\tinstall ok installed\\n'
else
    printf 'install ok installed'
fi
''',
                }
                for name, content in scripts.items():
                    path = root / name
                    path.write_text(content)
                    path.chmod(0o755)
                commands = root / "commands"
                marker = root / "marker"
                result = subprocess.run(
                    ["bash", str(PACKER_DIR / "scripts/configure-image-kernel.sh")],
                    env=os.environ | {"PATH": f"{root}:{os.environ['PATH']}",
                                      "IMAGE_PROFILE": profile, "TEST_KERNEL": kernel,
                                      "TEST_COMMANDS": str(commands), "TEST_MARKER": str(marker)},
                    text=True, capture_output=True,
                )
                self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
                calls = commands.read_text() if commands.exists() else ""
                self.assertEqual("apt-get -y purge" in calls, purge, calls)
                if success:
                    self.assertEqual(marker.read_text().strip(), profile)
                    if purge:
                        self.assertIn("linux-image-7.0.0-13-generic", calls)
                else:
                    self.assertFalse(marker.exists())


@unittest.skipUnless(shutil.which("packer"), "Packer is needed to evaluate HCL profiles")
class PackerProfileTest(unittest.TestCase):
    def test_kernel_and_artifact_directories(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            key = root / "key"
            key.touch()
            manifest = root / "material-manifest.json"
            for variant in ("fde", "non-fde"):
                for arch in ("amd64", "arm64"):
                    manifest.write_text(json.dumps({
                        "schemaVersion": "nke.dev/os-image-materials/v1",
                        "materials": [
                            {"binding": binding, "path": f"materials/{binding}",
                             "kind": kind, "architecture": arch, "digest": "sha256:" + "0" * 64}
                            for binding, kind in (("installer-iso", "disk-image"),
                                                  ("image-inputs", "archive"))
                        ],
                    }))
                    for profile in (None, "production", "development"):
                        with self.subTest(variant=variant, arch=arch, profile=profile):
                            result = subprocess.run(
                                [os.environ.get("PACKER", "packer"), "console",
                                 *(["-var", f"image_profile={profile}"] if profile else []),
                                 "-var", f"architecture={arch}",
                                 "-var", f"material_manifest={manifest}",
                                 "-var", f"output_directory={root}/output",
                                 "-var", f"ssh_private_key_file={key}",
                                 "-var", f"ssh_public_key_file={key}",
                                 "-var", f"efi_firmware_code={key}",
                                 "-var", f"efi_firmware_vars={key}",
                                 str(PACKER_DIR)],
                                input=f"jsonencode([local.kernel_package, local.variant_output_directories.{variant.replace('-', '_')}])\n",
                                text=True, capture_output=True,
                            )
                            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                            kernel, directory = json.loads(result.stdout)
                            expected = "linux-generic" if profile == "development" else (
                                "linux-nvidia-64k" if arch == "arm64" else "linux-nvidia")
                            self.assertEqual(kernel, expected)
                            suffix = "-development" if profile == "development" else ""
                            self.assertEqual(directory, str(root / f"output/{variant}{suffix}"))


if __name__ == "__main__":
    unittest.main()
