# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Tests for deploy-path-check.sh as a whole. Run by `make deploy-path-check`.

Each case copies controller/deploy/tenant-runtime somewhere disposable, changes
one thing, and runs the real checker against the copy. --online is driven by a
fake helm on PATH that prints a canned render, so no case needs the network.
"""
import os
import shutil
import stat
import subprocess
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CHECK = os.path.join(ROOT, "hack", "deploy-path-check.sh")
TARGET = "tenant-runtime"

FAKE_HELM = """#!/usr/bin/env bash
[[ "$1" == template ]] || exit 0
cat "${FAKE_HELM_RENDER}"
exit "${FAKE_HELM_EXIT:-0}"
"""

BUSYBOX_POD = """apiVersion: v1
kind: Pod
metadata:
  name: probe
  namespace: gpu-operator
spec:
  containers:
  - name: probe
    image: busybox:1.36
"""

# The two forms of the ClusterPolicy the real chart renders: a name under a
# repository, and a name for a disabled component with no repository at all.
CLEAN_RENDER = """kind: Deployment
spec:
  template:
    spec:
      containers:
      - name: gpu-operator
        image: nvcr.io/nvidia/gpu-operator:v26.3.2
---
kind: ClusterPolicy
spec:
  driver:
    repository: nvcr.io/nvidia
    image: driver
  vgpuManager:
    enabled: false
    image: vgpu-manager
"""


class DeployPathCheck(unittest.TestCase):
    def setUp(self):
        self.scratch = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.scratch)
        self.sbom = os.path.join(self.scratch, "sbom")
        shutil.copytree(os.path.join(ROOT, "controller", "deploy", TARGET),
                        os.path.join(self.sbom, TARGET))
        self.env = dict(os.environ)

    def write(self, relative, text):
        with open(os.path.join(self.sbom, TARGET, relative), "w") as handle:
            handle.write(text)

    def fake_helm(self, render, exit_code=0):
        bindir = os.path.join(self.scratch, "bin")
        os.makedirs(bindir, exist_ok=True)
        helm = os.path.join(bindir, "helm")
        with open(helm, "w") as handle:
            handle.write(FAKE_HELM)
        os.chmod(helm, os.stat(helm).st_mode | stat.S_IXUSR)
        rendered = os.path.join(self.scratch, "render.yaml")
        with open(rendered, "w") as handle:
            handle.write(render)
        self.env.update(PATH=bindir + os.pathsep + self.env["PATH"],
                        FAKE_HELM_RENDER=rendered, FAKE_HELM_EXIT=str(exit_code))

    def run_check(self, *args):
        result = subprocess.run(
            ["bash", CHECK, "--sbom", self.sbom, "--target", TARGET, *args],
            cwd=ROOT, env=self.env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            text=True, check=False)
        return result.returncode, result.stdout

    def assert_passes(self, *args):
        code, out = self.run_check(*args)
        self.assertEqual(code, 0, out)
        return out

    def assert_fails_with(self, needle, *args):
        code, out = self.run_check(*args)
        self.assertEqual(code, 1, out)
        self.assertIn(needle, out)
        self.assertIn("deploy-path-check: FAILED", out)

    def test_the_tenant_runtime_passes(self):
        self.assert_passes()

    def test_an_authored_bare_image_fails(self):
        self.write("namespaces/01-probe.yaml", BUSYBOX_POD)
        self.assert_fails_with("authored files: image busybox:1.36 names no registry")

    def test_an_authored_name_under_a_repository_passes(self):
        self.write("gpu-operator/validator-values.yaml",
                   "validator:\n  repository: nvcr.io/nvidia\n  image: gpu-operator\n")
        out = self.assert_passes()
        self.assertIn("authored files: image nvcr.io/nvidia", out)

    def test_quoted_name_keys_in_flow_metadata_pass(self):
        for metadata in ('{"name": "gpu-operator"}', "{'name': 'gpu-operator'}"):
            with self.subTest(metadata=metadata):
                self.write("namespaces/00-namespaces.yaml",
                           "apiVersion: v1\nkind: Namespace\nmetadata: %s\n" % metadata)
                self.assert_passes()

    def test_a_rendered_bare_image_fails(self):
        self.fake_helm(CLEAN_RENDER + "---\n" + BUSYBOX_POD)
        self.assert_fails_with("gpu-operator rendered: image busybox:1.36 names no registry", "--online")

    def test_a_clean_render_passes(self):
        self.fake_helm(CLEAN_RENDER)
        self.assert_passes("--online")

    def test_a_long_render_failure_still_reaches_the_summary(self):
        # More than a pipe buffer of error output, so a reader that stops
        # early would leave the writer to SIGPIPE.
        self.fake_helm("Error: something broke\n" * 20000, exit_code=1)
        self.assert_fails_with("gpu-operator: gpu-operator v26.3.2 did not render", "--online")


if __name__ == "__main__":
    unittest.main()
