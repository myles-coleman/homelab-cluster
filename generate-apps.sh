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
# Wave 10: Applications (default)
# All other apps will use wave 10

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
  source:
    path: manifests/cluster/$resource
    repoURL: "https://github.com/myles-coleman/homelab-cluster"
    targetRevision: main
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
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
