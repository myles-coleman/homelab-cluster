#!/usr/bin/env bash
#
# Generate one ArgoCD Application per service listed in
# manifests/cluster/kustomization.yaml.
#
# The script resolves the repository root from its own location and changes into
# it, so every path below is relative to the repository rather than to whatever
# directory the caller happened to be in. That matters now that it lives in
# scripts/ and is invoked from CI, from Taskfile, and by hand.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

BOOTSTRAP_DIR="manifests/bootstrap"
CLUSTER_KUSTOMIZATION="manifests/cluster/kustomization.yaml"

TEMP_KUST=$(mktemp)
cat "$BOOTSTRAP_DIR/kustomization.yaml" | grep -v "^- " > "$TEMP_KUST"

echo "Parsing resources from $CLUSTER_KUSTOMIZATION..."
resources=$(grep "^- " "$CLUSTER_KUSTOMIZATION" | sed 's/^- //')

# Application files this run owns, keyed by basename. Anything matching
# *-app.yaml in BOOTSTRAP_DIR but absent here is a retired service.
declare -A expected_app_files

# Namespace mapping for cases where directory name != namespace
declare -A namespace_map
namespace_map["metallb"]="metallb-system"
namespace_map["generic-device-plugin"]="device-system"
# spec 30: the csi-driver release lives in the existing cert-manager namespace so
# the driver shares cert-manager's ServiceAccount/RBAC surface instead of owning a
# second namespace for one DaemonSet.
namespace_map["cert-manager-csi-driver"]="cert-manager"

# Sync wave mapping for deployment order (lower numbers deploy first)
declare -A sync_wave_map
# Wave 0: Core infrastructure
sync_wave_map["external-secrets"]="0"
# Pod invariants must exist before any workload is created. The directory is
# cluster-scoped only; a binding deleted here is the rollback, never the policy.
sync_wave_map["admission-policy"]="0"
sync_wave_map["cert-manager"]="1"
# Wave 2: Network infrastructure
sync_wave_map["traefik"]="2"
sync_wave_map["metallb"]="2"
# Wave 3: Storage
sync_wave_map["longhorn"]="3"
# Wave 4: Auth
sync_wave_map["dex"]="4"
# Authentik shares the auth wave: applications in wave 10 attach the forward-auth
# middleware, and the outpost must exist before them.
sync_wave_map["authentik"]="4"
# Wave 5: GPU device registration
sync_wave_map["generic-device-plugin"]="5"
# Wave 10: Applications (default)
# All other apps will use wave 10

# Per-service extra ignoreDifferences entries, emitted verbatim below the shared
# ExternalSecret rule. Keep these tightly scoped: each entry documents a field
# that something other than this repo owns at runtime.
declare -A extra_ignore_differences
# metallb: the chart ships a placeholder CA (CN "Default Company Ltd") in the
# bgppeers conversion webhook, and cert-manager injects the real bundle after
# apply, so the value can never match. The API server also defaults the webhook
# service port to 443, which the rendered chart omits.
extra_ignore_differences["metallb"]='    - group: apiextensions.k8s.io
      kind: CustomResourceDefinition
      name: bgppeers.metallb.io
      jsonPointers:
        - /spec/conversion/webhook/clientConfig/caBundle
        - /spec/conversion/webhook/clientConfig/service/port
'
# signoz: the ClickHouse operator chart declares env vars with resourceFieldRef
# but no divisor, and the API server defaults it to "0". ArgoCD's structured-merge
# diff keeps that server-side default, so it reads as drift.
extra_ignore_differences["signoz"]='    - group: apps
      kind: Deployment
      name: signoz-clickhouse-operator
      jqPathExpressions:
        - .spec.template.spec.containers[].env[].valueFrom.resourceFieldRef.divisor
    # The zookeeper StatefulSet carries a `status: {phase: Pending}` block inside
    # its immutable volumeClaimTemplates; the API server persists it and the
    # committed manifest has no status there, so ArgoCD reports the StatefulSet
    # OutOfSync forever while every PVC is actually Bound. `kubectl diff` does not
    # show it because it ignores status. Surfaced after the 2026-11 sync that also
    # removed `mastra`; it is not upgrade-related (spec 28, node1 gate).
    - group: apps
      kind: StatefulSet
      name: signoz-zookeeper
      jqPathExpressions:
        - .spec.volumeClaimTemplates[].status
