# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Exercise the host/container boundary without a daemon or network."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).with_name('build.sh')


class BuilderTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.engine = self.root / 'docker'
        self.log = self.root / 'calls'
        self.engine.write_text('''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
with open(os.environ['CALL_LOG'], 'a') as f:
    f.write(json.dumps(sys.argv[1:]) + '\\n')
if sys.argv[1] == 'info':
    print(os.environ['ENGINE_PLATFORM'])
elif sys.argv[1] == 'run':
    sys.exit(int(os.environ.get('BUILD_EXIT', '23')))
''')
        self.engine.chmod(0o755)
        self.env = dict(os.environ, CONTAINER_ENGINE=str(self.engine), ARCH='arm64',
                        ENGINE_PLATFORM='linux/aarch64', CALL_LOG=str(self.log),
                        ARTIFACTS_DIR=str(self.root / 'artifacts'),
                        BUILDER_CACHE=str(self.root / 'cache'))

    def run_builder(self):
        return subprocess.run(['bash', str(SCRIPT), 'debs'], env=self.env,
                              capture_output=True, text=True)

    def test_rejects_cross_arch_engine_before_build(self):
        self.env['ENGINE_PLATFORM'] = 'linux/x86_64'
        result = self.run_builder()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('native arm64', result.stderr)
        self.assertNotIn('"run"', self.log.read_text())

    def test_failed_build_preserves_previous_artifacts(self):
        old = Path(self.env['ARTIFACTS_DIR']) / 'debs'
        old.mkdir(parents=True)
        (old / 'existing.deb').write_text('previous successful build')
        result = self.run_builder()
        self.assertEqual(result.returncode, 23, result.stderr)
        self.assertEqual((old / 'existing.deb').read_text(), 'previous successful build')
        calls = self.log.read_text()
        self.assertIn('readonly', calls)
        self.assertIn('type=volume,src=nke-package-builder-arm64,dst=/cache', calls)
        self.assertNotIn('K8S_VERSIONS=', calls)
        self.assertNotIn('CALICO_VERSIONS=', calls)
        self.assertNotIn('--privileged', calls)
        self.assertNotIn('/var/run/docker.sock', calls)

    def test_replacement_keeps_backup_outside_packer_upload(self):
        self.env['BUILD_EXIT'] = '0'
        artifacts = Path(self.env['ARTIFACTS_DIR'])
        old = artifacts / 'debs'
        old.mkdir(parents=True)
        (old / 'existing.deb').write_text('previous successful build')
        result = self.run_builder()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(list(artifacts.iterdir()), [old])
        backups = list(Path(self.env['BUILDER_CACHE']).glob('debs-previous.*/debs/existing.deb'))
        self.assertEqual(len(backups), 1)
        self.assertEqual(backups[0].read_text(), 'previous successful build')


if __name__ == '__main__':
    unittest.main()
