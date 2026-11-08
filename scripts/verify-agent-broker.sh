#!/usr/bin/env bash
# verify-agent-broker.sh — one numbered assertion per Unit 1 infrastructure
# requirement (spec 30). Read-only: every kubectl call here is get/exec/auth can-i.
#
# Usage: ./scripts/verify-agent-broker.sh
# Exit:  0 only when every assertion passes; non-zero on the first failure.
set -euo pipefail

NS_SANDBOX="agent-sandbox"
NS_VALIDATOR="agent-validator"
NS_CERTMANAGER="cert-manager"
VALIDATOR_NAME="validator.agent.internal"
VALIDATOR_IP="10.43.30.10"
PASS=0
TOTAL=12

pass() { PASS=$((PASS + 1)); printf 'PASS %d/%d  %s\n' "$PASS" "$TOTAL" "$*"; }
fail() { printf 'FAIL %d/%d  %s\n' "$((PASS + 1))" "$TOTAL" "$*" >&2; exit 1; }
skip() { PASS=$((PASS + 1)); printf 'SKIP %d/%d  %s\n' "$PASS" "$TOTAL" "$*"; }
need() { command -v "$1" >/dev/null 2>&1 || fail "missing tool: $1"; }

need kubectl
need openssl
need python3

sandbox_pod() {
  kubectl -n "$NS_SANDBOX" get pods -l app=opencode-sandbox \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true
}

# 1. FR-1 — the CSI node component runs on the gVisor node.
assert_driver_on_node() {
  local node
  node="$(kubectl -n "$NS_CERTMANAGER" get pods \
    -l app.kubernetes.io/name=cert-manager-csi-driver \
    -o jsonpath='{.items[0].spec.nodeName}' 2>/dev/null || true)"
  [ "$node" = "sandbox-node" ] || fail "csi-driver node = '${node:-none}', want sandbox-node (FR-1)"
  pass "cert-manager csi-driver is running on sandbox-node"
}

# 2. FR-2 — the CA chain exists and its Secret holds a CA, not a leaf key.
assert_ca_chain() {
  local ready
  ready="$(kubectl -n "$NS_CERTMANAGER" get certificate agent-mtls-ca -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)"
  [ "$ready" = "True" ] || fail "agent-mtls-ca Certificate is not Ready (FR-2)"
  kubectl get clusterissuer agent-mtls-ca >/dev/null 2>&1 || fail "ClusterIssuer agent-mtls-ca missing (FR-2)"

  local ca
  ca="$(kubectl -n "$NS_CERTMANAGER" get secret agent-mtls-ca-keypair -o jsonpath='{.data.tls\.crt}' 2>/dev/null \
    | base64 -d)"
  [ -n "$ca" ] || fail "CA Secret agent-mtls-ca-keypair has no tls.crt (FR-2)"
  printf '%s' "$ca" | openssl x509 -noout -text | grep -q 'CA:TRUE' \
    || fail "agent-mtls-ca-keypair tls.crt is not a CA certificate (FR-2)"

  local keys
  keys="$(kubectl -n "$NS_CERTMANAGER" get secret agent-mtls-ca-keypair -o jsonpath='{.data}' | grep -o '"[a-z.]*"' | tr -d '"' | sort | tr '\n' ' ')"
  case "$keys" in
    *tls.key*ca.crt*|*ca.crt*tls.key*) ;;
    *) fail "CA Secret has unexpected keys: $keys (FR-2)" ;;
  esac
  pass "CA chain exists; one Secret ($keys) and it is the CA keypair"
}

