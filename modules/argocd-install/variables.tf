variable "chart_version" {
  description = "The version of the ArgoCD Helm Chart to install"
  type        = string
  default     = "6.10.2"
}

variable "kubeconfig_path" {
  description = "Path to the kubeconfig file"
  type        = string
  default     = "~/.kube/config"
}

variable "kubeconfig_context" {
  description = "The Kubernetes context to use from the kubeconfig file"
  type        = string
  default     = "default"
}

variable "argocd_url" {
  description = "The ArgoCD URL"
  type        = string
  default     = "https://argocd.cowlab.org"
}

variable "dex_url" {
  description = "The Dex URL"
  type        = string
  default     = "https://dex.cowlab.org/dex"
}

variable "github_username" {
  description = "Your GitHub username for ArgoCD admin access"
  type        = string
  default     = "myles-coleman"
}