'

for resource in $resources; do
  echo "Generating Application for $resource..."
  
  # Track the Application files this run intends to own, so the prune below can
  # tell a live service from a retired one.
  expected_app_files["$resource-app.yaml"]=1

  # Use mapped namespace if exists, otherwise use resource name
  target_namespace="${namespace_map[$resource]:-$resource}"
  
  # Use mapped sync wave if exists, otherwise use default (10)
  sync_wave="${sync_wave_map[$resource]:-10}"
  
  cat > "$BOOTSTRAP_DIR/$resource-app.yaml" << EOF
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: $resource
  namespace: argocd
  annotations:
    argocd.argoproj.io/sync-wave: "$sync_wave"
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  destination:
    server: https://kubernetes.default.svc
    namespace: $target_namespace
  project: default
  # Everything is applied with ServerSideApply, so live objects carry no
  # kubectl.kubernetes.io/last-applied-configuration. Without it ArgoCD compares
  # desired vs live literally, and every CRD-defaulted field reads as drift.
  # The ESO API server fills these in on write, so an ExternalSecret that omits
  # them is OutOfSync forever:
  #   spec.data[].remoteRef.{conversionStrategy,decodingStrategy,metadataPolicy}
  #   spec.dataFrom[].extract.{conversionStrategy,decodingStrategy,metadataPolicy}
  #   spec.target.deletionPolicy, spec.target.template.{engineVersion,mergePolicy}
  ignoreDifferences:
    - group: external-secrets.io
      kind: ExternalSecret
      jqPathExpressions:
        - .spec.data[].remoteRef.conversionStrategy
        - .spec.data[].remoteRef.decodingStrategy
        - .spec.data[].remoteRef.metadataPolicy
        - .spec.dataFrom[].extract.conversionStrategy
        - .spec.dataFrom[].extract.decodingStrategy
        - .spec.dataFrom[].extract.metadataPolicy
        - .spec.target.deletionPolicy
        - .spec.target.template.engineVersion
        - .spec.target.template.mergePolicy
${extra_ignore_differences[$resource]:-}  source:
    path: manifests/dist/$resource
    repoURL: "https://github.com/myles-coleman/homelab-cluster"
    targetRevision: main
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - ServerSideApply=true
    retry:
      backoff:
        duration: 30s
        factor: 2
        maxDuration: 2m
      limit: 5
EOF

  echo "- $resource-app.yaml" >> "$TEMP_KUST"
done

mv "$TEMP_KUST" "$BOOTSTRAP_DIR/kustomization.yaml"

echo "Updated $BOOTSTRAP_DIR/kustomization.yaml with all application resources"

# Prune Application files for services that are no longer declared.
#
# Commenting a service out of CLUSTER_KUSTOMIZATION removes its line from the
# bootstrap kustomization, which makes ArgoCD prune the Application and cascade
# to the namespace. It does NOT remove the generated <service>-app.yaml file,
# because this script only ever writes files. The orphan is inert to kustomize,
# but it is a fully-formed Application manifest sitting in the tree, so it reads
# as live configuration to anyone browsing the repo. discord-bot was the first
# such orphan; without this, every retirement leaves one behind.
shopt -s nullglob
for app_file in "$BOOTSTRAP_DIR"/*-app.yaml; do
  app_basename="$(basename "$app_file")"
  if [ -z "${expected_app_files[$app_basename]:-}" ]; then
    echo "Removing orphaned Application file: $app_basename"
    rm -f "$app_file"
  fi
done
shopt -u nullglob
