variable "cloudflare_account_id" {
  description = "Cloudflare account ID"
  type        = string
}

variable "cloudflare_zone_id" {
  description = "Cloudflare zone ID for importing existing zone"
  type        = string
}

variable "domain" {
  description = "The domain name for the Cloudflare zone"
  type        = string
  default     = "cowlab.org"
}

variable "lb_ip" {
  description = "The IP address for the load balancer"
  type        = string
}

variable "homelab_ip" {
  description = "The IP address for the homelab server"
  type        = string
}

variable "google_oauth_client_id" {
  description = "Google OAuth Client ID for Cloudflare Access"
  type        = string
  sensitive   = true
}

variable "google_oauth_client_secret" {
  description = "Google OAuth Client Secret for Cloudflare Access"
  type        = string
  sensitive   = true
}

variable "allowed_emails" {
  description = "List of email addresses allowed to access applications via Cloudflare Access"
  type        = list(string)
}
