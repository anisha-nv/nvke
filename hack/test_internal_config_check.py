# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Exercise internal identifier detection against isolated repository fixtures."""

from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("internal-config-check.sh")


class InternalConfigCheckTest(unittest.TestCase):
    def scan(self, content):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            subprocess.run(
                ["git", "init", "--quiet", str(root)], check=True, capture_output=True
            )
            (root / "hack").mkdir()
            script = root / "hack" / SCRIPT.name
            shutil.copyfile(SCRIPT, script)
            (root / "fixture.txt").write_text(content, encoding="utf-8")
            return subprocess.run(
                ["bash", str(script)], capture_output=True, text=True, check=False
            )

    def test_checksum_is_not_an_org_id(self):
        versions = SCRIPT.parents[1] / "make" / "tool-versions.mk"
        checksum = next(
            line for line in versions.read_text().splitlines()
            if line.startswith("S5CMD_SHA256_AMD64")
        )
        result = self.scan(checksum)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_standalone_org_ids_are_rejected_without_printing_values(self):
        org_id = "12345678" + "90123456"
        for content in (org_id, f'org: "{org_id}"', f"org={org_id}, next"):
            with self.subTest(content=content):
                result = self.scan(content)
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                self.assertIn("sixteen-digit NGC or Forge org id", result.stdout)
                self.assertNotIn(org_id, result.stdout + result.stderr)

    def test_non_org_tokens_and_placeholder_pass(self):
        org_id = "12345678" + "90123456"
        for content in (f"a{org_id}b", f"_{org_id}_", f"9{org_id}9", "0" * 16):
            with self.subTest(content=content):
                result = self.scan(content)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
