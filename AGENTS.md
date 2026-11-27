# AGENTS.md — homelab-cluster

This file provides guidance to AI coding agents working in this repository. Read this file first before making any changes.

**Source of truth for active services**: Always check `manifests/cluster/kustomization.yaml` — it lists all currently deployed services. Commented-out entries are disabled.

---

## 1. Architecture Overview

### Cluster Purpose

This is a personal homelab Kubernetes cluster running media management, home automation, and infrastructure services on bare-metal Raspberry Pi 5 hardware, plus one x86_64 gVisor microVM (`sandbox-node`) for sandboxed AI workloads.

### Hardware & Nodes

| Node | Role | IP Address | Hardware |
|------|------|-----------|----------|
| node0 | k3s server (control plane) | 10.0.0.140 | Raspberry Pi 5, aarch64, NVMe SSD |
| node1 | k3s agent (worker) | 10.0.0.141 | Raspberry Pi 5, aarch64, NVMe SSD |
| node2 | k3s agent (worker) | 10.0.0.142 | Raspberry Pi 5, aarch64, NVMe SSD |
| node3 | k3s agent (worker) | 10.0.0.143 | Raspberry Pi 5, aarch64, NVMe SSD |
| node4 | k3s agent (worker) | 10.0.0.144 | Raspberry Pi 5, aarch64, NVMe SSD |
| sandbox-node | AI workload sandbox | — | x86_64 gVisor microVM on the `homelab` host |

### Operating System & Kubernetes

- **OS**: NixOS (unstable channel) — managed in the `nixos` monorepo
- **Kubernetes**: k3s v1.36 (lightweight K8s distribution)
- **Architecture**: aarch64 (ARM64) for every Raspberry Pi 5 node — images destined for them must support aarch64. `sandbox-node` is x86_64, so sandbox workloads need amd64 images.
- **Disabled k3s defaults**: servicelb and traefik are disabled in favor of MetalLB and a custom Traefik deployment

### Networking

| Component | Value |
|-----------|-------|
| **kube-vip VIP** (k3s API server) | 10.0.0.200 |
| **MetalLB / Traefik LoadBalancer IP** | 10.0.0.99 |
| **Domain** | cowlab.org |
| **DNS provider** | Cloudflare (wildcard `*.cowlab.org` → 10.0.0.99) |
| **Ingress controller** | Traefik v3 with Gateway API (`Gateway/traefik-gateway` + `HTTPRoute`) |
| **External access** | Cloudflare Zero Trust Tunnel (no service hostnames published) |

### Key Infrastructure Components

- **ArgoCD** — GitOps continuous delivery, syncs manifests from this repo to cluster
- **Traefik** — Ingress controller and reverse proxy (Helm chart, LB IP 10.0.0.99)
- **MetalLB** — Bare-metal load balancer providing external IPs (Helm chart)
- **Longhorn** — Distributed block storage across nodes with S3 backup (Helm chart)
- **External Secrets Operator (ESO)** — Syncs secrets from Vaultwarden/Bitwarden into Kubernetes (Helm chart)
- **cert-manager** — TLS certificate management (Helm chart)
- **Dex** — OIDC connector for ArgoCD authentication
- **Authentik** — Identity provider (Helm chart)
- **Cloudflare Tunnel** — Secure tunnel for public-facing services without exposing ports

### GPU Workloads

There is no dedicated GPU node. The five Raspberry Pi 5 nodes (`node0`–`node4`) are NVMe-backed aarch64 agents. `sandbox-node` is a separate x86_64 gVisor microVM running on the `homelab` host for AI workloads; it is intended to contain a workload escape to that VM rather than letting it reach the host or the rest of the cluster. `generic-device-plugin` and `llama-cpp` are disabled in the cluster manifests.

---

## 2. Repository Layout

