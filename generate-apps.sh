#!/bin/bash

BOOTSTRAP_DIR="manifests/bootstrap"
CLUSTER_KUSTOMIZATION="manifests/cluster/kustomization.yaml"

TEMP_KUST=$(mktemp)
cat "$BOOTSTRAP_DIR/kustomization.yaml" | grep -v "^- " > "$TEMP_KUST"

echo "Parsing resources from $CLUSTER_KUSTOMIZATION..."
resources=$(grep "^- " "$CLUSTER_KUSTOMIZATION" | sed 's/^- //')

# Namespace mapping for cases where directory name != namespace
declare -A namespace_map
namespace_map["metallb"]="metallb-system"
namespace_map["generic-device-plugin"]="device-system"

# Sync wave mapping for deployment order (lower numbers deploy first)
declare -A sync_wave_map
# Wave 0: Core infrastructure
sync_wave_map["external-secrets"]="0"
sync_wave_map["cert-manager"]="1"
# Wave 2: Network infrastructure
sync_wave_map["traefik"]="2"
sync_wave_map["metallb"]="2"
# Wave 3: Storage
sync_wave_map["longhorn"]="3"
# Wave 4: Auth
sync_wave_map["dex"]="4"
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

for resource in $resources; do
  echo "Generating Application for $resource..."
  
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
    path: manifests/cluster/$resource
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
