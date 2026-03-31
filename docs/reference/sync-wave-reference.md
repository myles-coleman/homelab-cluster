# Sync Wave Reference

ArgoCD uses **sync waves** to control the order in which services are deployed. Lower wave numbers deploy first, ensuring dependencies are ready before dependents start.

## Wave Assignments

| Wave | Service | Category | Rationale |
|------|---------|----------|-----------|
| 0 | external-secrets | Secrets | Must be running before any service that needs secrets from Vaultwarden |
| 1 | cert-manager | TLS | TLS certificates must be available before ingress is configured |
| 2 | traefik | Networking | Ingress controller must be ready to route traffic |
| 2 | metallb | Networking | Load balancer must be ready to assign external IPs to Traefik |
| 3 | longhorn | Storage | Distributed storage must be available before any service with PVCs |
| 4 | dex | Authentication | OIDC must be ready before ArgoCD UI needs authentication |
| 10 | *(all others)* | Applications | Default wave for application workloads |

## Dependency Chain

```
Wave 0: external-secrets
   │    (secrets available)
   ▼
Wave 1: cert-manager
   │    (TLS certificates available)
   ▼
Wave 2: traefik, metallb
   │    (ingress + load balancer ready)
   ▼
Wave 3: longhorn
   │    (persistent storage ready)
   ▼
Wave 4: dex
   │    (authentication ready)
   ▼
Wave 10: sonarr, radarr, jellyfin, ...
         (application workloads)
```

## How Waves Are Assigned

Wave assignments are defined in the `sync_wave_map` associative array in `generate-apps.sh`:

```bash
declare -A sync_wave_map
sync_wave_map["external-secrets"]="0"
sync_wave_map["cert-manager"]="1"
sync_wave_map["traefik"]="2"
sync_wave_map["metallb"]="2"
sync_wave_map["longhorn"]="3"
sync_wave_map["dex"]="4"
# All other services default to wave 10
```

The `generate-apps.sh` script applies the wave as an annotation on each ArgoCD Application:

```yaml
annotations:
  argocd.argoproj.io/sync-wave: "10"
```

## Adding a Custom Wave

If a new infrastructure service needs to deploy before wave 10:

1. Determine which existing services it depends on (must be in a lower wave)
2. Determine which services depend on it (must be in a higher wave)
3. Add the mapping in `generate-apps.sh`:
   ```bash
   sync_wave_map["my-infra-service"]="<wave-number>"
   ```
4. Run `./generate-apps.sh` or push to main to regenerate ArgoCD Applications

**⚠️ Warning**: Changing wave assignments can break the deployment chain. Always verify that dependencies are satisfied before modifying waves. See `AGENTS.md` Section 7 (Guardrails) for more details.
