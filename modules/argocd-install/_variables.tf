variable "argocd_host" {
  description = "The host to use for ArgoCD"
  type        = string
}

variable "dex_url" {
  description = "The Dex URL"
  type        = string
}

variable "github_username" {
  description = "Your GitHub username for ArgoCD admin access"
  type        = string
}
