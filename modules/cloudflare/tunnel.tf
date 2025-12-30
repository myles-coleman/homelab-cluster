# Random secret for tunnel authentication
resource "random_password" "tunnel_secret" {
  length = 64
}

# Cloudflare Tunnel for exposing services to the internet
resource "cloudflare_zero_trust_tunnel_cloudflared" "homelab" {
  account_id    = var.cloudflare_account_id
  name          = "homelab-tunnel"
  config_src    = "cloudflare"
  tunnel_secret = base64sha256(random_password.tunnel_secret.result)
}

# Tunnel configuration - routes traffic to Traefik
resource "cloudflare_zero_trust_tunnel_cloudflared_config" "homelab" {
  account_id = var.cloudflare_account_id
  tunnel_id  = cloudflare_zero_trust_tunnel_cloudflared.homelab.id

  config = {
    ingress = [
      {
        hostname = "jellyfin.${var.domain}"
        service  = "http://traefik.traefik.svc.cluster.local:80"
      },
      {
        # Catch-all rule (required) - returns 404 for unmatched requests
        service = "http_status:404"
      }
    ]
    warp_routing = {
      enabled = false
    }
  }
}

# DNS record pointing to the tunnel
resource "cloudflare_dns_record" "tunnel_jellyfin" {
  zone_id = cloudflare_zone.cowlab.id
  name    = "jellyfin.${var.domain}"
  type    = "CNAME"
  content = "${cloudflare_zero_trust_tunnel_cloudflared.homelab.id}.cfargotunnel.com"
  proxied = true
  ttl     = 1
}

# Data source to get the tunnel token for use in Kubernetes
data "cloudflare_zero_trust_tunnel_cloudflared_token" "homelab" {
  account_id = var.cloudflare_account_id
  tunnel_id  = cloudflare_zero_trust_tunnel_cloudflared.homelab.id
}
