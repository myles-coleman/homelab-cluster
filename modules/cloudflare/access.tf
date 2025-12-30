# Google OAuth Identity Provider
resource "cloudflare_zero_trust_access_identity_provider" "google" {
  account_id = var.cloudflare_account_id
  name       = "Google"
  type       = "google"

  config = {
    client_id     = var.google_oauth_client_id
    client_secret = var.google_oauth_client_secret
  }
}

# Access Application for Jellyfin
resource "cloudflare_zero_trust_access_application" "jellyfin" {
  account_id                = var.cloudflare_account_id
  name                      = "Jellyfin"
  domain                    = "jellyfin.${var.domain}"
  type                      = "self_hosted"
  session_duration          = "24h"
  auto_redirect_to_identity = true
  allowed_idps              = [cloudflare_zero_trust_access_identity_provider.google.id]

  # Embed the access policy directly in the application
  policies = [
    {
      name     = "Allow authorized users"
      decision = "allow"
      include = [
        {
          email = {
            email = var.allowed_emails[0]
          }
        }
      ]
    }
  ]
}
