include {
  path = find_in_parent_folders("root.hcl")
}

locals {
  common_inputs = yamldecode(file(find_in_parent_folders("common-inputs.yaml")))
}

inputs = {
  argocd_host = local.common_inputs.argocd_host
}

dependency "argocd_install" {
  config_path = "${get_repo_root()}/modules/argocd-install"
  skip_outputs = true
}

generate "provider" {
  path      = "_provider.tf"
  if_exists = "overwrite_terragrunt"
  contents  = <<EOF
provider "kubernetes" {
  config_path    = "${local.common_inputs.kubeconfig_path}"
  config_context = "${local.common_inputs.kubeconfig_context}"
}
EOF
}
