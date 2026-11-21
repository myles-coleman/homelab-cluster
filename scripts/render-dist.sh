#!/usr/bin/env bash
#
# Render every active service into dist/<service>/, one file per object.
#
# ArgoCD serves dist/, not manifests/cluster/. Everything that reaches the
# cluster is therefore in this tree, reviewable as a diff, and checkable by CI
# without rendering at sync time.
#
# The renderer resolves nothing against a registry: every image is already
# digest-pinned, so this is a pure function of the repository.
#
# Usage:
#   ./scripts/render-dist.sh           rewrite dist/
#   ./scripts/render-dist.sh --check   do not touch the working tree; exit 1 if
#                                      dist/ differs from a fresh render
#
# Requirements: kustomize at exactly $KUSTOMIZE_VERSION, python3.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER_KUSTOMIZATION="${ROOT}/manifests/cluster/kustomization.yaml"
OUT_DIR="${ROOT}/dist"

MODE="write"
case "${1:-}" in
  "") ;;
  --check) MODE="check" ;;
  -h|--help)
    sed -n '2,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
    exit 0
    ;;
  *)
    echo "render-dist: unknown argument: $1" >&2
    exit 2
    ;;
esac

# shellcheck source=scripts/tools.env
source "${ROOT}/scripts/tools.env"

command -v kustomize >/dev/null 2>&1 || {
  echo "render-dist: kustomize not found on PATH" >&2
  exit 1
}
command -v python3 >/dev/null 2>&1 || {
  echo "render-dist: python3 not found on PATH" >&2
  exit 1
}

# A different kustomize version re-serializes the whole tree: quoting, key order
# and list indentation all move. That produces a diff of thousands of lines for a
# change that touches nothing, so the version is checked rather than assumed.
ACTUAL="$(kustomize version 2>/dev/null | sed -E 's/^v?([0-9]+\.[0-9]+\.[0-9]+).*/\1/')"
if [ "${ACTUAL}" != "${KUSTOMIZE_VERSION}" ]; then
  cat >&2 <<EOF
render-dist: kustomize version mismatch
  expected (scripts/tools.env): ${KUSTOMIZE_VERSION}
  found on PATH:               ${ACTUAL}
Install the pinned version, or bump KUSTOMIZE_VERSION in its own pull request
whose diff is the re-render and nothing else.
EOF
  exit 1
fi

# Build into a scratch tree and swap it in. A partial render must never be able
# to land in dist/, and a stale file must never survive inside a service
# directory, which is exactly what `kustomize create --autodetect` does on its
# own: it lists what it finds, it never deletes what it should not find.
SCRATCH="$(mktemp -d)"
WORK="$(mktemp -d)"
trap 'rm -rf "${SCRATCH}" "${WORK}"' EXIT

mapfile -t services < <(grep -E '^- ' "${CLUSTER_KUSTOMIZATION}" | sed -E 's/^- +//')

for svc in "${services[@]}"; do
  src="${ROOT}/manifests/cluster/${svc}"
  if [ ! -d "${src}" ]; then
    echo "render-dist: '${svc}' is listed in resources: but ${src} does not exist" >&2
    exit 1
  fi

  rendered="${WORK}/${svc}.yaml"
  dest="${SCRATCH}/${svc}"
  mkdir -p "${dest}"

  kustomize build "${src}" > "${rendered}"
  python3 "${ROOT}/scripts/split-manifests.py" "${rendered}" "${dest}" 2>/dev/null
  ( cd "${dest}" && kustomize create --autodetect >/dev/null )
done

# Services that are no longer listed in resources: have no directory in the
# scratch tree, so they disappear on swap without any pruning logic of their own.
printf 'render-dist: rendered %d service(s)\n' "${#services[@]}" >&2

if [ "${MODE}" = "check" ]; then
  if [ ! -d "${OUT_DIR}" ]; then
    echo "render-dist: dist/ does not exist; run ./scripts/render-dist.sh" >&2
    exit 1
  fi
  if ! diff -r "${SCRATCH}" "${OUT_DIR}" >/dev/null 2>&1; then
    echo "render-dist: dist/ is stale. Run ./scripts/render-dist.sh and commit the result." >&2
    diff -r "${SCRATCH}" "${OUT_DIR}" | head -40 >&2 || true
    exit 1
  fi
  echo "render-dist: dist/ matches a fresh render" >&2
  exit 0
fi

rm -rf "${OUT_DIR}"
mv "${SCRATCH}" "${OUT_DIR}"
echo "render-dist: wrote ${OUT_DIR}" >&2
