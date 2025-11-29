variable "authentik_url" {
  description = "The URL of the Authentik instance"
  type        = string
}

variable "outpost_user_id" {
  description = "The user ID to create the outpost token for (typically a service account)"
  type        = number
}

variable "domain" {
  description = "The base domain for applications (e.g., cowlab.org)"
  type        = string
}

variable "applications" {
  description = "Map of applications to protect with forward auth"
  type = map(object({
    name          = string
    slug          = string
    external_host = string
    mode          = optional(string, "forward_single") # forward_single, forward_domain, or proxy
  }))
  default = {}
}
