#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

usage() {
  cat <<'HELP'
Usage: demo.sh --site NAME --token-file PATH
       demo.sh --site NAME --token-file PATH --inject-outage \
         --target ID --sentinel ID --disposable-target NAME --management-context CONTEXT

Requires nkectl, jq, yq (Mike Farah v4), python3; fault mode also needs kubectl
and Docker access to the disposable CAPD management fixture. The token file
contains an allowed CWE bearer token. Never pass a token on the command line.
The selected site's URL must be directly reachable (a local port-forward works).
Fault mode requires the target CAPI Cluster annotation
site-health.nke.nvidia.com/disposable=true, and never chooses a target for you.
HELP
}
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
site="" token_file="" inject=false target="" sentinel="" disposable="" management=""
while (($#)); do
  case "$1" in
    --help|-h) usage; exit 0 ;;
    --inject-outage) inject=true; shift ;;
    --site|--token-file|--target|--sentinel|--disposable-target|--management-context)
      (($# >= 2)) || fail "missing value for $1"
      case "$1" in
        --site) site="$2" ;; --token-file) token_file="$2" ;;
        --target) target="$2" ;; --sentinel) sentinel="$2" ;;
        --disposable-target) disposable="$2" ;; --management-context) management="$2" ;;
      esac
      shift 2 ;;
    *) fail "unknown argument $1" ;;
  esac
done
[[ -n "${site}" && -r "${token_file}" ]] || { usage >&2; exit 2; }
for tool in nkectl jq yq python3; do command -v "${tool}" >/dev/null || fail "missing ${tool}"; done
if [[ "${inject}" == true ]]; then
  [[ -n "${target}" && -n "${sentinel}" && "${target}" != "${sentinel}" && -n "${disposable}" && -n "${management}" ]] || fail "fault mode requires explicit target, sentinel, disposable name and management context"
  for tool in kubectl docker; do command -v "${tool}" >/dev/null || fail "missing ${tool}"; done
