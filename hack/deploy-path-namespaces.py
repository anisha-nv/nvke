# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Print the Namespace objects a deploy-path item declares, one name per line.

  deploy-path-namespaces.py <item-dir>

Reads every *.yaml and *.yaml.tmpl in the directory -- only files that exist,
so an item holding only templates is still read -- and every `---` document in
each. A document counts when its top-level `kind` is Namespace; the name is
the direct child `name` of its top-level `metadata`, in whichever order the two
keys appear, quoted or not.

kubectl is what applies these, so the test is "would kubectl create this
namespace", not "is this formatted the way the last author formatted it".
PyYAML is not guaranteed on the runners (see build/nke-sbom-items.py), so this
reads the block structure directly rather than loading YAML. An unreadable file
is a hard error: a check that skips what it cannot read reports a namespace
missing that is really there.
"""
import glob
import os
import re
import sys

DOC_SEP = re.compile(r"(?m)^---[ \t]*(?:#.*)?$")
TOP_KEY = re.compile(r"^([A-Za-z_][\w.-]*):(.*)$")
NAME = re.compile(r"^\s+name:(.*)$")
FLOW_NAME = re.compile(r"(?:^|[{,])\s*name:\s*([^,}]+)")


def scalar(value):
    """A plain or quoted YAML scalar, without a trailing comment."""
    value = re.sub(r"\s+#.*$", "", value).strip()
    if len(value) > 1 and value[0] == value[-1] and value[0] in "\"'":
        value = value[1:-1]
    return value


def namespaces(text):
    for doc in DOC_SEP.split(text):
        kind = name = None
        in_metadata = False
        child_indent = None
        for line in doc.splitlines():
            if not line.strip() or line.lstrip().startswith("#"):
                continue
            top = TOP_KEY.match(line)
            if top:
                key, rest = top.groups()
                in_metadata = key == "metadata"
                child_indent = None
                if key == "kind":
                    kind = scalar(rest)
                elif in_metadata and rest.strip().startswith("{"):
                    flow = FLOW_NAME.search(rest.strip().lstrip("{"))
                    if flow:
                        name = scalar(flow.group(1))
                continue
            if not in_metadata:
                continue
            indent = len(line) - len(line.lstrip())
            if child_indent is None:
                child_indent = indent
            if indent == child_indent:
                match = NAME.match(line)
                if match:
                    name = scalar(match.group(1))
        if kind == "Namespace" and name:
            yield name


def main(item_dir):
    files = sorted(glob.glob(os.path.join(item_dir, "*.yaml")) +
                   glob.glob(os.path.join(item_dir, "*.yaml.tmpl")))
    for path in files:
        try:
            with open(path) as handle:
                text = handle.read()
        except OSError as err:
            sys.exit("deploy-path-namespaces: %s" % err)
        for name in namespaces(text):
            print(name)


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit("usage: deploy-path-namespaces.py <item-dir>")
    main(sys.argv[1])
