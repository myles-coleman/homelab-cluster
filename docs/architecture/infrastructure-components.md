# Infrastructure Components

This page documents the key infrastructure services that support the cluster. These are deployed before application workloads via [sync waves](../reference/sync-wave-reference.md).

## ArgoCD

- **Purpose**: GitOps continuous delivery — syncs Kubernetes manifests from this repository to the cluster
- **Deployment**: Installed outside this repository by the `argocd-install` unit in [`myles-coleman/homelab-terraform`](https://github.com/myles-coleman/homelab-terraform); ArgoCD is not GitOps-managed and has no manifests or Application in this repo
- **Namespace**: `argocd`
- **URL**: [argocd.cowlab.org](https://argocd.cowlab.org)
- **Authentication**: Dex OIDC connector (integrated with Authentik)
- **Key config**: RBAC policies, metrics enabled, automated sync with prune and self-heal

The bootstrap Application is created by the `argocd-configure` unit in [`myles-coleman/homelab-terraform`](https://github.com/myles-coleman/homelab-terraform) and points ArgoCD at `manifests/bootstrap/`.

## Traefik

- **Purpose**: Ingress controller and reverse proxy — routes external HTTP(S) traffic to services
- **Deployment**: Helm chart rendered into `manifests/cluster/traefik/helm-chart.yaml`
- **Namespace**: `traefik`
- **Sync wave**: 2
- **LoadBalancer IP**: 10.0.0.99 (assigned by MetalLB)
- **Key config**:
    - Entry points: `web` (HTTP, redirects to HTTPS), `websecure` (HTTPS with TLS), `websecure-http3` (HTTP/3 UDP)
    - Gateway API provider: renders `GatewayClass/traefik` and `Gateway/traefik-gateway` with
      listeners `web` (8000/HTTP) and `websecure` (8443/HTTPS, wildcard certificate)
    - CRD provider: `kubernetescrd` with `ingressClass=traefik-external`, kept enabled for
      `Middleware` as `ExtensionRef`
    - Cross-namespace routing: `allowedRoutes.namespaces.from: All` on both listeners
    - Prometheus metrics on port 9100; dashboard/API on the unexposed internal entrypoint 8080

All services use Gateway API `HTTPRoute`s attached to `Gateway/traefik-gateway` — never standard
Kubernetes `Ingress` (provider disabled) and never the retired Traefik `IngressRoute`.

## MetalLB

- **Purpose**: Bare-metal load balancer — assigns external IPs to `LoadBalancer`-type Kubernetes services
- **Deployment**: Helm chart rendered into `manifests/cluster/metallb/helm-chart.yaml`
- **Namespace**: `metallb-system` (mapped from directory name `metallb` via `namespace_map` in `scripts/generate-apps.sh`)
- **Sync wave**: 2
- **Key config**: Provides the IP pool that includes 10.0.0.99 for Traefik

## Longhorn

- **Purpose**: Distributed block storage — provides persistent volumes replicated across cluster nodes
- **Deployment**: Helm chart rendered into `manifests/cluster/longhorn/helm-chart.yaml`
- **Namespace**: `longhorn`
- **Sync wave**: 3
- **Key config**:
    - Default StorageClass for the cluster
    - S3 backup bucket provisioned by the `s3-backups` unit in [`myles-coleman/homelab-terraform`](https://github.com/myles-coleman/homelab-terraform); the target is set in `manifests/cluster/longhorn/values.yaml` (`backupTarget`)
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
- **Deployment**: Helm chart rendered into `manifests/cluster/authentik/helm-chart.yaml`, with providers and applications configured by the `authentik` unit in [`myles-coleman/homelab-terraform`](https://github.com/myles-coleman/homelab-terraform)
- **Namespace**: `authentik`
- **Sync wave**: 10

## Cloudflare Tunnel (cloudflared)

- **Purpose**: Secure tunnel for public-facing services without exposing ports on the home network
- **Deployment**: Helm chart rendered into `manifests/cluster/cloudflared/helmchart.yaml`
- **Namespace**: `cloudflared`
- **Sync wave**: 10
- **Key config**:
    - Tunnel ingress rules managed by the `cloudflare` unit in [`myles-coleman/homelab-terraform`](https://github.com/myles-coleman/homelab-terraform)
    - Catch-all 404 rule only; no service hostname is published
    - DNS records for tunneled services are created by the same `cloudflare` unit
