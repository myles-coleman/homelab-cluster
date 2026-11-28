#!/usr/bin/env bash
#
# Prove the admission policies in manifests/cluster/admission-policy/ actually
# fire, and that they do not fire on anything they should not.
#
# Three checks per rule:
#   1. a deliberately bad pod produces a warning (Audit/Warn) or a Forbidden
#      error (Deny) naming that rule;
#   2. an exempt pod produces neither;
#   3. every running pod, re-submitted under a fresh name, produces neither.
#
# Everything is `--dry-run=server`. Nothing is created and nothing is modified.
#
# Run it before promoting a rule and again after, so the promotion's effect is
# observed rather than assumed.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

PINNED_IMAGE="beebecomebigbee/openbooks:master@sha256:270c92ed679133374ad4c5bc2218b7a3f31bf4c3ec09a1e815add9f53be1c657"
UNPINNED_IMAGE="beebecomebigbee/openbooks:master"
# The probe namespace must not carry a Pod Security `enforce` label, or PSA
# rejects a privileged/hostNetwork probe before the ValidatingAdmissionPolicy
# sees it and the check cannot tell which control fired. `default` is unmanaged
# and unlabelled; every repo-managed namespace is now baseline or privileged.
PROBE_NAMESPACE="${VAP_PROBE_NAMESPACE:-default}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

failures=0
checks=0

declare -A ACTIONS
load_actions() {
  local name actions
  while IFS=$'\t' read -r name actions; do
    [ -n "$name" ] && ACTIONS["$name"]="$actions"
  done < <(kubectl get validatingadmissionpolicybindings \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.validationActions}{"\n"}{end}' 2>/dev/null || true)
}

action_of() {
  printf '%s' "${ACTIONS[$1]:-}"
}

load_actions

# submit FILE -> prints combined client output, non-zero when rejected.
submit() {
  kubectl create --dry-run=server -f "$1" 2>&1
}

# expect_rule_trigger POLICY FILE LABEL
expect_rule_trigger() {
  local policy="$1" file="$2" label="$3" out rc action
  out="$(submit "$file")" && rc=0 || rc=$?
  action="$(action_of "$policy")"
  checks=$((checks + 1))
  if [ "$rc" -ne 0 ]; then
    if printf '%s' "$out" | grep -q "$policy"; then
      printf 'PASS  %-45s rejected by %s\n' "$label" "$policy"
      return 0
    fi
    printf 'FAIL  %-45s rejected, but not by %s:\n%s\n' "$label" "$policy" "$out"
    failures=$((failures + 1))
    return 0
  fi
  if printf '%s' "$out" | grep -q "Warning:.*$policy"; then
    printf 'PASS  %-45s warned by %s (%s)\n' "$label" "$policy" "$action"
    return 0
  fi
  printf 'FAIL  %-45s admitted with no %s warning/denial (actions=%s)\n' \
    "$label" "$policy" "$action"
  printf '%s\n' "$out" | sed 's/^/      /'
  failures=$((failures + 1))
}

# expect_clean FILE LABEL
expect_clean() {
  local file="$1" label="$2" out rc
  out="$(submit "$file")" && rc=0 || rc=$?
  checks=$((checks + 1))
  if [ "$rc" -ne 0 ] || printf '%s' "$out" | grep -q '^Warning:'; then
    printf 'FAIL  %-45s expected clean, got:\n%s\n' "$label" "$out"
    failures=$((failures + 1))
    return 0
  fi
  printf 'PASS  %-45s clean\n' "$label"
}

pod() {
  # pod FILE NAMESPACE NAME [extra spec yaml lines]
  local file="$1" ns="$2" name="$3" extra="${4:-}"
  cat >"$file" <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: ${name}
  namespace: ${ns}
spec:
  restartPolicy: Never
  containers:
    - name: probe
      image: ${PINNED_IMAGE}
      command: ["true"]
${extra}
EOF
}

pod "$TMP/bad-privileged.yaml" "$PROBE_NAMESPACE" vap-probe-privileged '      securityContext:
        privileged: true'
pod "$TMP/exempt-privileged.yaml" longhorn-system vap-probe-privileged-exempt '      securityContext:
        privileged: true'

