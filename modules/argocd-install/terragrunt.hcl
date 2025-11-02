include {
  path = find_in_parent_folders("root.hcl")
}

locals {
  common_inputs = yamldecode(file(find_in_parent_folders("common-inputs.yaml")))
}

inputs = {
  argocd_host = local.common_inputs.argocd_host
  dex_url    = "https://dex.cowlab.org/dex"
  github_username = "myles-coleman"
}

generate "provider" {
  path      = "_provider.tf"
  if_exists = "overwrite_terragrunt"
  contents  = <<EOF
provider "kubernetes" {
  config_path    = "${local.common_inputs.kubeconfig_path}"
  config_context = "${local.common_inputs.kubeconfig_context}"
}
provider "helm" {
  kubernetes = {
    config_path    = "${local.common_inputs.kubeconfig_path}"
    config_context = "${local.common_inputs.kubeconfig_context}"
  }
}
EOF
}
