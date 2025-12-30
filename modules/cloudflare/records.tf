resource "cloudflare_dns_record" "chat" {
  zone_id = cloudflare_zone.cowlab.id
  name    = "chat.cowlab.org"
  type    = "A"
  content = var.homelab_ip
  ttl     = 1
}

resource "cloudflare_dns_record" "npm" {
  zone_id = cloudflare_zone.cowlab.id
  name    = "npm.cowlab.org"
  type    = "A"
  content = var.homelab_ip
  ttl     = 1
}

resource "cloudflare_dns_record" "pihole" {
  zone_id = cloudflare_zone.cowlab.id
  name    = "pihole.cowlab.org"
  type    = "A"
  content = var.homelab_ip
  ttl     = 1
}

resource "cloudflare_dns_record" "pikvm" {
  zone_id = cloudflare_zone.cowlab.id
  name    = "pikvm.cowlab.org"
  type    = "A"
  content = var.homelab_ip
  ttl     = 1
}

resource "cloudflare_dns_record" "vaultwarden" {
  zone_id = cloudflare_zone.cowlab.id
  name    = "vaultwarden.cowlab.org"
  type    = "A"
  content = var.homelab_ip
  ttl     = 1
}

resource "cloudflare_dns_record" "lb_wildcard" {
  zone_id = cloudflare_zone.cowlab.id
  name    = "*.cowlab.org"
  type    = "A"
  content = var.lb_ip
  ttl     = 1
}

resource "cloudflare_dns_record" "docs_cname" {
  zone_id = cloudflare_zone.cowlab.id
  name    = "docs.cowlab.org"
  type    = "CNAME"
  content = "myles-coleman.github.io"
  ttl     = 1
}