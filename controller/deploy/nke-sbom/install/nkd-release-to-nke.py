#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Convert the description `nkdctl release verify` prints into release-manifest.yaml.

  nkd-release-to-nke.py [RELEASE.json] --source-repo URL --source-tag TAG
      [--source-commit SHA] [--versions NAME=V,NAME=V,...] > release-manifest.yaml

Reads a `nkd.dev/release/v3` document from the path or stdin and writes the
installer's spec to stdout. NKD stays the source of digests; this owns the
file's shape. The mapping:

  subject                 release
  lane, scope             same
  artifacts[].name        items[].name; the oci artifact named cwe, or whose
                          components include cwe, becomes `controller`
  artifacts[].kind        items[].type: oci -> image, helm-oci -> chart,
                          executable -> binary. apt-repository is dropped with
                          a note; any other kind is an error
  artifacts[].reference   items[].ref. When absent it is built as
                          <parent>/<name>@<digest>, parent being the subject's
                          repository with its trailing /releases removed
  artifacts[].digest      dropped; it is in ref

Two things the description does not carry come from flags: `source` (the NKE
repository, tag and commit the Release was pinned from) and the chart
versions, which stage 0 checks against release.yaml at that tag. A missing
flag is an error that names what to pass.

Standard library only.
"""
import argparse
import json
import sys

API_VERSION = "nkd.dev/release/v3"
KINDS = {"oci": "image", "helm-oci": "chart", "executable": "binary"}
DROPPED_KINDS = ("apt-repository",)


def die(msg):
    sys.exit("nkd-release-to-nke: %s" % msg)


def parse_versions(text):
    versions = {}
    for pair in filter(None, (p.strip() for p in (text or "").split(","))):
        if "=" not in pair:
            die("--versions: expected NAME=VERSION, got %s" % pair)
        name, version = pair.split("=", 1)
        if not name or not version:
            die("--versions: expected NAME=VERSION, got %s" % pair)
        versions[name] = version
    return versions


def load(path):
    try:
        if path == "-":
            doc = json.load(sys.stdin)
        else:
            with open(path) as handle:
                doc = json.load(handle)
    except (OSError, ValueError) as err:
        die("cannot read %s: %s" % (path, err))
    if not isinstance(doc, dict):
        die("%s: the document is not a JSON object" % path)
    version = doc.get("apiVersion")
    if version is not None and version != API_VERSION:
        die("%s: apiVersion is %s, expected %s" % (path, version, API_VERSION))
    for key in ("subject", "lane", "scope", "artifacts"):
        if key not in doc:
            die("%s: missing %s" % (path, key))
    if not isinstance(doc["artifacts"], list) or not doc["artifacts"]:
        die("%s: artifacts must be a non-empty list" % path)
    return doc


def parent_of(subject):
    """ghcr.io/x/nke-recipes/releases@sha256:... -> ghcr.io/x/nke-recipes"""
    repo = subject.split("@", 1)[0]
    if repo.endswith("/releases"):
        repo = repo[:-len("/releases")]
    return repo


def convert(doc, versions):
    items = []
    missing_versions = []
    parent = parent_of(doc["subject"])
    for index, art in enumerate(doc["artifacts"]):
        for key in ("name", "kind", "digest"):
            if key not in art:
                die("artifacts[%d]: missing %s" % (index, key))
        kind = art["kind"]
        if kind in DROPPED_KINDS:
            sys.stderr.write("nkd-release-to-nke: dropping %s %s; the installer has no use for it\n"
                             % (kind, art["name"]))
            continue
        if kind not in KINDS:
            die("artifacts[%d] %s: unknown kind %s" % (index, art["name"], kind))
        name = art["name"]
        components = art.get("components") or []
        if kind == "oci" and (name == "cwe" or "cwe" in components):
            name = "controller"
        ref = art.get("reference") or "%s/%s@%s" % (parent, art["name"], art["digest"])
        item = {"name": name, "type": KINDS[kind], "ref": ref, "version": ""}
        if kind == "helm-oci":
            if name not in versions:
                missing_versions.append(name)
            else:
                item["version"] = versions[name]
        items.append(item)
    chart_names = [i["name"] for i in items if i["type"] == "chart"]
    unknown = [n for n in versions if n not in chart_names]
    if unknown:
        die("--versions names charts the release does not carry: %s" % ", ".join(unknown))
    if missing_versions:
        die("chart versions are not in the description; pass --versions %s"
            % ",".join("%s=<version>" % n for n in missing_versions))
    return items


def emit(doc, items, source, out):
    name_w = max(len(i["name"]) for i in items) + 1
    type_w = max(len(i["type"]) for i in items) + 1
    with_version = [i for i in items if i["version"]]
    ref_w = max([len(i["ref"]) for i in with_version] or [0]) + 1
    out.write("# Converted from `nkdctl release verify` output. Item names match the\n"
              "# deploy items in nke-deploy.yaml; `ref` carries the digest, so there is\n"
              "# no separate checksum.\n")
    out.write("apiVersion: nke.nvidia.com/release/v1\n")
    out.write("release: %s\n" % doc["subject"])
    out.write("lane: %s\n" % doc["lane"])
    out.write("scope: %s\n" % doc["scope"])
    out.write("source:\n")
    out.write("  repo: %s\n" % source["repo"])
    out.write("  tag: %s\n" % source["tag"])
    out.write("  commit: %s\n" % (source["commit"] or '""'))
    out.write("items:\n")
    for item in items:
        line = "  - {name: %s type: %s ref: " % (
            (item["name"] + ",").ljust(name_w), (item["type"] + ",").ljust(type_w))
        if item["version"]:
            line += "%s version: \"%s\"}" % ((item["ref"] + ",").ljust(ref_w), item["version"])
        else:
            line += "%s}" % item["ref"]
        out.write(line + "\n")


def main():
    parser = argparse.ArgumentParser(add_help=True, usage=__doc__.split("\n\n")[1].strip())
    parser.add_argument("release", nargs="?", default="-", help="release description JSON; stdin by default")
    parser.add_argument("--source-repo", default="", help="NKE repository the Release was pinned from")
    parser.add_argument("--source-tag", default="", help="NKE tag the Release was pinned from")
    parser.add_argument("--source-commit", default="", help="commit of that tag; may be omitted")
    parser.add_argument("--versions", default="", help="chart versions as NAME=V,NAME=V")
    args = parser.parse_args()

    missing = [flag for flag, value in (("--source-repo", args.source_repo),
                                        ("--source-tag", args.source_tag)) if not value]
    if missing:
        die("the description carries no source pin; pass %s" % " and ".join(missing))
    for value, flag in ((args.source_repo, "--source-repo"), (args.source_tag, "--source-tag"),
                        (args.source_commit, "--source-commit")):
        if any(c.isspace() for c in value):
            die("%s must not contain whitespace: %s" % (flag, value))

    doc = load(args.release)
    items = convert(doc, parse_versions(args.versions))
    emit(doc, items, {"repo": args.source_repo, "tag": args.source_tag,
                      "commit": args.source_commit}, sys.stdout)


if __name__ == "__main__":
    main()
