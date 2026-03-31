# DNS and Ingress

This guide covers how DNS resolution and ingress routing work in the cluster, and how to configure them for new services.

## DNS Overview

All DNS is managed through **Cloudflare**:

| Record | Type | Value | Purpose |
|--------|------|-------|---------|
| `*.cowlab.org` | A | 10.0.0.99 | Wildcard — routes all subdomains to Traefik LB |
| `docs.cowlab.org` | CNAME | GitHub Pages | Documentation site |
| `pihole.cowlab.org` | A | Direct IP | Pi-hole DNS (not via Traefik) |
| `pikvm.cowlab.org` | A | Direct IP | PiKVM (not via Traefik) |
| `vaultwarden.cowlab.org` | A | Direct IP | Vaultwarden (not via Traefik) |

The wildcard record means **any new `<service>.cowlab.org` subdomain automatically resolves** to the Traefik load balancer without any DNS changes required.

## Ingress with Traefik IngressRoute

All cluster services use the Traefik `IngressRoute` CRD for HTTP(S) ingress. **Never use standard Kubernetes `Ingress` (`networking.k8s.io/v1`).**

### Standard IngressRoute Pattern

```yaml
apiVersion: traefik.io/v1alpha1
kind: IngressRoute
metadata:
  name: <service-name>
  namespace: <service-name>
  annotations:
    kubernetes.io/ingress.class: traefik-external
spec:
  entryPoints:
    - websecure
  routes:
    - match: Host(`<service-name>.cowlab.org`)
      kind: Rule
      services:
        - name: <service-name>
          port: <port>
```

### Key Details

- **Entry point**: Always `websecure` (HTTPS on port 443). Traefik automatically redirects HTTP → HTTPS.
- **Annotation**: `kubernetes.io/ingress.class: traefik-external` is required.
- **Host matching**: Uses backtick syntax: `` Host(`service.cowlab.org`) ``
- **Cross-namespace**: Traefik is configured with `allowCrossNamespace=true`, so IngressRoutes in any namespace work.

## Adding DNS Records

### For Standard Services

If your service uses `<name>.cowlab.org`, **no DNS changes are needed** — the wildcard record handles it automatically.

### For Non-Standard DNS

If you need a specific DNS record (e.g., a different domain, or an A record pointing to a specific IP instead of the load balancer), add it via Terragrunt:

1. Edit `modules/cloudflare/records.tf`
2. Add a new `cloudflare_record` resource:

```hcl
resource "cloudflare_record" "my_service" {
  zone_id = data.cloudflare_zone.domain.id
  name    = "my-service"
  content = "10.0.0.99"
  type    = "A"
  ttl     = 1
  proxied = false
}
```

3. Open a **pull request** — the `deploy.yaml` workflow runs `terragrunt plan` for review
4. Merge to `main` — `terragrunt apply` creates the record

## Cloudflare Zero Trust Tunnel

For services that need public internet access (outside the home network), the cluster uses a Cloudflare Zero Trust Tunnel:

- **Current routes**: `jellyfin.cowlab.org` → `traefik.traefik.svc.cluster.local:80`
- **Configuration**: Managed via Terragrunt in `modules/cloudflare/tunnel.tf`
- **How it works**: Cloudflare's edge network receives the request and forwards it through an encrypted tunnel to the `cloudflared` pod running in the cluster, which then routes to Traefik

### Adding a New Tunnel Route

1. Edit `modules/cloudflare/tunnel.tf` to add a new ingress rule
2. Create a Cloudflare Access policy if authentication is needed
3. Open a pull request for review

## TLS Certificates

- **cert-manager** handles TLS certificate provisioning
- Deployed at sync wave 1, before Traefik (wave 2)
- Certificates are automatically provisioned for services using HTTPS ingress

## Traffic Flow Summary

```
Client → Cloudflare DNS (*.cowlab.org → 10.0.0.99)
       → Traefik (port 443, websecure entrypoint)
       → IngressRoute Host() match
       → Kubernetes Service
       → Pod
```

For tunnel-based access:
```
Client → Cloudflare Edge
       → Cloudflare Tunnel → cloudflared pod
       → Traefik (traefik.traefik.svc.cluster.local:80)
       → IngressRoute Host() match
       → Kubernetes Service
       → Pod
```