pod "$TMP/bad-hostnet.yaml" "$PROBE_NAMESPACE" vap-probe-hostnet '  hostNetwork: true'
pod "$TMP/exempt-hostnet.yaml" metallb-system vap-probe-hostnet-exempt '  hostNetwork: true'

pod "$TMP/bad-unpinned.yaml" "$PROBE_NAMESPACE" vap-probe-unpinned
sed -i "s|image: ${PINNED_IMAGE}|image: ${UNPINNED_IMAGE}|" "$TMP/bad-unpinned.yaml"
pod "$TMP/exempt-unpinned.yaml" argocd vap-probe-unpinned-exempt
sed -i "s|image: ${PINNED_IMAGE}|image: ${UNPINNED_IMAGE}|" "$TMP/exempt-unpinned.yaml"

pod "$TMP/bad-root.yaml" "$PROBE_NAMESPACE" vap-probe-root '      securityContext:
        runAsUser: 0'
pod "$TMP/exempt-root.yaml" cert-manager vap-probe-root-exempt '      securityContext:
        runAsUser: 0'

echo "== policy probes =="
expect_rule_trigger deny-privileged       "$TMP/bad-privileged.yaml" "privileged pod outside exemptions"
expect_clean                               "$TMP/exempt-privileged.yaml" "privileged pod in longhorn-system"
expect_rule_trigger deny-host-namespaces  "$TMP/bad-hostnet.yaml"    "hostNetwork pod outside exemptions"
expect_clean                               "$TMP/exempt-hostnet.yaml" "hostNetwork pod in metallb-system"
expect_rule_trigger deny-unpinned-images  "$TMP/bad-unpinned.yaml"   "unpinned image outside exemptions"
expect_clean                               "$TMP/exempt-unpinned.yaml" "unpinned image in argocd"
expect_rule_trigger require-nonroot       "$TMP/bad-root.yaml"       "runAsUser 0 outside exemptions"
expect_clean                               "$TMP/exempt-root.yaml"    "runAsUser 0 in cert-manager"

echo
echo "== fleet sweep =="

# Turn each running pod into a fresh Pod manifest. Keeping the spec and dropping
# the status/owner fields is what makes it a create rather than an update.
python3 - "$TMP/pods" <<'PY'
import json, os, subprocess, sys
out = sys.argv[1]
os.makedirs(out, exist_ok=True)
pods = json.loads(subprocess.check_output(["kubectl", "get", "pods", "-A", "-o", "json"]))
for p in pods["items"]:
    md = p["metadata"]
    spec = dict(p["spec"])
    spec.pop("nodeName", None)
    doc = {
        "apiVersion": "v1",
        "kind": "Pod",
        "metadata": {
            "name": md["name"] + "-vap-sweep",
            "namespace": md["namespace"],
            "labels": md.get("labels", {}),
        },
        "spec": spec,
    }
    with open(os.path.join(out, f'{md["namespace"]}__{md["name"]}.json'), "w") as fh:
        json.dump(doc, fh)
print(len(pods["items"]))
PY

# count_finding OUT -> "deny", "audit", or "none"
#
# A finding for a Deny rule is a real outage risk: that pod would not be
# recreatable. A finding for an Audit rule is the rule doing its job and is
# expected while the rule is non-blocking, so it is reported but not failed.
count_finding() {
  local out="$1" p action seen="none"
  for p in deny-privileged deny-host-namespaces deny-unpinned-images require-nonroot; do
    printf '%s' "$out" | grep -q "$p" || continue
    action="$(action_of "$p")"
    case "$action" in
      *Deny*) echo deny; return 0 ;;
      *) seen="audit" ;;
    esac
  done
  echo "$seen"
}

