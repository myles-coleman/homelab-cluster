---
description: Add a new service to the Kubernetes cluster
---

# Add a New Service

This workflow creates all required Kubernetes manifests for a new service and registers it with ArgoCD.

## Steps

1. **Gather service details** — Ask the user for:
   - Service name (lowercase, hyphenated, e.g., `my-app`)
   - Container image (must support `aarch64`/`arm64`, e.g., `lscr.io/linuxserver/sonarr:latest`)
   - Container port (e.g., `8080`)
   - Whether the service needs persistent storage (yes/no)

2. **Create the service directory**

// turbo
```bash
mkdir -p manifests/cluster/<service-name>
```

3. **Create `namespace.yaml`** in `manifests/cluster/<service-name>/` using the template from `manifests/cluster/_template/namespace.yaml`. Replace all `CHANGEME` with the service name.

4. **Create `deployment.yaml`** in `manifests/cluster/<service-name>/` using the template from `manifests/cluster/_template/deployment.yaml`. Replace:
   - All `CHANGEME` name/namespace references with the service name
   - `CHANGEME` image with the container image
   - `CHANGEME` containerPort with the port number
   - Uncomment volume mounts if persistent storage is needed

5. **Create `service.yaml`** in `manifests/cluster/<service-name>/` using the template from `manifests/cluster/_template/service.yaml`. Replace all `CHANGEME` with the service name and port.

6. **Create `ingress.yaml`** in `manifests/cluster/<service-name>/` using the template from `manifests/cluster/_template/ingress.yaml`. Replace all `CHANGEME` with the service name and port. The host will be `<service-name>.cowlab.org`.

7. **If persistent storage is needed**, create a `persistent-volume-claim.yaml`:
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
Uncomment the volume mounts in `deployment.yaml` and update the PVC name.

8. **Create `kustomization.yaml`** in `manifests/cluster/<service-name>/` listing all created resource files:
```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - namespace.yaml
  - deployment.yaml
  - service.yaml
  - ingress.yaml
  # - persistent-volume-claim.yaml  # Uncomment if storage was added
```

9. **Register the service** — Add the service name to `manifests/cluster/kustomization.yaml` under the `resources:` list.

10. **Remind the user**:
    - The `generate-apps.yml` GitHub Actions workflow will automatically run `generate-apps.sh` when changes to `manifests/cluster/` are pushed to `main`. This creates the ArgoCD Application in `manifests/bootstrap/`.
    - Alternatively, run `./generate-apps.sh` locally to preview the generated Application YAML.
    - The wildcard DNS record `*.cowlab.org` already points to the Traefik load balancer (10.0.0.99), so `<service-name>.cowlab.org` will resolve automatically.
    - If the service needs a specific DNS record outside the wildcard (e.g., a non-cowlab.org domain or an A record pointing somewhere other than the LB), add it via Terragrunt in `modules/cloudflare/records.tf` and open a PR.
