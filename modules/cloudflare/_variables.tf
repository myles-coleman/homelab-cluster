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