fi
scratch="$(mktemp -d)"
staged=()
manifest=/etc/kubernetes/manifests/kube-apiserver.yaml
backup=/etc/kubernetes/site-health-demo-kube-apiserver.yaml
restore() {
  local container failed=0
  for container in "${staged[@]}"; do
    if ! docker exec "${container}" sh -c 'if test -e "$1"; then mv "$1" "$2"; else test -e "$2"; fi' restore "${backup}" "${manifest}"; then
      printf 'FAIL: restore manifest on %s\n' "${container}" >&2
      failed=1
    fi
  done
  return "${failed}"
}
cleanup() {
  local result=$?
  trap - EXIT INT TERM
  if ((${#staged[@]})); then
    if restore && wait_state True; then
      printf 'PASS: cleanup restored target API reachability\n'
    else
      printf 'FAIL: cleanup recovery; inspect %s on the recorded target containers\n' "${backup}" >&2
      result=1
    fi
  fi
  rm -rf "${scratch}"
  exit "${result}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
nkectl config view | yq -o=json >"${scratch}/config.json"
api_url="$(jq -er --arg site "${site}" '.sites[$site].url' "${scratch}/config.json")"
export SITE_HEALTH_SITE="${site}" SITE_HEALTH_URL="${api_url}" SITE_HEALTH_TOKEN_FILE="${token_file}"

# Python keeps credentials out of argv and provides an isolated, credential-free
# CLI config for denial controls. Output never includes response bodies/tokens.
cat >"${scratch}/compare.py" <<'PY'
import http.server, json, os, pathlib, subprocess, tempfile, threading, urllib.error, urllib.request
site, base = os.environ['SITE_HEALTH_SITE'], os.environ['SITE_HEALTH_URL'].rstrip('/')
token = pathlib.Path(os.environ['SITE_HEALTH_TOKEN_FILE']).read_text().strip()
assert token and '\n' not in token, 'token file must contain one token'
def run(*args, env=None):
    return subprocess.check_output(['nkectl', *args], env=env, timeout=60, stderr=subprocess.DEVNULL)
def request(bearer):
    headers = {'Authorization': 'Bearer '+bearer} if bearer else {}
    try:
        with urllib.request.urlopen(urllib.request.Request(base+'/api/clusters', headers=headers), timeout=30) as r:
            return r.status, r.read()
    except urllib.error.HTTPError as e:
        return e.code, b''
def projection(rows):
    return sorted(({'id': r['id'], 'name': r['name'], 'condition':r.get('conditions',{}).get('apiServerReachable')} for r in rows), key=lambda r:r['id'])
def status(r):
    c=r['conditions']
    for key, name in [('deleting','Deleting'), ('updateFailed','UpdateFailed')]:
        if (c.get(key) or {}).get('status')=='True': return name
    if c.get('updateInProgress') is not None: return 'Updating'
    for key, name in [('controlPlaneUpgrading','Upgrading'),('cpNodesRecycling','Recycling'),('provisioning','Provisioning'),('provisioned','Provisioned')]:
        if (c.get(key) or {}).get('status')=='True': return name
    return 'Unknown'
mode=os.environ.get('SITE_HEALTH_CHECK','parity')
if mode=='auth':
    for invalid in ['', 'example-invalid-token']:
        code,_=request(invalid)
        assert code==401, 'raw API must reject missing/invalid credentials with 401'
        seen=[]
        class DenialProxy(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                code, body=request(invalid)
                seen.append(code)
                self.send_response(code); self.end_headers(); self.wfile.write(body)
            def log_message(self,*args): pass
        server=http.server.ThreadingHTTPServer(('127.0.0.1',0),DenialProxy)
        thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
        try:
            with tempfile.TemporaryDirectory() as directory:
                path=pathlib.Path(directory)/'nkectl';path.mkdir()
                (path/'config.yaml').write_text(json.dumps({'sites':{'denied':{'url':f'http://127.0.0.1:{server.server_port}','kubeconfig':''}},'current-site':'denied'}))
                result=subprocess.run(['nkectl','--site','denied','get','clusters'],env={**os.environ,'XDG_CONFIG_HOME':directory},stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=60)
                assert result.returncode!=0 and seen==[401], 'CLI must receive denial from CWE'
        finally:
            server.shutdown();server.server_close();thread.join()
    assert request(token)[0]==200, 'allowed raw API caller failed'
    run('--site',site,'get','clusters','-o','json')
else:
    code,body=request(token);assert code==200, 'allowed raw API caller failed'
    before=json.loads(body)['clusters']
    for fmt in ['json','yaml']:
        data=run('--site',site,'get','clusters','-o',fmt)
        if fmt=='yaml': data=subprocess.check_output(['yq','-o=json'],input=data,timeout=15)
        assert projection(before)==projection(json.loads(data)), fmt+' differs from API'
    lines=run('--site',site,'get','clusters').decode().strip().splitlines()
    assert lines[0].rstrip().endswith('API REACHABLE') and len(lines)==len(before)+1, 'table header/row count differs'
    rows={}
    for line in lines[1:]:
        fields=line.split();assert len(fields)==7,'table column count differs';rows[fields[4]]=fields
    for r in before:
        fields=rows[r['id']]
        reach=(r['conditions'].get('apiServerReachable') or {}).get('status')
        if reach not in ['True','False']: reach='Unknown'
        assert fields[0]==r['name'] and fields[3]==status(r) and fields[6]==reach,'table differs from API'
    code,body=request(token);assert code==200 and projection(before)==projection(json.loads(body)['clusters']),'API changed during comparison'
PY
parity() {
  local deadline=$((SECONDS+300)) matches=0
  while ((SECONDS < deadline)); do
    if python3 "${scratch}/compare.py" 2>/dev/null; then matches=$((matches+1)); else matches=0; fi
    if ((matches >= 2)); then printf 'PASS: API and CLI table/JSON/YAML agree\n'; return 0; fi
    sleep 5
  done
  fail "API/CLI comparison did not settle within five minutes"
}
wait_state() {
  local want="$1" deadline=$((SECONDS+300)) data
  while ((SECONDS < deadline)); do
    data="$(nkectl --site "${site}" get clusters -o json)" || return 1
    jq -e --arg target "${target}" --arg sentinel "${sentinel}" \
      '[.[]|select(.id==$target or .id==$sentinel)]|length==2' <<<"${data}" >/dev/null || return 1
    # The sentinel is an invariant throughout the outage, including settling.
    jq -e --arg id "${sentinel}" '.[]|select(.id==$id)|.conditions.apiServerReachable.status=="True"' <<<"${data}" >/dev/null || return 1
    if jq -e --arg id "${target}" --arg want "${want}" '.[]|select(.id==$id)|.conditions.apiServerReachable.status==$want' <<<"${data}" >/dev/null; then return 0; fi
    sleep 5
  done
  return 1
}
SITE_HEALTH_CHECK=auth python3 "${scratch}/compare.py" 2>/dev/null || fail "API/CLI authentication controls"
printf 'PASS: existing API/CLI authentication enforcement\n'
parity
if [[ "${inject}" != true ]]; then
  printf 'SKIP: outage isolation and recovery (observation mode)\n'
  exit 0
fi
nkectl --site "${site}" get clusters -o json >"${scratch}/baseline.json"
jq -e --arg id "${target}" --arg name "${disposable}" '.[]|select(.id==$id)|.name==$name' "${scratch}/baseline.json" >/dev/null || fail "target ID/name mismatch"
wait_state True || fail "target and sentinel must initially be reachable"
kubectl --context "${management}" get clusters.cluster.x-k8s.io -A -o json >"${scratch}/clusters.json"
jq -e --arg name "${disposable}" '[.items[]|select(.metadata.name==$name and .metadata.annotations["site-health.nke.nvidia.com/disposable"]=="true" and .spec.infrastructureRef.kind=="DockerCluster")]|length==1' "${scratch}/clusters.json" >/dev/null || fail "target is not one explicitly marked disposable CAPD cluster"
namespace="$(jq -r --arg name "${disposable}" '.items[]|select(.metadata.name==$name)|.metadata.namespace' "${scratch}/clusters.json")"
kubectl --context "${management}" -n "${namespace}" get machines.cluster.x-k8s.io -l "cluster.x-k8s.io/cluster-name=${disposable},cluster.x-k8s.io/control-plane" -o json >"${scratch}/machines.json"
jq -e '.items|length>0 and all(.[]; .status.nodeRef.name != null)' "${scratch}/machines.json" >/dev/null || fail "all control-plane endpoints must be registered"
endpoints=()
while IFS= read -r node; do endpoints+=("${node}"); done < <(jq -r '.items[].status.nodeRef.name' "${scratch}/machines.json")
for container in "${endpoints[@]}"; do
  owner="$(docker inspect --format '{{ index .Config.Labels "io.x-k8s.kind.cluster" }}' "${container}")"
  [[ "${owner}" == "${disposable}" ]] || fail "container ownership mismatch"
  docker exec "${container}" test ! -e "${backup}" || fail "existing backup on ${container}"
  docker exec "${container}" test -f "${manifest}" || fail "missing manifest on ${container}"
done
printf 'Target %s (%s), endpoints: %s\n' "${target}" "${disposable}" "${endpoints[*]}"
for container in "${endpoints[@]}"; do
  staged+=("${container}")
  docker exec "${container}" mv "${manifest}" "${backup}"
done
# Used by disposable-fixture validation to prove EXIT recovery on assertion failure.
[[ "${SITE_HEALTH_DEMO_FAIL_AFTER_STAGING:-false}" != true ]] || fail "forced assertion after staging"
wait_state False || fail "target did not become unreachable while sentinel stayed reachable"
printf 'PASS: target False, sentinel True, both remain listed\n'
parity
restore || fail "restore target manifests"
wait_state True || fail "target did not recover"
parity
printf 'PASS: target recovered to True\n'
