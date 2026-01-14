resource "authentik_outpost" "traefik" {
  name = "traefik-forward-auth"
  type = "proxy"

  protocol_providers = [
    for provider in authentik_provider_proxy.apps : provider.id
  ]
}
