# homelab-cluster

A GitOps-managed Kubernetes homelab cluster running on Raspberry Pi 5 nodes with NixOS, orchestrated by ArgoCD.

## 🏗️ Architecture

- **OS**: NixOS (declarative, reproducible configuration)
- **Kubernetes**: k3s lightweight distribution
- **GitOps**: ArgoCD for automated deployments
- **Storage**: Longhorn distributed block storage
- **Ingress**: Traefik with automatic TLS via cert-manager
- **Load Balancer**: MetalLB for bare-metal service exposure
- **Secrets**: External Secrets Operator with Vaultwarden backend

## 🚀 Deployment

### Sync Waves

Applications deploy in order:
1. **Wave 0**: External Secrets
2. **Wave 1**: Cert-manager
3. **Wave 2**: Traefik, MetalLB
4. **Wave 3**: Longhorn
5. **Wave 4**: Dex
6. **Wave 10**: All other applications

## 📁 Repository Structure

```
.
├── manifests/
│   ├── bootstrap/          # ArgoCD Application definitions
│   └── cluster/            # Kubernetes manifests per application
├── modules/                # Reusable Kustomize modules
├── docs/                   # MkDocs documentation
├── generate-apps.sh        # ArgoCD app generator script
└── common-inputs.yaml      # Shared configuration values
```

## 🔧 Key Features

- **Declarative GitOps**: All infrastructure as code
- **Automated Sync**: ArgoCD monitors Git and auto-deploys changes
- **Dependency Management**: Sync waves ensure proper deployment order
- **Secret Management**: External Secrets Operator with Vault
- **TLS Automation**: Cert-manager with Let's Encrypt
- **Modular Design**: Reusable Kustomize modules for common patterns
# homelab-cluster-mcp
