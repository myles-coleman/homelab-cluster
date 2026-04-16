# AGENTS.md — homelab-cluster

This file provides guidance to AI coding agents working in this repository. Read this file first before making any changes.

**Source of truth for active services**: Always check `manifests/cluster/kustomization.yaml` — it lists all currently deployed services. Commented-out entries are disabled.

---

## 1. Architecture Overview

### Cluster Purpose

This is a personal homelab Kubernetes cluster running media management, home automation, and infrastructure services on bare-metal Raspberry Pi 5 hardware.

### Hardware & Nodes

| Node | Role | IP Address | Hardware |
|------|------|-----------|----------|
| node0 | k3s server (control plane) | 10.0.0.140 | Raspberry Pi 5, aarch64, NVMe SSD |
| node1 | k3s agent (worker) | 10.0.0.141 | Raspberry Pi 5, aarch64, NVMe SSD |
| node2 | k3s agent (worker) | 10.0.0.142 | Raspberry Pi 5, aarch64, NVMe SSD |
| node3 | k3s agent (worker) | 10.0.0.143 | Raspberry Pi 5, aarch64, NVMe SSD |
| node4 | k3s agent (GPU worker) | 10.0.0.144 | Raspberry Pi 5 8GB, aarch64, SD card, AMD RX 6700 XT eGPU |

### Operating System & Kubernetes

- **OS**: NixOS (unstable channel) — managed in a separate repository (`rpi5-nixos`)
- **Kubernetes**: k3s v1.33 (lightweight K8s distribution)
- **Architecture**: aarch64 (ARM64) — all container images must support this architecture
- **Disabled k3s defaults**: servicelb and traefik are disabled in favor of MetalLB and a custom Traefik deployment

### Networking

| Component | Value |
|-----------|-------|
| **kube-vip VIP** (k3s API server) | 10.0.0.200 |
| **MetalLB / Traefik LoadBalancer IP** | 10.0.0.99 |
| **Domain** | cowlab.org |
| **DNS provider** | Cloudflare (wildcard `*.cowlab.org` → 10.0.0.99) |
| **Ingress controller** | Traefik v3 (IngressRoute CRD) |
| **External access** | Cloudflare Zero Trust Tunnel (currently routes jellyfin.cowlab.org) |

### Key Infrastructure Components

- **ArgoCD** — GitOps continuous delivery, syncs manifests from this repo to cluster
- **Traefik** — Ingress controller and reverse proxy (Helm chart, LB IP 10.0.0.99)
- **MetalLB** — Bare-metal load balancer providing external IPs (Helm chart)
- **Longhorn** — Distributed block storage across nodes with S3 backup (Helm chart)
- **External Secrets Operator (ESO)** — Syncs secrets from Vaultwarden/Bitwarden into Kubernetes (Helm chart)
- **cert-manager** — TLS certificate management (Helm chart)
- **Dex** — OIDC connector for ArgoCD authentication
- **Authentik** — Identity provider (Helm chart, Terragrunt-managed config)
- **Cloudflare Tunnel** — Secure tunnel for public-facing services without exposing ports

### GPU Workloads

Node4 has an AMD RX 6700 XT eGPU connected via PCIe, running Vulkan compute through Mesa RADV drivers. GPU access in Kubernetes is provided by `squat/generic-device-plugin` (DaemonSet in `device-system` namespace), which exposes `/dev/dri/renderD128` and `/dev/dri/card0` as a schedulable resource.

| Property | Value |
|----------|-------|
| **Kubernetes resource** | `gpu.cowlab.org/dri: 1` |
| **Node labels** | `gpu.node/type=amd-vulkan`, `gpu.node/vram=12Gi` |
| **Node taint** | `gpu=amd:NoSchedule` |
| **GPU backend** | Vulkan (Mesa RADV, no ROCm on ARM64) |

To schedule a GPU workload, a pod spec needs: nodeSelector (`gpu.node/type: amd-vulkan`), toleration (`gpu=amd:NoSchedule`), and resource request/limit (`gpu.cowlab.org/dri: 1`). See `manifests/cluster/llama-cpp/deployment.yaml` for a complete example.

