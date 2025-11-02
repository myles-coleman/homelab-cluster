terraform_binary = "tofu"

remote_state {
  backend = "s3"
  generate = {
    path      = "_backend.tf"
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
