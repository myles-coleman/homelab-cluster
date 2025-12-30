output "zone_id" {
  description = "The Cloudflare zone ID"
  value       = cloudflare_zone.cowlab.id
}
