# GitOps Deployment Flow

## Overview

This cluster uses a GitOps model: the Git repository is the single source of truth for all Kubernetes resources. ArgoCD continuously reconciles the cluster state to match what's defined in Git.

## Deployment Pipeline

```mermaid
flowchart TB
    DEV["Developer pushes to main"]
    
    subgraph "GitHub Actions"
        GA_DETECT{"Detect changed paths"}
        GA_GEN["generate-apps.yml<br/>Runs scripts/generate-apps.sh"]
        GA_REL["release.yaml<br/>Semantic release"]
    end
    
    subgraph "ArgoCD (in-cluster)"
        BOOT["Bootstrap Application<br/>watches manifests/bootstrap/"]
        APPS["Per-Service Applications<br/>(sonarr-app, radarr-app, etc.)"]
    end
    
    subgraph "Kubernetes Cluster"
        NS["Namespaces & Resources<br/>synced from manifests/dist/"]
    end

    DEV --> GA_DETECT
    GA_DETECT -->|"manifests/cluster/**"| GA_GEN
    GA_DETECT -->|"any push to main"| GA_REL
    
    GA_GEN -->|"Commits updated<br/>bootstrap files"| BOOT
    
    BOOT -->|"Syncs Application CRDs"| APPS
    APPS -->|"Syncs manifests/dist/<br/>to namespaces"| NS
```

## ArgoCD Bootstrap Chain

The cluster uses a layered bootstrap approach to manage all services through ArgoCD:

1. **ArgoCD install** — ArgoCD itself is installed outside this repository and is **not** GitOps-managed: there is no `argocd` directory under `manifests/cluster/` and no Application for it. Rebuilding a cluster means installing ArgoCD first, then applying the bootstrap Application.
2. **Bootstrap Application** — An ArgoCD Application pointing at `manifests/bootstrap/`, which lists all individual `*-app.yaml` files
3. **Per-service Applications** — Each `*-app.yaml` is an ArgoCD Application that syncs the corresponding `manifests/dist/<service>/` directory to the cluster

### How Applications Are Generated

The `scripts/generate-apps.sh` script reads `manifests/cluster/kustomization.yaml` and generates an ArgoCD Application YAML for each listed service. It handles:

- **Namespace mapping** — defaults to the service directory name, unless overridden in `namespace_map` (e.g., `metallb` → `metallb-system`)
- **Sync wave assignment** — defaults to wave 10, unless overridden in `sync_wave_map`

This script runs automatically via the `generate-apps.yml` GitHub Actions workflow whenever files in `manifests/cluster/` change.

## Sync Wave Ordering

ArgoCD deploys services in wave order (lower numbers first). This ensures dependencies are ready before dependents start:

| Wave | Services | Rationale |
|------|----------|-----------|
| 0 | external-secrets | Secrets must be available for all other services |
| 1 | cert-manager | TLS certificates needed for ingress |
| 2 | traefik, metallb | Networking layer (ingress + load balancer) |
| 3 | longhorn | Storage layer for persistent workloads |
| 4 | dex | Authentication (OIDC for ArgoCD) |
| 10 | All application workloads | Default wave for apps (sonarr, radarr, prowlarr, etc.) |

See [Sync Wave Reference](../reference/sync-wave-reference.md) for detailed documentation.

## ArgoCD Sync Policy

All applications are configured with:

- **Automated sync** — Changes in Git are automatically applied to the cluster
- **Prune** — Resources removed from Git are deleted from the cluster
- **Self-heal** — Manual changes to cluster resources are automatically reverted to match Git
- **Retry** — 5 retries with exponential backoff (30s initial, 2m max)

## GitHub Actions Workflows

| Workflow | Trigger | Purpose |
|----------|---------|---------|
| `generate-apps.yml` | Changes to `manifests/cluster/**` | Runs `scripts/generate-apps.sh` to regenerate ArgoCD Application YAMLs |
| `release.yaml` | Any push to `main` | Semantic release (versioning + changelog) |

Infrastructure changes do not flow through this repository. ArgoCD installation, Cloudflare
DNS and tunnel rules, Longhorn backup buckets, Authentik configuration and Tailscale ACLs are
Terragrunt units in [`myles-coleman/homelab-terraform`](https://github.com/myles-coleman/homelab-terraform), which has its own `deploy.yaml` (plan on PR, apply on merge to `main`).
