#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Exercise bootstrap ordering and process ownership without a VM or Nix daemon."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("bootstrap-packages.sh")


class BootstrapTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.tools = self.root / "tools"
        self.tools.mkdir()
        self.events = self.root / "events"
        self.env = os.environ | {
            "PATH": f"{self.tools}:{os.environ['PATH']}",
            "TEST_ROOT": str(self.root),
            "TEST_EVENTS": str(self.events),
            "arch": "arm64",
            "guest_system": "aarch64-linux",
            "host_system": "aarch64-darwin",
            "runner_drv": "/nix/store/package-runner.drv",
            "bootstrap_drv": "/nix/store/bootstrap.drv",
            "bootstrap_program": "run-nixos-vm",
            "bootstrap_host_key": "ssh-ed25519 PUBLIC_TEST_KEY",
            "NKE_BUILDER_STATE_DIR": str(self.root / "state with spaces"),
            "NKE_BUILDER_BOOT_TIMEOUT": "5",
        }
        self.script("nix-store", '#!/bin/sh\nprintf "%s\\n" "$2"\n')
        self.script("flock", '''#!/usr/bin/env python3
import fcntl, sys
fcntl.flock(int(sys.argv[-1]), fcntl.LOCK_EX | fcntl.LOCK_NB)
''')
        self.script("nix", '''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
args = sys.argv[1:]
root = Path(os.environ['TEST_ROOT'])
with open(os.environ['TEST_EVENTS'], 'a') as f:
    f.write(json.dumps(['nix', *args]) + '\\n')
if '--dry-run' in args:
    if os.environ.get('PLAN_FAIL'):
        print('cache query failed', file=sys.stderr)
        sys.exit(9)
    if os.environ.get('CACHED'):
        print('this path will be fetched:\\n  /nix/store/cached-linux-package', file=sys.stderr)
    else:
        print('this derivation will be built:\\n  /nix/store/missing.drv', file=sys.stderr)
elif args[:2] == ['derivation', 'show']:
    print(json.dumps({'derivations': {'missing.drv': {'system': os.environ.get('MISSING_SYSTEM', 'aarch64-linux')}}}))
elif args[:2] == ['store', 'info']:
    print(json.dumps({'trusted': not bool(os.environ.get('UNTRUSTED'))}))
elif args[0] == 'build':
    bootstrap = '/nix/store/bootstrap.drv^out' in args
    if os.environ.get('BUILD_FAIL') and not bootstrap:
        sys.exit(7)
    print(root / ('bootstrap' if bootstrap else 'runner'))
else:
    sys.exit('unexpected nix command: ' + repr(args))
''')
        self.script("ssh", '''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
root = Path(os.environ['TEST_ROOT'])
if sys.argv[-1] == 'poweroff':
    (root / 'stop').touch()
    with open(os.environ['TEST_EVENTS'], 'a') as f: f.write(json.dumps(['poweroff']) + '\\n')
else:
    sys.exit(0 if (root / 'started').exists() and not os.environ.get('BOOT_FAIL') else 1)
''')
        for package in ("bootstrap", "runner"):
            (self.root / package / "bin").mkdir(parents=True)
        self.script("run-nixos-vm", '''#!/usr/bin/env python3
import json, os, signal, time
from pathlib import Path
root = Path(os.environ['TEST_ROOT'])
(root / 'pid').write_text(str(os.getpid()))
(root / 'started').touch()
with open(os.environ['TEST_EVENTS'], 'a') as f: f.write(json.dumps(['started']) + '\\n')
def stop(*args):
    (root / 'terminated').touch()
    raise SystemExit(0)
signal.signal(signal.SIGTERM, stop)
while not (root / 'stop').exists(): time.sleep(0.02)
''', self.root / "bootstrap" / "bin")
        self.script("build-packages-arm64", '''#!/usr/bin/env python3
import json, os, sys
with open(os.environ['TEST_EVENTS'], 'a') as f: f.write(json.dumps(['packages', *sys.argv[1:]]) + '\\n')
''', self.root / "runner" / "bin")

    def script(self, name, source, directory=None):
        p = (directory or self.tools) / name
        p.write_text(source)
        p.chmod(0o755)

    def run_build(self, **env):
        result = subprocess.run(
            ["bash", str(SCRIPT), "--source", "/checkout with spaces", "--output", "/artifacts"],
            env=self.env | env, capture_output=True, text=True, timeout=15,
        )
        events = [json.loads(line) for line in self.events.read_text().splitlines()] if self.events.exists() else []
        return result, events

    def test_cached_outputs_skip_bootstrap(self):
        result, events = self.run_build(CACHED="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn(['started'], events)
        self.assertEqual(events[-1], ['packages', '--source', '/checkout with spaces', '--output', '/artifacts'])

    def test_only_host_builds_skip_bootstrap(self):
        result, events = self.run_build(MISSING_SYSTEM="aarch64-darwin")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn(['started'], events)
        self.assertEqual(events[-1][0], 'packages')

    def test_missing_linux_work_bootstraps_before_realization_and_stops_before_packages(self):
        result, events = self.run_build()
        self.assertEqual(result.returncode, 0, result.stderr)
        build = next(i for i, e in enumerate(events) if e[:2] == ['nix', 'build'] and '--dry-run' not in e and '/nix/store/package-runner.drv^out' in e)
        self.assertLess(events.index(['started']), build)
        self.assertLess(build, events.index(['poweroff']))
        self.assertEqual(events[-1][0], 'packages')
        args = events[build]
        self.assertIn('--builders', args)
        self.assertIn('127.0.0.1', args[args.index('--builders') + 1])
        self.assertIn('state%20with%20spaces', args[args.index('--builders') + 1])
        self.assertFalse((self.root / 'terminated').exists())

    def test_native_linux_does_not_bootstrap(self):
        result, events = self.run_build(host_system="aarch64-linux")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn(['started'], events)

    def test_build_failure_stops_bootstrap_and_does_not_run_packages(self):
        result, events = self.run_build(BUILD_FAIL="1")
        self.assertEqual(result.returncode, 7, result.stderr)
        self.assertIn(['poweroff'], events)
        self.assertFalse(any(e[0] == 'packages' for e in events))

    def test_failed_plan_is_not_treated_as_a_cache_miss(self):
        result, events = self.run_build(PLAN_FAIL="1")
        self.assertEqual(result.returncode, 9, result.stderr)
        self.assertIn('cache query failed', result.stderr)
        self.assertNotIn(['started'], events)

    def test_untrusted_user_fails_before_bootstrap(self):
        result, events = self.run_build(UNTRUSTED="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('trusted', result.stderr)
        self.assertNotIn(['started'], events)

    def test_boot_timeout_stops_owned_vm(self):
        result, events = self.run_build(BOOT_FAIL="1", NKE_BUILDER_BOOT_TIMEOUT="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue((self.root / 'terminated').exists(), result.stderr)
        self.assertFalse(any(e[0] == 'packages' for e in events))


if __name__ == '__main__':
    unittest.main()
