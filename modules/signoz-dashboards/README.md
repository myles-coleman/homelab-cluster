# signoz-dashboards

Declarative SigNoz dashboards managed with Terragrunt + OpenTofu using the
official [`SignNoz/signoz`](https://registry.terraform.io/providers/SigNoz/signoz/latest)
provider (v0.1.4).

## Units

Each dashboard is its own Terragrunt unit with its own state key, so one invalid
dashboard cannot block the others:

| Unit | Dashboard |
| --- | --- |
| `homelab-overview/` | Homelab Overview — cluster/node triage surface |
| `kubernetes-nodes/` | Kubernetes & Nodes — node and workload drill-down |
| `pi5-thermal/` | Pi 5 Thermal — per-node temperature, CPU frequency, load |

## Configuration

The SigNoz provider is HTTP-based, so this module deviates from the
kubeconfig-based modules. `common.hcl` supplies the endpoint and token from
environment variables, and each unit generates `_provider.tf` from it:

- `SIGNOZ_ENDPOINT` (default `https://signoz.cowlab.org`)
- `SIGNOZ_ACCESS_TOKEN` (never committed; supplied by GitHub Actions secrets or
  the local shell)

See the [API key runbook](../../../docs/operator-guide/runbooks/signoz-dashboard-api-key.md).

## Local validation

`terragrunt` needs AWS credentials to evaluate the S3 remote state, so local
schema validation is done directly with OpenTofu:

```bash
cd <unit>
terragrunt init -backend=false   # generates _provider.tf / _backend.tf, then stops on AWS creds
tofu init -backend=false
tofu validate
tofu fmt -check -recursive
```

## Notes and assumptions

- **Data-links**: the provider supports dashboard/panel `links`, but target
  dashboard IDs are server-assigned. Links are intentionally left empty until
  the dashboards exist and their IDs are stable; use SigNoz's native navigation
  and the shared `homelab` tag in the meantime.
- **Cardinality**: panels group only by verified low-cardinality labels
  (`k8s.node.name`, `k8s.namespace.name`, `host.name`, `k8s_node_name`).
- **Thermal metric names**: the Pi 5 Thermal board uses `node_thermal_zone_temp`
  and `node_cpu_frequency_hertz` (node_exporter collectors scraped by the
  `signoz-k8s-infra` Prometheus preset). The Prometheus receiver relabels the
  node to `k8s_node_name`. Confirm the exact series/attribute names once data is
  flowing and adjust if the receiver renames them.
