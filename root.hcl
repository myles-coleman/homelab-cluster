locals {
  tofu_paths = [
    "/snap/bin/tofu",                    # Snap installation (Ubuntu/Debian)
    "/run/current-system/sw/bin/tofu",  # NixOS system profile
    "${get_env("HOME")}/.nix-profile/bin/tofu",  # NixOS user profile
    "/usr/local/bin/tofu",              # Homebrew (macOS) or manual install
    "/usr/bin/tofu",                    # System package manager
  ]
  
  terraform_binary = coalesce([
    for path in local.tofu_paths : path if fileexists(path)
  ]...)
  
  inputs = yamldecode(file("common-inputs.yaml"))
  
  common_vars = {
    aws_region = "us-west-1"
    environment = "dev"
    project_name = "myles-homelab"
  }
}

terraform_binary = local.terraform_binary

remote_state {
  backend = "s3"
  generate = {
    path      = "backend.tf"
    if_exists = "overwrite_terragrunt"
  }
  config = {
    bucket         = "myles-homelab-tfstate"
    key            = "${path_relative_to_include()}/terraform.tfstate"
    region         = "us-west-1"
    encrypt        = true
    use_lockfile   = true
  }
}