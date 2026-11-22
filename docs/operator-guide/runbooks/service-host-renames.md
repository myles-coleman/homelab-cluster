# Service host renames and the Jellyseerr → Seerr migration

Three hostnames changed at once, and one service was replaced by its upstream
successor. The repository owns the routing, the workload and the stylesheet; it
does **not** own the Authentik provider bindings or the Google OAuth client, so
this runbook records the order those out-of-band edits have to happen in.

| Old | New | What breaks if the order is wrong |
|-----|-----|-----------------------------------|
| `jellyseerr.cowlab.org` | `seerr.cowlab.org` | Old bookmarks 400 instead of redirecting |
| `openwebui.cowlab.org` | `chat.cowlab.org` | Old bookmarks 400 instead of redirecting |
| `authentik.cowlab.org` | `auth.cowlab.org` | Google login fails on whichever host is not registered |

## Why a redirect and not a second hostname

Every gated host sits behind one Authentik **proxy provider in forward-auth
(single application) mode**. The outpost matches an incoming request to an
application by exact `X-Forwarded-Host`, and an application carries exactly one
external host. Serving the backend on two hostnames therefore answers `400` on the
hostname the application is not bound to — it does not "work on both".

So `manifests/cluster/*/httproute-legacy-host.yaml` issues a `301` to the new host
instead. A `RequestRedirect` filter needs no application and never reaches a Pod,
so it keeps working regardless of what Authentik is bound to.

`auth.cowlab.org` is the exception: Authentik answers on any `Host`, so its route
lists both names and both really do serve.

## Before the merge

### 0. Register the new IdP redirect URI (Google Cloud Console)

This one is first because it is the only step that must beat the merge. The
`authentik-outpost` Deployment sets `AUTHENTIK_HOST_BROWSER` to
`https://auth.cowlab.org`, so the moment ArgoCD applies this commit every login
redirect goes to the new host — and Authentik builds the Google `redirect_uri`
from the host the browser is on. If the new URI is not registered, Google refuses
the login and there is no way in through the login page.

Google matches the string exactly, and while both hosts are served both URIs are
reachable, so register both:

- `https://authentik.cowlab.org/source/oauth/callback/google/` (already there)
- `https://auth.cowlab.org/source/oauth/callback/google/` (add)

Adding a URI is a no-op until something uses it, so this is safe to do days ahead.
Do not remove the old one until the old hostname is dropped from
`manifests/cluster/authentik/httproute.yaml`.

### 1. Confirm last night's Longhorn backup

The Seerr migration rewrites the SQLite database in place and is one-way.

```bash
kubectl -n longhorn-system get backups.longhorn.io | grep dfa46de3c83c467a
```

Keep the `backup-dfa46de3c83c467a` volume untouched; the claim name and the
namespace deliberately do not change (see `manifests/cluster/jellyseerr/kustomization.yaml`).

## After the merge

ArgoCD syncs on its own. One thing then needs a human.

### 1. Point the applications at the new hosts (Authentik)

For each of the Open WebUI and Seerr applications, in the Authentik admin UI:

- **Applications → Providers → &lt;provider&gt; → External host**: set
  `https://chat.cowlab.org` and `https://seerr.cowlab.org` respectively. This is
  what the outpost matches; until it changes, the new host answers `400`.
- **Applications → &lt;application&gt; → Launch URL**: same change. This is only
  the link in the Authentik user portal, but a stale link there is the next
  bookmark that breaks.

The outpost polls its configuration, so no restart is needed. Verify:

```bash
# The outpost must now accept the new host and reject nothing it should serve.
kubectl -n authentik port-forward svc/authentik-outpost 19000:9000 &
for h in chat.cowlab.org seerr.cowlab.org; do
  curl -s -o /dev/null -w "$h %{http_code}\n" \
    -H "X-Forwarded-Host: $h" -H "X-Forwarded-Uri: /" \
    -H "X-Forwarded-Proto: https" -H "X-Forwarded-Method: GET" \
    -H "X-Idp-Key: authentik" \
    http://127.0.0.1:19000/outpost.goauthentik.io/auth/traefik
done
kill %1
```

`302` to `https://auth.cowlab.org/...` is the pass. `400` means the provider's
external host has not been updated yet.

### 2. Brand domain (Authentik)

**System → Brands → default → Domain** → `auth.cowlab.org`. Used in
email templates and default flow links; it does not affect routing, which is why
it is last.

## Verifying the Seerr migration

```bash
kubectl -n jellyseerr logs deploy/seerr | grep -i -E "migrat|error" | head
kubectl -n jellyseerr get pods -w
```

A healthy first start reports the database migrations running to completion, then
`Server listening on port 5055`. The container runs as uid 1000, so a permission
error on `/app/config` means the `fsGroup` chown did not happen — check that the
Pod was actually recreated rather than reused.

## Rolling back

- **Routing**: `git revert` the commit. The legacy-host routes and the new ones
  are separate objects, so a revert is a clean delete of the redirect.
- **Seerr**: there is no downgrade path. Restore
  `backup-dfa46de3c83c467a` from the Longhorn backup taken before the migration,
  then revert the image to `fallenbagel/jellyseerr` — accepting that the old image
  is end-of-life.

## Removing the old hostnames later

Once the Authentik bindings and the Google redirect URI have been updated and a
week has passed:

1. Delete `httproute-legacy-host.yaml` from `open-webui` and `jellyseerr`, and
   remove them from each `kustomization.yaml`.
2. Drop `authentik.cowlab.org` from `manifests/cluster/authentik/httproute.yaml`.
3. Delete the old redirect URI in Google Cloud Console.
