include {
  path = find_in_parent_folders("root.hcl")
}

locals {
  common_inputs = yamldecode(file(find_in_parent_folders("common-inputs.yaml")))
}

inputs = {
  cluster_name = local.common_inputs.cluster_name
  bucket_name = "myles-homelab-backups"
  environment = local.common_inputs.environment
  aws_region = local.common_inputs.aws_region
  backup_retention_days = 90
}

generate "provider" {
  path      = "_provider.tf"
  if_exists = "overwrite_terragrunt"
  contents  = <<EOF
provider "aws" {
  region = "${local.common_inputs.aws_region}"

  default_tags {
    tags = {
      Environment = "${local.common_inputs.environment}"
      Project     = "${local.common_inputs.common_tags.Project}"
      Terraform   = "${local.common_inputs.common_tags.Terraform}"
      ManagedBy   = "${local.common_inputs.common_tags.ManagedBy}"
    }
  }
}
EOF
}
