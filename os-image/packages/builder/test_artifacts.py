# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""The host package attempt must leave a current, unambiguous guest input."""
import os
from pathlib import Path
import subprocess
import shutil
import tempfile
import unittest

SCRIPT = Path(__file__).parents[2] / 'packer/scripts/prepare-packages.sh'


class ArtifactsTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.artifacts = self.root / 'artifacts'
        self.cache = self.root / 'cache'
        self.env = dict(os.environ, ARCH='arm64', ARTIFACTS_DIR=str(self.artifacts),
                        BUILDER_CACHE=str(self.cache), CONTAINER_ENGINE=str(self.root / 'missing-docker'))

    def run_prepare(self):
        return subprocess.run(['bash', str(SCRIPT)], env=self.env, capture_output=True, text=True)

    def test_missing_docker_stages_fallback_without_reusing_old_debs(self):
        old = self.artifacts / 'debs'
        old.mkdir(parents=True)
        (old / 'stale.deb').write_text('old build')
        (self.artifacts / '.interrupted-output').write_text('partial')
        result = self.run_prepare()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('guest', result.stderr)
        self.assertFalse(old.exists())
        self.assertFalse((self.artifacts / '.interrupted-output').exists())
        sources = self.artifacts / 'package-source/os-image/packages'
        self.assertFalse(list(sources.rglob('bin')))
        self.assertFalse(list(sources.rglob('dist')))
        self.assertFalse(list(sources.rglob('__pycache__')))
        self.assertFalse(list(sources.rglob('test_*.py')))
        self.assertTrue(list(self.cache.glob('*/inputs/debs/stale.deb')))
        self.assertTrue((self.artifacts / 'package-source/os-image/packages/kubeadm-distro/lock/arm64/v1.36.3.lock').is_file())
        config = (self.artifacts / 'package-build.conf').read_text()
        self.assertIn('ARCH=arm64', config)
        self.assertNotIn('NGC_API_KEY', config)

    def test_only_allowlisted_sources_cross_guest_boundary(self):
        result = self.run_prepare()
        self.assertEqual(result.returncode, 0, result.stderr)
        fixture = self.root / 'checkout'
        shutil.copytree(self.artifacts / 'package-source', fixture)
        script = fixture / 'os-image/packer/scripts/prepare-packages.sh'
        script.parent.mkdir(parents=True)
        shutil.copy2(SCRIPT, script)
        packages = fixture / 'os-image/packages'
        for relative in ['.env.local', 'kubeadm-distro/dist/large-cache',
                         'kubeadm-distro/bin/tool', 'builder/__pycache__/cached.pyc',
                         'kubeadm-distro/packaging/unrelated-file']:
            path = packages / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text('must remain on host')
        result = subprocess.run(['bash', str(script)], env=self.env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        staged = self.artifacts / 'package-source'
        for path in staged.rglob('*'):
            if path.is_file():
                self.assertNotIn(b'must remain on host', path.read_bytes())
        self.assertTrue((staged / 'os-image/packages/kubeadm-distro/packaging/postinst').is_file())

    def test_unavailable_daemon_is_a_guest_fallback(self):
        engine = self.root / 'docker'
        engine.write_text('#!/bin/sh\necho "daemon unavailable" >&2\nexit 19\n')
        engine.chmod(0o755)
        self.env['CONTAINER_ENGINE'] = str(engine)
        result = self.run_prepare()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('guest', result.stderr)
        self.assertFalse((self.artifacts / 'debs').exists())
        self.assertTrue((self.artifacts / 'package-source/make/tool-versions.mk').is_file())

    def fake_engine(self, success):
        engine = self.root / 'docker'
        engine.write_text("""#!/usr/bin/env python3
import sys
from pathlib import Path
if sys.argv[1] == 'info':
    print('linux/aarch64')
elif sys.argv[1] == 'run':
    mount = next(v for v in sys.argv if v.startswith('type=bind,') and v.endswith('dst=/out'))
    output = Path(mount.split(',src=')[1].split(',dst=')[0])
    (output / 'current.deb').write_text('current package')
    sys.exit(""" + ('0' if success else '23') + ")\n")
        engine.chmod(0o755)
        self.env['CONTAINER_ENGINE'] = str(engine)

    def test_failed_container_does_not_upload_partial_debs(self):
        self.fake_engine(False)
        result = self.run_prepare()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse((self.artifacts / 'debs').exists())
        self.assertFalse(list(self.artifacts.glob('.debs.*')))
        self.assertIn('guest', result.stderr)

    def test_successful_container_exports_current_packages_and_fallback_sources(self):
        self.fake_engine(True)
        result = self.run_prepare()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.artifacts / 'debs/current.deb').read_text(), 'current package')
        self.assertTrue((self.artifacts / 'package-source/make/tool-versions.mk').is_file())
        self.assertIn('container debs ready', result.stdout)


if __name__ == '__main__':
    unittest.main()
