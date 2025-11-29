resource "authentik_provider_proxy" "apps" {
  for_each = var.applications

  name               = each.value.name
  external_host      = each.value.external_host
  authorization_flow = data.authentik_flow.default_authorization.id
  invalidation_flow  = data.authentik_flow.default_invalidation.id
  mode = each.value.mode
  access_token_validity = "hours=8"
  internal_host_ssl_validation = false
}
