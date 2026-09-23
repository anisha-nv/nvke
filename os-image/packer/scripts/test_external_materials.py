# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Tests for the source-owned external-runner material adapters."""

import hashlib
import io
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest


SCRIPTS = Path(__file__).resolve().parent


def write_executable(path: Path, content: str) -> None:
    path.write_text(content, encoding="utf-8")
    path.chmod(0o755)


def write_package_archive(path: Path, package_name: str) -> None:
    payload = f"package:{package_name}\n".encode()
    with tarfile.open(path, "w") as archive:
        member = tarfile.TarInfo(package_name)
        member.mode = 0o644
        member.size = len(payload)
        archive.addfile(member, io.BytesIO(payload))


class ComposeImageInputsTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.materials = self.root / "materials"
        self.source_files = self.root / "source-files"
        self.tools = self.root / "tools"
        for directory in (self.materials, self.source_files, self.tools):
            directory.mkdir()
        (self.materials / "agent").write_bytes(b"agent-deb\n")
        for binding in ("calico", "kubeadm-distro", "ovn", "spire-agent"):
            write_package_archive(self.materials / binding, f"{binding}.deb")
        (self.source_files / "nke-agent-spire.conf").write_text("override\n", encoding="utf-8")
        (self.source_files / "wait-spiffe-socket.sh").write_text("#!/bin/sh\n", encoding="utf-8")
        write_executable(
            self.tools / "dpkg-scanpackages",
            "#!/bin/sh\nprintf 'Package: fixture\\n'\n",
        )

    def tearDown(self):
        self.temporary.cleanup()

    def run_compose(self, output: Path):
        environment = os.environ.copy()
        environment["PATH"] = f"{self.tools}:{environment['PATH']}"
        return subprocess.run(
            [
                sys.executable,
                str(SCRIPTS / "compose-image-inputs.py"),
                "--materials-dir",
                str(self.materials),
                "--source-files-dir",
                str(self.source_files),
                "--output",
                str(output),
                "--source-date-epoch",
                "1787932620",
            ],
            env=environment,
            check=False,
            capture_output=True,
            text=True,
        )

    def test_composes_deterministic_archive(self):
        first = self.root / "first.tar"
        second = self.root / "second.tar"
        first_result = self.run_compose(first)
        second_result = self.run_compose(second)
        self.assertEqual(first_result.returncode, 0, first_result.stderr)
        self.assertEqual(second_result.returncode, 0, second_result.stderr)
        self.assertEqual(
            hashlib.sha256(first.read_bytes()).digest(),
            hashlib.sha256(second.read_bytes()).digest(),
        )
        with tarfile.open(first) as archive:
            names = {member.name.removeprefix("./") for member in archive.getmembers()}
        self.assertEqual(
            names,
            {
                ".",
                "debs",
                "debs/Packages",
                "debs/calico.deb",
                "debs/kubeadm-distro.deb",
                "debs/ovn.deb",
                "debs/spire-agent.deb",
                "nke-agent-spire.conf",
                "nke-agent.deb",
                "wait-spiffe-socket.sh",
            },
        )

    def test_rejects_duplicate_package_names(self):
        write_package_archive(self.materials / "ovn", "calico.deb")
        result = self.run_compose(self.root / "duplicate.tar")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("duplicate Debian package filename", result.stderr)

    def test_rejects_archive_links(self):
        with tarfile.open(self.materials / "ovn", "w") as archive:
            member = tarfile.TarInfo("ovn.deb")
            member.type = tarfile.SYMTYPE
            member.linkname = "/etc/passwd"
            archive.addfile(member)
        result = self.run_compose(self.root / "unsafe.tar")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("unsafe or unexpected member", result.stderr)


class PatchInstallerISOTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.tools = self.root / "tools"
        self.tools.mkdir()
        self.source = self.root / "source.iso"
        self.source.write_bytes(b"source-iso\n")
        write_executable(
            self.tools / "7z",
            """#!/bin/sh
set -eu
for argument in "$@"; do
  case "$argument" in -o*) output=${argument#-o};; esac
done
mkdir -p "$output"
printf 'set timeout=30\nmenuentry fixture {}\n' >"$output/grub.cfg"
""",
        )
        write_executable(
            self.tools / "xorriso",
            """#!/bin/sh
set -eu
while [ "$#" -gt 0 ]; do
  case "$1" in
    -indev) input=$2; shift 2;;
    -outdev) output=$2; shift 2;;
    *) shift;;
  esac
done
cp "$input" "$output"
printf 'patched\n' >>"$output"
""",
        )

    def tearDown(self):
        self.temporary.cleanup()

    def run_patch(self, source: Path, output: Path):
        environment = os.environ.copy()
        environment["PATH"] = f"{self.tools}:{environment['PATH']}"
        return subprocess.run(
            [str(SCRIPTS / "patch-installer-iso.sh"), str(source), str(output)],
            env=environment,
            check=False,
            capture_output=True,
            text=True,
        )

    def test_patches_caller_supplied_iso(self):
        output = self.root / "output" / "installer.iso"
        result = self.run_patch(self.source, output)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(output.read_bytes(), b"source-iso\npatched\n")

    def test_rejects_source_symlink(self):
        source_link = self.root / "source-link.iso"
        source_link.symlink_to(self.source)
        result = self.run_patch(source_link, self.root / "installer.iso")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("source is not one regular file", result.stderr)


if __name__ == "__main__":
    unittest.main()
