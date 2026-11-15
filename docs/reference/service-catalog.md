# Service Catalog

This catalog lists all services defined in `manifests/cluster/`. The **source of truth** for which services are actively deployed is `manifests/cluster/kustomization.yaml`.

## Active Services

| Service | Namespace | Wave | URL | Storage | Type | Description |
|---------|-----------|------|-----|---------|------|-------------|
| external-secrets | external-secrets | 0 | — | No | Helm | Secret syncing from Vaultwarden via bitwarden-cli |
| cert-manager | cert-manager | 1 | — | No | Helm | Automated TLS certificate management |
| traefik | traefik | 2 | — | No | Helm | Ingress controller and reverse proxy (LB: 10.0.0.99) |
| metallb | metallb-system | 2 | — | No | Helm | Bare-metal load balancer for external IP allocation |
| longhorn | longhorn | 3 | longhorn.cowlab.org | Yes | Helm | Distributed block storage with S3 backup |
| dex | dex | 4 | — | No | Helm | OIDC connector for ArgoCD authentication |
| sonarr | sonarr | 10 | sonarr.cowlab.org | Yes | Raw | TV show management and download automation |
| radarr | radarr | 10 | radarr.cowlab.org | Yes | Raw | Movie management and download automation |
| lidarr | lidarr | 10 | lidarr.cowlab.org | Yes | Raw | Music management and download automation |
| prowlarr | prowlarr | 10 | prowlarr.cowlab.org | Yes | Raw | Indexer manager for *arr applications |
| jellyseerr | jellyseerr | 10 | jellyseerr.cowlab.org | Yes | Raw | Media request management for Jellyfin |
| discord-bot | discord-bot | 10 | — | No | Raw | Custom Discord bot |
| qbittorrent | qbittorrent | 10 | qbittorrent.cowlab.org | Yes | Raw | BitTorrent client |
| openbooks | openbooks | 10 | openbooks.cowlab.org | Yes | Raw | eBook search and download |
| calibre-web | calibre | 10 | calibre.cowlab.org | Yes | Raw | eBook library management and reader |
| youtubedl-material | youtubedl-material | 10 | ytdl.cowlab.org | Yes | Raw | YouTube video downloader with web UI |
| homepage | homepage | 10 | portal.cowlab.org | No | Raw | Dashboard/homepage for cluster services |
| copyparty | copyparty | 10 | copyparty.cowlab.org | Yes | Raw | File sharing and upload server |
| cloudflared | cloudflared | 10 | — | No | Helm | Cloudflare Zero Trust Tunnel agent |
| argocd-secret | argocd-secret | 10 | — | No | Raw | ArgoCD secret configuration |
| node-exporter | node-exporter | 10 | — | No | Raw | Host thermal and CPU-frequency metrics for the Pi 5 nodes |
| sandbox-telemetry | sandbox-telemetry | 10 | — | No | Raw | Pull-based sandbox pod/guest metrics and Strata `/metrics` for SigNoz |

## Disabled Services

These services are commented out in `manifests/cluster/kustomization.yaml`:

| Service | Description |
|---------|-------------|
| vaultwarden | Password manager (runs outside cluster) |
| dapr | Distributed Application Runtime |
