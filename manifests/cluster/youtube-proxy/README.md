# youtube-proxy

Materialious (`materialious-full`) as an account-free YouTube frontend. No
Invidious, no companion: the bundled backend does playback and proxies upstream
requests server-side, so the browser never talks to Google directly.

| Item | Value |
|------|-------|
| Host | https://youtube.cowlab.org |
| Image | `wardpearce/materialious-full:1.18.26` (arm64 + amd64) |
| Storage | 5Gi Longhorn PVC, sqlite at `/materialious-data/materialious.db` |
| Namespace | `youtube-proxy` |

This hostname previously belonged to `youtubedl-material`, which moved to
`ytdl.cowlab.org` in the same change. Both IngressRoutes cannot claim the same
host, so they must be applied together.

## Why not Invidious

Invidious requires `invidious-companion` for playback, and the nixpkgs
`services.invidious` module still wires the deprecated `inv-sig-helper`. Running
it also means Postgres holding subscriptions and history in plaintext, versus
Materialious' end-to-end-encrypted subscriptions and single sqlite file. Add it
later only for the Invidious API or companion's per-request IPv6 egress.

## Privacy posture

Enabled:

- `PUBLIC_INTERNAL_AUTH=true` — accounts live in this namespace, no Google.
- `PUBLIC_REQUIRE_AUTH=true`, `PUBLIC_REGISTRATION_ALLOWED=false` — private instance.
- `HISTORY_CULLING=30` — watch history self-deletes after 30 days.
- SponsorBlock and DeArrow **disabled**. Both report watched video IDs to a
  third party. Re-enable only against a self-hosted SponsorBlock server.

Still external:

- **Return YouTube Dislike** (`returnyoutubedislikeapi.com`) — sends the video ID,
  returns a count. Kept because it is useful and low-signal. To remove the leak,
  self-host `1337kavin/ryd-proxy` (multi-arch, but last pushed 2024-04) and repoint
  `PUBLIC_DEFAULT_RETURNYTDISLIKES_INSTANCE`.
- The container's own egress to YouTube. Proxying changes what **Google** sees
  (this cluster's IP, not your devices) — it does not hide anything from your
  ISP, which still sees TLS to `googlevideo.com` on the same uplink.

This is a privacy setup, not an anonymity setup. Anonymity requires controlling
egress (VPN/Tor/IPv6 rotation), which is a separate project.

## Cookie secret

`COOKIE_SECRET` must be >= 16 characters or the container refuses to start.

The value lives in the Vaultwarden secure note
`youtube-proxy / materialious COOKIE_SECRET`, whose body is a single
`openssl rand -hex 32` string with no trailing newline. `external-secret.yaml`
pins that item by UUID, so ESO renders `materialious-secrets` with no manual
step. Rotating it invalidates every session; new logins regenerate cookies.

A trailing newline in the note becomes part of the secret — keep it to one line.

## Bootstrap the first account

`PUBLIC_REGISTRATION_ALLOWED` is `false`, so there is no way to create the first
user on a fresh database. Chicken-and-egg — do this once, in order:

1. Temporarily set `PUBLIC_REGISTRATION_ALLOWED` to `"true"`.
2. Merge/push and let ArgoCD roll the pod.
3. Open https://youtube.cowlab.org and register the username **`bee`**
   (it is in `PUBLIC_ADMIN_USERNAMES`, so it gets admin rights). The
   proof-of-work captcha needs HTTPS, which Traefik provides.
4. Set `PUBLIC_REGISTRATION_ALLOWED` back to `"false"` and push again.

Keep the window short: while it is `true`, anyone who can reach the host can
register.

## Operational notes

- `strategy: Recreate` is deliberate. sqlite on a RWO volume must never have two
  writers; a rolling update would double-mount the PVC.
- Requests are served from the pod, so video throughput is bounded by the node's
  network and the cluster uplink. Watch `kubectl top pod -n youtube-proxy`.
- The image tag is pinned to a version, not a digest. Bump deliberately and check
  the changelog for new `PUBLIC_*` defaults.

## Network policy

`networkpolicy.yaml` selects the pod and:

- admits ingress only from the `traefik` namespace and the five node IPs
  (kubelet probes), so a host on the LAN cannot reach the pod directly;
- permits egress DNS plus TCP 80/443 with RFC1918 and loopback excluded, so the
  built-in CORS proxy cannot be turned into a path into cluster Services or the
  LAN.

NetworkPolicy has no hostname matcher, so "only YouTube" is not expressible on
vanilla Kubernetes — the exclusion of private ranges is what carries the value.
After the first sync, confirm the policy is actually enforced in both
directions before trusting it:

```bash
kubectl exec -n youtube-proxy deploy/materialious -- wget -qO- --timeout=3 https://example.com >/dev/null && echo egress-ok
kubectl exec -n youtube-proxy deploy/materialious -- wget -qO- --timeout=3 http://10.0.0.150 >/dev/null || echo lan-blocked
```

If the second command returns content instead of failing, the CNI ignored
`except` and the egress rule is open to web ports only.
