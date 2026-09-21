include "root" {
  path = find_in_parent_folders("root.hcl")
}

locals {
  common = read_terragrunt_config(find_in_parent_folders("common.hcl"))
}

generate "provider" {
  path      = "_provider.tf"
  if_exists = "overwrite_terragrunt"
  contents  = <<EOF
provider "signoz" {
  endpoint     = "${local.common.locals.signoz_endpoint}"
  access_token = "${local.common.locals.signoz_access_token}"
}
EOF
}
