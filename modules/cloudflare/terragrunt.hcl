include {
  path = find_in_parent_folders("root.hcl")
}

locals {
  common_inputs         = yamldecode(file(find_in_parent_folders("common-inputs.yaml")))
  cloudflare_api_token  = get_env("CLOUDFLARE_API_TOKEN")
}

inputs = {
  domain                = local.common_inputs.domain
  cloudflare_account_id = "2d5355a5b98f357a3fb2faf0c1bfc397"
  cloudflare_zone_id    = "69f233ea91536ada17282a49b70420cf"
  lb_ip                 = "10.0.0.99"
  homelab_ip            = "10.0.0.150"
}

generate "provider" {
  path      = "_provider.tf"
  if_exists = "overwrite_terragrunt"
  contents  = <<EOF
provider "cloudflare" {
  api_token = "${local.cloudflare_api_token}"
}
EOF
}
