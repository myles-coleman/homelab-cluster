# Longhorn Backup & Restore

## Overview
Restore a Longhorn PVC from an S3 backup

## Process

### 1. Restore Volume in Longhorn UI

1. Open https://longhorn.cowlab.org
2. Navigate to **Backup and Restore** → **Backups**
3. Find the backup for your PVC
4. Click **⋮** → **Restore**
5. Note the restored volume name (e.g., `backup-1e0a71b480e741a4`)

### 2. Create PersistentVolume for Restored Volume

Create `<app>-backup-pv.yaml`:

```yaml
apiVersion: v1
kind: PersistentVolume
metadata:
  name: backup-1e0a71b480e741a4  # Use restored volume name
spec:
  capacity:
    storage: 50Mi  # Match original PVC size
  volumeMode: Filesystem
  accessModes:
    - ReadWriteOnce
  persistentVolumeReclaimPolicy: Retain
  storageClassName: longhorn
  csi:
    driver: driver.longhorn.io
    fsType: ext4
    volumeHandle: backup-1e0a71b480e741a4  # Use restored volume name
    volumeAttributes:
      numberOfReplicas: "3"
      staleReplicaTimeout: "20"
```

### 3. Create PVC Pointing to Restored Volume

Create `<app>-config-restore-pvc.yaml`:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: app-config  # Match original PVC name
  namespace: app-namespace
  annotations:
    longhorn.io/volume-name: "backup-1e0a71b480e741a4"  # Restored volume name
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 50Mi  # Match original size
  storageClassName: longhorn
```

### 4. Apply the Restore

```bash
# 1. Disable ArgoCD auto-sync
kubectl patch app <app-name> -n argocd --type json -p='[{"op": "remove", "path": "/spec/syncPolicy"}]'

# 2. Scale down application
kubectl scale deployment <app-name> -n <namespace> --replicas=0

# 3. Delete existing PVC
kubectl delete pvc <pvc-name> -n <namespace>

# 4. Create PV and PVC
kubectl apply -f <app>-backup-pv.yaml
kubectl apply -f <app>-config-restore-pvc.yaml
```

### 5. Update Git Repository

```bash
# Copy restore manifests to replace original
cp <app>-config-restore-pvc.yaml manifests/cluster/<app>/config-pvc.yaml

# Add to kustomization.yaml if needed
# Commit and push
git add manifests/cluster/<app>/
git commit -m "chore: restore <app> PVC from backup"
git push
```

### 6. Re-enable ArgoCD

```bash
kubectl patch app <app-name> -n argocd --type merge -p '{"spec":{"syncPolicy":{"automated":{"prune":true,"selfHeal":true},"retry":{"backoff":{"duration":"30s","factor":2,"maxDuration":"2m"},"limit":5}}}}'
```

## Verification

```bash
# Check pod is running
kubectl get pods -n <namespace>

# Check PVC is using correct volume
kubectl get pvc <pvc-name> -n <namespace> -o jsonpath='{.spec.volumeName}'

# Check application logs
kubectl logs -n <namespace> deployment/<app-name>

# Verify data in application UI
```

## Key Points

- **PV is required**: Longhorn doesn't auto-create PVs for restored volumes
- **Annotation matters**: `longhorn.io/volume-name` tells Longhorn which volume to use
- **ArgoCD sync**: Must be disabled during restore to prevent reconciliation
- **Git update**: Critical to prevent ArgoCD from reverting changes
- **Volume name**: Must match exactly between PV, PVC annotation, and Longhorn volume
