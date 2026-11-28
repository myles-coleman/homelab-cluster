#!/usr/bin/env bash
#
# Prove the four NetworkPolicy components compile and that the parent's
# namespace transformer reaches them. A component that renders with no
# namespace would produce a policy ArgoCD applies to the Application's
# destination, which is not necessarily the service's namespace.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

fail=0
# Overridable so the negative control is reproducible: an empty value omits the
# namespace line and the script must fail. `NETPOL_PROBE_NAMESPACE= ./scripts/...`
probe_namespace="${NETPOL_PROBE_NAMESPACE-netpol-probe}"
for component in _strict _baseline _permissive _standalone; do
  tmp="$(mktemp -d)"
  cp -r "manifests/cluster/_netpols/${component}" "$tmp/comp"
  {
    echo 'apiVersion: kustomize.config.k8s.io/v1beta1'
    echo 'kind: Kustomization'
    [ -n "$probe_namespace" ] && echo "namespace: ${probe_namespace}"
    echo 'components:'
    echo '  - ./comp'
  } >"$tmp/kustomization.yaml"
  if ! out="$(kustomize build "$tmp" 2>&1)"; then
    printf 'FAIL  %-12s does not build:\n%s\n' "$component" "$out"
    fail=1
    rm -rf "$tmp"
    continue
  fi
  policies="$(printf '%s' "$out" | grep -c '^kind: NetworkPolicy' || true)"
  namespaced="$(printf '%s' "$out" | grep -c "^  namespace: ${probe_namespace}$" || true)"
  if [ "$policies" -eq 0 ]; then
    printf 'FAIL  %-12s renders no NetworkPolicy\n' "$component"
    fail=1
  elif [ "$namespaced" -lt "$policies" ]; then
    printf 'FAIL  %-12s renders %s policies but only %s in the parent namespace\n' \
      "$component" "$policies" "$namespaced"
    fail=1
  else
    printf 'PASS  %-12s %s policy/policies, namespace inherited\n' "$component" "$policies"
  fi
  rm -rf "$tmp"
done

if [ "$fail" -ne 0 ]; then
  echo "check-netpol-components: FAIL"
  exit 1
fi
echo "check-netpol-components: PASS"
