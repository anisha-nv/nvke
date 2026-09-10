# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Guest package fallback accepts only verified output and builds only when absent."""
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("guest.sh")
KUBEADM_DIR = Path(__file__).parents[1] / "kubeadm-distro"


class GuestFallbackTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.artifacts = self.root / "artifacts"
        self.source = self.artifacts / "package-source"
        (self.source / "os-image/packages/builder").mkdir(parents=True)
        (self.source / "make").mkdir()
        (self.source / "make/tool-versions.mk").write_text(
            "CRANE_VERSION ?= 0.21.9\nNFPM_VERSION ?= 2.47.0\n"
        )
        (self.artifacts / "package-build.conf").write_text(
            "ARCH=arm64\nBUILD_OVN=0\nK8S_VERSIONS=''\nCALICO_VERSIONS=''\nOVN_STREAMS=''\n"
        )
        self.marker = self.root / "marker"
        self.apt_log = self.root / "apt-log"
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self._command("dpkg", "printf '%s\\n' \"${TEST_DPKG_ARCH:-arm64}\"")
        self._command("apt-get", '''
if [[ -n "${TEST_APT_LOG:-}" ]]; then printf '%s\\n' "$*" >> "$TEST_APT_LOG"; fi
''')
        self._command("uname", "printf 'Linux\\n'")
        self._command("go", "printf 'go version go1.26.6 linux/arm64\\n'")
        for command in ("make", "gpg", "dpkg-scanpackages", "gzip", "tar"):
            self._command(command, ":")
        self._command("curl", '''
output=""
while (( $# > 0 )); do
    if [[ "$1" == -o ]]; then output="$2"; shift; fi
    shift
done
: > "$output"
''')
        sha256sum = shutil.which("sha256sum")
        self.assertIsNotNone(sha256sum)
        self._command("sha256sum", f'''
if [[ "${{1:-}}" == -c && "${{2:-}}" == - ]]; then cat >/dev/null; exit 0; fi
exec "{sha256sum}" "$@"
''')
        self.env = dict(os.environ, PATH=f"{self.bin}:{os.environ['PATH']}", TEST_MARKER=str(self.marker))

    def _command(self, name, body):
        command = self.bin / name
        command.write_text(f"#!/usr/bin/env bash\nset -eu\n{body}\n")
        command.chmod(0o755)

    def _path_without_gpg(self):
        tools = self.root / "portable-tools"
        tools.mkdir()
        # Keep mocked scripts on a modern Bash while excluding all inherited
        # commands, especially gpg on Linux CI images.
        for name in ("awk", "basename", "bash", "cat", "comm", "df", "env", "find", "grep", "mkdir",
                     "mktemp", "rm", "sort", "xargs"):
            source = shutil.which(name)
            self.assertIsNotNone(source, name)
            (tools / name).symlink_to(source)
        return f"{self.bin}:{tools}"

    def _ready_debs(self, corrupt=False):
        debs = self.artifacts / "debs"
        debs.mkdir()
        (debs / "nke-example.deb").write_bytes(b"deb")
        (debs / "Packages").write_text("Package: nke-example\n")
        (debs / "ARCH").write_text("arm64\n")
        lines = []
        for name in ("nke-example.deb", "Packages", "ARCH"):
            digest = hashlib.sha256((debs / name).read_bytes()).hexdigest()
            listed_name = f"./{name}" if name.endswith(".deb") else name
            lines.append(f"{digest}  {listed_name}")
        if corrupt:
            lines[0] = "0" * 64 + "  ./nke-example.deb"
        (debs / "SHA256SUMS").write_text("\n".join(lines) + "\n")

    def _shared_builder(self, exit_code=0, record_versions=False, record_tmpdir=False):
        builder = self.source / "os-image/packages/builder/build-packages.sh"
        version_record = ""
        if record_versions:
            version_record = '''
for name in K8S_VERSIONS CALICO_VERSIONS OVN_STREAMS; do
    if [[ -v "$name" ]]; then printf '%s=set:%s\\n' "$name" "${!name}"; else printf '%s=unset\\n' "$name"; fi
done > "$TEST_VERSION_MARKER"
'''
        tmpdir_record = ""
        if record_tmpdir:
            tmpdir_record = 'printf "%s\\n" "$TMPDIR" > "$TEST_TMPDIR_MARKER"\n'
        builder.write_text(f'''#!/usr/bin/env bash
set -eu
printf build >> "$TEST_MARKER"
{version_record}
{tmpdir_record}
out="$3"
mkdir -p "$out"
printf deb > "$out/nke-example.deb"
printf 'Package: nke-example\\n' > "$out/Packages"
printf 'arm64\\n' > "$out/ARCH"
(cd "$out" && sha256sum ./nke-example.deb Packages ARCH > SHA256SUMS)
exit {exit_code}
''')
        builder.chmod(0o755)

    def run_guest(self):
        return subprocess.run(["bash", str(SCRIPT), str(self.artifacts)], env=self.env,
                              capture_output=True, text=True)

    def test_ready_packages_skip_bootstrap_and_shared_build(self):
        self._ready_debs()
        self._shared_builder()
        result = self.run_guest()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(self.marker.exists())

    def test_absent_packages_invoke_shared_build(self):
        self._shared_builder()
        result = self.run_guest()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.marker.read_text(), "build")

    def test_blank_version_selectors_are_unset_for_make_defaults(self):
        versions = self.root / "versions"
        self.env["TEST_VERSION_MARKER"] = str(versions)
        self._shared_builder(record_versions=True)
        result = self.run_guest()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(versions.read_text(),
                         "K8S_VERSIONS=unset\nCALICO_VERSIONS=unset\nOVN_STREAMS=unset\n")

    def test_guest_build_uses_artifact_disk_for_subprocess_temporaries(self):
        tmpdir_marker = self.root / "tmpdir"
        self.env["TEST_TMPDIR_MARKER"] = str(tmpdir_marker)
        self._shared_builder(record_tmpdir=True)
        result = self.run_guest()
        self.assertEqual(result.returncode, 0, result.stderr)
        build_root = Path(tmpdir_marker.read_text().strip()).parent
        self.assertTrue(str(build_root).startswith(str(self.artifacts / "build.")))
        self.assertFalse(build_root.exists())

    def test_corrupt_packages_are_rejected_without_building(self):
        self._ready_debs(corrupt=True)
        self._shared_builder()
        result = self.run_guest()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("checksum", result.stderr.lower())
        self.assertFalse(self.marker.exists())

    def test_failed_guest_build_fails_the_bake(self):
        self._shared_builder(exit_code=34)
        result = self.run_guest()
        self.assertEqual(result.returncode, 34)
        self.assertEqual(self.marker.read_text(), "build")

    def test_guest_architecture_mismatch_stops_before_bootstrap(self):
        self.env["TEST_DPKG_ARCH"] = "amd64"
        self._shared_builder()
        result = self.run_guest()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("dpkg architecture", result.stderr)
        self.assertFalse(self.marker.exists())

    def test_kubeadm_debs_dry_run_bundles_one_version_per_build(self):
        result = subprocess.run(
            ["make", "-n", "debs", "ARCH=arm64", "OUT_DIR=/tmp/nke-test-debs",
             "K8S_VERSIONS=v1.36.3 v1.35.3", "DISCARD_PACKAGE_CACHE=1"],
            cwd=KUBEADM_DIR, capture_output=True, text=True,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.count("for v in"), 1)
        self.assertIn("./bundle.sh --version \"$v\"", result.stdout)
        self.assertIn("DISCARD_PACKAGE_CACHE", result.stdout)

    def test_apt_cleanup_includes_package_reinstalled_from_rc_state(self):
        (self.bin / "gpg").unlink()
        host_tools = self.root / "host-tools"
        host_tools.mkdir()
        host_gpg = host_tools / "gpg"
        host_gpg.write_text("#!/usr/bin/env bash\nexit 0\n")
        host_gpg.chmod(0o755)
        self.assertEqual(subprocess.run([str(host_gpg)]).returncode, 0)
        # A usable host gpg exists, but the closed guest PATH below omits it.
        self.env["PATH"] = self._path_without_gpg()
        self.env["TEST_APT_LOG"] = str(self.apt_log)
        locale_log = self.root / "locale-log"
        portable_tools = self.root / "portable-tools"
        for name in ("sort", "comm"):
            utility = portable_tools / name
            real_utility = shutil.which(name)
            self.assertIsNotNone(real_utility, name)
            utility.unlink()
            utility.write_text(f'''#!/usr/bin/env bash
printf '%s:%s\\n' '{name}' "${{LC_ALL:-unset}}" >> "$TEST_LOCALE_LOG"
exec '{real_utility}' "$@"
''')
            utility.chmod(0o755)
        self.env["TEST_LOCALE_LOG"] = str(locale_log)
        query_count = self.root / "dpkg-query-count"
        self._command("dpkg-query", f'''
count=0
[[ -f "{query_count}" ]] && count=$(<"{query_count}")
count=$((count + 1))
printf '%s' "$count" > "{query_count}"
if [[ "$count" == 1 ]]; then
    printf 'installed base\\nconfig-files gnupg\\n'
else
    printf 'installed base\\ninstalled gnupg\\n'
fi
''')
        self._shared_builder()
        result = self.run_guest()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("-y purge gnupg", self.apt_log.read_text())
        self.assertEqual(locale_log.read_text(), "sort:C\nsort:C\ncomm:C\n")


if __name__ == "__main__":
    unittest.main()
