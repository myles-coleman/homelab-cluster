# Adding a New Service

This guide walks through adding a new application to the cluster. By the end, your service will be deployed via ArgoCD with ingress, and optionally persistent storage.

## Prerequisites

- Access to the repository (push to `main` or create a PR)
- Container image that supports `aarch64`/`arm64` architecture
- Service name (lowercase, hyphenated, e.g., `my-app`)

## Quick Start

A skeleton template is available in `manifests/cluster/_template/`. You can copy it and replace the `CHANGEME` placeholders:

```bash
cp -r manifests/cluster/_template manifests/cluster/<service-name>
```

Then find and replace all `CHANGEME` values with your service details.

## Step-by-Step

### 1. Create the Service Directory

```bash
mkdir manifests/cluster/<service-name>
```

### 2. Create namespace.yaml

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: <service-name>
```

### 3. Create deployment.yaml

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: <service-name>
  namespace: <service-name>
spec:
  replicas: 1
  selector:
    matchLabels:
      app: <service-name>
  template:
    metadata:
      labels:
        app: <service-name>
    spec:
      containers:
      - name: <service-name>
        image: <container-image>
        env:
        - name: TZ
          value: "Etc/UTC"
        ports:
        - containerPort: <port>
        resources:
          limits:
            memory: "512Mi"
            cpu: "500m"
          requests:
            memory: "256Mi"
            cpu: "250m"
```

Adjust resource limits based on your service's needs. Existing services like sonarr use `1024Mi` memory limit.

### 4. Create service.yaml

```yaml
apiVersion: v1
kind: Service
metadata:
  name: <service-name>
  namespace: <service-name>
spec:
  selector:
    app: <service-name>
  ports:
    - name: http
      protocol: TCP
      port: <port>
      targetPort: <port>
```

### 5. Create ingress.yaml

Use the Traefik `IngressRoute` CRD — **never** standard Kubernetes `Ingress`:

```yaml
apiVersion: traefik.io/v1alpha1
kind: IngressRoute
metadata:
  name: <service-name>
  namespace: <service-name>
  annotations:
    kubernetes.io/ingress.class: traefik-external
spec:
  entryPoints:
    - websecure
  routes:
    - match: Host(`<service-name>.cowlab.org`)
      kind: Rule
      services:
        - name: <service-name>
          port: <port>
```

The wildcard DNS `*.cowlab.org` already points to the Traefik load balancer (10.0.0.99), so your service will be accessible at `https://<service-name>.cowlab.org` automatically.

### 6. Create kustomization.yaml

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - namespace.yaml
  - deployment.yaml
  - service.yaml
  - ingress.yaml
```

Add any additional resource files (PVCs, ConfigMaps, ExternalSecrets) to this list.

### 7. Register the Service

Add your service to `manifests/cluster/kustomization.yaml`:

```yaml
resources:
  # ... existing services ...
  - <service-name>
```

### 8. Deploy

Push your changes to `main`. The following happens automatically:

1. `generate-apps.yml` runs `generate-apps.sh`, creating `manifests/bootstrap/<service-name>-app.yaml`
2. ArgoCD's bootstrap Application detects the new Application CRD
3. ArgoCD syncs `manifests/cluster/<service-name>/` to the cluster
4. Your service is live at `https://<service-name>.cowlab.org`

To preview the generated ArgoCD Application locally: `./generate-apps.sh`

## Optional: Persistent Storage

If your service needs persistent data, add a PVC using Longhorn (the default StorageClass):

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: <service-name>-data
  namespace: <service-name>
spec:
  accessModes:
    - ReadWriteOnce
  storageClassName: longhorn
  resources:
    requests:
      storage: 5Gi
```

Then add volume mounts to your deployment:

```yaml
# In the container spec:
volumeMounts:
- name: data-volume
  mountPath: /data

# In the pod spec:
volumes:
- name: data-volume
  persistentVolumeClaim:
    claimName: <service-name>-data
```

Don't forget to add the PVC file to `kustomization.yaml`.

## Optional: Secrets

If your service needs secrets from Vaultwarden, see [Managing Secrets](managing-secrets.md).

## Optional: Custom Namespace

If the Kubernetes namespace must differ from the directory name, add a mapping in `generate-apps.sh`:

```bash
namespace_map["<service-name>"]="<actual-namespace>"
```

Currently only `metallb` → `metallb-system` uses this.

## Optional: Custom Sync Wave

If your service is infrastructure that must deploy before wave 10, add to `generate-apps.sh`:

```bash
sync_wave_map["<service-name>"]="<wave-number>"
```

See [Sync Wave Reference](../reference/sync-wave-reference.md) for the current wave assignments.

## Optional: DNS Records

The wildcard `*.cowlab.org` handles most cases. If you need a specific DNS record (e.g., a non-cowlab.org domain), add it via Terragrunt in `modules/cloudflare/records.tf` and open a pull request.
