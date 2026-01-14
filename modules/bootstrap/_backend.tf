terraform {
  backend "s3" {
    bucket       = "myles-homelab-tfstate"
    key          = "bootstrap/terraform.tfstate"
    region       = "us-west-1"
    encrypt      = true
    use_lockfile = true
  }
}
