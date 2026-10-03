#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Render site-config.yaml into the files stage 5 installs from.

  describe_site.py render --config site-config.yaml --out DIR --sbom-dir controller/deploy
                          [--release-file nke-release.yaml]
  describe_site.py check  --out DIR
  describe_site.py keys   --config site-config.yaml --sbom-dir controller/deploy

render writes seven files to DIR, mode 0600:

  bootstrap-config.json   the context build/render-template.py renders the
                          nke-base-config template item against
  controller.yaml         --values-for controller=...
  spire.yaml              --values-for spire=...
  postgresql.yaml         --values-for postgresql=...
  temporal.yaml           --values-for temporal=...
  site.env                SITE_NAME, SITE_VIP and the two image references
                          for stage 6 and the operator; no secrets
  images.txt              every external image the install pulls, one per
                          line, sorted: `helm template` of each helm item
                          with its values and the site values, the CAPNICo
                          manifest image, the Cluster API and cert-manager
                          images at the pinned versions. Stage 2 pre-pulls
                          them. Lines starting with # are notes.

and three optional files:

  nktp.yaml               --values-for nktp=...; the `nktp:` block of the
                          config, copied as written. Only when the block is
                          present. A rerun without the block removes it.
  version-bom.cdx.json    CycloneDX 1.5: one component per release spec item
                          with its digest, keyed by deploy item. Only with
                          --release-file.
  capnico-kustomization.yaml
                          an `images:` override that repoints the CAPNICo
                          manifest at registry.mirror. Only when the config
                          sets it.

The controller image. `images.controller` is optional. Its default is the
`controller` item's ref in the release spec, passed in as the environment
variable RELEASE_CONTROLLER_REF by 03-describe-site.sh, so this file parses no
spec. A digest reference renders as image.repository plus image.digest, which
the chart turns into repository:appVersion@digest; containerd resolves that
by the digest, the name stage 2 pulled. Neither present is an error.

The mirror. `registry.mirror` is a registry host with an optional path
prefix. When set, the values files repoint every chart image at it, each
image keeping its upstream path: Bitnami's global.imageRegistry (plus
global.security.allowInsecureImages, without which the chart refuses a
foreign registry), Temporal's server.image.repository,
admintools.image.repository and web.image.repository, SPIRE's
global.spire.image.registry, which that chart applies to all of its images.
The CAPNICo image gets a kustomize override file. images.txt then lists the
mirrored names, because `helm template` runs with the same values. Not
repointed, and listed under their upstream names: the Cluster API and
cert-manager images clusterctl installs in stage 4, and the nktp chart's
images, which the `nktp:` block sets.

The derivations follow controller/hack/ci/deploy-sbom.sh, which does the same
for the e2e lanes from environment variables. The upstream CA values follow
cli/internal/bootstrap/cwedeploy (upstreamCAArgs): the chart takes the PEM
contents, so they are inlined into spire.yaml. Nothing here writes a Secret
manifest; stage 5 renders those at apply time and deletes them.

Standard library only. PyYAML is not guaranteed, so this reads a small YAML
subset: two levels, `section:` then `  key: value`, scalars bare or quoted,
`#` comments. Anything else is refused by name. Every required key must be a
non-empty scalar, as render-template.py requires of the BootstrapConfig. The
one exception is the `nktp:` block: its lines are not parsed here, they are
dedented and passed through to helm, which parses them. The chart README's
"The backend" section lists the keys that block sets.

images.txt needs helm and the network for the chart sources. IMAGES_TXT_SKIP=1
or DRY_RUN in the environment skips `helm template` and writes the static
entries with a note. A chart that fails to render is an error naming the item.