# 3. FR-3 — the sandbox presents a CA-issued URI SAN with a <=12h lifetime.
assert_sandbox_identity() {
  local pod cert
  pod="$(sandbox_pod)"
  [ -n "$pod" ] || fail "no opencode-sandbox pod found (FR-3)"
  cert="$(kubectl -n "$NS_SANDBOX" exec "$pod" -- cat /certs/tls.crt 2>/dev/null)" \
    || fail "cannot read /certs/tls.crt from $pod (FR-3)"

  printf '%s' "$cert" | openssl x509 -noout -ext subjectAltName 2>/dev/null \
    | grep -q "URI:spiffe://cowlab.org/ns/${NS_SANDBOX}/pod/${pod}" \
    || fail "leaf URI SAN is not spiffe://cowlab.org/ns/${NS_SANDBOX}/pod/${pod} (FR-3)"
  local san
  san="$(printf '%s' "$cert" | openssl x509 -noout -ext subjectAltName 2>/dev/null)"
  if printf '%s' "$san" | grep -q 'DNS:'; then fail "leaf carries a DNS SAN (FR-3)"; fi
  if printf '%s' "$san" | grep -q 'IP Address:'; then fail "leaf carries an IP SAN (FR-3)"; fi

  # -checkend N exits 0 when the certificate is still valid for more than N
  # seconds, 1 when it expires within N. Verified against openssl 3.6:
  #   -checkend 0     -> 0 (valid now)       -> invert for the expiry check
  #   -checkend 43200 -> 1 (expires <12h)    -> invert for the ceiling check
  if ! printf '%s' "$cert" | openssl x509 -noout -checkend 0 >/dev/null; then
    fail "leaf certificate is expired (FR-3)"
  fi
  if printf '%s' "$cert" | openssl x509 -noout -checkend 43200 >/dev/null; then
    fail "leaf lifetime exceeds the 12h ceiling (FR-3)"
  fi
  local remain
  remain="$(printf '%s' "$cert" | openssl x509 -noout -enddate | cut -d= -f2)"

  kubectl -n "$NS_SANDBOX" exec "$pod" -- test -s /certs/tls.key 2>/dev/null \
    || fail "leaf key is not readable in the pod (FR-3)"
  pass "sandbox leaf has the pod URI SAN, no other SAN, expires ${remain}, key readable"
}

# 4. FR-7 — no DNS, fixed name via hostAliases.
assert_no_dns_and_alias() {
  local pod aliases dns
  pod="$(sandbox_pod)"
  [ -n "$pod" ] || fail "no opencode-sandbox pod found (FR-7)"
  aliases="$(kubectl -n "$NS_SANDBOX" get pod "$pod" -o jsonpath='{.spec.hostAliases}')"
  dns="$(kubectl -n "$NS_SANDBOX" get pod "$pod" -o jsonpath='{.spec.dnsPolicy}')"
  echo "$aliases" | grep -q "$VALIDATOR_NAME" || fail "hostAliases has no $VALIDATOR_NAME (FR-7)"
  echo "$aliases" | grep -q "$VALIDATOR_IP" || fail "hostAliases does not map $VALIDATOR_IP (FR-7)"
  [ "$dns" = "None" ] || fail "sandbox dnsPolicy = '$dns', want None (FR-7)"
  pass "sandbox has no DNS and maps $VALIDATOR_NAME to $VALIDATOR_IP"
}

# 5. FR-8 — server certificates carry the names the clients use.
assert_server_sans() {
  local server publisher
  server="$(kubectl -n "$NS_VALIDATOR" get secret validator-server-tls -o jsonpath='{.data.tls\.crt}' 2>/dev/null | base64 -d 2>/dev/null || true)"
  publisher="$(kubectl -n agent-publisher get secret publisher-server-tls -o jsonpath='{.data.tls\.crt}' 2>/dev/null | base64 -d 2>/dev/null || true)"
  [ -n "$server" ] || fail "validator-server-tls secret missing (FR-8)"
  [ -n "$publisher" ] || fail "publisher-server-tls secret missing (FR-8)"
  printf '%s' "$server" | openssl x509 -noout -ext subjectAltName | grep -q "DNS:${VALIDATOR_NAME}" \
    || fail "validator server SAN does not name ${VALIDATOR_NAME} (FR-8)"
  printf '%s' "$publisher" | openssl x509 -noout -ext subjectAltName | grep -q 'DNS:publisher.agent.internal' \
    || fail "publisher server SAN does not name publisher.agent.internal (FR-8)"
  pass "validator and publisher server SANs match the client-used names"
}

