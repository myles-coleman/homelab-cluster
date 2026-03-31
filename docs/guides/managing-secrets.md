# Managing Secrets

This guide covers how secrets are managed in the cluster using the External Secrets Operator (ESO) and Vaultwarden/Bitwarden.

## Architecture

```
Vaultwarden (self-hosted Bitwarden)
        │
        ▼
bitwarden-cli (webhook service in-cluster)
        │
        ▼
External Secrets Operator
        │
        ▼
Kubernetes Secret (native)
        │
        ▼
Application Pod (mounts the secret)
```

1. Secrets are stored in **Vaultwarden** (a self-hosted Bitwarden server)
2. The **bitwarden-cli** service runs in the `external-secrets` namespace and exposes a webhook API
3. **External Secrets Operator** queries bitwarden-cli to fetch secret values
4. ESO creates native **Kubernetes Secrets** that pods can mount or reference

## ClusterSecretStore Types

The cluster has 4 `ClusterSecretStore` resources, each extracting different data from Bitwarden items:

| Store Name | Use Case | What It Extracts |
|-----------|----------|-----------------|
| `bitwarden-login` | Username/password credentials | `$.data.login.username` or `$.data.login.password` |
| `bitwarden-fields` | Custom fields on items | Field value by name |
| `bitwarden-notes` | Secure note content | `$.data.notes` |
| `bitwarden-attachments` | File attachments | Raw binary content |

## Creating an ExternalSecret

To provide secrets to your service, create an `ExternalSecret` resource:

### Example: Login Credentials

```yaml
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: my-service-secret
  namespace: my-service
spec:
  refreshInterval: 15m
  secretStoreRef:
    kind: ClusterSecretStore
    name: bitwarden-login
  target:
    name: my-service-secret
  data:
    - secretKey: username
      remoteRef:
        key: <bitwarden-item-id>
        property: username
    - secretKey: password
      remoteRef:
        key: <bitwarden-item-id>
        property: password
```

Replace `<bitwarden-item-id>` with the UUID of the Bitwarden item containing the credentials.

### Example: Custom Fields

```yaml
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: my-service-api-key
  namespace: my-service
spec:
  refreshInterval: 15m
  secretStoreRef:
    kind: ClusterSecretStore
    name: bitwarden-fields
  target:
    name: my-service-api-key
  data:
    - secretKey: api-key
      remoteRef:
        key: <bitwarden-item-id>
        property: api_key
```

### Example: Secure Notes

```yaml
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: my-service-config
  namespace: my-service
spec:
  refreshInterval: 15m
  secretStoreRef:
    kind: ClusterSecretStore
    name: bitwarden-notes
  target:
    name: my-service-config
  data:
    - secretKey: config
      remoteRef:
        key: <bitwarden-item-id>
```

## Using Secrets in Deployments

Reference the generated Kubernetes Secret in your deployment:

```yaml
# As environment variables:
env:
- name: DB_PASSWORD
  valueFrom:
    secretKeyRef:
      name: my-service-secret
      key: password

# As a mounted file:
volumeMounts:
- name: secret-volume
  mountPath: /etc/secrets
  readOnly: true
volumes:
- name: secret-volume
  secret:
    secretName: my-service-secret
```

## Adding the ExternalSecret to Kustomize

Add your ExternalSecret YAML file to the service's `kustomization.yaml`:

```yaml
resources:
  - namespace.yaml
  - deployment.yaml
  - service.yaml
  - ingress.yaml
  - external-secret.yaml
```

## Important Notes

- **Never hardcode secrets** in any committed file. Always use ExternalSecret resources.
- The `.gitignore` excludes `manifests/cluster/external-secrets/secret.yaml` to prevent accidental secret commits.
- `refreshInterval: 15m` means secrets are re-synced every 15 minutes. Adjust as needed.
- The `bitwarden-cli` webhook service must be running for secret syncing to work.
- ESO deploys at **sync wave 0** — it's the first service to start, ensuring secrets are available for all other services.
