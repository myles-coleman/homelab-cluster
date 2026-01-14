resource "authentik_application" "apps" {
  for_each = var.applications

  name              = each.value.name
  slug              = each.value.slug
  protocol_provider = authentik_provider_proxy.apps[each.key].id
  group             = "homelab"

  # Optional: Customize application appearance
  # meta_description = "Description of ${each.value.name}"
  # meta_publisher   = "Homelab"
  # meta_icon        = "https://example.com/icon.png"

  policy_engine_mode = "any"
}