- `manifests/cluster/<service>/` — Per-service Kubernetes manifests (Kustomize). Each service has namespace.yaml, deployment.yaml, service.yaml, httproute.yaml, and kustomization.yaml.
- `manifests/cluster/_template/` — Skeleton manifests for new services (NOT deployed). Copy this when adding a service.
- `manifests/cluster/kustomization.yaml` — Source of truth for all active services.
- `manifests/bootstrap/` — AUTO-GENERATED ArgoCD Application definitions. Never edit directly.
- `manifests/dist/` — AUTO-GENERATED rendered manifests. This is what ArgoCD applies. Never edit directly.
- `docs/` — Plain Markdown documentation, browsable in the repository.
- `scripts/generate-apps.sh` — Generates ArgoCD Application YAMLs from the cluster kustomization.
- `renovate.json` — Renovate config. It bumps image tags in `manifests/cluster/` and is blocked from `manifests/dist/`, so a Renovate PR always needs a re-render (see below).
- `.github/workflows/actions.lock` — machine-generated: the commit every action used by these workflows resolves to.
- `.github/workflows/verify-lockfile.yml` — fails CI when the lockfile is stale.

---

### Generated artifacts — do NOT hand-edit

Two trees in this repository are generated. Editing them by hand produces a change
that either disappears on the next regeneration or, worse, drifts from the source
it claims to represent.

| Tree | Produced by | What it is |
|------|-------------|------------|
| `manifests/dist/` | `scripts/render-dist.sh` | The rendered manifests ArgoCD actually applies: one file per object, named `<Kind>_<namespace>_<name>.yaml`, plus a generated `kustomization.yaml` per service. |
| `manifests/bootstrap/` | `scripts/generate-apps.sh` | One ArgoCD `Application` per active service. |
| `manifests/cluster/*/helm-chart.yaml` and `manifests/cluster/cloudflared/helmchart.yaml` | `helmfile template --skip-deps` | Pre-rendered Helm output. Never edit the file; change `values.yaml` and re-render. |

**Why `manifests/dist/` exists.** ArgoCD used to build `manifests/cluster/<service>/` at sync
time, so nothing in the repository proved what reached the cluster. `manifests/dist/` is that
proof: the deployed YAML is committed, reviewable as a diff, and checked by CI
before merge.

**What to do instead of editing `manifests/dist/`:**

```bash
./scripts/render-dist.sh          # regenerate after changing manifests/cluster/
./scripts/render-dist.sh --check  # what CI runs: fail if manifests/dist/ is stale
```

**What CI enforces** (`.github/workflows/dist-verify.yml`, `scripts/verify-dist.py`):

- every image reference in `manifests/dist/` is pinned as `repository:tag@sha256:…`, with
  exceptions listed by exact value in `scripts/dist-allowlist.txt`, each with a reason;
- `manifests/dist/<service>` has the same object set as `kustomize build manifests/cluster/<service>`,
  and the same bodies apart from image digests;
- `manifests/dist/` matches a fresh render;
- every `Namespace`, `PersistentVolumeClaim` and `PersistentVolume` carries
  `argocd.argoproj.io/sync-options: Prune=false`.

**Image updates come from Renovate, and the bot re-renders for you.** `renovate.json` bumps
tags in `manifests/cluster/` and is deliberately blocked from `manifests/dist/`, because that
tree is generated. Its `postUpgradeTasks` runs two scripts before it commits, in this order:

1. `./scripts/render-helm-charts.sh` — re-renders `helm-chart.yaml` for any service whose
   `values.yaml` changed.
2. `./scripts/render-dist.sh` — re-renders `manifests/dist/`.

so a Renovate PR arrives with the rendered tree already updated and `dist-verify` green.

**Helm-based services have two layers, and only the lower one is deployed.** The chart's
`values.yaml` is the source of truth; `helm-chart.yaml` is its render; `manifests/dist/` is
the render of *that*. Editing `values.yaml` alone changes nothing until `helmfile template`
runs — and `dist-verify` cannot catch the disagreement, because it renders from
`helm-chart.yaml` and sees no change. Use `task helm SERVICE=<name>`, which
`render-helm-charts.sh` wraps.

That only works in the self-hosted workflow (`.github/workflows/renovate.yaml`), because
`postUpgradeTasks` is a self-hosted-only feature — so do not swap it for the Mend-hosted app
without also accepting a manual re-render on every PR.

Re-render by hand when you *amend* a Renovate PR, or when its run failed:

```bash
git fetch origin && git switch <renovate-branch>
./scripts/render-dist.sh
git commit -am "chore(dist): re-render after image bump"
git push
```

Every image is pinned as `tag@sha256:…`, so Renovate only has to move the tag and the render
recomputes the digest. Never hand-edit a digest in either tree.

Never delete a `Prune=false` annotation to "clean up" a manifest. Those annotations
are what stops a short render from deleting a namespace or a Longhorn volume.