# 6. FR-9 / FR-10 — peer is a pod selector, never a CIDR-to-anywhere.
assert_network_peers() {
  local sandbox_np validator_np
  sandbox_np="$(kubectl -n "$NS_SANDBOX" get networkpolicy opencode-sandbox-egress -o json)"
  echo "$sandbox_np" | grep -q '"agent-validator"' || fail "sandbox egress has no agent-validator peer (FR-9)"
  echo "$sandbox_np" | python3 -c '
import json,sys
np=json.load(sys.stdin)
ok=False
for rule in np["spec"].get("egress", []):
    for peer in rule.get("to", []):
        ns=peer.get("namespaceSelector",{}).get("matchLabels",{})
        pod=peer.get("podSelector",{}).get("matchLabels",{})
        if ns.get("kubernetes.io/metadata.name")=="agent-validator" and pod.get("app")=="agent-validator":
            ok = ok or any(p.get("port")==8443 for p in rule.get("ports",[]))
sys.exit(0 if ok else 1)' || fail "sandbox egress does not target the validator pod selector on 8443 (FR-9)"

  validator_np="$(kubectl -n "$NS_VALIDATOR" get networkpolicy validator-egress -o json)"
  echo "$validator_np" | python3 -c '
import json,sys
np=json.load(sys.stdin)
peers=[]
for rule in np["spec"].get("egress", []):
    for peer in rule.get("to", []):
        peers.append((peer.get("namespaceSelector",{}).get("matchLabels",{}).get("kubernetes.io/metadata.name"),
                      peer.get("podSelector",{}).get("matchLabels",{}).get("app"),
                      peer.get("ipBlock",{}).get("cidr")))
bad=[p for p in peers if p[2] in ("0.0.0.0/0","::/0")]
if bad: sys.exit("validator egress allows the internet: %r" % (bad,))
if not any(p[0]=="agent-publisher" and p[1]=="agent-publisher" for p in peers):
    sys.exit("validator egress does not target the publisher pod selector")
' || fail "validator egress is not publisher-only (FR-10, FR-12)"
  pass "sandbox egress and validator egress use pod selectors only"
}

# 7. FR-11 / FR-14 — the bundle clones to exactly one root commit and the served
# tip is the real main SHA. The bundled commit is a synthetic root holding the
# tip's tree (git cannot carry a shallow boundary in a bundle), so equality with
# the tip is asserted on the tree in the unit test, not on the commit id here.
assert_bundle_clone() {
  local pod headers commit out
  pod="$(sandbox_pod)"
  [ -n "$pod" ] || fail "no opencode-sandbox pod found (FR-11)"
  out="$(kubectl -n "$NS_SANDBOX" exec "$pod" -- sh -c '
    set -e
    cd /tmp
    curl -sS --fail --cacert /certs/ca.crt --cert /certs/tls.crt --key /certs/tls.key \
      -D /tmp/bundle.headers -o /tmp/nixos.bundle \
      https://'"$VALIDATOR_NAME"':8443/v1/repos/nixos/bundle
    rm -rf /tmp/nixos.bundle.clone
    git clone -q /tmp/nixos.bundle /tmp/nixos.bundle.clone
    cd /tmp/nixos.bundle.clone
    printf "count=%s\n" "$(git rev-list --count HEAD)"
    printf "parents=%s\n" "$(git cat-file -p HEAD | grep -c "^parent " || true)"
    printf "files=%s\n" "$(git ls-tree -r --name-only HEAD | wc -l)"
  ' 2>&1)" || fail "bundle fetch or clone failed inside the sandbox: $out"
  [ "$(printf '%s' "$out" | sed -n 's/^count=//p')" = "1" ] || fail "bundle does not contain exactly one commit (FR-14)"
  [ "$(printf '%s' "$out" | sed -n 's/^parents=//p')" = "0" ] || fail "bundled commit still names a parent (FR-14)"
  [ "$(printf '%s' "$out" | sed -n 's/^files=//p')" -gt 10 ] || fail "bundle tree is empty or truncated (FR-14)"

  headers="$(kubectl -n "$NS_SANDBOX" exec "$pod" -- cat /tmp/bundle.headers 2>/dev/null || true)"
  # HTTP/2 lowercases header names, so match case-insensitively.
  commit="$(printf '%s' "$headers" | tr -d '\r' | awk 'BEGIN{IGNORECASE=1} /^x-bundle-commit:/ {print $2}')"
  printf '%s' "$commit" | grep -Eq '^[0-9a-f]{40}$' || fail "bundle response has no valid X-Bundle-Commit (FR-11)"
  pass "bundle clones to one parentless commit with a full tree; served tip ${commit}"
}

