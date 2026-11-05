# Sandbox + Strata Telemetry

How the agent sandbox, its guest node, and Strata are made observable in SigNoz
(issue #74), and how to verify or repair each path.

## Data path

```
bee-gpu-server                         sandbox-node (microVM)                 cluster
──────────────                         ─────────────────────                  ───────
Strata /metrics (tailnet only) ──pull──▶ sandbox-telemetry collector ──OTLP──▶ signoz-otel-collector
                                          │  (hostNetwork, pinned to node)         │
                                          │                                        ▼
sandbox pod ──kubeletstats──────────────▶ k8s-infra otel-agent ──────────────▶ SigNoz
guest OS   ──hostmetrics────────────────▶ k8s-infra otel-agent
guest OS   ──node_exporter──────────────▶ k8s-infra deployment (prometheus)
squid pod  ──filelog (container)────────▶ k8s-infra otel-agent
```

The direction matters: the collector **pulls** from Strata; the sandbox never
pushes. A compromised pod cannot forge its own telemetry, and the sandbox
NetworkPolicy does not need an egress allowance for a metrics endpoint.

## Why the Strata collector is pinned to `sandbox-node`

Strata is published only on `bee-gpu-server`'s `tailscale0` interface
(`DOCKER-USER ... ! -i tailscale0 -j DROP`). Of the cluster nodes, only
`sandbox-node` can currently reach `100.127.170.8` over the tailnet; `node0`–`node4`
time out. `hostNetwork: true` puts the collector on that node's tailnet route.
Pinning also keeps the scrape local to the node that needs it.

Do not add a NodePort or an IngressRoute for Strata metrics. That would expose an
unauthenticated (or bearer-key-bearing) endpoint to the LAN and, through the
wildcard DNS, to Cloudflare.

## Strata's `Accept` header

`/metrics` content-negotiates. Without an `Accept` header it returns the JSON
snapshot; the Prometheus text payload requires `Accept: text/plain`. The
collector sets that header explicitly. If you debug by hand:

```bash
KEY="$(ssh bee@100.127.170.8 'sudo cat /run/strata/strata.env' | cut -d= -f2-)"
curl -s -H "Authorization: Bearer $KEY" -H 'Accept: text/plain' \
  http://127.0.0.1:8080/metrics | head
```

## Verify

```bash
# The collector pod is on sandbox-node and Ready.
kubectl -n sandbox-telemetry get deploy,pod -o wide

# It resolved the ExternalSecret and is not crash-looping.
kubectl -n sandbox-telemetry get externalsecret
kubectl -n sandbox-telemetry logs deploy/sandbox-telemetry --tail=50

# The telemetry agents now tolerate the sandbox taint.
kubectl -n signoz-k8s-infra get ds k8s-infra-otel-agent -o wide
kubectl -n node-exporter get ds node-exporter -o wide
```

In SigNoz, query the metrics:

```promql
max({__name__="up", "service.name"="strata"})
{__name__="strata:gpu_util"}
{__name__="strata:live_tok_s"}
k8s.pod.network.io{k8s.namespace.name="agent-sandbox"}
k8s.container.restarts{k8s.namespace.name="agent-sandbox"}
node_nf_conntrack_entries{k8s_node_name="sandbox-node"}
```

The first two return data as soon as the collector is healthy. The `k8s.*`
series appear after the k8s-infra agent schedules on `sandbox-node`.

## Alerts

The rules are managed in `myles-coleman/homelab-terraform` under
`modules/signoz-alerts/`. They are applied by the normal `deploy.yaml` apply on
`main`; see the module README for the exact rules and the signals that are
intentionally not covered yet.

To confirm a rule can fire without waiting for the real event, use a synthetic
target:

- **Sandbox restart**: `kubectl -n agent-sandbox delete pod opencode` (the
  Sandbox controller recreates it), then watch the alert history.
- **Strata scrape down**: stop `docker-strata` on `bee-gpu-server` for five
  minutes, or scale the `sandbox-telemetry` Deployment to zero.
- **Conntrack**: lower the alert target temporarily in a branch, or run a
  bounded SYN scan from the sandbox.
- **Squid denied**: from the sandbox, `curl -x http://10.43.0.250:3128
  http://example.com`; Squid logs `TCP_DENIED` and the log rule fires.

Record the alert-history screenshot or API output as the proof artifact.

## Known gaps

| Gap | Why | Follow-up |
| --- | --- | --- |
| Strata 401 rate | Strata exposes no auth-failure counter and does not log rejected requests | Instrument upstream or wrap the engine image |
| gVisor `runsc` OOM/start failures | `/var/lib/runsc-logs` is on the guest volume and not ingested | Add a sampled filelog DaemonSet; the pod-restart rule is the coarse proxy |
| Sandbox egress outside the proxy | the hypervisor nftables filter drops silently | Add nftables counters on `sandbox-node-egress-filter` and a host-level scrape |
| Per-consumer Strata labels | Strata's `/metrics` is unlabelled aggregates today | Add a consumer label upstream; the collector already carries the `strata_consumer=shared` placeholder |

Retention: Strata metrics are one model server at a 15 s scrape, so full
request-level series are cheap compared with VRAM history. Keep the SigNoz
default 30-day metrics retention unless ClickHouse pressure says otherwise.

## Rollback

1. Delete `manifests/cluster/sandbox-telemetry/` from the cluster kustomization
   and run `./generate-apps.sh`; ArgoCD prunes the namespace.
2. Revert the `sandbox` toleration blocks in `signoz-k8s-infra/values.yaml` and
   `node-exporter/daemonset.yaml` if the extra agents on the untrusted node are
   not wanted.
3. `terragrunt destroy` the `signoz-alerts` unit to remove the rules.