---

## 2. Repository Layout

- `manifests/cluster/<service>/` — Per-service Kubernetes manifests (Kustomize). Each service has namespace.yaml, deployment.yaml, service.yaml, ingress.yaml, and kustomization.yaml.
- `manifests/cluster/_template/` — Skeleton manifests for new services (NOT deployed). Copy this when adding a service.
- `manifests/cluster/kustomization.yaml` — Source of truth for all active services.
- `manifests/bootstrap/` — AUTO-GENERATED ArgoCD Application definitions. Never edit directly.
- `modules/` — Terragrunt/OpenTofu infrastructure modules (argocd-install, argocd-configure, cloudflare, s3-backups, authentik, bootstrap).
- `docs/` — MkDocs documentation site (Material theme).
- `generate-apps.sh` — Generates ArgoCD Application YAMLs from the cluster kustomization.
- `common-inputs.yaml` — Shared Terragrunt variables.
- `root.hcl` — Terragrunt root config (S3 backend, tofu binary).

---

## 3. Deployment Flow

### GitOps Pipeline

On push to main, GitHub Actions runs these workflows based on changed paths:

- **manifests/cluster/** changes → `generate-apps.yml` runs `generate-apps.sh` to regenerate `manifests/bootstrap/*-app.yaml`, commits and pushes
- **modules/** changes → `deploy.yaml` runs Terragrunt plan (on PR) / apply (on merge). Uses Tailscale VPN for cluster access and AWS OIDC for state bucket.
- **docs/** changes → `pages.yaml` builds MkDocs and deploys to GitHub Pages
- **Any push to main** → `release.yaml` runs semantic release (versioning + changelog)

### ArgoCD Bootstrap Chain

This is the core deployment mechanism:

1. **`argocd-install`** (Terragrunt module) — Installs ArgoCD via Helm chart into the `argocd` namespace
2. **`argocd-configure`** (Terragrunt module) — Creates a "bootstrap" Application resource pointing to `manifests/bootstrap/`
3. **Bootstrap Application** — ArgoCD reads `manifests/bootstrap/kustomization.yaml`, which lists all individual `*-app.yaml` files
4. **Per-service Applications** — Each `*-app.yaml` is an ArgoCD Application that syncs `manifests/cluster/<service>/` to the cluster

### Sync Wave Ordering

ArgoCD deploys services in wave order (lower numbers first). This ensures dependencies are ready before dependents:

| Wave | Services | Rationale |
|------|----------|-----------|
| 0 | external-secrets | Secrets must be available for all other services |
| 1 | cert-manager | TLS certificates needed for ingress |
| 2 | traefik, metallb | Networking layer (ingress + load balancer) |
| 3 | longhorn | Storage layer for persistent workloads |
| 4 | dex | Authentication (OIDC for ArgoCD) |
| 5 | generic-device-plugin | GPU device registration (must be ready before GPU workloads) |
| 10 | All application workloads | Default wave for apps (sonarr, radarr, jellyfin, etc.) |

Wave assignments are defined in the `sync_wave_map` associative array in `generate-apps.sh`. Any service not in the map defaults to wave 10.

### ArgoCD Sync Policy

All applications are configured with:
- **Automated sync**: Changes in Git are automatically applied
- **Prune**: Resources removed from Git are deleted from the cluster
- **Self-heal**: Manual changes to cluster resources are reverted to match Git
- **Retry**: 5 retries with exponential backoff (30s → 2m)

---

## 4. Service Onboarding Guide

To add a new service to the cluster, follow these steps:

### Step 1: Create the Service Directory

Copy the template: `cp -r manifests/cluster/_template manifests/cluster/<service-name>`

### Step 2: Edit the Manifests

Update all placeholder values in the copied files (namespace.yaml, deployment.yaml, service.yaml, ingress.yaml, kustomization.yaml). Use any existing service (e.g., `manifests/cluster/sonarr/`) as a reference for a complete working example.

### Step 3: Optional Resources

- **PersistentVolumeClaim** — if the service needs persistent storage (Longhorn is the default StorageClass)
- **ExternalSecret** — if the service needs secrets from Vaultwarden (see Section 5 for patterns)
- **ConfigMap** — for non-sensitive configuration

### Step 4: Register the Service

Add the service directory name to the `resources` list in `manifests/cluster/kustomization.yaml`.

### Step 5: Generate ArgoCD Application

Either:
- **Push to main** — the `generate-apps.yml` GitHub Actions workflow runs `generate-apps.sh` automatically
- **Run locally** — `./generate-apps.sh` (generates `manifests/bootstrap/<service-name>-app.yaml`)

### Step 6: Custom Namespace (if needed)

If the Kubernetes namespace differs from the directory name, add a mapping to the `namespace_map` in `generate-apps.sh`. Currently only `metallb` → `metallb-system` uses this.

### Step 7: Custom Sync Wave (if needed)

If the service needs to deploy before wave 10 (e.g., it's infrastructure), add to the `sync_wave_map` in `generate-apps.sh`.

---

## 5. Manifest Patterns Reference

### Raw Manifest Services (e.g., sonarr, radarr, jellyfin)

Standard pattern: `namespace.yaml` + `deployment.yaml` + `service.yaml` + `ingress.yaml` + `kustomization.yaml`, plus optional PVC/PV files.

### Helm-Based Services (e.g., traefik, longhorn, cert-manager, external-secrets)

These services use a different pattern:

- **`helmfile.yaml`** — Defines the Helm release (chart repo, version, values file)
- **`helm-chart.yaml`** — Pre-rendered Helm chart output (the actual K8s resources)
- **`values.yaml`** — Helm chart configuration values
- **`kustomization.yaml`** — Lists all resources including the rendered chart

Helm-based services in the cluster: traefik, metallb, longhorn, external-secrets, cert-manager, dex, authentik, cloudflared, dapr.

To update a Helm chart: modify `values.yaml` and re-render the chart into `helm-chart.yaml`. Never hand-edit `helm-chart.yaml`.

### External Secrets Patterns

The cluster uses 4 `ClusterSecretStore` types, all backed by a `bitwarden-cli` webhook service:

| Store Name | Use Case | jsonPath |
|-----------|----------|----------|
| `bitwarden-login` | Username/password from login items | `$.data.login.{{ .remoteRef.property }}` |
| `bitwarden-fields` | Custom fields from items | `$.data.fields[?@.name=="{{ .remoteRef.property }}"].value` |
| `bitwarden-notes` | Secure notes content | `$.data.notes` |
| `bitwarden-attachments` | File attachments | Raw binary content |

For ExternalSecret examples, see any service with a `secret.yaml` file (e.g., search `manifests/cluster/` for `ExternalSecret` resources).

### Traefik IngressRoute Pattern

Always use the Traefik CRD (`traefik.io/v1alpha1 IngressRoute`), **never** standard Kubernetes `networking.k8s.io/v1 Ingress`. The `kubernetes.io/ingress.class: traefik-external` annotation is required. Entry point is always `websecure`. Host matcher uses the pattern `Host(\`<service>.cowlab.org\`)`.

---

## 6. Terragrunt Modules Reference

All modules use OpenTofu (`terraform_binary = "tofu"` in `root.hcl`) with S3 remote state backend. Common variables are loaded from `common-inputs.yaml`.

**Modules**: `bootstrap` (AWS OIDC + S3 state bucket, must apply first), `argocd-install` (Helm chart), `argocd-configure` (bootstrap Application, depends on argocd-install), `cloudflare` (DNS records + Zero Trust Tunnel), `s3-backups` (Longhorn backup buckets), `authentik` (identity provider).

**Shared patterns**: `root.hcl` defines the S3 backend and tofu binary. `common-inputs.yaml` holds shared variables (domain, cluster name, AWS region, etc.). Each module's `terragrunt.hcl` uses `generate` blocks to create provider configs from kubeconfig.

---

## 7. Debugging

When diagnosing cluster issues, always gather information yourself using kubectl rather than asking the user to paste logs. The kubeconfig is available on the local machine. Useful commands:

- `kubectl get pods -n <namespace>` — check pod status and restarts
- `kubectl logs <pod> -n <namespace>` — get container logs (add `--previous` for crash loops)
- `kubectl describe pod <pod> -n <namespace>` — events, conditions, image pull errors
- `kubectl get events -n <namespace> --sort-by=.lastTimestamp` — recent namespace events
- `kubectl get nodes -o wide` — node status and readiness
- `kubectl top pods -n <namespace>` — resource usage (if metrics-server is running)

For ArgoCD sync issues: `kubectl get applications -n argocd` and check sync status/health. For Helm-based services, check the rendered resources match expectations.

---

## 8. Guardrails

### ⛔ NEVER Do These

1. **Never modify files in `manifests/bootstrap/` directly** — They are auto-generated by `generate-apps.sh`. Your changes will be overwritten by the `generate-apps.yml` GitHub Actions workflow.

2. **Never hardcode secrets, API keys, or tokens** in any file. Use External Secrets Operator for cluster secrets and GitHub Actions secrets for CI/CD.

3. **Never modify `root.hcl` or `_backend.tf` files** without explicit user instruction. These control the Terraform state backend and misconfiguration can cause state loss.

4. **Never delete or modify PersistentVolume/PersistentVolumeClaim manifests** without explicit user instruction. This risks permanent data loss for stateful applications.

5. **Never change sync wave assignments** in `generate-apps.sh` without understanding the dependency chain. Incorrect ordering can cause deployment failures (e.g., deploying an app before its secret store is ready).

6. **Never modify `generate-apps.sh` namespace_map or sync_wave_map** without understanding the impact on all services. Changes affect every ArgoCD Application.

7. **Always use pull requests for Terragrunt module changes** — The `deploy.yaml` workflow runs `terragrunt plan` on PRs for review. Never push Terragrunt changes directly to main.

### ✅ Safe Operations

- Creating new service directories in `manifests/cluster/`
- Adding entries to `manifests/cluster/kustomization.yaml`
- Modifying deployment images, resource limits, environment variables
- Updating Helm chart `values.yaml` files
- Creating/modifying documentation in `docs/`
- Creating/modifying `.windsurfrules`, `AGENTS.md`, `opencode.json`

---

## 9. Sensitive Information

### ✅ Safe to Reference (Non-Secret)

These values appear in committed files and are safe to use:

- **Domain**: `cowlab.org`
- **Node IPs**: 10.0.0.140, 10.0.0.141, 10.0.0.142, 10.0.0.143
- **kube-vip VIP**: 10.0.0.200
- **Traefik LB IP**: 10.0.0.99
- **Service names and namespaces**: All visible in `manifests/cluster/kustomization.yaml`
- **Port numbers**: All visible in service.yaml and ingress.yaml files
- **Container images**: All visible in deployment.yaml files
- **Helm chart versions**: All visible in helmfile.yaml and helm-chart.yaml files
- **ArgoCD host**: argocd.cowlab.org
- **GitHub repo URL**: https://github.com/myles-coleman/homelab-cluster

### 🔒 Secrets (Never Hardcode)

These values must NEVER appear in any committed file:

- **API tokens**: Authentik, Cloudflare, Tailscale
- **OAuth client secrets**: Dex OIDC, ArgoCD SSO
- **k3s join token**: Used for node registration
- **KUBECONFIG contents**: Cluster access credentials
- **Bitwarden/Vaultwarden credentials**: Master password, API keys
- **AWS credentials**: Access keys for S3 state bucket (managed via OIDC)
- **Any password or private key**

### How Secrets Are Managed

- **CI/CD secrets**: Stored in GitHub Actions secrets (`Settings → Secrets and variables → Actions`). Referenced in workflow files as `${{ secrets.NAME }}`.
- **Cluster workload secrets**: Flow through External Secrets Operator: Vaultwarden (source) → bitwarden-cli (webhook) → ESO → Kubernetes Secret.
- **Terragrunt secrets**: Passed via environment variables in CI or from GitHub Actions secrets. The `.gitignore` excludes `manifests/cluster/external-secrets/secret.yaml`.
