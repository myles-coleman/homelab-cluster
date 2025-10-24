# PiKVM + Traefik Setup

This directory deploys the resources needed to expose a PiKVM device—running on a local IP (e.g., `10.0.0.175`)—securely over the internet at `https://pikvm.cowlab.org`. The key goals are:

1. **Publicly Trusted Certificate**  
   Users see a valid HTTPS certificate (issued via Let’s Encrypt) when visiting `pikvm.cowlab.org`.

2. **Force HTTPS at PiKVM**  
   PiKVM enforces HTTPS and provides its own certificate, but that certificate is self-signed (e.g., `CN=localhost`).

3. **Re-Encryption**  
   Traefik terminates the external TLS connection (from end-users) for `pikvm.cowlab.org` and then establishes another TLS session (HTTPS) to PiKVM internally. This avoids infinite redirects and browser warnings.

---

## Why This Approach?

- **PiKVM Forces HTTPS**  
  If PiKVM were accessed over HTTP, it would redirect to HTTPS. If Traefik had already terminated TLS and then forwarded only HTTP, this could result in an infinite redirect loop.

- **Certificate Mismatch**  
  PiKVM’s certificate is self-signed for `localhost`. Browsers will show a warning if exposed directly. By letting Traefik present a valid certificate for `pikvm.cowlab.org`, end users see no warning.

- **InsecureSkipVerify**  
  Traefik must ignore the mismatch between `pikvm.cowlab.org` and `CN=localhost` on PiKVM’s certificate. The `ServersTransport` resource with `insecureSkipVerify: true` allows Traefik to trust that self-signed cert internally.

---

## How It Works

1. **ExternalName Service (`service.yaml`)**  
   - `pikvm-external-ssl` points to `10.0.0.175:443`.  
   - This lets Traefik treat PiKVM as if it were a normal Kubernetes Service, even though it’s an external IP.

2. **ServersTransport (`serverstransport.yaml`)**  
   - Instructs Traefik to skip certificate verification when connecting to PiKVM, because PiKVM’s certificate is self-signed and mismatched from the domain `pikvm.cowlab.org`.  
   - Without this, Traefik would refuse the connection or throw errors about invalid certificates.

3. **IngressRoute (`ingress.yaml`)**  
   - Matches `Host('pikvm.cowlab.org')` on the `websecure` (HTTPS) entry point.  
   - Forwards traffic **via HTTPS** (`port: 443` and `scheme: https`) to PiKVM, re-encrypting the connection.  
   - References `serversTransport: "pikvm-transport"` for the certificate validation skip.  
   - Uses `certResolver: "letsencrypt"` so Traefik automatically obtains a valid Let’s Encrypt certificate for `pikvm.cowlab.org`.

When a user visits `https://pikvm.cowlab.org`:

1. Traefik terminates TLS with a trusted Let’s Encrypt certificate.  
2. Traefik creates a new TLS connection to PiKVM at `10.0.0.175:443`.  
3. PiKVM sees valid HTTPS traffic and does not issue additional redirects.
