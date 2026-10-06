#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Parse release-manifest.yaml, the one file that names every artifact the install pulls.

  release_spec.py meta   FILE                         shell-sourceable lines
  release_spec.py items  FILE [--type image|chart|binary]
                                                      "name\\ttype\\tref\\tversion"
  release_spec.py ref    FILE NAME                    the item's ref
  release_spec.py digest FILE NAME                    sha256:<hex> from the ref

meta prints RELEASE, LANE, SCOPE, SOURCE_REPO, SOURCE_TAG and SOURCE_COMMIT,
each single-quoted, for `eval`. items prints one TSV line per item in file
order; version is empty for non-chart items.

The file has the shape of nke-deploy.yaml: a flat `items:` list in flow
style, one entry per artifact. This is the whole grammar:

  apiVersion: nke.nvidia.com/release/v1
  release: <string>
  lane: <token>
  scope: <token>
  source:
    repo: <string>
    tag: <string>
    commit: <string, may be empty>
  items:
    - {name: X, type: image|binary, ref: <repo>@sha256:<64 hex>}
    - {name: X, type: chart, ref: <repo>@sha256:<64 hex>, version: "V"}

Comments and blank lines are ignored. Every other line is an error that
names the file and line, because a parser that skips what it does not
understand turns a malformed spec into an install that silently does less
than it claims. Chart items carry a version, other items do not. Names are
unique. Every top-level key is required; only source.commit may be empty.

