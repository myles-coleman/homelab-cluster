resource "authentik_token" "outpost" {
  identifier  = "traefik-outpost-token"
  user        = var.outpost_user_id
  description = "API token for Traefik forward auth outpost"
  intent      = "api"
  expiring    = false
  retrieve_key = true
}
