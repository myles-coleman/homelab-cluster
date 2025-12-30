output "zone_id" {
  description = "The Cloudflare zone ID"
  value       = cloudflare_zone.cowlab.id
}

output "tunnel_id" {
  description = "The Cloudflare Tunnel ID"
  value       = cloudflare_zero_trust_tunnel_cloudflared.homelab.id
}

output "tunnel_token" {
  description = "The Cloudflare Tunnel token for cloudflared"
  value       = data.cloudflare_zero_trust_tunnel_cloudflared_token.homelab.token
  sensitive   = true
}
