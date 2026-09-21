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
    def test_kernel_and_artifact_names(self):
        for variant in ("fde", "non-fde"):
            for arch in ("amd64", "arm64"):
                for profile in (None, "production", "development"):
                    with self.subTest(variant=variant, arch=arch, profile=profile):
                        result = subprocess.run(
                            ["packer", "console",
                             *(["-var", f"image_profile={profile}"] if profile else []),
                             "-var", f"iso_url=https://example.com/ubuntu-26.04.1-live-server-{arch}.iso",
                             str(PACKER_DIR / f"nke-node-{variant}.pkr.hcl")],
                            input="jsonencode([local.kernel_package, local.image_name, local.output_directory])\n",
                            text=True, capture_output=True,
                        )
                        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                        kernel, name, directory = json.loads(result.stdout)
                        expected = "linux-generic" if profile == "development" else (
                            "linux-nvidia-64k" if arch == "arm64" else "linux-nvidia")
                        self.assertEqual(kernel, expected)
                        suffix = "-development" if profile == "development" else ""
                        self.assertEqual(name, f"nke-node-{variant}{suffix}-ubuntu-26.04.1-{arch}.qcow2")
                        self.assertEqual(directory, str(PACKER_DIR / f"output-{variant}{suffix}"))


if __name__ == "__main__":
    unittest.main()
