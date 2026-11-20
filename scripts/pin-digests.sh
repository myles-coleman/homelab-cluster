#!/usr/bin/env bash
#
# Resolve container image tags to manifest-list digests, and report them.
#
# This script NEVER writes to a manifest. It prints a TSV mapping that a human
# reviews and applies. That is deliberate: the pinning pass runs once, against
# real registries, and a throttled or silently-wrong resolution must be visible
# to the person applying it rather than baked into a file by a script.
#
# The renderer and CI never call this. Nothing in the steady-state path contacts
# a registry, which is what makes `dist/` a pure function of the repository.
#
# Usage:
#   ./scripts/pin-digests.sh [--scope plain|chart|all]
#
# Output: service <TAB> old-ref <TAB> pinned-ref <TAB> arm64
#
# Exit codes: 0 all resolved, 1 a reference failed to resolve or lacks arm64,
#             2 usage or missing tool.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER_KUSTOMIZATION="${ROOT}/manifests/cluster/kustomization.yaml"
SCOPE="all"

usage() {
  cat <<'EOF'
usage: pin-digests.sh [--scope plain|chart|all]

  --scope plain   only services with no helm-chart.yaml (hand-written manifests)
  --scope chart   only services rendered from a Helm chart
  --scope all     every service listed in resources: (default)
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --scope)
      [ "$#" -ge 2 ] || { echo "pin-digests: --scope needs a value" >&2; exit 2; }
      SCOPE="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "pin-digests: unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

case "$SCOPE" in
  plain|chart|all) ;;
  *) echo "pin-digests: invalid --scope '$SCOPE'" >&2; exit 2 ;;
esac

command -v kustomize >/dev/null 2>&1 || {
  echo "pin-digests: kustomize not found on PATH (try: nix-shell -p kustomize)" >&2
  exit 2
}
command -v crane >/dev/null 2>&1 || {
  echo "pin-digests: crane not found on PATH (try: nix-shell -p crane)" >&2
  exit 2
}

# refs miss the tag when a manifest writes `image: repo` with no tag. Kubernetes
# then defaults to :latest, so resolve it and emit repo@sha256:... which is
# valid and keeps the intent explicit.
resolve() {
  local ref="$1" digest manifest arch
  digest="$(crane digest "$ref" 2>/dev/null)" || return 1
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || return 1

  if manifest="$(crane manifest "$ref" 2>/dev/null)"; then
    if grep -q '"manifests"' <<<"$manifest"; then
      # Multi-arch: the index must carry an arm64 entry, because a
      # platform-specific digest would pin correctly and pull nothing on the
      # cluster's Raspberry Pi nodes.
      grep -Eq '"architecture"[[:space:]]*:[[:space:]]*"arm64"' <<<"$manifest" || return 2
      arch="arm64-index"
    else
      # Single manifest: architecture lives only in the image config, so the
      # config must be fetched and checked. Labelling it "assumed" would let a
      # single-arch amd64 image through, which is the same failure as pinning a
      # platform digest.
      crane config "$ref" 2>/dev/null \
        | grep -Eq '"architecture"[[:space:]]*:[[:space:]]*"arm64"' || return 2
      arch="arm64-single"
    fi
  else
    return 1
  fi

  printf '%s\t%s\n' "$digest" "$arch"
}

failed=0
rows=0

mapfile -t services < <(grep -E '^- ' "$CLUSTER_KUSTOMIZATION" | sed -E 's/^- +//')

for svc in "${services[@]}"; do
  dir="${ROOT}/manifests/cluster/${svc}"
  [ -d "$dir" ] || { echo "pin-digests: missing directory for '$svc'" >&2; exit 1; }

  is_chart=0
  # A service is chart-based if any generated Helm output or helmfile is present.
  # cloudflared names its generated file `helmchart.yaml`, not `helm-chart.yaml`,
  # so matching only the documented name would treat generated output as
  # hand-written and edit it by hand.
  for marker in helmfile.yaml helm-chart.yaml helmchart.yaml; do
    [ -f "${dir}/${marker}" ] && is_chart=1
  done

  if [ "$SCOPE" = "plain" ] && [ "$is_chart" -eq 1 ]; then continue; fi
  if [ "$SCOPE" = "chart" ] && [ "$is_chart" -eq 0 ]; then continue; fi

  build="$(kustomize build "$dir")"

  while IFS= read -r line; do
    ref="$(sed -E 's/^[[:space:]]*-?[[:space:]]*image:[[:space:]]*//' <<<"$line" | tr -d "\"'" | sed -E 's/[[:space:]]+$//')"
    [ -n "$ref" ] || continue
    # Already pinned, or a templated value that is not a literal reference.
    if [[ "$ref" == *"@sha256:"* ]]; then continue; fi
    if [[ "$ref" == *'{{'* ]]; then continue; fi

    if out="$(resolve "$ref")"; then
      digest="${out%%$'\t'*}"
      printf '%s\t%s\t%s@%s\tarm64-ok\n' "$svc" "$ref" "$ref" "$digest"
      rows=$((rows + 1))
    else
      rc=$?
      if [ "$rc" -eq 2 ]; then
        printf '%s\t%s\t%s\tNO-ARM64\n' "$svc" "$ref" "-" >&2
      else
        printf '%s\t%s\t%s\tUNRESOLVED\n' "$svc" "$ref" "-" >&2
      fi
      failed=$((failed + 1))
    fi
  done < <(printf '%s\n' "$build" | grep -E '^[[:space:]]*-?[[:space:]]*image:')
done

echo "pin-digests: ${rows} reference(s) resolved, ${failed} failed (scope=${SCOPE})" >&2

[ "$failed" -eq 0 ] || exit 1
