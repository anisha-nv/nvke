# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""print-installed-disk-cache-key.sh, run for real against the project."""

import os
import shutil
from pathlib import Path
import subprocess
import tempfile
import unittest

PACKER_DIR = Path(__file__).resolve().parents[1]
SCRIPT = PACKER_DIR / "scripts" / "print-installed-disk-cache-key.sh"

BASE_ENV = {
    "ARCH": "amd64",
    "UBUNTU_RELEASE": "26.04.1",
    "UBUNTU_SNAPSHOT": "20260930T000000Z",
    "VARIANT": "fde",
    "ISO_URL": "https://releases.ubuntu.com/26.04/ubuntu-26.04.1-live-server-amd64.iso",
    "LUKS_PASSPHRASE": "changeit",
    "SERIAL_CONSOLE": "",
    "DISK_SIZE": "12G",
}


class InstalledDiskCacheKeyTest(unittest.TestCase):
    def run_key(self, script, **overrides):
        env = os.environ.copy()
        env.update(BASE_ENV | overrides)
        return subprocess.run(
            ["bash", str(script)], env=env, text=True, capture_output=True, check=False
        )

    def test_key_changes_with_the_snapshot(self):
        a = self.run_key(SCRIPT, UBUNTU_SNAPSHOT="20260930T000000Z")
        b = self.run_key(SCRIPT, UBUNTU_SNAPSHOT="20261007T000000Z")
        self.assertEqual(a.returncode, 0, a.stderr)
        self.assertEqual(b.returncode, 0, b.stderr)
        self.assertNotEqual(a.stdout, b.stdout)

    def test_key_does_not_depend_on_the_checkout_path(self):
        with tempfile.TemporaryDirectory() as tmp:
            copy = Path(tmp) / "a-differently-named-checkout"
            shutil.copytree(PACKER_DIR, copy)
            here = self.run_key(SCRIPT)
            there = self.run_key(copy / "scripts" / "print-installed-disk-cache-key.sh")
            self.assertEqual(here.returncode, 0, here.stderr)
            self.assertEqual(there.returncode, 0, there.stderr)
            self.assertEqual(here.stdout, there.stdout)

    def test_fails_loudly_on_a_missing_required_input(self):
        env = os.environ.copy()
        env.update(BASE_ENV)
        del env["ARCH"]
        result = subprocess.run(
            ["bash", str(SCRIPT)], env=env, text=True, capture_output=True, check=False
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")


if __name__ == "__main__":
    unittest.main()
