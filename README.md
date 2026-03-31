# homelab-cluster

A GitOps-managed Kubernetes homelab cluster running on 4x Raspberry Pi 5 nodes with NixOS and k3s v1.33, orchestrated by ArgoCD.

## Architecture

- **4x Raspberry Pi 5** nodes (aarch64, NVMe SSD) — 1 server + 3 agents
- **NixOS** (unstable channel) for declarative OS configuration
- **k3s v1.33** lightweight Kubernetes distribution
- **ArgoCD** for GitOps continuous delivery (auto-sync, prune, self-heal)
- **Traefik** ingress controller with MetalLB load balancer (10.0.0.99)
- **Longhorn** distributed block storage with S3 backups
- **External Secrets Operator** syncing secrets from Vaultwarden
- **Cloudflare** DNS (wildcard `*.cowlab.org`) + Zero Trust Tunnel

## Repository Structure

```
manifests/cluster/    # Per-service Kubernetes manifests (Kustomize)
manifests/bootstrap/  # ArgoCD Application definitions (auto-generated)
modules/              # Terragrunt/OpenTofu infrastructure modules
docs/                 # MkDocs documentation site
```

## Deployment

Push to `main` → GitHub Actions → ArgoCD syncs to cluster. Services deploy in [sync wave order](docs/reference/sync-wave-reference.md): external-secrets (0) → cert-manager (1) → traefik/metallb (2) → longhorn (3) → dex (4) → apps (10).

## Documentation

Full documentation is available at [docs.cowlab.org](https://docs.cowlab.org) (or [GitHub Pages](https://myles-coleman.github.io/homelab-cluster)):

- [Architecture](docs/architecture/) — Cluster topology, GitOps flow, infrastructure components
- [Guides](docs/guides/) — Adding services, managing secrets, DNS and ingress
- [Operator Guide](docs/operator-guide/) — Terraform modules, backup/restore runbooks
- [Reference](docs/reference/) — Service catalog, sync wave reference

## AI-Assisted Development

This repository includes configuration for AI coding assistants:

- **[`AGENTS.md`](AGENTS.md)** — Comprehensive agent orientation (architecture, conventions, guardrails, onboarding). Read by [OpenCode](https://opencode.ai) and Windsurf Cascade.
- **[`.windsurfrules`](.windsurfrules)** — Windsurf Cascade-specific rules (conventions, workflows, guardrails)
- **[`opencode.json`](opencode.json)** — OpenCode instruction file references
- **[`.windsurf/workflows/add-service.md`](.windsurf/workflows/add-service.md)** — Automated workflow for adding new services
