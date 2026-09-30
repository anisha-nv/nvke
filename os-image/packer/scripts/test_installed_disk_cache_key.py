# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""print-installed-disk-cache-key.sh: stable unless an actual input changes."""

import os
from pathlib import Path
import subprocess
import tempfile
import unittest

PACKER_DIR = Path(__file__).resolve().parents[1]
SCRIPT = PACKER_DIR / "scripts" / "print-installed-disk-cache-key.sh"


class InstalledDiskCacheKeyTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        # A self-contained copy of the real install-stage inputs, so tests can
        # mutate one file at a time without touching the actual project.
        self.packer_root = self.root / "packer"
        self.packer_root.mkdir()
        for name in (
            "locals.pkr.hcl",
            "variables.pkr.hcl",
            "versions.pkr.hcl",
            "Makefile",
        ):
            (self.packer_root / name).write_text(
                (PACKER_DIR / name).read_text(encoding="utf-8"), encoding="utf-8"
            )
        for variant in ("fde", "non-fde"):
            (self.packer_root / f"nke-node-{variant}-install.pkr.hcl").write_text(
                (PACKER_DIR / f"nke-node-{variant}-install.pkr.hcl").read_text(
                    encoding="utf-8"
                ),
                encoding="utf-8",
            )
        cidata = self.packer_root / "cidata"
        cidata.mkdir()
        for name in ("autoinstall-lvm.pkrtpl", "user-data.pkrtpl"):
            (cidata / name).write_text(
                (PACKER_DIR / "cidata" / name).read_text(encoding="utf-8"),
                encoding="utf-8",
            )

    def run_key(self, *, variant="fde", arch="amd64", ubuntu_release="26.04.1",
                ubuntu_snapshot="20260930T000000Z", iso_sha256="a" * 64):
        env = os.environ.copy()
        env.update(
            ARCH=arch,
            UBUNTU_RELEASE=ubuntu_release,
            UBUNTU_SNAPSHOT=ubuntu_snapshot,
            VARIANT=variant,
            ISO_URL=f"https://example.invalid/ubuntu-{ubuntu_release}-live-server-{arch}.iso",
            ISO_CHECKSUM="file:https://example.invalid/SHA256SUMS",
            PACKER_ROOT=str(self.packer_root),
            # Skips the network round-trip to resolve the ISO's own digest.
            ISO_SHA256=iso_sha256,
        )
        result = subprocess.run(
            ["bash", str(SCRIPT)], env=env, text=True, capture_output=True, check=False
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        key = result.stdout.strip()
        self.assertRegex(key, r"^[0-9a-f]{64}$")
        return key

    def test_key_is_stable_for_identical_inputs(self):
        self.assertEqual(self.run_key(), self.run_key())

    def test_key_changes_with_the_ubuntu_snapshot(self):
        self.assertNotEqual(
            self.run_key(ubuntu_snapshot="20260930T000000Z"),
            self.run_key(ubuntu_snapshot="20261007T000000Z"),
        )

    def test_key_changes_with_the_iso_digest(self):
        self.assertNotEqual(
            self.run_key(iso_sha256="a" * 64),
            self.run_key(iso_sha256="b" * 64),
        )

    def test_key_changes_with_arch_release_and_variant(self):
        base = self.run_key()
        self.assertNotEqual(base, self.run_key(arch="arm64"))
        self.assertNotEqual(base, self.run_key(ubuntu_release="24.04.5"))
        self.assertNotEqual(base, self.run_key(variant="non-fde"))

    def test_key_changes_when_cidata_changes(self):
        before = self.run_key()
        template = self.packer_root / "cidata" / "autoinstall-lvm.pkrtpl"
        template.write_text(
            template.read_text(encoding="utf-8") + "\n# perturbed\n", encoding="utf-8"
        )
        self.assertNotEqual(before, self.run_key())

    def test_key_changes_when_the_install_stage_template_changes(self):
        before = self.run_key()
        template = self.packer_root / "nke-node-fde-install.pkr.hcl"
        template.write_text(
            template.read_text(encoding="utf-8") + "\n# perturbed\n", encoding="utf-8"
        )
        self.assertNotEqual(before, self.run_key())

    def test_key_is_unaffected_by_unrelated_provision_only_content(self):
        # The provision-stage templates are not install-stage inputs; a
        # change there must not invalidate a perfectly good cached disk.
        before = self.run_key()
        provision = self.packer_root / "nke-node-fde-provision.pkr.hcl"
        provision.write_text("# not part of the install stage\n", encoding="utf-8")
        self.assertEqual(before, self.run_key())


if __name__ == "__main__":
    unittest.main()
