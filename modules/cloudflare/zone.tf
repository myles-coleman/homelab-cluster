resource "cloudflare_zone" "cowlab" {
  account = {
    id = var.cloudflare_account_id
  }
  name = var.domain
}