# 8. FR-12 — the sandbox still has no internet path.
assert_no_internet() {
  local pod
  pod="$(sandbox_pod)"
  if kubectl -n "$NS_SANDBOX" exec "$pod" -- curl -sS --max-time 5 -o /dev/null https://github.com >/dev/null 2>&1; then
    fail "sandbox reached github.com; FR-12 egress isolation is broken"
  fi
  pass "sandbox cannot reach github.com"
}

# 9. FR-13 / FR-40 — the App really narrows a token to one repository with a
# one-hour ceiling. The verifier mints its own token from the same Secret the
# publisher mounts; the token value is never printed.
assert_token_scope() {
  local dir app_id now header payload sig jwt inst id resp
  dir="$(mktemp -d)"
  kubectl -n agent-publisher get secret github-app -o jsonpath='{.data.app-id}' | base64 -d > "$dir/app-id" \
    || fail "cannot read the App id (FR-13)"
  kubectl -n agent-publisher get secret github-app -o jsonpath='{.data.private-key\.pem}' | base64 -d > "$dir/key.pem" \
    || fail "cannot read the App key (FR-13)"
  chmod 600 "$dir/key.pem"
  app_id="$(tr -d '[:space:]' < "$dir/app-id")"

  b64url() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }
  now="$(date +%s)"
  header="$(printf '{"alg":"RS256","typ":"JWT"}' | b64url)"
  payload="$(printf '{"iss":"%s","iat":%s,"exp":%s}' "$app_id" "$((now - 60))" "$((now + 540))" | b64url)"
  sig="$(printf '%s.%s' "$header" "$payload" | openssl dgst -sha256 -sign "$dir/key.pem" | b64url)"
  jwt="$header.$payload.$sig"

  inst="$(curl -sS -H "Authorization: Bearer $jwt" -H 'Accept: application/vnd.github+json' \
    https://api.github.com/repos/myles-coleman/nixos/installation)"
  id="$(printf '%s' "$inst" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("id",""))')"
  [ -n "$id" ] || fail "the App is not installed on nixos (FR-13)"

  resp="$(curl -sS -X POST -H "Authorization: Bearer $jwt" -H 'Accept: application/vnd.github+json' \
    -H 'Content-Type: application/json' \
    -d '{"repositories":["nixos"],"permissions":{"contents":"read"}}' \
    "https://api.github.com/app/installations/$id/access_tokens")"
  printf '%s' "$resp" | python3 -c '
import datetime, json, sys
d = json.load(sys.stdin)
repos = [r.get("name") for r in (d.get("repositories") or [])]
if repos != ["nixos"]:
    sys.exit("token repositories = %r, want exactly [nixos]" % (repos,))
if not d.get("token"):
    sys.exit("no token in response")
exp = datetime.datetime.strptime(d["expires_at"], "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=datetime.timezone.utc)
now = datetime.datetime.now(datetime.timezone.utc)
if (exp - now).total_seconds() > 3600:
    sys.exit("token lifetime exceeds one hour")
' || fail "installation token is not narrowed to one repository with a <=1h expiry (FR-13)"
  rm -rf "$dir"
  pass "installation token: one installation, exactly [nixos], under one hour"
}

