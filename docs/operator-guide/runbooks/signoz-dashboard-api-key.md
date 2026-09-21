# SigNoz Dashboard API Key

Bootstrap and rotation procedure for the SigNoz service-account API key used by
the `signoz-dashboards` Terragrunt module and the CI dashboard smoke check.

## Overview

The SigNoz Terraform provider cannot create service accounts or API keys, so the
key is created manually once and then supplied to CI and local runs as a secret.
It is never committed to Git.

- **`SIGNOZ_ENDPOINT`** — root URL of the SigNoz UI (`https://signoz.cowlab.org`).
- **`SIGNOZ_ACCESS_TOKEN`** — the service-account API key.

## Bootstrap (first time)

1. Open `https://signoz.cowlab.org` and sign in as an admin.
2. Go to **Settings → Service Accounts** and create a service account, for
   example `terraform-dashboards`.
3. Assign the least-privileged role that can manage dashboards. If SigNoz does
   not offer a dashboard-only role, use the narrowest role available rather than
   a full admin key.
4. Create an API key for the service account and copy it once (it is shown only
   once).
5. Store the values:
   - **GitHub repository variable** `SIGNOZ_ENDPOINT` = `https://signoz.cowlab.org`
     (`Settings → Secrets and variables → Actions → Variables`).
   - **GitHub repository secret** `SIGNOZ_ACCESS_TOKEN` = the API key
     (`Settings → Secrets and variables → Actions → Secrets`).
6. For local runs, export the token in your shell (never write it to a file):

   ```bash
   export SIGNOZ_ENDPOINT="https://signoz.cowlab.org"
   export SIGNOZ_ACCESS_TOKEN="[YOUR_API_KEY_HERE]"
   ```

## Verify

The endpoint must be reachable with valid TLS over Tailscale (CI) or the local
network:

```bash
curl -sS -o /dev/null -w '%{http_code}\n' "$SIGNOZ_ENDPOINT/"
```

Run the dashboard smoke check from the
[`homelab-terraform`](https://github.com/myles-coleman/homelab-terraform)
repository, which queries each dashboard's principal series:

```bash
# from the homelab-terraform repository
./scripts/signoz-dashboard-smoke.sh
```

## Rotation

Rotate the key periodically, or immediately if it is exposed.

1. Create a new API key for the same service account in SigNoz.
2. Update the `SIGNOZ_ACCESS_TOKEN` GitHub secret with the new value.
3. Confirm the next `deploy.yaml` run in `homelab-terraform` plans/applies
   cleanly and the smoke job passes.
4. Revoke the old key in SigNoz.

## Known failure mode: expired key

An expired or revoked key does **not** fail only the dashboards. Because the
`signoz-dashboards` units share the `homelab-terraform` `deploy.yaml` apply run
with the other modules, an invalid key can fail an otherwise unrelated
`terragrunt apply` until it is rotated. If a deploy fails with a SigNoz
authentication error:

1. Rotate the key as above.
2. Re-run the failed workflow (or `terragrunt apply` for the affected units).

## Rollback

The dashboards are additive and do not touch existing resources. To roll back:

1. Revert the relevant commit in `homelab-terraform`, or remove the unit from
   its `deploy.yaml` matrix.
2. Run `terragrunt destroy` in the affected unit directory (dashboards only).
3. The `node-exporter` DaemonSet and the `signoz-k8s-infra` preset are
   independent; revert their commits separately if needed.

## Security notes

- Never commit the API key, put it in `common.hcl`/`terragrunt.hcl`, or paste it
  into proof artifacts.
- The provider is configured to read `SIGNOZ_ACCESS_TOKEN` from the environment,
  so the value is never written to the generated `_provider.tf` or to Terraform
  state. Do not add an `access_token` argument to the provider block.
