# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Tests for deploy-path-namespaces.py. Run by `make deploy-path-check`."""
import importlib.util
import io
import os
import tempfile
import unittest
from contextlib import redirect_stdout

_SPEC = importlib.util.spec_from_file_location(
    "deploy_path_namespaces",
    os.path.join(os.path.dirname(os.path.abspath(__file__)), "deploy-path-namespaces.py"))
dpn = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(dpn)

NS = "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: gpu-operator\n"


def names(text):
    return list(dpn.namespaces(text))


class Namespaces(unittest.TestCase):
    def test_block_style(self):
        self.assertEqual(names(NS), ["gpu-operator"])

    def test_quoted_name(self):
        self.assertEqual(names(NS.replace("gpu-operator", '"gpu-operator"')), ["gpu-operator"])
        self.assertEqual(names(NS.replace("gpu-operator", "'gpu-operator'")), ["gpu-operator"])

    def test_metadata_before_kind(self):
        text = "apiVersion: v1\nmetadata:\n  name: gpu-operator\nkind: Namespace\n"
        self.assertEqual(names(text), ["gpu-operator"])

    def test_two_documents(self):
        text = NS + "---\n" + NS.replace("gpu-operator", "nvidia-dra")
        self.assertEqual(names(text), ["gpu-operator", "nvidia-dra"])

    def test_flow_metadata(self):
        text = "kind: Namespace\nmetadata: {name: gpu-operator, labels: {a: b}}\n"
        self.assertEqual(names(text), ["gpu-operator"])

    def test_flow_metadata_nested_name_ignored(self):
        text = "kind: Namespace\nmetadata: {labels: {name: gpu-operator}, name: actual-namespace}\n"
        self.assertEqual(names(text), ["actual-namespace"])

    def test_flow_metadata_without_name(self):
        text = "kind: Namespace\nmetadata: {labels: {name: gpu-operator}}\n"
        self.assertEqual(names(text), [])

    def test_trailing_comment(self):
        self.assertEqual(names(NS.replace("gpu-operator", "gpu-operator  # the operator")), ["gpu-operator"])

    def test_other_kinds_ignored(self):
        text = "kind: ConfigMap\nmetadata:\n  name: gpu-operator\n"
        self.assertEqual(names(text), [])

    def test_nested_name_ignored(self):
        text = ("kind: Namespace\nmetadata:\n  labels:\n    name: wrong\n"
                "  name: gpu-operator\n")
        self.assertEqual(names(text), ["gpu-operator"])

    def test_commented_name_ignored(self):
        text = "kind: Namespace\nmetadata:\n  # name: wrong\n  name: gpu-operator\n"
        self.assertEqual(names(text), ["gpu-operator"])


class ItemDirectory(unittest.TestCase):
    def run_main(self, files):
        with tempfile.TemporaryDirectory() as item:
            for name, text in files.items():
                with open(os.path.join(item, name), "w") as handle:
                    handle.write(text)
            out = io.StringIO()
            with redirect_stdout(out):
                dpn.main(item)
            return out.getvalue().split()

    def test_templates_only(self):
        self.assertEqual(self.run_main({"ns.yaml.tmpl": NS}), ["gpu-operator"])

    def test_yaml_and_templates(self):
        files = {"a.yaml": NS, "b.yaml.tmpl": NS.replace("gpu-operator", "nvidia-dra")}
        self.assertEqual(self.run_main(files), ["gpu-operator", "nvidia-dra"])

    def test_other_files_ignored(self):
        self.assertEqual(self.run_main({"README.md": NS, "values.yml": NS}), [])


if __name__ == "__main__":
    unittest.main()
