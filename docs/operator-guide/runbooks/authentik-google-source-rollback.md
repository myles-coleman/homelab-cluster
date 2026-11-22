# Authentik Google source rollback

This runbook restores the production Authentik `google` source after the
`openidconnect` / `prompt=select_account` switch. It is the recovery procedure
for spec 07.

## When to use it

Roll back immediately if any of these is true:

- an existing user cannot sign in after the switch;
- the OIDC `sub` no longer matches the stored Google connection identifier;
- the callback profile has no usable `email` claim, so the enrollment allowlist
  cannot resolve;
- enrollment is denied for an address that is in `ALLOWED_EMAILS`;
- a gated host returns an authentication error that is not explained by the
  error page.

The Authentik session is not the source of truth for recovery: ArgoCD's
automated sync does not need an interactive login, so a Git revert is the
primary path even while Authentik login is broken.

## Normal rollback

The staged revert is the branch `revert/07-google-oidc-source`. It reverts only
the blueprint commit, not the switch-account error page or this runbook.

1. Confirm the branch still targets the source change:

   ```bash
   git fetch origin
   git log --oneline origin/main..origin/revert/07-google-oidc-source
   ```

2. Merge the revert branch to `main` through a pull request (or push the
   reviewed revert commit), and let ArgoCD sync.
3. If the mounted blueprint has not re-applied within one reconciliation cycle,
   restart the worker:

   ```bash
   kubectl rollout restart deploy/authentik-worker -n authentik
   ```

4. Verify the live source has returned to the google type:

   ```bash
   kubectl exec -n authentik deploy/authentik-server -- ak shell -c "
   from authentik.sources.oauth.models import OAuthSource
   s=OAuthSource.objects.get(slug='google')
   print(s.provider_type)
   print(s.authorization_url)
   "
   ```

   Expected: `google` and the google-type URL. The source login redirect must
   no longer contain `prompt=select_account`:

   ```bash
   curl -sS -o /dev/null -D - https://authentik.cowlab.org/source/oauth/login/google/
   ```

5. Sign in once and confirm the existing user and groups are unchanged.

## Break-glass when Git or ArgoCD is unavailable

This is an emergency action, not the normal path. It bypasses the GitOps flow
and must be followed by the repository revert when Git/ArgoCD is reachable
again.

1. Stop the worker from re-applying the OIDC blueprint:

   ```bash
   kubectl scale deploy/authentik-worker -n authentik --replicas=0
   ```

2. Point the source back at the google provider type:

   ```bash
   kubectl exec -n authentik deploy/authentik-server -- ak shell -c "
   from authentik.sources.oauth.models import OAuthSource
   s=OAuthSource.objects.get(slug='google')
   s.provider_type='google'
   s.save()
   print('provider_type=' + s.provider_type)
   "
   ```

   The `google` provider type ignores the stored custom URLs, so the provider
   type is the only field that must change for login to recover. Do not delete
   the source, its connections, or any group.

3. Verify the source login redirect no longer contains `prompt=select_account`
   and sign in once.

4. Restore the worker:

   ```bash
   kubectl scale deploy/authentik-worker -n authentik --replicas=1
   ```

5. Complete the normal repository revert as soon as Git/ArgoCD is available,
   then confirm the worker has applied the reverted blueprint.

## Data preservation

The rollback changes only the source provider type and its URL/auth-method
attributes. It must not:

- delete or recreate the source row;
- delete `UserOAuthSourceConnection` rows;
- delete users or groups;
- change `homelab` or `copyparty-admins` membership.

After recovery, compare the connection count and group membership against the
baseline in
`07-proofs/07-task-02-baseline.md` (artifacts vault, spec 07).

## Verification checklist

- [ ] `provider_type` is `google`.
- [ ] The source redirect has no `prompt=select_account`.
- [ ] The source still has three connections.
- [ ] `homelab` and `copyparty-admins` still have their members.
- [ ] An existing user can sign in.
- [ ] The enrollment allowlist still resolves an email.
- [ ] The worker is running and has applied the reverted blueprint.

## Test-client cleanup

If a dedicated Google test client was created for the disposable rehearsal,
delete it (or remove the temporary `http://localhost:9000/source/oauth/callback/google/`
redirect URI) after the rehearsal. The production client is not modified.