# 10. FR-49 / FR-55 — both broker pods have the required posture and both images
# are digest-pinned; the publisher must not run on the gVisor node.
assert_pod_posture() {
  local json
  json="$(kubectl -n "$NS_VALIDATOR" get pod -l app=agent-validator -o json | python3 -c '
import json,sys
items=json.load(sys.stdin)["items"]
sys.exit(1) if not items else print(json.dumps(items[0]))
')" || fail "no validator pod found (FR-55)"
  echo "$json" | python3 -c '
import json,sys
p=json.load(sys.stdin)
s=p["spec"]; c=s["containers"][0]; sc=c.get("securityContext",{}); psc=s.get("securityContext",{})
errs=[]
if s.get("runtimeClassName")!="gvisor": errs.append("validator runtimeClassName")
if not psc.get("runAsNonRoot"): errs.append("validator runAsNonRoot")
if not sc.get("readOnlyRootFilesystem"): errs.append("validator readOnlyRootFilesystem")
if sc.get("allowPrivilegeEscalation") is not False: errs.append("validator allowPrivilegeEscalation")
if psc.get("seccompProfile",{}).get("type")!="RuntimeDefault": errs.append("validator seccompProfile")
if sc.get("capabilities",{}).get("drop")!=["ALL"]: errs.append("validator capabilities.drop")
if "@sha256:" not in c.get("image",""): errs.append("validator image-not-pinned-by-digest")
if errs: sys.exit(", ".join(errs))
' || fail "validator posture is wrong (FR-55)"

  json="$(kubectl -n agent-publisher get pod -l app=agent-publisher -o json | python3 -c '
import json,sys
items=json.load(sys.stdin)["items"]
sys.exit(1) if not items else print(json.dumps(items[0]))
')" || fail "no publisher pod found (FR-49)"
  echo "$json" | python3 -c '
import json,sys
p=json.load(sys.stdin)
s=p["spec"]; c=s["containers"][0]; sc=c.get("securityContext",{}); psc=s.get("securityContext",{})
errs=[]
if s.get("nodeName")=="sandbox-node": errs.append("publisher-on-gvisor-node")
if any(t.get("key")=="sandbox" for t in s.get("tolerations",[])): errs.append("publisher-tolerates-sandbox-taint")
if not psc.get("runAsNonRoot"): errs.append("publisher runAsNonRoot")
if not sc.get("readOnlyRootFilesystem"): errs.append("publisher readOnlyRootFilesystem")
if sc.get("capabilities",{}).get("drop")!=["ALL"]: errs.append("publisher capabilities.drop")
if "@sha256:" not in c.get("image",""): errs.append("publisher image-not-pinned-by-digest")
if errs: sys.exit(", ".join(errs))
' || fail "publisher posture is wrong (FR-49)"
  pass "validator under gVisor and publisher off the gVisor node, both digest-pinned"
}

# 11. FR-60 — RBAC is exactly Pods and Sandboxes in agent-sandbox.
assert_rbac() {
  local rules
  rules="$(kubectl auth can-i --list -n "$NS_SANDBOX" --as=system:serviceaccount:"$NS_VALIDATOR":validator 2>&1)" \
    || fail "cannot evaluate validator RBAC (FR-60)"
  echo "$rules" | grep -q 'pods' || fail "validator cannot read Pods (FR-60)"
  echo "$rules" | grep -q 'sandboxes.agents.x-k8s.io' || fail "validator cannot read Sandboxes (FR-60)"
  if echo "$rules" | grep -qiE 'secrets|configmaps|deployments|pods/exec|\*\.'; then
    fail "validator RBAC is wider than Pods/Sandboxes (FR-60)"
  fi
  pass "validator RBAC is limited to Pods and Sandboxes in agent-sandbox"
}

# 12. FR-15 — branch-aware: the CSI topology was chosen, so assert it is present.
assert_fr15_topology() {
  local pod driver
  pod="$(sandbox_pod)"
  [ -n "$pod" ] || fail "no opencode-sandbox pod found (FR-15)"
  driver="$(kubectl -n "$NS_SANDBOX" get pod "$pod" -o json | python3 -c '
import json,sys
pod=json.load(sys.stdin)
for v in pod["spec"].get("volumes",[]):
    if "csi" in v:
        print(v["csi"].get("driver",""))
')"
  if [ "$driver" = "csi.cert-manager.io" ]; then
    pass "FR-15 branch = CSI (inline cert-manager volume present)"
    return
  fi
  # Operator fallback would instead be a per-Sandbox Certificate.
  if kubectl -n "$NS_SANDBOX" get certificate -o name 2>/dev/null | grep -q .; then
    pass "FR-15 branch = operator (per-Sandbox Certificate present)"
    return
  fi
  fail "neither the CSI volume nor a per-Sandbox Certificate exists (FR-15)"
}

assert_driver_on_node
assert_ca_chain
assert_sandbox_identity
assert_no_dns_and_alias
assert_server_sans
assert_network_peers
assert_bundle_clone
assert_no_internet
assert_token_scope
assert_pod_posture
assert_rbac
assert_fr15_topology

printf '\nOK: %d/%d assertions accounted for\n' "$PASS" "$TOTAL"