Standard library only: PyYAML is not guaranteed where the stages run, which
is why this is a regex parser and not a YAML load, like build/nke-sbom-items.py.
"""
import re
import sys

API_VERSION = "nke.nvidia.com/release/v1"
TYPES = ("image", "chart", "binary")

TOP_SCALAR = re.compile(r'^(apiVersion|release|lane|scope): (\S+)$')
TOP_SECTION = re.compile(r'^(source|items):$')
SOURCE_KV = re.compile(r'^  (repo|tag|commit):(?: (\S+))?$')
ITEM = re.compile(r'^  - \{name: ([\w.-]+), +type: (\w+), +ref: (\S+?)'
                  r'(?:, +version: "([^"]+)")?\}$')
REF = re.compile(r'^[^@\s]+@(sha256:[0-9a-f]{64})$')
TOKEN = re.compile(r'^[\w.-]+$')
EMPTY = ('""', "''")


class SpecError(Exception):
    pass


def unquote(value):
    """Strip the quotes YAML uses to keep a value a string. '""' becomes ''."""
    if value is None:
        return ""
    if len(value) > 1 and value[0] == value[-1] and value[0] in "\"'":
        return value[1:-1]
    return value


def significant(path):
    """Yield (lineno, line) for everything that is not blank or a whole-line comment."""
    try:
        with open(path) as handle:
            raw = handle.read()
    except OSError as err:
        raise SpecError(str(err))
    for num, line in enumerate(raw.splitlines(), 1):
        if line.strip() and not line.lstrip().startswith("#"):
            yield num, line


def parse(path):
    """Return (meta, items). meta is a dict; items is a list of dicts in file order."""
    meta = {}
    items = []
    names = set()
    section = None
    seen_sections = set()

    def fail(num, msg):
        raise SpecError("%s:%d: %s" % (path, num, msg))

    for num, line in significant(path):
        top = TOP_SCALAR.match(line)
        if top:
            key, value = top.groups()
            if key in meta:
                fail(num, "duplicate key: %s" % key)
            meta[key] = unquote(value)
            section = None
            continue
        sec = TOP_SECTION.match(line)
        if sec:
            section = sec.group(1)
            if section in seen_sections:
                fail(num, "duplicate section: %s" % section)
            seen_sections.add(section)
            continue
        if not line.startswith("  "):
            fail(num, "unrecognised line: %s" % line.strip())
        if section == "source":
            kv = SOURCE_KV.match(line)
            if not kv:
                fail(num, "unrecognised line under source: %s" % line.strip())
            key, value = kv.groups()
            full = "source.%s" % key
            if full in meta:
                fail(num, "duplicate key: %s" % full)
            meta[full] = unquote(value)
            if key != "commit" and not meta[full]:
                fail(num, "%s is empty" % full)
            continue
        if section == "items":
            item = ITEM.match(line)
            if not item:
                fail(num, "unrecognised item line: %s" % line.strip())
            name, kind, ref, version = item.groups()
            if kind not in TYPES:
                fail(num, "item %s: type %s is not one of %s" % (name, kind, ", ".join(TYPES)))
            digest = REF.match(ref)
            if not digest:
                fail(num, "item %s: ref must end in @sha256:<64 hex>: %s" % (name, ref))
            if kind == "chart" and version is None:
                fail(num, "item %s: chart items need version: \"...\"" % name)
            if kind != "chart" and version is not None:
                fail(num, "item %s: only chart items carry version" % name)
            if name in names:
                fail(num, "duplicate item name: %s" % name)
            names.add(name)
            items.append({"name": name, "type": kind, "ref": ref,
                          "version": version or "", "digest": digest.group(1)})
            continue
        fail(num, "unrecognised line: %s" % line.strip())

    for key in ("apiVersion", "release", "lane", "scope",
                "source.repo", "source.tag", "source.commit"):
        if key not in meta:
            raise SpecError("%s: missing %s" % (path, key))
    if meta["apiVersion"] != API_VERSION:
        raise SpecError("%s: apiVersion is %s, expected %s"
                        % (path, meta["apiVersion"], API_VERSION))
    for key in ("lane", "scope"):
        if not TOKEN.match(meta[key]):
            raise SpecError("%s: %s must be a token: %s" % (path, key, meta[key]))
    if "items" not in seen_sections:
        raise SpecError("%s: missing items:" % path)
    if not items:
        raise SpecError("%s: items: is empty" % path)
    return meta, items


def shell_quote(value):
    """Single-quote a value for `eval`. Every value is quoted, even a safe one."""
    return "'" + value.replace("'", "'\\''") + "'"


def cmd_meta(meta, _items, _args):
    for var, key in (("RELEASE", "release"), ("LANE", "lane"), ("SCOPE", "scope"),
                     ("SOURCE_REPO", "source.repo"), ("SOURCE_TAG", "source.tag"),
                     ("SOURCE_COMMIT", "source.commit")):
        print("%s=%s" % (var, shell_quote(meta[key])))


def cmd_items(_meta, items, args):
    kind = None
    if args:
        if len(args) != 2 or args[0] != "--type" or args[1] not in TYPES:
            usage()
        kind = args[1]
    for item in items:
        if kind is None or item["type"] == kind:
            print("%s\t%s\t%s\t%s" % (item["name"], item["type"], item["ref"], item["version"]))


def find(items, name, path):
    for item in items:
        if item["name"] == name:
            return item
    raise SpecError("%s: no item named %s" % (path, name))


def cmd_ref(_meta, items, args):
    if len(args) != 1:
        usage()
    print(find(items, args[0], FILE)["ref"])


def cmd_digest(_meta, items, args):
    if len(args) != 1:
        usage()
    print(find(items, args[0], FILE)["digest"])


COMMANDS = {"meta": cmd_meta, "items": cmd_items, "ref": cmd_ref, "digest": cmd_digest}


def usage():
    sys.exit("usage: release_spec.py meta FILE | items FILE [--type image|chart|binary]"
             " | ref FILE NAME | digest FILE NAME")


if len(sys.argv) < 3 or sys.argv[1] not in COMMANDS:
    usage()
FILE = sys.argv[2]
try:
    META, ITEMS = parse(FILE)
    COMMANDS[sys.argv[1]](META, ITEMS, sys.argv[3:])
except SpecError as err:
    sys.exit("release_spec: %s" % err)
