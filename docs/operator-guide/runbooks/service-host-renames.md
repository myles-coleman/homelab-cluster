# Service host renames and the Jellyseerr → Seerr migration

Three hostnames changed at once, and one service was replaced by its upstream
successor. The change spans three owners, and knowing which is which is the whole
runbook: the failure mode is editing a value in the Authentik admin UI that
Terraform owns, watching it work, and having it silently revert on the next
apply.

| Old | New | What breaks if the order is wrong |
|-----|-----|-----------------------------------|
| `jellyseerr.cowlab.org` | `seerr.cowlab.org` | Old bookmarks 400 instead of redirecting |
| `openwebui.cowlab.org` | `chat.cowlab.org` | Old bookmarks 400 instead of redirecting |
| `authentik.cowlab.org` | `auth.cowlab.org` | Google login fails on whichever host is not registered |

## Where each piece lives

| Piece | Owner |
|-------|-------|
| HTTPRoute, Deployment, Service, copyparty stylesheet | **this repo** (`manifests/cluster/`) |
| Authentik application and proxy-provider `external_host` | **`homelab-terraform`**, `modules/authentik/terragrunt.hcl` |
| DNS records and tunnel ingress | **`homelab-terraform`**, `modules/cloudflare/records.tf` and `tunnel.tf` |
| Google OAuth redirect URI | **Google Cloud Console** — the only manual step |

The Authentik values above are Terraform inputs. **Do not change them in the
Authentik admin UI**: the edit works until the next `apply --all`, which puts the
declared value back. That happened once during this migration, on
`internal_host_ssl_validation`, and it showed up as unexplained drift in a later
plan.

## Why a redirect and not a second hostname

Every gated host sits behind one Authentik **proxy provider in forward-auth
(single application) mode**. The outpost matches an incoming request to an
application by exact `X-Forwarded-Host`, and an application carries exactly one
external host. Serving the backend on two hostnames therefore answers `400` on the
hostname the application is not bound to — it does not "work on both".

So `manifests/cluster/*/httproute-legacy-host.yaml` issues a `301` to the new host
instead. A `RequestRedirect` filter needs no application and never reaches a Pod,
so it keeps working regardless of what Authentik is bound to.

`auth.cowlab.org` was the exception: Authentik answers on any `Host`, so its route
listed both names and both really did serve. That name has since been retired —
route, tunnel CNAME and Google URI — so only `auth.cowlab.org` remains.

## Why DNS has two layers

The `*.cowlab.org` wildcard is an A record to the load balancer, `10.0.0.99`.
That is LAN-only: it never leaves the house. Remote access needs a second layer —
a **proxied CNAME to the Cloudflare tunnel** plus an ingress rule in the tunnel.

The tunnel used to carry one ingress rule per hostname, each with its own
`origin_server_name`. It now carries a single `*.cowlab.org` rule using
`match_sn_ito_host`, so cloudflared presents the request's `Host` as SNI and the
Gateway's wildcard certificate covers it. **A new remotely reachable host is
therefore one proxied CNAME** — no ingress rule, and once external-dns owns the
records, only an HTTPRoute.

`chat.cowlab.org` is worth knowing about: it used to be an A record to the
`homelab` host (`10.0.0.150`), where Nginx Proxy Manager answered with its
"Default Site" and had no certificate for the name — the `tlsv1 unrecognized
name` on 443. That record was deleted and replaced by the tunnel CNAME.

## Before the merge

### 1. Register the new IdP redirect URI (Google Cloud Console)

This is the only step that must beat the merge, and it is manual.

The `authentik-outpost` Deployment sets `AUTHENTIK_HOST_BROWSER` to
`https://auth.cowlab.org`, so the moment ArgoCD applies the cluster commit every
login redirect goes to the new host — and Authentik builds the Google
`redirect_uri` from the host the browser is on. If the new URI is not registered,
Google refuses the login and there is no way in through the login page.

Google matches the string exactly, and while both hosts are served both URIs are
reachable, so register both:

- `https://authentik.cowlab.org/source/oauth/callback/google/` (already there)
- `https://auth.cowlab.org/source/oauth/callback/google/` (add)

Adding a URI is a no-op until something uses it, so this is safe to do days ahead.
Do not remove the old one until the old hostname is dropped from
`manifests/cluster/authentik/httproute.yaml`.

### 2. Confirm last night's Longhorn backup

The Seerr migration rewrites the SQLite database in place and is one-way.

```bash
kubectl -n longhorn-system get backups.longhorn.io \
  -o jsonpath='{range .items[?(@.status.volumeName=="backup-dfa46de3c83c467a")]}{.metadata.name}{"\t"}{.status.snapshotCreatedAt}{"\n"}{end}' \
  | sort -k2 | tail -3
```

