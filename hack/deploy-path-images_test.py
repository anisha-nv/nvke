# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Tests for deploy-path-images.py. Run by `make deploy-path-check`."""
import importlib.util
import os
import unittest

_SPEC = importlib.util.spec_from_file_location(
    "deploy_path_images",
    os.path.join(os.path.dirname(os.path.abspath(__file__)), "deploy-path-images.py"))
dpi = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(dpi)

POD = """apiVersion: v1
kind: Pod
metadata:
  name: probe
spec:
  initContainers:
  - name: wait
    image: nvcr.io/nvidia/cuda:12.5.0-base
  containers:
  - name: probe
    image: busybox:1.36
    resources:
      limits:
        image: not-a-container-field
  - name: sidecar
    image: "alpine"
"""


def refs(text):
    return sorted(dpi.image_refs(text))


class ImageRefs(unittest.TestCase):
    def test_bare_container_images_are_reported(self):
        self.assertEqual(refs(POD), ["alpine", "busybox:1.36", "nvcr.io/nvidia/cuda:12.5.0-base"])

    def test_container_list_at_the_parent_indent(self):
        text = "spec:\n  containers:\n  - name: a\n    image: busybox:1.36\n"
        self.assertEqual(refs(text), ["busybox:1.36"])
        text = "containers:\n- name: a\n  image: busybox:1.36\n"
        self.assertEqual(refs(text), ["busybox:1.36"])

    def test_name_fragment_under_a_repository(self):
        text = "driver:\n  repository: nvcr.io/nvidia\n  image: driver\n  version: 580.126.20\n"
        self.assertEqual(refs(text), ["nvcr.io/nvidia"])

    def test_name_fragment_without_a_repository(self):
        # The ClusterPolicy names images for components it leaves disabled.
        text = "spec:\n  vgpuManager:\n    enabled: false\n    image: vgpu-manager\n"
        self.assertEqual(refs(text), [])

    def test_path_images_outside_containers(self):
        text = "operator:\n  image: nvcr.io/nvidia/gpu-operator:v26.3.2\n"
        self.assertEqual(refs(text), ["nvcr.io/nvidia/gpu-operator:v26.3.2"])

    def test_quoted_keys_and_comments(self):
        text = 'containers:\n- "image": busybox:1.36  # implicit registry\n'
        self.assertEqual(refs(text), ["busybox:1.36"])

    def test_documents_are_independent(self):
        text = "containers:\n- name: a\n---\nvalidator:\n  image: gpu-operator\n"
        self.assertEqual(refs(text), [])

    def test_empty_values_ignored(self):
        text = "image:\n  repository: nvcr.io/nvidia/driver\n  tag: v1\n"
        self.assertEqual(refs(text), ["nvcr.io/nvidia/driver"])


if __name__ == "__main__":
    unittest.main()
