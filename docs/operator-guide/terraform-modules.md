# Terraform/Terragrunt Modules

This page documents the infrastructure-as-code modules in `modules/`. All modules use **OpenTofu** (via Terragrunt's `terraform_binary = "tofu"` setting) with an **S3 remote state backend**.

## Shared Patterns

### Configuration Hierarchy

```
root.hcl                          # S3 backend config, tofu binary
├── common-inputs.yaml            # Shared variables (env, domain, etc.)
└── modules/
    ├── terragrunt.hcl            # Common include (find_in_parent_folders)
    └── <module>/
        └── terragrunt.hcl       # Module-specific inputs, deps, providers
```

- **`root.hcl`** — Defines the S3 backend and sets `tofu` as the Terraform binary
- **`common-inputs.yaml`** — Shared variables: environment (`production`), project name (`homelab-cluster`), cluster name, AWS region, kubeconfig path, ArgoCD host, Authentik URL, domain (`cowlab.org`)
- **`modules/terragrunt.hcl`** — Common include that references `root.hcl` via `find_in_parent_folders()`
- **Per-module `terragrunt.hcl`** — Loads common inputs, defines module-specific inputs, declares dependencies, and generates provider configurations (Kubernetes, Helm) from kubeconfig

### CI/CD Pipeline

Infrastructure changes are deployed via the `deploy.yaml` GitHub Actions workflow:

- **On pull request** — Runs `terragrunt plan` for review (no changes applied)
- **On merge to main** — Runs `terragrunt apply`
- **Network access** — Uses Tailscale VPN to reach the cluster from CI runners
- **State access** — Uses AWS OIDC (no static credentials) for S3 state bucket

**⚠️ Always open a pull request for Terragrunt changes** — never push directly to main.

## Module: `bootstrap`

- **Purpose**: Creates the foundational AWS resources needed by all other modules
- **Key resources**:
    - AWS OIDC provider (for GitHub Actions → AWS authentication without static credentials)
    - S3 bucket for Terraform state storage
- **Notes**: Must be applied first. This is the only module that bootstraps its own state.

## Module: `argocd-install`

- **Purpose**: Installs ArgoCD into the cluster via Helm chart
- **Key resources**:
    - Helm release for ArgoCD
    - Kubernetes namespace (`argocd`)
    - Random client secret for Dex OIDC integration
- **Inputs**: `argocd_host`, `dex_url`, `github_username`
- **Configuration**:
    - RBAC policies for ArgoCD access control
    - Prometheus metrics enabled
    - Dex OIDC integration with Authentik as upstream IdP
- **Providers**: Kubernetes, Helm (generated from kubeconfig)

## Module: `argocd-configure`

- **Purpose**: Creates the bootstrap ArgoCD Application that manages all other applications
- **Key resources**:
    - `argocd_application` resource named `bootstrap`
    - Points to `manifests/bootstrap/` in this repository
- **Depends on**: `argocd-install` (declared via Terragrunt `dependency` block)
- **Inputs**: `argocd_host`
- **Providers**: Kubernetes (generated from kubeconfig)
- **Notes**: This is the link between Terragrunt-managed infrastructure and GitOps-managed applications

## Module: `cloudflare`

- **Purpose**: Manages DNS records and Cloudflare Zero Trust Tunnel
- **Key resources**:
    - DNS A records: wildcard (`*.cowlab.org` → LB IP), plus specific records for chat, npm, pihole, pikvm, vaultwarden
    - DNS CNAME record: `docs.cowlab.org` → GitHub Pages
    - Cloudflare Zero Trust Tunnel: routes `jellyfin.cowlab.org` to `traefik.traefik.svc.cluster.local:80`
    - Tunnel token (used by `cloudflared` pod in-cluster)
- **Configuration files**:
    - `records.tf` — DNS record definitions
    - `tunnel.tf` — Tunnel and routing configuration

## Module: `s3-backups`

- **Purpose**: S3 infrastructure for Longhorn distributed storage backups
- **Key resources**:
    - S3 bucket for backup data
    - IAM user with scoped permissions
    - IAM policy for bucket access
- **Notes**: Longhorn is configured to use this bucket for automatic volume backups

## Module: `authentik`

- **Purpose**: Configuration for the Authentik identity provider
- **Key resources**:
    - Authentik provider and application resources
- **Notes**: Authentik itself is deployed as a Helm chart in `manifests/cluster/authentik/`. This module manages its configuration (providers, applications, flows) via Terraform.