sweep_findings=0
sweep_audit=0
sweep_checked=0
while IFS= read -r -d '' f; do
  sweep_checked=$((sweep_checked + 1))
  out="$(submit "$f")" && rc=0 || rc=$?
  kind="$(count_finding "$out")"
  if [ "$kind" = "deny" ] || { [ "$rc" -ne 0 ] && printf '%s' "$out" | grep -q 'Error from server (Forbidden)'; }; then
    printf 'FAIL  %-45s would be rejected by a Deny rule\n' "$(basename "$f" .json)"
    printf '%s\n' "$out" | grep -E '^(Warning:|Error)' | sed 's/^/      /'
    sweep_findings=$((sweep_findings + 1))
  elif [ "$kind" = "audit" ]; then
    printf 'WARN  %-45s Audit finding (non-blocking)\n' "$(basename "$f" .json)"
    printf '%s\n' "$out" | grep '^Warning:' | sed 's/^/      /'
    sweep_audit=$((sweep_audit + 1))
  fi
done < <(find "$TMP/pods" -name '*.json' -print0)

checks=$((checks + 1))
if [ "$sweep_findings" -ne 0 ]; then
  printf 'FAIL  fleet sweep: %d Deny finding(s) across %d pods\n' "$sweep_findings" "$sweep_checked"
  failures=$((failures + 1))
elif [ "$sweep_audit" -ne 0 ]; then
  printf 'PASS  fleet sweep: 0 Deny, %d Audit finding(s) across %d pods\n' "$sweep_audit" "$sweep_checked"
else
  printf 'PASS  fleet sweep: 0 findings across %d pods\n' "$sweep_checked"
fi

echo
echo "== rendered pod sweep =="

# The live sweep cannot see a Pod manifest that exists only in dist/: Helm test
# hooks, one-shot Jobs that have been pruned, and anything that has not run yet.
# Those are exactly the pods a promotion would first reject, so submit them too.
python3 - "$TMP/rendered" <<'PY'
import glob, json, os, sys
import yaml
out = sys.argv[1]
os.makedirs(out, exist_ok=True)
n = 0
for path in sorted(glob.glob("manifests/dist/*/*.yaml")):
    try:
        docs = list(yaml.safe_load_all(open(path)))
    except Exception as exc:
        sys.exit(f"cannot parse {path}: {exc}")
    for doc in docs:
        if not doc or doc.get("kind") != "Pod":
            continue
        md = doc.setdefault("metadata", {})
        md["name"] = str(md.get("name", "pod")) + "-vap-rendered"
        ns = md.get("namespace", "default")
        with open(os.path.join(out, f"{ns}__{md['name']}.json"), "w") as fh:
            json.dump(doc, fh)
        n += 1
print(n)
PY

rendered_findings=0
rendered_audit=0
rendered_checked=0
while IFS= read -r -d '' f; do
  rendered_checked=$((rendered_checked + 1))
  out="$(submit "$f")" && rc=0 || rc=$?
  kind="$(count_finding "$out")"
  if [ "$kind" = "deny" ] || { [ "$rc" -ne 0 ] && printf '%s' "$out" | grep -q 'Error from server (Forbidden)'; }; then
    printf 'FAIL  %-45s would be rejected by a Deny rule\n' "$(basename "$f" .json)"
    printf '%s\n' "$out" | grep -E '^(Warning:|Error)' | sed 's/^/      /'
    rendered_findings=$((rendered_findings + 1))
  elif [ "$kind" = "audit" ]; then
    printf 'WARN  %-45s Audit finding (non-blocking)\n' "$(basename "$f" .json)"
    printf '%s\n' "$out" | grep '^Warning:' | sed 's/^/      /'
    rendered_audit=$((rendered_audit + 1))
  fi
done < <(find "$TMP/rendered" -name '*.json' -print0)

checks=$((checks + 1))
if [ "$rendered_findings" -ne 0 ]; then
  printf 'FAIL  rendered pod sweep: %d Deny finding(s) across %d pods\n' "$rendered_findings" "$rendered_checked"
  failures=$((failures + 1))
elif [ "$rendered_audit" -ne 0 ]; then
  printf 'PASS  rendered pod sweep: 0 Deny, %d Audit finding(s) across %d pods\n' "$rendered_audit" "$rendered_checked"
else
  printf 'PASS  rendered pod sweep: 0 findings across %d pods\n' "$rendered_checked"
fi

echo
if [ "$failures" -ne 0 ]; then
  echo "verify-admission-policy: FAIL ($failures of $checks checks)"
  exit 1
fi
echo "verify-admission-policy: PASS ($checks checks)"
