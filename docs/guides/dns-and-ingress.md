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

## Ingress with Gateway API

All cluster services use the Kubernetes Gateway API: an `HTTPRoute` attached to the shared
`Gateway/traefik-gateway` in namespace `traefik`. **Never use a standard Kubernetes `Ingress`
(`networking.k8s.io/v1`)** — that provider is disabled — and `IngressRoute` is retired.

### Standard HTTPRoute Pattern

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: <service-name>
  namespace: <service-name>
spec:
  parentRefs:
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: traefik-gateway
      namespace: traefik
      sectionName: websecure
  hostnames:
    - <service-name>.cowlab.org
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /
      backendRefs:
        - group: ""
          kind: Service
          name: <service-name>
          port: <port>
          weight: 1
```

### Key Details

- **Gateway**: one `Gateway` (`traefik-gateway`) and one `GatewayClass` (`traefik`), both owned by
  the Traefik Helm chart through `gateway.listeners` in `manifests/cluster/traefik/values.yaml`.
  Listeners use the in-pod entrypoint ports: `web` = 8000/HTTP, `websecure` = 8443/HTTPS.
- **Section name**: attach to `websecure`. The `web` listener exists only for the HTTP → HTTPS
  redirect and serves no routes.
- **TLS**: the listener's `certificateRefs` (`cowlab-production-tls`) terminates TLS. Routes carry
  no `tls` block.
- **Cross-namespace**: both listeners set `allowedRoutes.namespaces.from: All`, so an `HTTPRoute`
  in any namespace can attach. This replaces the old `allowCrossNamespace` flag.
- **Redirect**: `http://` requests get a `308` to `https://<host>/` with no port in the `Location`
  header.
- **Verification**: `kubectl get httproute -n <namespace>` must show `Accepted=True` and
  `ResolvedRefs=True`.

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
       → HTTPRoute hostnames match
       → Kubernetes Service
       → Pod
```

For tunnel-based access:
```
Client → Cloudflare Edge
       → Cloudflare Tunnel → cloudflared pod
       → Traefik (traefik.traefik.svc.cluster.local:80)
       → HTTPRoute hostnames match
       → Kubernetes Service
       → Pod
```
