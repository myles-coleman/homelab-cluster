output "outpost_token_identifier" {
  description = "The identifier of the auto-generated outpost token (copy key from Authentik UI)"
  value       = "ak-outpost-${authentik_outpost.traefik.id}-api"
}

output "outpost_id" {
  description = "The ID of the Authentik outpost"
  value       = authentik_outpost.traefik.id
}

output "applications" {
  description = "Map of created applications with their IDs"
  value = {
    for k, v in authentik_application.apps : k => {
      id   = v.id
      uuid = v.uuid
      slug = v.slug
    }
  }
}

output "providers" {
  description = "Map of created proxy providers with their IDs"
  value = {
    for k, v in authentik_provider_proxy.apps : k => {
      id        = v.id
      client_id = v.client_id
    }
  }
}