Keep the `backup-dfa46de3c83c467a` volume untouched; the claim name and the
namespace deliberately do not change (see `manifests/cluster/jellyseerr/kustomization.yaml`).

## Merge order

1. **`homelab-cluster`** — routes, the Seerr workload, the `AUTHENTIK_HOST_BROWSER`
   change. ArgoCD syncs on merge. The new hostnames answer `400` from this point
   until step 2, because the outpost is still bound to the old ones; the old
   hostnames already `301`, so the only broken thing is the URL nobody has yet.
2. **`homelab-terraform`** — DNS records, the tunnel wildcard rule, and the
   Authentik `external_host` values. Merging `main` **applies**, there is no
   approval step: DNS starts resolving through the tunnel and the providers are
   repointed in the same run.

Reversing the order leaves the new hostnames serving `400` with DNS already
pointing at them, which looks like a routing failure and is not one.

## After the apply

Nothing to click. Verify:

```bash
# The outpost should accept both new hosts.
kubectl -n authentik port-forward svc/authentik-outpost 19000:9000 &
for h in chat.cowlab.org seerr.cowlab.org; do
  printf '%s ' "$h"
  curl -s -o /dev/null -w '%{http_code}\n' \
    -H "X-Forwarded-Host: $h" -H "X-Forwarded-Uri: /" \
    -H "X-Forwarded-Proto: https" -H "X-Forwarded-Method: GET" \
    -H "X-Idp-Key: authentik" \
    http://127.0.0.1:19000/outpost.goauthentik.io/auth/traefik
done
kill %1
```

`302` to `https://auth.cowlab.org/...` is the pass. `400` means the provider's
external host has not been applied.

Then end to end, which also proves the tunnel wildcard is routing:

```bash
for h in chat seerr auth; do
  printf '%-8s ' "$h"
  curl -s -o /dev/null -w '%{http_code} %{redirect_url}\n' "https://$h.cowlab.org/"
done
```

### Brand domain (Authentik, manual)

**System → Brands → default → Domain** → `auth.cowlab.org`. This one is *not*
managed by Terraform, so the UI is the only place to set it, and there is nothing
to revert it. It is used in email templates and default flow links; it does not
affect routing, which is why it is last.

## Verifying the Seerr migration

```bash
kubectl -n jellyseerr logs deploy/seerr | grep -i -E "migrat|error" | head
kubectl -n jellyseerr get pods -w
```

A healthy first start reports the settings migrations running to completion, then
`Server ready on port 5055`. Two things that look like failures and are not:

- `Skipping starting the scheduled jobs as we have no Plex/Jellyfin/Emby servers
  setup yet` — this cluster has no media server configured, so the request
  scheduler has nothing to schedule.
- The first sync leaves the Pod `0/1` for a minute or two: the new Deployment
  cannot attach the ReadWriteOnce config volume until ArgoCD has pruned the old
  `jellyseerr` Deployment that still holds it.

The container runs as uid 1000, so a permission error on `/app/config` means the
`fsGroup` chown did not happen — check that the Pod was actually recreated rather
than reused.

## Rolling back

- **Routing**: revert the `homelab-cluster` commit. The legacy-host routes and the
  new ones are separate objects, so a revert is a clean delete of the redirect.
- **DNS / Authentik**: revert the `homelab-terraform` commit. The apply restores
  the previous records and provider hosts. Note that the dead `chat` A record
  comes back with it.
- **Seerr**: there is no downgrade path. Restore
  `backup-dfa46de3c83c467a` from the Longhorn backup taken before the migration,
  then revert the image to `fallenbagel/jellyseerr` — accepting that the old image
  is end-of-life.

## Removing the old hostnames later

The identity provider's old name is already gone: `authentik.cowlab.org` was
dropped from the route, from its tunnel CNAME, and from the Google console — in
that order, so nothing was ever pointing at a host that no longer answered.

What remains are the two application hostnames, still serving their 301s. Once the
new names have been live for a while:

1. Delete `httproute-legacy-host.yaml` from `open-webui` and `jellyseerr` in this
   repo, and remove them from each `kustomization.yaml`.
2. In `homelab-terraform`, delete the `tunnel_jellyseerr` and `tunnel_openwebui`
   records (marked as transition aliases in `records.tf`).

Neither needs a Google console change: only the `auth.cowlab.org` redirect URI is
registered now. Both old names still resolve inside the LAN through the
`*.cowlab.org` wildcard, and will answer 404 once their routes are gone.
