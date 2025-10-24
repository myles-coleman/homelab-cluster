# CopyParty Kubernetes Deployment

This directory contains Kubernetes manifests for deploying CopyParty, a file sharing and management web application.

## Overview

- **Application**: CopyParty
- **Image**: `copyparty/ac:1.18.6`
- **URL**: `https://copyparty.cowlab.org`
- **Port**: 3923

## Features

- File sharing and upload capabilities
- Web-based file management interface
- Multimedia indexing and thumbnails
- User authentication system
- Configurable permissions per folder

## Storage

- **Data Volume**: 50Gi Longhorn PVC for shared files
- **Config Volume**: 1Gi Longhorn PVC for configuration and metadata

## Default Configuration

The deployment includes a basic configuration with:
- Admin user: `admin` / `changeme123` (⚠️ **Change this password!**)
- Public read access to main folder
- Upload folder for anonymous uploads
- File indexing and multimedia scanning enabled

## Deployment

```bash
# Deploy using kubectl
kubectl apply -k .

# Or using kustomize
kustomize build . | kubectl apply -f -
```

## Post-Deployment

1. **Change the default admin password** by editing the ConfigMap:
   ```bash
   kubectl edit configmap copyparty-config -n copyparty
   ```

2. **Access the application** at: https://copyparty.cowlab.org

3. **Upload files** to the `/uploads` folder or manage files through the web interface

## Configuration

The main configuration is stored in the `copyparty-config` ConfigMap. You can customize:
- User accounts and passwords
- Volume permissions
- File indexing options
- Upload restrictions

Refer to the [CopyParty documentation](https://github.com/9001/copyparty) for advanced configuration options.

## Security Notes

- Default admin password should be changed immediately
- Consider restricting upload permissions in production
- Review user access controls based on your requirements
