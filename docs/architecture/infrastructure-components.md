# Infrastructure Components

This page documents the key infrastructure services that support the cluster. These are deployed before application workloads via [sync waves](../reference/sync-wave-reference.md).

## ArgoCD

- **Purpose**: GitOps continuous delivery — syncs Kubernetes manifests from this repository to the cluster
- **Deployment**: Helm chart installed via Terragrunt module (`modules/argocd-install/`)
- **Namespace**: `argocd`
- **URL**: [argocd.cowlab.org](https://argocd.cowlab.org)
- **Authentication**: Dex OIDC connector (integrated with Authentik)
- **Key config**: RBAC policies, metrics enabled, automated sync with prune and self-heal

The bootstrap Application is created by the `modules/argocd-configure/` Terragrunt module, which points ArgoCD at `manifests/bootstrap/`.

## Traefik

- **Purpose**: Ingress controller and reverse proxy — routes external HTTP(S) traffic to services
- **Deployment**: Helm chart rendered into `manifests/cluster/traefik/helm-chart.yaml`
- **Namespace**: `traefik`
- **Sync wave**: 2
- **LoadBalancer IP**: 10.0.0.99 (assigned by MetalLB)
- **Key config**:
    - Entry points: `web` (HTTP, redirects to HTTPS), `websecure` (HTTPS with TLS), `websecure-http3` (HTTP/3 UDP)
    - CRD provider: `kubernetescrd` with `ingressClass=traefik-external`
    - Cross-namespace routing enabled
    - Prometheus metrics on port 9100

All services use `IngressRoute` CRD (`traefik.io/v1alpha1`) for ingress — never standard Kubernetes `Ingress`.

## MetalLB

- **Purpose**: Bare-metal load balancer — assigns external IPs to `LoadBalancer`-type Kubernetes services
- **Deployment**: Helm chart rendered into `manifests/cluster/metallb/helm-chart.yaml`
- **Namespace**: `metallb-system` (mapped from directory name `metallb` via `namespace_map` in `generate-apps.sh`)
- **Sync wave**: 2
- **Key config**: Provides the IP pool that includes 10.0.0.99 for Traefik

## Longhorn

- **Purpose**: Distributed block storage — provides persistent volumes replicated across cluster nodes
- **Deployment**: Helm chart rendered into `manifests/cluster/longhorn/helm-chart.yaml`
- **Namespace**: `longhorn`
- **Sync wave**: 3
- **Key config**:
    - Default StorageClass for the cluster
    - S3 backup configured via Terragrunt module (`modules/s3-backups/`)
    - Requires `open-iscsi` on all nodes (configured in NixOS)

See [Backup and Restore Runbook](../operator-guide/runbooks/backup-and-restore.md) for Longhorn backup procedures.

## External Secrets Operator (ESO)

- **Purpose**: Syncs secrets from Vaultwarden/Bitwarden into Kubernetes Secrets
- **Deployment**: Helm chart rendered into `manifests/cluster/external-secrets/helm-chart.yaml`
- **Namespace**: `external-secrets`
- **Sync wave**: 0 (first to deploy — other services depend on secrets)
- **Key config**:
    - 4 `ClusterSecretStore` types: `bitwarden-login`, `bitwarden-fields`, `bitwarden-notes`, `bitwarden-attachments`
    - Backed by a `bitwarden-cli` webhook service running in the cluster
    - Services reference secrets via `ExternalSecret` resources

See [Managing Secrets](../guides/managing-secrets.md) for usage patterns.

## cert-manager

- **Purpose**: Automated TLS certificate management
- **Deployment**: Helm chart rendered into `manifests/cluster/cert-manager/helm-chart.yaml`
- **Namespace**: `cert-manager`
- **Sync wave**: 1

## Dex

- **Purpose**: OIDC connector — provides authentication for ArgoCD
- **Deployment**: Helm chart rendered into `manifests/cluster/dex/helm-chart.yaml`
- **Namespace**: `dex`
- **Sync wave**: 4
- **Key config**: Integrates with Authentik as the upstream identity provider

## Authentik

- **Purpose**: Identity provider — centralized authentication and SSO
- **Deployment**: Helm chart rendered into `manifests/cluster/authentik/helm-chart.yaml`, with configuration managed via Terragrunt (`modules/authentik/`)
- **Namespace**: `authentik`
- **Sync wave**: 10

## Cloudflare Tunnel (cloudflared)

- **Purpose**: Secure tunnel for public-facing services without exposing ports on the home network
- **Deployment**: Helm chart rendered into `manifests/cluster/cloudflared/helmchart.yaml`
- **Namespace**: `cloudflared`
- **Sync wave**: 10
- **Key config**:
    - Tunnel configuration managed via Terragrunt (`modules/cloudflare/tunnel.tf`)
    - Currently routes `jellyfin.cowlab.org` to `traefik.traefik.svc.cluster.local:80`
    - DNS CNAME record for tunneled services created automatically by the Cloudflare module
