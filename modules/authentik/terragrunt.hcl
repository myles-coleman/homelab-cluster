include {
  path = find_in_parent_folders("root.hcl")
}

locals {
  common_inputs   = yamldecode(file(find_in_parent_folders("common-inputs.yaml")))
  authentik_token = get_env("AUTHENTIK_TOKEN")
}

inputs = {
  authentik_url   = local.common_inputs.authentik_url
  domain          = local.common_inputs.domain
  outpost_user_id = 7  # akadmin user ID
  
  applications = {
    jellyfin = {
      name          = "Jellyfin"
      slug          = "jellyfin"
      external_host = "https://jellyfin.${local.common_inputs.domain}"
      mode          = "forward_single"
    }
    
    # sonarr = {
    #   name          = "Sonarr"
    #   slug          = "sonarr"
    #   external_host = "https://sonarr.${local.common_inputs.domain}"
    #   mode          = "forward_single"
    # }
    
    # radarr = {
    #   name          = "Radarr"
    #   slug          = "radarr"
    #   external_host = "https://radarr.${local.common_inputs.domain}"
    #   mode          = "forward_single"
    # }
    
    # Example of domain-level forward auth
    # This would protect all *.cowlab.org domains
    # domain_level = {
    #   name          = "Domain Level Auth"
    #   slug          = "domain-auth"
    #   external_host = "https://${local.common_inputs.domain}"
    #   mode          = "forward_domain"
    # }
  }
}

generate "provider" {
  path      = "_provider.tf"
  if_exists = "overwrite_terragrunt"
  contents  = <<EOF
provider "authentik" {
  url   = "${local.common_inputs.authentik_url}"
}
EOF
}
