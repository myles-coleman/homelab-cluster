# DNS and Ingress

This guide covers how DNS resolution and ingress routing work in the cluster, and how to configure them for new services.

## DNS Overview

All DNS is managed through **Cloudflare**:

| Record | Type | Value | Purpose |
|--------|------|-------|---------|
| `*.cowlab.org` | A | 10.0.0.99 | Wildcard — every subdomain resolves to the Traefik LB, **LAN only** |
| `<service>.cowlab.org` | CNAME, proxied | `<tunnel-id>.cfargotunnel.com` | Opt-in remote access through the Cloudflare tunnel |
| `docs.cowlab.org` | CNAME | GitHub Pages | Retired: formerly the MkDocs site, no longer deployed |
| `pihole.cowlab.org` | A | Direct IP | Pi-hole DNS (not via Traefik) |
| `pikvm.cowlab.org` | A | Direct IP | PiKVM (not via Traefik) |
| `vaultwarden.cowlab.org` | A | Direct IP | Vaultwarden (not via Traefik) |

The wildcard record means **any new `<service>.cowlab.org` subdomain automatically resolves** to the Traefik load balancer without any DNS changes — from inside the network. It points at a private address, so it does nothing for a remote browser. Remote access is a second, per-host record: a proxied CNAME to the tunnel, described below.

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

If your service uses `<name>.cowlab.org` and only needs to be reachable from the
LAN, **no DNS changes are needed** — the wildcard record handles it.

If it also needs to be reachable from outside, add one proxied CNAME in
`modules/cloudflare/records.tf` in
[`myles-coleman/homelab-terraform`](https://github.com/myles-coleman/homelab-terraform),
then open a pull request there — that repository's `deploy.yaml` plans on the PR
and applies on merge to `main`. The tunnel needs no matching change: it has a
single `*.cowlab.org` rule.

DNS is managed as code, just not in this repository. Nothing in `homelab-cluster`
configures Cloudflare. These records are the last Terraform-owned piece of
publishing a service; they move to external-dns, driven by HTTPRoute annotations,
as a follow-up.

### For Non-Standard DNS

If you need a specific DNS record (e.g., a different domain, or an A record pointing to a specific IP instead of the load balancer), add it to `modules/cloudflare/records.tf` in [`myles-coleman/homelab-terraform`](https://github.com/myles-coleman/homelab-terraform), then open a pull request there — that repository's `deploy.yaml` plans on the PR and applies on merge to `main`.

DNS is managed as code, just not in this repository. Nothing in `homelab-cluster` configures Cloudflare.

## Cloudflare Zero Trust Tunnel

For services that need public internet access (outside the home network), the cluster uses a Cloudflare Zero Trust Tunnel:

- **Ingress**: one `*.cowlab.org` rule pointing at `https://traefik.traefik.svc.cluster.local:443`, with `match_sn_ito_host` so cloudflared presents the request's `Host` as SNI and the Gateway's wildcard certificate covers it. `jellyfin.cowlab.org` keeps its own rule above it because it targets the `homelab` host directly rather than the cluster. The required `http_status:404` catch-all stays last.
- **Configuration**: Managed by the `cloudflare` unit in [`myles-coleman/homelab-terraform`](https://github.com/myles-coleman/homelab-terraform) (`tunnel.tf`); the cluster side is only the tunnel token in `manifests/cluster/cloudflared/values.yaml`
- **How it works**: Cloudflare's edge network receives the request and forwards it through an encrypted tunnel to the `cloudflared` pod running in the cluster, which then routes to Traefik

### Adding Remote Access to a Service

1. Give the host an `HTTPRoute` in this repository, as for any other service.
2. Add a proxied CNAME for it in `modules/cloudflare/records.tf` in [`myles-coleman/homelab-terraform`](https://github.com/myles-coleman/homelab-terraform) — that is the **entire** tunnel-side change, because the ingress is one wildcard rule. Do not add an ingress rule.
3. Create a Cloudflare Access policy only for a service that sits outside the Authentik gate.
4. Open a pull request in that repository — its `deploy.yaml` runs `terragrunt plan` for review

A proxied record with no matching `HTTPRoute` reaches Traefik and gets a 404 rather than the tunnel's catch-all. That is the better failure: it is visible in `kubectl get httproute` instead of the Cloudflare dashboard.

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
