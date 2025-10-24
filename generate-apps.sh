#!/bin/bash

BOOTSTRAP_DIR="manifests/bootstrap"
CLUSTER_KUSTOMIZATION="manifests/cluster/kustomization.yaml"

TEMP_KUST=$(mktemp)
cat "$BOOTSTRAP_DIR/kustomization.yaml" | grep -v "^- " > "$TEMP_KUST"

echo "Parsing resources from $CLUSTER_KUSTOMIZATION..."
resources=$(grep "^- " "$CLUSTER_KUSTOMIZATION" | sed 's/^- //')

for resource in $resources; do
  echo "Generating Application for $resource..."
  
  cat > "$BOOTSTRAP_DIR/$resource-app.yaml" << EOF
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: $resource
  namespace: argocd
spec:
  destination:
    server: https://kubernetes.default.svc
    namespace: $resource
  project: default
  source:
    path: manifests/cluster/$resource
    repoURL: "https://github.com/myles-coleman/k3s-nixos"
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
