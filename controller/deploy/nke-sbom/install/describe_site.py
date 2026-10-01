#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Render site-config.yaml into the files stage 5 installs from.

  describe_site.py render --config site-config.yaml --out DIR --sbom-dir controller/deploy
  describe_site.py check  --out DIR
  describe_site.py keys   --config site-config.yaml --sbom-dir controller/deploy

render writes six files to DIR, mode 0600:

  bootstrap-config.json   the context build/render-template.py renders the
                          nke-base-config template item against
  controller.yaml         --values-for controller=...
  spire.yaml              --values-for spire=...
  postgresql.yaml         --values-for postgresql=...
  temporal.yaml           --values-for temporal=...
  site.env                SITE_NAME and SITE_VIP for stage 6; no secrets

The derivations follow controller/hack/ci/deploy-sbom.sh, which does the same
for the e2e lanes from environment variables. The upstream CA values follow
cli/internal/bootstrap/cwedeploy (upstreamCAArgs): the chart takes the PEM
contents, so they are inlined into spire.yaml. Nothing here writes a Secret
manifest; stage 5 renders those at apply time and deletes them.

Standard library only. PyYAML is not guaranteed, so this reads a small YAML
subset: two levels, `section:` then `  key: value`, scalars bare or quoted,
`#` comments. Anything else is refused by name. Every required key must be a
non-empty scalar, as render-template.py requires of the BootstrapConfig.

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
import sys

SCHEMA_NOTE = "The site-config.yaml schema is unstable until it is folded into BootstrapConfig."

# Required keys and the one optional key. A key not listed here is an error.
REQUIRED = [
    "site.name", "site.trustDomain", "site.vip", "site.arch",
    "spire.caCertFile", "spire.caKeyFile",
    "images.controller", "images.profiles",
    "nico.endpoint", "nico.orgId", "nico.token", "nico.apiName",
    "registry.server", "registry.username", "registry.token",
    "tenant.user", "tenant.password",
]
OPTIONAL = ["db.password"]

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

OUTPUT_FILES = ["bootstrap-config.json", "controller.yaml", "spire.yaml",
                "postgresql.yaml", "temporal.yaml", "site.env"]
ENV_FILE = "site.env"
ENV_KEYS = ["SITE_NAME", "SITE_VIP"]
PASSWORD_FILE = "db-password"

SECRET_KEY_RE = re.compile(r"password|token|key|secret", re.I)


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


def parse_site_config(text, source):
    """Parse the two-level subset into {section: {key: value-or-None}}."""
    data = {}
    section = None
    indent = None
    pending = None  # (section, key) whose value was empty on its line
    lines = text.splitlines()
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
    return data


# --- validation --------------------------------------------------------------

def flatten(data):
    return {"%s.%s" % (s, k): v for s, keys in data.items() for k, v in keys.items()}


def validate(data, config_dir):
    flat = flatten(data)
    known = set(REQUIRED) | set(OPTIONAL)
    for path in sorted(flat):
        if path not in known:
            raise ConfigError("unknown key %s. Known keys: %s" % (path, ", ".join(sorted(known))))
    for section in data:
        if section not in {p.split(".")[0] for p in known}:
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
    if "db.password" in flat and flat["db.password"] is None:
        raise ConfigError("db.password is null; omit the key to generate one.")

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
        split_image(flat[path], path)

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
    return flat, files


def split_image(ref, path):
    if "@" in ref:
        raise ConfigError("%s must be repository:tag, not a digest reference." % path)
    repo, sep, tag = ref.rpartition(":")
    if not sep or not repo or not tag or "/" in tag:
        raise ConfigError("%s must be repository:tag, got %r." % (path, ref))
    return repo, tag


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
    cwe_repo, cwe_tag = split_image(flat["images.controller"], "images.controller")
    prof_repo, prof_tag = split_image(flat["images.profiles"], "images.profiles")

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
        "image": {"repository": cwe_repo, "tag": cwe_tag},
        "profiles": {"image": {"repository": prof_repo, "tag": prof_tag}},
        "appDb": {"postgresPassword": password},
        "nkeOIDCAuth": {
            "discoveryURL": "https://%s:%d/.well-known/openid-configuration" % (vip, OIDC_NODEPORT),
        },
        "spiffe": {"trustDomain": td, "spire": {"serverAddress": vip}},
    }
    spire = {
        "global": {"spire": {"trustDomain": td, "clusterName": site_name}},
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
    temporal = {"server": {"config": {"persistence": {
        "default": {"sql": {"password": password}},
        "visibility": {"sql": {"password": password}},
    }}}}
    # Stage 6 sources this for `nkectl config set-site`. Both values are
    # validated above as a DNS label and an IP address, so they need no quoting.
    site_env = {"SITE_NAME": site_name, "SITE_VIP": vip}
    return {
        "bootstrap-config.json": bootstrap,
        "controller.yaml": controller,
        "spire.yaml": spire,
        "postgresql.yaml": postgresql,
        "temporal.yaml": temporal,
        ENV_FILE: site_env,
    }


# --- output ------------------------------------------------------------------

def to_yaml(node, indent=0):
    """Emit a dict/list/scalar tree as YAML. Every scalar is JSON-quoted,
    which YAML accepts as a double-quoted scalar, so PEM blocks survive."""
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
            if isinstance(value, (dict, list)):
                raise ConfigError("nested collections in lists are not emitted")
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


def cmd_render(args):
    flat, files = load_config(args.config)
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
    note("db password %s" % origin)
    print("site config: %s" % args.config)
    print("out dir:     %s" % args.out)
    for name, tree in outputs.items():
        print("%s:" % name)
        for path in key_paths(tree):
            value = tree
            for part in path.split("."):
                value = value[part]
            print("  %s: %s" % (path, redacted(path, value)))
    print(SCHEMA_NOTE)


def cmd_keys(args):
    flat, files = load_config(args.config)
    outputs = build(flat, files, args.sbom_dir, "x")
    for name, tree in outputs.items():
        for path in key_paths(tree):
            print("%s %s" % (name, path))


# Top-level keys each values file must carry. check re-reads the files
# without a YAML parser, so it looks for these at column 0.
EXPECTED_TOP = {
    "controller.yaml": ["image", "profiles", "appDb", "nkeOIDCAuth", "spiffe"],
    "spire.yaml": ["global", "spire-server", "spiffe-oidc-discovery-provider"],
    "postgresql.yaml": ["auth"],
    "temporal.yaml": ["server"],
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
    for name in OUTPUT_FILES + [PASSWORD_FILE]:
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
    return 1 if failures else 0


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="command", required=True)
    p = sub.add_parser("render")
    p.add_argument("--config", required=True)
    p.add_argument("--out", required=True)
    p.add_argument("--sbom-dir", required=True)
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