### Workflow dependencies — the lockfile, not inline SHAs

Workflow `uses:` references name readable tags (`actions/checkout@v7.0.1`). What pins them
is `.github/workflows/actions.lock`, which records the commit each reference resolves to,
including the actions a composite action calls itself, plus the source repository's numeric
identity. It replaces pinning by hand inside the workflow files.

- After adding or changing any `uses:`, run `gh actions-lock` from the repository root and
  commit the regenerated lockfile in the same change. `.github/workflows/verify-lockfile.yml`
  runs `gh actions-lock --verify` on every pull request, so an un-locked reference fails CI.
- Never edit `actions.lock` by hand; it is machine-generated and the next run overwrites it.
- Keep tags in workflows. A SHA written into a `uses:` is locked as a SHA, which adds nothing
  over the lockfile entry and makes the workflow harder to read.
- Renovate regenerates the lockfile in the same pull request as an action bump. Its lockfile
  support is marked experimental, so review an actions PR as two files: the workflow and
  `actions.lock`. `pinDigests` is disabled for the `github-actions` manager because the
  lockfile is the pin now.
- An action bump does not touch `manifests/`, so the `postUpgradeTasks` render is a no-op for
  it: `render-helm-charts.sh` and `render-dist.sh` still run, find nothing to change, and
  `dist-verify`'s `test` job stays green.

---

## 3. Deployment Flow

### ⚠️ GitOps-Only Workflow

**All cluster changes MUST follow the GitOps flow.** Edit manifests in Git, commit, and let ArgoCD sync them to the cluster. **Never use `kubectl apply`, `kubectl create`, or any imperative write command to deploy or modify resources.** ArgoCD's self-heal will revert manual changes, and imperative applies bypass review, history, and the sync wave dependency chain.

`kubectl` is permitted only for **read-only operations** (e.g., `get`, `logs`, `describe`, `top`, `exec` for debugging) and **one-off operational commands** (e.g., `rollout restart`, `delete pod` to force a restart). It must never be used to apply or patch manifests.

### GitOps Pipeline

On push to main, GitHub Actions runs these workflows based on changed paths:

