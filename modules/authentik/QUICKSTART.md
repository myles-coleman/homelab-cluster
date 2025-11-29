# Quick Start Guide

## TL;DR

```bash
# 1. Create Authentik admin API token (in UI)
# 2. Update common-inputs.yaml with token
# 3. Run terraform
cd modules/authentik-forward-auth
terragrunt apply

# 4. Get outpost token
terragrunt output -raw outpost_token

# 5. Store in Bitwarden and update external-secret.yaml
# 6. Deploy
kubectl apply -k manifests/cluster/authentik/
kubectl apply -k manifests/cluster/traefik/

# 7. Update app IngressRoutes with middleware
# 8. Test
```

## Prerequisites Checklist

- [ ] Authentik is running and accessible
- [ ] Terraform/Terragrunt installed
- [ ] Created Authentik admin API token
- [ ] Updated `common-inputs.yaml` with token
- [ ] Configured applications in `terragrunt.hcl`

## Step-by-Step

### 1. Create Admin Token (One-Time)

In Authentik UI:
- **Directory → Tokens & App passwords → Create**
- Identifier: `terraform-admin-token`
- User: `akadmin`
- Intent: `API Token`
- Expiring: `No`
- **Copy the token!**

### 2. Configure

Edit `common-inputs.yaml`:
```yaml
authentik_url: https://authentik.cowlab.org
authentik_token: YOUR_TOKEN_HERE
authentik_outpost_user_id: 1
domain: cowlab.org
```

### 3. Define Applications

Edit `terragrunt.hcl`:
```hcl
applications = {
  myapp = {
    name          = "My App"
    slug          = "myapp"
    external_host = "https://myapp.cowlab.org"
    mode          = "forward_single"
  }
}
```

### 4. Apply

```bash
cd modules/authentik-forward-auth
terragrunt plan   # Review
terragrunt apply  # Apply
```

### 5. Get Outpost Token

```bash
terragrunt output -raw outpost_token
```

### 6. Store in Bitwarden

1. Create Bitwarden secure note with token
2. Copy item ID
3. Update `manifests/cluster/authentik/external-secret.yaml`:
   ```yaml
   - secretKey: AUTHENTIK_OUTPOST_TOKEN
     remoteRef:
       key: YOUR_BITWARDEN_ITEM_ID
   ```

### 7. Deploy to Kubernetes

```bash
kubectl apply -k manifests/cluster/authentik/
kubectl apply -k manifests/cluster/traefik/
```

### 8. Protect Applications

Update each app's IngressRoute:
```yaml
spec:
  routes:
    - match: Host(`myapp.cowlab.org`)
      middlewares:
        - name: authentik
          namespace: traefik
      services:
        - name: myapp
          port: 8080
```

### 9. Test

1. Clear cookies
2. Visit `https://myapp.cowlab.org`
3. Should redirect to Authentik login
4. Log in
5. Should access app

## Outputs

```bash
# Get outpost token
terragrunt output -raw outpost_token

# Get all outputs
terragrunt output

# Get specific output
terragrunt output outpost_id
```

## Troubleshooting

### Authentication Error
```bash
# Verify token works
curl -H "Authorization: Bearer YOUR_TOKEN" \
  https://authentik.cowlab.org/api/v3/core/users/
```

### Outpost Not Working
```bash
# Check logs
kubectl logs -n authentik -l app=authentik-outpost

# Verify secret
kubectl get secret authentik-secrets -n authentik \
  -o jsonpath='{.data.outpost-token}' | base64 -d
```

### State Issues
```bash
# Refresh state
terragrunt refresh

# View state
terragrunt state list
```

## Adding More Apps

1. Edit `terragrunt.hcl` → add to `applications`
2. Run `terragrunt apply`
3. Update app's IngressRoute
4. Test

## Documentation

- Full guide: [terraform-authentik-setup.md](../../docs/operator-guide/runbooks/terraform-authentik-setup.md)
- Module README: [README.md](./README.md)
- Forward auth guide: [forward-auth-setup.md](../../docs/operator-guide/runbooks/forward-auth-setup.md)
