# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Print the image references YAML names, one per line, sorted and unique.

  deploy-path-images.py [FILE...]     (no FILE, or -, reads stdin)

Three kinds of field count:

  - every `repository:` value;
  - every `image:` value carrying a path, such as nvcr.io/nvidia/driver;
  - an `image:` without one, such as busybox:1.36, when it is a container's
    own image -- a direct child of a containers, initContainers or
    ephemeralContainers list element. That is a complete reference whose
    registry is implicit, which is exactly what the allowlist has to see.

Any other `image:` without a path is a name under a separate `repository:`,
the form Helm values and the GPU operator's ClusterPolicy use, and the
repository is what gets checked. Its sibling repository may legitimately be
absent: the ClusterPolicy names images for components it leaves disabled.

PyYAML is not guaranteed on the runners (see build/nke-sbom-items.py), so this
follows block indentation rather than loading YAML, like
deploy-path-namespaces.py beside it.
"""
import re
import sys

DOC_SEP = re.compile(r"(?m)^---[ \t]*(?:#.*)?$")
KEY = re.compile(r"""^(["']?)([A-Za-z_][\w.-]*)\1:(?:[ \t]+(.*))?$""")
CONTAINER_LISTS = {"containers", "initContainers", "ephemeralContainers"}


def scalar(value):
    """A plain or quoted YAML scalar, without a trailing comment."""
    value = re.sub(r"\s+#.*$", "", value or "").strip()
    if len(value) > 1 and value[0] == value[-1] and value[0] in "\"'":
        value = value[1:-1]
    return value


def image_fields(text):
    """Yield (key, value, in_container) for each repository: and image: field.

    in_container says the field belongs directly to a container list element.
    """
    for doc in DOC_SEP.split(text):
        # One entry per open block mapping: (indent of its keys, the key whose
        # value it is). The elements of a list of mappings share the list's
        # key as parent, since their keys sit at the same indent.
        stack, last_key = [], None
        for line in doc.splitlines():
            stripped = line.strip()
            if not stripped or stripped.startswith("#"):
                continue
            body = line.lstrip()
            if body.startswith("- "):
                body = body[2:].lstrip()
            indent = len(line) - len(body)

            while stack and stack[-1][0] > indent:
                stack.pop()
            if not stack or stack[-1][0] < indent:
                stack.append((indent, last_key))

            match = KEY.match(body)
            if not match:
                continue
            key, value = match.group(2), scalar(match.group(3))
            last_key = key
            if key in ("repository", "image") and value:
                yield key, value, stack[-1][1] in CONTAINER_LISTS


def image_refs(text):
    refs = set()
    for key, value, in_container in image_fields(text):
        if key == "repository" or "/" in value or in_container:
            refs.add(value)
    return refs


def main(paths):
    refs = set()
    for path in paths or ["-"]:
        try:
            if path == "-":
                text = sys.stdin.read()
            else:
                with open(path) as handle:
                    text = handle.read()
        except OSError as err:
            sys.exit("deploy-path-images: %s" % err)
        refs |= image_refs(text)
    for ref in sorted(refs):
        print(ref)


if __name__ == "__main__":
    main(sys.argv[1:])