- **manifests/cluster/** changes → `generate-apps.yml` runs `scripts/generate-apps.sh` to regenerate `manifests/bootstrap/*-app.yaml`, commits and pushes
- **Any push to main** → `release.yaml` runs semantic release (versioning + changelog)

### ArgoCD Bootstrap Chain

This is the core deployment mechanism:

1. **ArgoCD install** — ArgoCD itself is installed outside this repository and is **not** GitOps-managed: there is no `argocd` directory under `manifests/cluster/` and no Application for it. Rebuilding a cluster means installing ArgoCD first, then applying the bootstrap Application below.
2. **Bootstrap Application** — An ArgoCD Application pointing at `manifests/bootstrap/`, which lists all individual `*-app.yaml` files
3. **Per-service Applications** — Each `*-app.yaml` is an ArgoCD Application that syncs `manifests/dist/<service>/` to the cluster

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
| 10 | All application workloads | Default wave for apps (sonarr, radarr, prowlarr, etc.) |

Wave assignments are defined in the `sync_wave_map` associative array in `scripts/generate-apps.sh`. Any service not in the map defaults to wave 10.

### ArgoCD Sync Policy

All applications are configured with:
- **Automated sync**: Changes in Git are automatically applied
- **Prune**: Resources removed from Git are deleted from the cluster
- **Self-heal**: Manual changes to cluster resources are reverted to match Git
- **Retry**: 5 retries with exponential backoff (30s → 2m)

### Admission policy — pod invariants

`manifests/cluster/admission-policy/` holds four `ValidatingAdmissionPolicy` +
binding pairs that match **Pods**, not Deployments. That is deliberate: a policy
that matches a Deployment never sees the pod a controller generates from a custom
resource. These do see the `agents.x-k8s.io Sandbox` pod and the
`ClickHouseInstallation` pods, which no `manifests/dist/` check can reach.

| Rule | Action | Rejects | Exempt namespaces |
|------|--------|---------|-------------------|
| `deny-privileged` | `Deny` | any `privileged: true` container, including init/ephemeral | `kube-system`, `longhorn-system`, `cert-manager` |
| `deny-host-namespaces` | `Deny` | `hostNetwork`, `hostPID`, `hostIPC` | `kube-system`, `metallb-system`, `sandbox-telemetry`, `node-exporter` |
| `deny-unpinned-images` | `Audit, Warn` | any image without `@sha256:<64 hex>` | `kube-system`, `argocd`, `longhorn-system`, `ai-artifacts-site` |
| `require-nonroot` | `Deny` | explicit `runAsUser: 0`, `runAsNonRoot: false`, `allowPrivilegeEscalation: true` | `kube-system`, `longhorn-system`, `cert-manager` |

`deny-unpinned-images` stays non-blocking on purpose. Three Pod manifests in
`dist/` use the chart-hardcoded `busybox:1.35` Helm test hook
(`signoz`, `signoz-k8s-infra`), which is on `scripts/dist-allowlist.txt` because
no chart value overrides it. Promoting the rule would make `helm test` fail in
those namespaces. The rule becomes promotable when those test images are
pinnable, not before.

The exemptions are enumerated in each policy's `matchConditions`, one namespace
per line with a comment naming the measured violation. They are not a label
selector, so adding one is a reviewed line in a manifest.

**Check before promoting a rule.** `task admission` runs
`scripts/verify-admission-policy.sh`: server-side dry-run only, one bad pod per
rule, one exempt pod per rule, a re-submission of every running pod under a fresh
name, and a re-submission of every `Pod` object in `manifests/dist/` — including
Helm test hooks that are not running. A rule may be promoted from `Audit, Warn`
to `Deny` only when both sweeps report zero unexpected findings for it.

**Rollback is deleting the binding, never the policy.** A
`ValidatingAdmissionPolicyBinding` with no binding is inert, so
`git revert <promotion-commit>` and let ArgoCD sync restores admission
immediately while leaving the policy object in place. Do not delete the policy
first: the binding would then have nothing to apply, but the safer, reversible
move is the reverse.

**Mutating policies are deliberately not used.** `MutatingAdmissionPolicy` has no
audit mode — its first apply mutates the whole fleet — so seccomp defaulting and
similar defaulting work needs its own canary spec first.


---

## 4. Service Onboarding Guide

To add a new service to the cluster, follow these steps:

### Step 1: Create the Service Directory

Copy the template: `cp -r manifests/cluster/_template manifests/cluster/<service-name>`

### Step 2: Edit the Manifests

Update all placeholder values in the copied files (namespace.yaml, deployment.yaml, service.yaml, httproute.yaml, kustomization.yaml). Use any existing service (e.g., `manifests/cluster/sonarr/`) as a reference for a complete working example.

### Step 3: Optional Resources

- **PersistentVolumeClaim** — if the service needs persistent storage (Longhorn is the default StorageClass)
- **ExternalSecret** — if the service needs secrets from Vaultwarden (see Section 5 for patterns)
- **ConfigMap** — for non-sensitive configuration

### Step 4: Register the Service

Add the service directory name to the `resources` list in `manifests/cluster/kustomization.yaml`.

### Step 5: Generate ArgoCD Application

Either:
- **Push to main** — the `generate-apps.yml` GitHub Actions workflow runs `scripts/generate-apps.sh` automatically
- **Run locally** — `./scripts/generate-apps.sh` (generates `manifests/bootstrap/<service-name>-app.yaml`)

### Step 6: Custom Namespace (if needed)

If the Kubernetes namespace differs from the directory name, add a mapping to the `namespace_map` in `scripts/generate-apps.sh`. Current mappings: `metallb` → `metallb-system`, `generic-device-plugin` → `device-system`, `cert-manager-csi-driver` → `cert-manager`.

### Step 7: Custom Sync Wave (if needed)

If the service needs to deploy before wave 10 (e.g., it's infrastructure), add to the `sync_wave_map` in `scripts/generate-apps.sh`.

---

## 5. Manifest Patterns Reference

### Raw Manifest Services (e.g., sonarr, radarr, prowlarr)

Standard pattern: `namespace.yaml` + `deployment.yaml` + `service.yaml` + `httproute.yaml` + `kustomization.yaml`, plus optional PVC/PV files.

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

### Gateway API HTTPRoute Pattern

Always use a Gateway API `HTTPRoute` attached to `Gateway/traefik-gateway` in namespace `traefik`
with `sectionName: websecure`. **Never** use a standard Kubernetes `networking.k8s.io/v1 Ingress`
(that provider is disabled) and never a Traefik `IngressRoute` (retired — the CRD provider stays
enabled only for `Middleware` as `ExtensionRef`).

- `parentRefs` must state `group: gateway.networking.k8s.io`, `kind: Gateway`,
  `name: traefik-gateway`, `namespace: traefik`, `sectionName: websecure`.
- `backendRefs` must state `group: ""`, `kind: Service`, `name`, `port`, `weight: 1`, and the rule
  must state `matches: [{path: {type: PathPrefix, value: /}}]`. Omitted defaults leave the object
  permanently `OutOfSync` in ArgoCD even though it serves traffic.
- Do not add `tls` or `certResolver`: the `websecure` listener terminates TLS with the
  `cowlab-production-tls` wildcard certificate.
- Host is `<service>.cowlab.org`; the wildcard record resolves it with no DNS change.
- Verify with `kubectl get httproute -n <namespace>` showing `Accepted=True` and
  `ResolvedRefs=True`.

---

## 6. Debugging

When diagnosing cluster issues, always gather information yourself using kubectl rather than asking the user to paste logs. The kubeconfig is available on the local machine. Useful commands:

- `kubectl get pods -n <namespace>` — check pod status and restarts
- `kubectl logs <pod> -n <namespace>` — get container logs (add `--previous` for crash loops)
- `kubectl describe pod <pod> -n <namespace>` — events, conditions, image pull errors
- `kubectl get events -n <namespace> --sort-by=.lastTimestamp` — recent namespace events
- `kubectl get nodes -o wide` — node status and readiness
- `kubectl top pods -n <namespace>` — resource usage (if metrics-server is running)

For ArgoCD sync issues: `kubectl get applications -n argocd` and check sync status/health. For Helm-based services, check the rendered resources match expectations.

---

## 7. Guardrails

### ⛔ NEVER Do These

1. **Never modify files in `manifests/bootstrap/` directly** — They are auto-generated by `scripts/generate-apps.sh`. Your changes will be overwritten by the `generate-apps.yml` GitHub Actions workflow.

2. **Never hardcode secrets, API keys, or tokens** in any file. Use External Secrets Operator for cluster secrets and GitHub Actions secrets for CI/CD.

3. **Never delete or modify PersistentVolume/PersistentVolumeClaim manifests** without explicit user instruction. This risks permanent data loss for stateful applications.

4. **Never change sync wave assignments** in `scripts/generate-apps.sh` without understanding the dependency chain. Incorrect ordering can cause deployment failures (e.g., deploying an app before its secret store is ready).

5. **Never modify `scripts/generate-apps.sh` namespace_map or sync_wave_map** without understanding the impact on all services. Changes affect every ArgoCD Application.

6. **Never hand-edit `.github/workflows/actions.lock`**, and never merge a `uses:` change without the lockfile update `gh actions-lock` produces. CI fails on a stale lockfile.

### ✅ Safe Operations

- Creating new service directories in `manifests/cluster/`
- Adding entries to `manifests/cluster/kustomization.yaml`
- Modifying deployment images, resource limits, environment variables
- Updating Helm chart `values.yaml` files
- Creating/modifying documentation in `docs/`
- Creating/modifying `.windsurfrules`, `AGENTS.md`, `opencode.json`

---

## 8. Sensitive Information

### ✅ Safe to Reference (Non-Secret)

These values appear in committed files and are safe to use:

- **Domain**: `cowlab.org`
- **Node IPs**: 10.0.0.140, 10.0.0.141, 10.0.0.142, 10.0.0.143, 10.0.0.144
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

### Agent broker key rotation

The agent broker's long-lived credentials — the `agent-mtls` CA key and the GitHub App private key — are rotated every 90 days, or immediately on suspected exposure. The procedure (CA rotation, leaf reissue order, trust redistribution, App key swap, and expiry monitoring) is in [`docs/operator-guide/runbooks/agent-mtls-rotation.md`](docs/operator-guide/runbooks/agent-mtls-rotation.md). The recorded reason the cadence matters — k3s secret encryption at rest is not enabled — is in [`docs/operator-guide/runbooks/agent-secret-encryption.md`](docs/operator-guide/runbooks/agent-secret-encryption.md).
