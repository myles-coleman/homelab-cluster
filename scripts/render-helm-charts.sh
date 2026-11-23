#!/usr/bin/env bash
#
# Re-render the Helm chart for every service whose chart inputs changed:
# values.yaml (an image bump) or helmfile.yaml (a chart-version bump).
#
# Renovate edits values.yaml, but ArgoCD serves manifests/dist/, and dist/ is
# rendered from helm-chart.yaml. Without this step a version bump changes the
# source of truth and nothing that reaches the cluster — and `dist-verify` still
# passes, because it renders from the unchanged helm-chart.yaml and therefore
# never sees the disagreement.
#
# This runs immediately before render-dist.sh as a Renovate postUpgradeTasks
# command. It needs helm and helmfile at the versions pinned in
# scripts/tools.env: a different helm re-serialises the output, so the pull
# request would stop matching what a local `task helm` produces.
#
# Usage:
#   ./scripts/render-helm-charts.sh                # services whose chart inputs changed
#   ./scripts/render-helm-charts.sh <service> ...  # render these explicitly

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# shellcheck disable=SC1091
source "${ROOT}/scripts/tools.env"

die() { echo "render-helm-charts: $*" >&2; exit 1; }

command -v helm >/dev/null 2>&1 || die "helm not found on PATH"
command -v helmfile >/dev/null 2>&1 || die "helmfile not found on PATH"

# A version difference rewrites quoting and ordering in the rendered chart, which
# turns a one-line value bump into an unreadable diff. Fail closed instead.
have_helm="$(helm version --short 2>/dev/null | sed -E 's/^v?([0-9]+\.[0-9]+\.[0-9]+).*/\1/')"
if [ "${have_helm}" != "${HELM_VERSION}" ]; then
  die "helm version mismatch
  expected (scripts/tools.env): ${HELM_VERSION}
  found:                        ${have_helm:-none}
Install the pinned version; do not bump it in this script."
fi

have_helmfile="$(helmfile --version 2>/dev/null | sed -E 's/^helmfile version v?([0-9]+\.[0-9]+\.[0-9]+).*/\1/')"
if [ "${have_helmfile}" != "${HELMFILE_VERSION}" ]; then
  die "helmfile version mismatch
  expected (scripts/tools.env): ${HELMFILE_VERSION}
  found:                        ${have_helmfile:-none}"
fi

# Which services to render.
services=()
if [ "$#" -gt 0 ]; then
  services=("$@")
else
  # `git diff HEAD` covers staged and unstaged edits, which is the state
  # postUpgradeTasks runs in: Renovate has written the new version but has not
  # committed yet.
  while IFS= read -r path; do
    [ -n "${path}" ] || continue
    services+=("$(basename "$(dirname "${path}")")")
  # Both files feed `helmfile template`: values.yaml carries an image or setting
  # bump, helmfile.yaml carries a chart-version bump. Keying on values.yaml alone
  # missed every "update helm release X to vY" update, which is half of what
  # Renovate proposes for these services.
  #
  # If a service ever gains a second values file, add its pattern here too.
  done < <(git -C "${ROOT}" diff --name-only HEAD -- \
             'manifests/cluster/*/values.yaml' \
             'manifests/cluster/*/helmfile.yaml' \
             2>/dev/null | sort -u)
fi

if [ "${#services[@]}" -eq 0 ]; then
  echo "render-helm-charts: no chart inputs changed, nothing to render"
  exit 0
fi

for svc in "${services[@]}"; do
  dir="${ROOT}/manifests/cluster/${svc}"

  if [ ! -d "${dir}" ]; then
    die "${svc}: no such service directory"
  fi

  # A raw-manifest service can carry a values.yaml for other reasons; only a
  # helmfile means there is a chart to render.
  if [ ! -f "${dir}/helmfile.yaml" ]; then
    echo "render-helm-charts: ${svc} has no helmfile.yaml, skipping"
    continue
  fi

  out="helm-chart.yaml"
  [ -f "${dir}/helmchart.yaml" ] && out="helmchart.yaml"

  echo "render-helm-charts: rendering ${svc} -> ${out}"

  # Render into a temp file and move it into place, so an interrupted or failed
  # helmfile run cannot leave a truncated chart behind for render-dist.sh to
  # publish.
  (
    cd "${dir}"
    # --skip-deps skips repo/index refresh, so the repos this chart needs have to
    # be registered first. Fetching a pinned version keeps the output
    # deterministic; it is the chart *version* that is pinned, not the repo HEAD.
    helmfile repos >/dev/null
    helmfile template --skip-deps > "/tmp/render-helm-charts.${svc}.yaml"
    mv "/tmp/render-helm-charts.${svc}.yaml" "${out}"
  )
done

echo "render-helm-charts: done (${#services[@]} service(s))"