check re-reads DIR and refuses missing files, unparseable JSON, missing keys
and empty values. keys prints the dotted key paths render would write, for the
golden test in testdata/.
"""
import argparse
import ipaddress
import json
import os
import re
import secrets
import shutil
import subprocess
import sys
import tempfile

SCHEMA_NOTE = "The site-config.yaml schema is unstable until it is folded into BootstrapConfig."

# Required keys and the optional keys. A key not listed here is an error.
REQUIRED = [
    "site.name", "site.trustDomain", "site.vip", "site.arch",
    "spire.caCertFile", "spire.caKeyFile",
    "images.profiles",
    "nico.endpoint", "nico.orgId", "nico.token", "nico.apiName",
    "registry.server", "registry.username", "registry.token",
    "tenant.user", "tenant.password",
]
OPTIONAL = ["db.password", "images.controller", "registry.mirror"]
# Sections passed through unparsed. Each maps to the values file it becomes.
PASSTHROUGH = {"nktp": "nktp.yaml"}

CONTROLLER_REF_ENV = "RELEASE_CONTROLLER_REF"

ARCHES = ("amd64", "arm64")

# sha256 of /app/tpm_attestor_server in
# ghcr.io/spiffe/spire-tpm-plugin-tpm-attestor-server:v1.9.0, per architecture.
# Same table as controller/hack/ci/deploy-sbom.sh resolve_tpm_checksum() and
# cli/internal/bootstrap/cwedeploy. A wrong value crash-loops spire-server
# with "checksums did not match".
TPM_CHECKSUM = {
    "amd64": "46d0caad8c25a027dd11c93e18b58a8bc6fbd9f1fe2e36fa2a0dd440986de4dc",
    "arm64": "b69d0153c8241383f24da138bc85429a620d9f962a2f10abb314aa0e39046d16",
}

# The SPIFFE path cloud-workflow-engine presents. Matches spire-server.adminIDs
# in controller/deploy/nke-sbom/spire/values.yaml.
CWE_ADMIN_PATH = "/nke-service/cloud-workflow-engine"

OIDC_NODEPORT = 30443

# Image repositories the Temporal chart (0.74.0) defaults to, in its
# values.yaml: server.image.repository, admintools.image.repository,
# web.image.repository. The chart has no registry key, so the mirror is
# written into the repository. Refresh with the chart pin in
# nke-sbom/temporal/release.yaml.
TEMPORAL_IMAGES = {
    "server": "temporalio/server",
    "admintools": "temporalio/admin-tools",
    "web": "temporalio/ui",
}

# Images `clusterctl init` installs in stage 4, at the versions pinned in
# make/tool-versions.mk. The provider components manifests of a Cluster API
# release name one controller image each; the cert-manager release manifest
# names three. Refresh with those pins.
CAPI_IMAGES = [
    "registry.k8s.io/cluster-api/cluster-api-controller:{capi}",
    "registry.k8s.io/cluster-api/kubeadm-bootstrap-controller:{capi}",
    "registry.k8s.io/cluster-api/kubeadm-control-plane-controller:{capi}",
]
CERT_MANAGER_IMAGES = [
    "quay.io/jetstack/cert-manager-cainjector:{cm}",
    "quay.io/jetstack/cert-manager-controller:{cm}",
    "quay.io/jetstack/cert-manager-webhook:{cm}",
]

IMAGES_FILE = "images.txt"
BOM_FILE = "version-bom.cdx.json"
CAPNICO_KUSTOMIZATION = "capnico-kustomization.yaml"
OUTPUT_FILES = ["bootstrap-config.json", "controller.yaml", "spire.yaml",
                "postgresql.yaml", "temporal.yaml", "site.env"]
OPTIONAL_FILES = sorted(PASSTHROUGH.values()) + [CAPNICO_KUSTOMIZATION]
ENV_FILE = "site.env"
ENV_KEYS = ["SITE_NAME", "SITE_VIP", "SITE_IMAGE_CONTROLLER", "SITE_IMAGE_PROFILES"]
PASSWORD_FILE = "db-password"

SECRET_KEY_RE = re.compile(r"password|token|key|secret", re.I)
DIGEST_RE = re.compile(r"sha256:[0-9a-f]{64}")
# Reference characters, before the digest.
IMAGE_NAME_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._:/-]*")


class ConfigError(Exception):
    pass


def die(msg):
    sys.exit("describe-site: %s" % msg)


# --- YAML subset -------------------------------------------------------------

_SECTION_RE = re.compile(r"^([A-Za-z_][A-Za-z0-9_]*):\s*(#.*)?$")
_ENTRY_RE = re.compile(r"^(\s+)([A-Za-z_][A-Za-z0-9_]*):(?:\s+(.*?))?\s*$")


def _strip_comment(value):
    """Drop a trailing ` # comment` from a bare scalar."""
    m = re.search(r"\s#", value)
    return value[:m.start()].rstrip() if m else value


def _scalar(raw, where):
    """Return (value, is_null) for the text after `key:`."""
    if raw is None or raw == "":
        return None, True
    if raw[0] in "\"'":
        quote = raw[0]
        end = raw.find(quote, 1)
        while quote == '"' and end > 0 and raw[end - 1] == "\\":
            end = raw.find(quote, end + 1)
        if end < 0:
            raise ConfigError("%s: unterminated %s-quoted string." % (where, quote))
        rest = raw[end + 1:].strip()
        if rest and not rest.startswith("#"):
            raise ConfigError("%s: unexpected text after the closing quote: %r." % (where, rest))
        body = raw[1:end]
        if quote == '"':
            try:
                body = json.loads('"%s"' % body)
            except ValueError:
                raise ConfigError("%s: invalid escape in double-quoted string." % where)
        return body, False
    value = _strip_comment(raw)
    if value in ("~", "null", "Null", "NULL"):
        return None, True
    if value[0] in "[{":
        raise ConfigError("%s: flow lists and maps are not supported; use a scalar." % where)
    if value.startswith("- "):
        raise ConfigError("%s: lists are not supported; use a scalar." % where)
    if value[0] in "|>":
        raise ConfigError("%s: block scalars are not supported; use a quoted string." % where)
    return value, False


def split_passthrough(text, source):
    """Separate the PASSTHROUGH sections from the rest of the text.

    Returns (remaining_lines, {section: raw_lines}). A passthrough section
    runs from its `section:` line to the next line at column 0. Its lines are
    kept as written, blank lines and comments included.
    """
    rest = []
    raw = {}
    current = None
    for lineno, line in enumerate(text.splitlines(), 1):
        where = "%s:%d" % (source, lineno)
        if line and not line[0].isspace():
            m = _SECTION_RE.match(line)
            current = m.group(1) if m and m.group(1) in PASSTHROUGH else None
            if current is not None:
                if current in raw:
                    raise ConfigError("%s: duplicate section %r." % (where, current))
                raw[current] = []
                rest.append("")
                continue
        # Keep the line count so error messages still name the right line.
        rest.append("" if current is not None else line)
        if current is not None:
            raw[current].append(line)
    return rest, raw


def dedent_block(section, lines):
    """Strip the common indentation from a passthrough block and check it is
    a non-empty map. Returns the text helm will read."""
    content = [l for l in lines if l.strip() and not l.strip().startswith("#")]
    if not content:
        raise ConfigError("%s is empty; omit the section or give it keys." % section)
    for l in content:
        if "\t" in l[:len(l) - len(l.lstrip())]:
            raise ConfigError("%s: tabs are not allowed in indentation." % section)
    indent = min(len(l) - len(l.lstrip(" ")) for l in content)
    if indent == 0:
        raise ConfigError("%s: entries must be indented under the section." % section)
    if not re.match(r"[A-Za-z_][A-Za-z0-9_.-]*:(\s|$)", content[0].strip()):
        raise ConfigError("%s must be a map of `key: value` entries, got %r." % (section, content[0].strip()))
    out = []
    for l in lines:
        if not l.strip():
            out.append("")
        elif l[:indent].strip() == "":
            out.append(l[indent:])
        else:
            out.append(l.lstrip(" "))
    return "\n".join(out).strip("\n") + "\n"


def parse_site_config(text, source):
    """Parse the two-level subset into {section: {key: value-or-None}}.

    PASSTHROUGH sections come back as text under data[section].
    """
    data = {}
    section = None
    indent = None
    pending = None  # (section, key) whose value was empty on its line
    lines, raw = split_passthrough(text, source)
    for lineno, line in enumerate(lines, 1):
        where = "%s:%d" % (source, lineno)
        if "\t" in line[:len(line) - len(line.lstrip())]:
            raise ConfigError("%s: tabs are not allowed in indentation." % where)
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        if stripped == "---":
            continue
        if stripped.startswith("- "):
            raise ConfigError("%s: lists are not supported." % where)
        m = _SECTION_RE.match(line)
        if m:
            pending = None
            section = m.group(1)
            if section in data:
                raise ConfigError("%s: duplicate section %r." % (where, section))
            data[section] = {}
            continue
        m = _ENTRY_RE.match(line)
        if not m:
            if ":" in stripped:
                raise ConfigError("%s: top-level scalar %r; expected `section:` then indented `key: value`."
                                  % (where, stripped.split(":", 1)[0]))
            raise ConfigError("%s: cannot parse %r." % (where, stripped))
        if section is None:
            raise ConfigError("%s: %r appears before any section." % (where, m.group(2)))
        this_indent = len(m.group(1))
        if indent is None:
            indent = this_indent
        if this_indent > indent:
            if pending:
                raise ConfigError("%s.%s: nested maps are not supported; the value must be a scalar."
                                  % pending)
            raise ConfigError("%s: unexpected indentation." % where)
        if this_indent < indent:
            raise ConfigError("%s: inconsistent indentation." % where)
        key = m.group(2)
        if key in data[section]:
            raise ConfigError("%s: duplicate key %s.%s." % (where, section, key))
        value, is_null = _scalar(m.group(3), "%s.%s" % (section, key))
        data[section][key] = value
        pending = (section, key) if is_null else None
    for name, block in raw.items():
        data[name] = dedent_block(name, block)
    return data


# --- validation --------------------------------------------------------------

def flatten(data):
    return {"%s.%s" % (s, k): v for s, keys in data.items()
            if s not in PASSTHROUGH for k, v in keys.items()}


def validate(data, config_dir):
    """Return (flat, files, passthrough). passthrough maps a values file name
    to the text it holds, for every PASSTHROUGH section present.

    images.controller is filled from RELEASE_CONTROLLER_REF when the config
    does not set it. flat["images.controller.origin"] says which.
    """
    flat = flatten(data)
    known = set(REQUIRED) | set(OPTIONAL)
    for path in sorted(flat):
        if path not in known:
            raise ConfigError("unknown key %s. Known keys: %s" % (path, ", ".join(sorted(known))))
    for section in data:
        if section not in {p.split(".")[0] for p in known} and section not in PASSTHROUGH:
            raise ConfigError("unknown section %s." % section)
    for path in REQUIRED:
        if path not in flat:
            raise ConfigError("%s is missing." % path)
        value = flat[path]
        if value is None:
            raise ConfigError("%s is null." % path)
        if not isinstance(value, str):
            raise ConfigError("%s is not a scalar." % path)
        if value.strip() == "":
            raise ConfigError("%s is empty." % path)
        if value.startswith("REPLACE_"):
            raise ConfigError("%s still holds the placeholder %s." % (path, value))
    for path in OPTIONAL:
        if path in flat and flat[path] is None:
            raise ConfigError("%s is null; omit the key instead." % path)
        if path in flat and isinstance(flat[path], str) and flat[path].startswith("REPLACE_"):
            raise ConfigError("%s still holds the placeholder %s." % (path, flat[path]))

    if flat.get("images.controller"):
        flat["images.controller.origin"] = "from site config"
    else:
        ref = os.environ.get(CONTROLLER_REF_ENV, "").strip()
        if not ref:
            raise ConfigError("images.controller is missing and %s is not set: set images.controller in the "
                              "site config, or export RELEASE_FILE so the controller item's ref is the default."
                              % CONTROLLER_REF_ENV)
        flat["images.controller"] = ref
        flat["images.controller.origin"] = "from the release spec"

    site = data["site"]
    if site["arch"] not in ARCHES:
        raise ConfigError("site.arch must be one of %s, got %r." % (", ".join(ARCHES), site["arch"]))
    try:
        ipaddress.ip_address(site["vip"])
    except ValueError:
        raise ConfigError("site.vip must be an IP address, got %r." % site["vip"])
    td = site["trustDomain"]
    if not re.fullmatch(r"[a-z0-9._-]+", td):
        raise ConfigError("site.trustDomain %r is not a SPIFFE trust domain: lowercase letters, digits, . _ - only, no scheme." % td)
    if not re.fullmatch(r"[a-z0-9]([a-z0-9-]*[a-z0-9])?", site["name"]):
        raise ConfigError("site.name %r must be a DNS label: lowercase letters, digits and -." % site["name"])

    for path in ("images.controller", "images.profiles"):
        parse_image(flat[path], path)
    if "registry.mirror" in flat:
        validate_mirror(flat["registry.mirror"])

    files = {}
    for path, marker in (("spire.caCertFile", "CERTIFICATE"), ("spire.caKeyFile", "PRIVATE KEY")):
        p = flat[path]
        if not os.path.isabs(p):
            p = os.path.join(config_dir, p)
        if not os.path.isfile(p):
            raise ConfigError("%s: %s does not exist." % (path, p))
        with open(p) as fh:
            pem = fh.read()
        if not pem.strip():
            raise ConfigError("%s: %s is empty." % (path, p))
        if "-----BEGIN" not in pem or marker not in pem:
            raise ConfigError("%s: %s is not a PEM %s." % (path, p, marker.lower()))
        files[path] = pem
    passthrough = {PASSTHROUGH[s]: data[s] for s in PASSTHROUGH if s in data}
    return flat, files, passthrough


def parse_image(ref, path):
    """Split an image reference into the chart's image keys.

    Accepts repository:tag, repository@sha256:<hex> and
    repository:tag@sha256:<hex>. Returns {"repository": ..} plus "tag" and
    "digest" when present. The digest wins at pull time; the tag is kept
    because the chart renders one.
    """
    name, _, digest = ref.partition("@")
    if digest and not DIGEST_RE.fullmatch(digest):
        raise ConfigError("%s: the digest must be sha256:<64 hex>, got %r." % (path, digest))
    if not name or not IMAGE_NAME_RE.fullmatch(name):
        raise ConfigError("%s may only contain letters, digits and . _ : / - before any @, got %r." % (path, ref))
    repo, sep, tag = name.rpartition(":")
    if sep and repo and tag and "/" not in tag:
        out = {"repository": repo, "tag": tag}
    elif digest:
        out = {"repository": name}
    else:
        raise ConfigError("%s must be repository:tag or repository@sha256:<digest>, got %r." % (path, ref))
    if digest:
        out["digest"] = digest
    return out


_MIRROR_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9.-]*(:[0-9]{1,5})?(/[A-Za-z0-9._-]+)*")


def validate_mirror(mirror):
    if "://" in mirror:
        raise ConfigError("registry.mirror is a host, not a URL: drop the scheme from %r." % mirror)
    if mirror.endswith("/"):
        raise ConfigError("registry.mirror must not end in /, got %r." % mirror)
    if not _MIRROR_RE.fullmatch(mirror):
        raise ConfigError("registry.mirror must be host[:port][/prefix], got %r." % mirror)


def mirror_ref(ref, mirror):
    """Repoint an image reference at the mirror, keeping its upstream path.

    The leading component is a registry when it has a dot or a port or is
    localhost, as containerd decides; a short name like busybox:1.37 has no
    registry and keeps its name under the mirror. Same scheme as
    hack/mirror-bootstrap-images.sh and as the three charts render with their
    registry keys set.
    """
    first, sep, rest = ref.partition("/")
    if sep and ("." in first or ":" in first or first == "localhost"):
        ref = rest
    return "%s/%s" % (mirror, ref)


# --- derivation --------------------------------------------------------------

def oidc_domains(sbom_dir, vip):
    """The static additionalDomains from the SBOM spire overlay plus the VIP.

    A values file replaces a list rather than appending to it, so the static
    hostnames are read out of the overlay rather than retyped here. Same
    approach as deploy-sbom.sh.
    """
    overlay = os.path.join(sbom_dir, "nke-sbom", "spire", "overlay.yaml")
    try:
        with open(overlay) as fh:
            raw = fh.read()
    except OSError as err:
        raise ConfigError("reading %s: %s" % (overlay, err))
    blocks = re.findall(r"^[ \t]*additionalDomains:[ \t]*\n((?:[ \t]*-[ \t]*\S.*\n)+)", raw, re.M)
    if len(blocks) != 1:
        raise ConfigError("expected exactly one additionalDomains block in %s, found %d" % (overlay, len(blocks)))
    domains = [re.sub(r"^[ \t]*-[ \t]*", "", line).strip().strip("\"'")
               for line in blocks[0].splitlines() if line.strip()]
    if vip not in domains:
        domains.append(vip)
    return domains


def capnico_image(sbom_dir):
    """The image: line of the CAPNICo manifest, as (repository, tag)."""
    manifest = os.path.join(sbom_dir, "nke-sbom", "capnico", "capnico.yaml")
    try:
        with open(manifest) as fh:
            for line in fh:
                m = re.match(r"^\s*image:\s*[\"']?([^\s\"']+)", line)
                if m:
                    ref = m.group(1)
                    repo, sep, tag = ref.rpartition(":")
                    if not sep or "/" in tag:
                        raise ConfigError("%s: image %r is not repository:tag" % (manifest, ref))
                    return repo, tag
    except OSError as err:
        raise ConfigError("reading %s: %s" % (manifest, err))
    raise ConfigError("no image: line in %s" % manifest)


def tool_version(sbom_dir, name):
    """One `NAME ?= value` pin from make/tool-versions.mk."""
    path = os.path.join(sbom_dir, "..", "..", "make", "tool-versions.mk")
    try:
        with open(path) as fh:
            for line in fh:
                m = re.match(r"^%s\s*\?=\s*(\S+)" % re.escape(name), line)
                if m:
                    return m.group(1)
    except OSError as err:
        raise ConfigError("reading %s: %s" % (path, err))
    raise ConfigError("%s not found in %s" % (name, path))


def resolve_password(flat, out_dir):
    """The database password: config, else the previous run, else new."""
    stored_path = os.path.join(out_dir, PASSWORD_FILE)
    stored = None
    if os.path.isfile(stored_path):
        with open(stored_path) as fh:
            stored = fh.read().strip() or None
    configured = flat.get("db.password")
    if configured:
        if stored and stored != configured:
            note("db.password differs from the one in %s; using db.password" % stored_path)
        return configured, "from site config"
    if stored:
        return stored, "reusing %s" % stored_path
    return secrets.token_urlsafe(24), "generated"


def build(flat, files, sbom_dir, password):
    site_name = flat["site.name"]
    td = flat["site.trustDomain"]
    vip = flat["site.vip"]
    mirror = flat.get("registry.mirror")
    cwe_image = parse_image(flat["images.controller"], "images.controller")
    prof_image = parse_image(flat["images.profiles"], "images.profiles")

    bootstrap = {
        "ClusterName": site_name,
        "Nico": {
            "Endpoint": flat["nico.endpoint"],
            "OrgID": flat["nico.orgId"],
            "Token": flat["nico.token"],
            "APIName": flat["nico.apiName"],
        },
        "Registry": {
            "Server": flat["registry.server"],
            "Username": flat["registry.username"],
            "Token": flat["registry.token"],
        },
        "Tenant": {
            "User": flat["tenant.user"],
            "Password": flat["tenant.password"],
        },
    }
    controller = {
        "image": cwe_image,
        "profiles": {"image": prof_image},
        "appDb": {"postgresPassword": password},
        "nkeOIDCAuth": {
            "discoveryURL": "https://%s:%d/.well-known/openid-configuration" % (vip, OIDC_NODEPORT),
        },
        "spiffe": {"trustDomain": td, "spire": {"serverAddress": vip}},
    }
    spire_global = {"spire": {"trustDomain": td, "clusterName": site_name}}
    if mirror:
        spire_global["spire"]["image"] = {"registry": mirror}
    spire = {
        "global": spire_global,
        "spire-server": {
            "adminIDs": ["spiffe://%s%s" % (td, CWE_ADMIN_PATH)],
            "nodeAttestor": {"tpmDirect": {"checksum": TPM_CHECKSUM[flat["site.arch"]]}},
            # The bootstrap CA the node agents already trust. A self-signed
            # root is its own bundle.
            "upstreamAuthority": {"disk": {
                "enabled": True,
                "secret": {"data": {
                    "certificate": files["spire.caCertFile"],
                    "key": files["spire.caKeyFile"],
                    "bundle": files["spire.caCertFile"],
                }},
            }},
        },
        "spiffe-oidc-discovery-provider": {
            "config": {
                "jwksUri": "https://%s:%d/keys" % (vip, OIDC_NODEPORT),
                "additionalDomains": oidc_domains(sbom_dir, vip),
            },
            "tls": {"certManager": {"certificate": {"ipAddresses": [vip]}}},
        },
    }
    # Both keys: the Bitnami chart reads auth.postgresPassword for the
    # postgres superuser, and deploy-sbom.sh sets auth.password. One value.
    postgresql = {"auth": {"postgresPassword": password, "password": password}}
    if mirror:
        # The chart refuses an image outside its own registry unless told the
        # substitution is intended.
        postgresql["global"] = {"imageRegistry": mirror, "security": {"allowInsecureImages": True}}
    temporal = {"server": {"config": {"persistence": {
        "default": {"sql": {"password": password}},
        "visibility": {"sql": {"password": password}},
    }}}}
    if mirror:
        for key, repo in TEMPORAL_IMAGES.items():
            temporal.setdefault(key, {})["image"] = {"repository": mirror_ref(repo, mirror)}
    # Stage 6 reads the name and VIP. The image references are the resolved
    # ones, for the operator. Every value is validated above (DNS label, IP
    # address, reference characters), so none needs quoting.
    site_env = {
        "SITE_NAME": site_name,
        "SITE_VIP": vip,
        "SITE_IMAGE_CONTROLLER": flat["images.controller"],
        "SITE_IMAGE_PROFILES": flat["images.profiles"],
    }
    outputs = {
        "bootstrap-config.json": bootstrap,
        "controller.yaml": controller,
        "spire.yaml": spire,
        "postgresql.yaml": postgresql,
        "temporal.yaml": temporal,
        ENV_FILE: site_env,
    }
    if mirror:
        repo, tag = capnico_image(sbom_dir)
        outputs[CAPNICO_KUSTOMIZATION] = {
            "resources": ["capnico.yaml"],
            "images": [{"name": repo, "newName": mirror_ref(repo, mirror), "newTag": tag}],
        }
    return outputs


# --- inventory ---------------------------------------------------------------

_IMAGE_LINE_RE = re.compile(r"^\s*(?:-\s+)?image:\s*[\"']?([^\s\"']+)[\"']?\s*$", re.M)
_IMAGE_FLAG_RE = re.compile(r"--[a-z-]*image=[\"']?([^\s\"']+)")


def extract_images(manifests):
    """Image references in rendered manifests: `image:` fields, list items
    included, and `--*image=` flags, which operators use for the images they
    start. Deduplicated, sorted."""
    found = set(_IMAGE_LINE_RE.findall(manifests))
    found.update(_IMAGE_FLAG_RE.findall(manifests))
    return sorted(found)


def same_image(ref, other):
    """Two references name one image when their digests match, or the
    strings do."""
    d1 = DIGEST_RE.search(ref)
    d2 = DIGEST_RE.search(other)
    if d1 and d2:
        return d1.group(0) == d2.group(0)
    return ref == other


def helm_items(sbom_dir):
    """The helm items of nke-deploy.yaml in list order, each with its chart
    source and values files, read through build/nke-sbom-items.py."""
    items_py = os.path.join(sbom_dir, "..", "..", "build", "nke-sbom-items.py")
    deploy = os.path.join(sbom_dir, "nke-sbom", "nke-deploy.yaml")
    out = run_tool([sys.executable, items_py, "items", deploy], "listing the deploy items")
    items = []
    for line in out.splitlines():
        name, kind = line.split("\t")
        if kind != "helm":
            continue
        item_dir = os.path.join(sbom_dir, "nke-sbom", name)
        rel = run_tool([sys.executable, items_py, "release", os.path.join(item_dir, "release.yaml")],
                       "reading %s/release.yaml" % name)
        fields = rel.rstrip("\n").split("\x1f")
        # single, chart, version, repository, path, release, namespace, values, requires
        items.append({
            "name": name, "dir": item_dir, "chart": fields[1], "version": fields[2],
            "repository": fields[3], "path": fields[4], "release": fields[5],
            "namespace": fields[6], "values": [v for v in fields[7].split(",") if v],
        })
    return items


def run_tool(argv, what, env=None):
    try:
        proc = subprocess.run(argv, capture_output=True, text=True, env=env)
    except OSError as err:
        raise ConfigError("%s: %s: %s" % (what, argv[0], err))
    if proc.returncode != 0:
        tail = "\n".join(proc.stderr.strip().splitlines()[-8:])
        raise ConfigError("%s failed (exit %d):\n%s" % (what, proc.returncode, tail))
    return proc.stdout


def build_chart_dependencies(chart, helm_home):
    """Download the archives a path chart's Chart.lock pins into charts/,
    which a checkout does not carry. Same steps as build/use-nke-sbom.sh: the
    repositories are added to a throwaway Helm config so the caller's list is
    left alone."""
    if not os.path.isfile(os.path.join(chart, "Chart.lock")):
        return
    env = dict(os.environ,
               HELM_REPOSITORY_CONFIG=os.path.join(helm_home, "repositories.yaml"),
               HELM_REPOSITORY_CACHE=os.path.join(helm_home, "cache"))
    urls = set()
    with open(os.path.join(chart, "Chart.yaml")) as fh:
        for line in fh:
            m = re.match(r"^\s*repository:\s*(\S+)", line)
            if m and not m.group(1).startswith("oci://"):
                urls.add(m.group(1).strip("\"'"))
    for n, url in enumerate(sorted(urls), 1):
        run_tool(["helm", "repo", "add", "dependency-%d" % n, url], "adding chart repository %s" % url, env)
    run_tool(["helm", "dependency", "build", chart, "--skip-refresh"],
             "building chart dependencies of %s" % chart, env)


def template_item(item, out_dir, helm_home):
    """Render one helm item as stage 5 installs it: the item's values, then
    the site values file when stage 3 wrote one."""
    argv = ["helm", "template", item["release"], "--namespace", item["namespace"]]
    if item["path"]:
        chart = os.path.normpath(os.path.join(item["dir"], "..", item["path"]))
        build_chart_dependencies(chart, helm_home)
        argv.insert(3, chart)
    elif item["repository"].startswith("oci://"):
        argv.insert(3, "%s/%s" % (item["repository"].rstrip("/"), item["chart"]))
        argv += ["--version", item["version"]]
    elif item["repository"]:
        argv.insert(3, item["chart"])
        argv += ["--repo", item["repository"], "--version", item["version"]]
    else:
        raise ConfigError("item %s names no chart source" % item["name"])
    for values in item["values"]:
        argv += ["--values", os.path.join(item["dir"], values)]
    site_values = os.path.join(out_dir, "%s.yaml" % item["name"])
    if os.path.isfile(site_values):
        argv += ["--values", site_values]
    env = dict(os.environ,
               HELM_REPOSITORY_CONFIG=os.path.join(helm_home, "repositories.yaml"),
               HELM_REPOSITORY_CACHE=os.path.join(helm_home, "cache"))
    return run_tool(argv, "helm template of item %s" % item["name"], env)


def inventory_images(flat, sbom_dir, out_dir):
    """The lines of images.txt: notes first, then the sorted references."""
    mirror = flat.get("registry.mirror")
    controller_ref = flat["images.controller"]
    notes = []
    images = set()

    skip = os.environ.get("IMAGES_TXT_SKIP") or os.environ.get("DRY_RUN")
    if skip:
        notes.append("skipped: helm template not run (%s); the chart images are missing from this list"
                     % ("IMAGES_TXT_SKIP=1" if os.environ.get("IMAGES_TXT_SKIP") else "DRY_RUN"))
    elif not shutil.which("helm"):
        raise ConfigError("helm not found on PATH; it renders the charts for images.txt. Set IMAGES_TXT_SKIP=1 to skip.")
    else:
        helm_home = tempfile.mkdtemp(prefix="describe-site-helm.")
        try:
            for item in helm_items(sbom_dir):
                for ref in extract_images(template_item(item, out_dir, helm_home)):
                    if same_image(ref, controller_ref):
                        continue
                    images.add(ref)
        finally:
            shutil.rmtree(helm_home, ignore_errors=True)

    repo, tag = capnico_image(sbom_dir)
    capnico = "%s:%s" % (repo, tag)
    images.add(mirror_ref(capnico, mirror) if mirror else capnico)

    capi = tool_version(sbom_dir, "CAPI_VERSION")
    cm = tool_version(sbom_dir, "CERT_MANAGER_VERSION")
    for tmpl in CAPI_IMAGES:
        images.add(tmpl.format(capi=capi))
    for tmpl in CERT_MANAGER_IMAGES:
        images.add(tmpl.format(cm=cm))
    if mirror:
        notes.append("registry.mirror repoints the postgresql, temporal, spire and capnico images. The rest keep "
                     "their upstream names: clusterctl installs Cluster API and cert-manager in stage 4, and the "
                     "nktp chart's images are set in the nktp block. A hosts.toml on the nodes covers them.")

    lines = ["# External images the install pulls. Generated by describe_site.py; stage 2 pre-pulls each line."]
    lines += ["# " + n for n in notes]
    lines += sorted(images)
    return lines


def release_spec(install_dir, release_file, command, *args):
    """One release_spec.py subcommand, by the CLI contract."""
    return run_tool([sys.executable, os.path.join(install_dir, "release_spec.py"), command, release_file] + list(args),
                    "release_spec.py %s %s" % (command, release_file))


def build_bom(install_dir, release_file, controller_ref):
    """CycloneDX 1.5: the deploy path as the root component, one component per
    spec item. Chart items are applications with the chart version; the
    controller image is a container. Every hash is the ref's digest. The
    nke:deploy-item property keys a component to its deploy item."""
    meta = {}
    for line in release_spec(install_dir, release_file, "meta").splitlines():
        key, _, value = line.partition("=")
        meta[key] = value[1:-1].replace("'\\''", "'")
    components = []
    for line in release_spec(install_dir, release_file, "items").splitlines():
        name, kind, ref, version = line.split("\t")
        digest = DIGEST_RE.search(ref)
        component = {
            "type": "container" if kind == "image" else "application",
            "bom-ref": "nke:%s" % name,
            "name": name,
            "hashes": [{"alg": "SHA-256", "content": digest.group(0)[len("sha256:"):]}],
            "properties": [
                {"name": "nke:deploy-item", "value": name},
                {"name": "nke:type", "value": kind},
                {"name": "nke:ref", "value": ref},
            ],
        }
        if version:
            component["version"] = version
        components.append(component)
    return {
        "bomFormat": "CycloneDX",
        "specVersion": "1.5",
        "version": 1,
        "metadata": {
            "component": {
                "type": "application",
                "bom-ref": "nke:deploy-path",
                "name": "controller/deploy/nke-sbom",
                "version": meta.get("SOURCE_TAG", ""),
                "properties": [
                    {"name": "nke:release", "value": meta.get("RELEASE", "")},
                    {"name": "nke:lane", "value": meta.get("LANE", "")},
                    {"name": "nke:scope", "value": meta.get("SCOPE", "")},
                    {"name": "nke:controller-image", "value": controller_ref},
                ],
            },
        },
        "components": components,
    }


# --- output ------------------------------------------------------------------

def to_yaml(node, indent=0):
    """Emit a dict/list/scalar tree as YAML. Every scalar is JSON-quoted,
    which YAML accepts as a double-quoted scalar, so PEM blocks survive.
    A list of flat maps is emitted as `- key: value` entries."""
    pad = "  " * indent
    out = []
    if isinstance(node, dict):
        for key, value in node.items():
            if isinstance(value, (dict, list)):
                out.append("%s%s:" % (pad, key))
                out.append(to_yaml(value, indent + 1))
            else:
                out.append("%s%s: %s" % (pad, key, json.dumps(value)))
    elif isinstance(node, list):
        for value in node:
            if isinstance(value, dict):
                if any(isinstance(v, (dict, list)) for v in value.values()):
                    raise ConfigError("nested collections in list maps are not emitted")
                first = True
                for key, scalar in value.items():
                    out.append("%s%s%s: %s" % (pad, "- " if first else "  ", key, json.dumps(scalar)))
                    first = False
            elif isinstance(value, list):
                raise ConfigError("nested lists are not emitted")
            else:
                out.append("%s- %s" % (pad, json.dumps(value)))
    else:
        out.append("%s%s" % (pad, json.dumps(node)))
    return "\n".join(out)


def to_env(node):
    """Emit a flat dict as KEY=value lines a shell can source."""
    return "\n".join("%s=%s" % (key, value) for key, value in node.items())


def write_private(path, text):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as fh:
        fh.write(text)
        if not text.endswith("\n"):
            fh.write("\n")
    os.chmod(path, 0o600)


def key_paths(node, prefix=""):
    paths = []
    if isinstance(node, dict):
        for key, value in node.items():
            paths.extend(key_paths(value, "%s%s." % (prefix, key)))
    else:
        paths.append(prefix.rstrip("."))
    return paths


def redacted(path, value):
    if SECRET_KEY_RE.search(path.rsplit(".", 1)[-1]):
        return "<redacted>"
    if isinstance(value, str) and "-----BEGIN" in value:
        return "<pem>"
    return value


def note(msg):
    print("describe-site: %s" % msg, file=sys.stderr)


# --- commands ----------------------------------------------------------------

def load_config(path):
    try:
        with open(path) as fh:
            text = fh.read()
    except OSError as err:
        raise ConfigError("reading %s: %s" % (path, err))
    data = parse_site_config(text, path)
    return validate(data, os.path.dirname(os.path.abspath(path)))


def remove_stale(path, why):
    if os.path.exists(path):
        os.remove(path)
        note("%s removed; %s" % (os.path.basename(path), why))


def cmd_render(args):
    flat, files, passthrough = load_config(args.config)
    install_dir = os.path.dirname(os.path.abspath(__file__))
    os.makedirs(args.out, mode=0o700, exist_ok=True)
    os.chmod(args.out, 0o700)
    password, origin = resolve_password(flat, args.out)
    outputs = build(flat, files, args.sbom_dir, password)
    write_private(os.path.join(args.out, PASSWORD_FILE), password)
    for name, tree in outputs.items():
        if name.endswith(".json"):
            text = json.dumps(tree, indent=2)
        elif name == ENV_FILE:
            text = to_env(tree)
        else:
            text = to_yaml(tree)
        write_private(os.path.join(args.out, name), text)
    # An optional file is written when its section is present and removed
    # when it is not, so a stale copy from an earlier run cannot reach helm.
    for name in sorted(PASSTHROUGH.values()):
        path = os.path.join(args.out, name)
        if name in passthrough:
            write_private(path, passthrough[name])
        else:
            remove_stale(path, "the config has no %s block" % _section_of(name))
    if CAPNICO_KUSTOMIZATION not in outputs:
        remove_stale(os.path.join(args.out, CAPNICO_KUSTOMIZATION), "the config sets no registry.mirror")
    note("db password %s" % origin)
    note("images.controller %s: %s" % (flat["images.controller.origin"], flat["images.controller"]))

    # The inventory, after the values files: helm template reads them.
    lines = inventory_images(flat, args.sbom_dir, args.out)
    write_private(os.path.join(args.out, IMAGES_FILE), "\n".join(lines))
    if args.release_file:
        bom = build_bom(install_dir, args.release_file, flat["images.controller"])
        write_private(os.path.join(args.out, BOM_FILE), json.dumps(bom, indent=2))
    else:
        remove_stale(os.path.join(args.out, BOM_FILE), "no release spec was given")
        note("%s not written: no release spec. Set RELEASE_FILE." % BOM_FILE)

    print("site config: %s" % args.config)
    print("out dir:     %s" % args.out)
    for name, tree in outputs.items():
        print("%s:" % name)
        for path in key_paths(tree):
            value = tree
            for part in path.split("."):
                value = value[part] if isinstance(value, dict) else value
            print("  %s: %s" % (path, redacted(path, value)))
    for name, text in passthrough.items():
        print("%s: passed through, top-level keys %s" % (name, ", ".join(top_level_keys(text))))
    entries = [l for l in lines if not l.startswith("#")]
    print("%s: %d images%s" % (IMAGES_FILE, len(entries), "; see the notes in the file" if len(lines) - len(entries) > 1 else ""))
    if args.release_file:
        print("%s: %d components" % (BOM_FILE, len(bom["components"])))
    print(SCHEMA_NOTE)


def cmd_keys(args):
    flat, files, passthrough = load_config(args.config)
    outputs = build(flat, files, args.sbom_dir, "x")
    for name, tree in outputs.items():
        for path in key_paths(tree):
            print("%s %s" % (name, path))
    for name, text in passthrough.items():
        for key in top_level_keys(text):
            print("%s %s" % (name, key))


def top_level_keys(text):
    return re.findall(r"^([A-Za-z_][A-Za-z0-9_.-]*):", text, re.M)


def _section_of(filename):
    return [s for s, f in PASSTHROUGH.items() if f == filename][0]


# Top-level keys each values file must carry. check re-reads the files
# without a YAML parser, so it looks for these at column 0.
EXPECTED_TOP = {
    "controller.yaml": ["image", "profiles", "appDb", "nkeOIDCAuth", "spiffe"],
    "spire.yaml": ["global", "spire-server", "spiffe-oidc-discovery-provider"],
    "postgresql.yaml": ["auth"],
    "temporal.yaml": ["server"],
    CAPNICO_KUSTOMIZATION: ["resources", "images"],
}
EXPECTED_JSON = ["ClusterName", "Nico.Endpoint", "Nico.OrgID", "Nico.Token", "Nico.APIName",
                 "Registry.Server", "Registry.Username", "Registry.Token",
                 "Tenant.User", "Tenant.Password"]


def cmd_check(args):
    failures = 0

    def fail(msg):
        nonlocal failures
        failures += 1
        print("FAIL  %s" % msg)

    if not os.path.isdir(args.out):
        fail("out dir %s does not exist" % args.out)
        return 1
    print("PASS  out dir %s exists" % args.out)
    for name in OUTPUT_FILES + [PASSWORD_FILE, IMAGES_FILE]:
        path = os.path.join(args.out, name)
        if not os.path.isfile(path):
            fail("%s is missing" % path)
            continue
        if os.stat(path).st_mode & 0o077:
            fail("%s is not mode 0600" % path)
        if name.endswith(".json"):
            try:
                with open(path) as fh:
                    tree = json.load(fh)
            except ValueError as err:
                fail("%s does not parse: %s" % (name, err))
                continue
            for dotted in EXPECTED_JSON:
                node = tree
                for part in dotted.split("."):
                    node = node.get(part) if isinstance(node, dict) else None
                if node is None or node == "":
                    fail("%s: %s is missing or empty" % (name, dotted))
            print("PASS  %s parses with %d fields" % (name, len(key_paths(tree))))
        elif name == PASSWORD_FILE:
            with open(path) as fh:
                if not fh.read().strip():
                    fail("%s is empty" % name)
        elif name == IMAGES_FILE:
            with open(path) as fh:
                lines = [l.strip() for l in fh if l.strip()]
            entries = [l for l in lines if not l.startswith("#")]
            skipped = [l for l in lines if l.startswith("# skipped:")]
            if not entries:
                fail("%s lists no image" % name)
            elif skipped:
                print("WARN  %s has %d images and a skip note: %s" % (name, len(entries), skipped[0][2:]))
            else:
                print("PASS  %s lists %d images" % (name, len(entries)))
        elif name == ENV_FILE:
            with open(path) as fh:
                env = dict(line.rstrip("\n").split("=", 1) for line in fh if "=" in line)
            for key in ENV_KEYS:
                if not env.get(key):
                    fail("%s: %s is missing or empty" % (name, key))
            print("PASS  %s has %s" % (name, ", ".join(ENV_KEYS)))
        else:
            with open(path) as fh:
                text = fh.read()
            top = re.findall(r"^([A-Za-z][A-Za-z0-9_-]*):", text, re.M)
            for key in EXPECTED_TOP[name]:
                if key not in top:
                    fail("%s: top-level key %s is missing" % (name, key))
            if re.search(r': ""$', text, re.M) or re.search(r'^\s*- ""$', text, re.M):
                fail("%s has an empty value" % name)
            print("PASS  %s has keys %s" % (name, ", ".join(top)))
    for name in OPTIONAL_FILES:
        path = os.path.join(args.out, name)
        if not os.path.isfile(path):
            if name in PASSTHROUGH.values():
                print("PASS  %s absent; the %s release uses the committed values" % (name, _section_of(name)))
            else:
                print("PASS  %s absent; no registry.mirror" % name)
            continue
        if os.stat(path).st_mode & 0o077:
            fail("%s is not mode 0600" % path)
        with open(path) as fh:
            top = top_level_keys(fh.read())
        if not top:
            fail("%s has no top-level key" % name)
        for key in EXPECTED_TOP.get(name, []):
            if key not in top:
                fail("%s: top-level key %s is missing" % (name, key))
        print("PASS  %s has keys %s" % (name, ", ".join(top)))
    bom_path = os.path.join(args.out, BOM_FILE)
    if not os.path.isfile(bom_path):
        print("WARN  %s absent; it needs a release spec" % BOM_FILE)
    else:
        try:
            with open(bom_path) as fh:
                bom = json.load(fh)
            ok = bom.get("bomFormat") == "CycloneDX" and bom.get("specVersion") == "1.5" and bom.get("components")
        except ValueError as err:
            ok = False
            bom = {"error": str(err)}
        if ok:
            print("PASS  %s has %d components" % (BOM_FILE, len(bom["components"])))
        else:
            fail("%s is not a CycloneDX 1.5 document with components: %s" % (BOM_FILE, bom.get("error", "")))
    return 1 if failures else 0


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="command", required=True)
    p = sub.add_parser("render")
    p.add_argument("--config", required=True)
    p.add_argument("--out", required=True)
    p.add_argument("--sbom-dir", required=True)
    p.add_argument("--release-file", default="",
                   help="nke-release.yaml; the BOM is written from its items. Empty: no BOM.")
    p = sub.add_parser("check")
    p.add_argument("--out", required=True)
    p = sub.add_parser("keys")
    p.add_argument("--config", required=True)
    p.add_argument("--sbom-dir", required=True)
    args = parser.parse_args(argv)
    try:
        if args.command == "render":
            cmd_render(args)
        elif args.command == "keys":
            cmd_keys(args)
        else:
            return cmd_check(args)
    except ConfigError as err:
        die(str(err))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
