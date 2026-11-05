# Sandbox Agent Observability

Runbook for the OpenCode sandbox's OTLP telemetry: how to verify it is working and how to
turn it off.

The sandbox pod exports **traces and logs** over OTLP to the node-local `k8s-infra` agent
(`10.0.0.146:4318`). One environment variable enables both signals:
`OTEL_EXPORTER_OTLP_ENDPOINT`. The agent's `transform/agent_telemetry` processor scopes,
truncates, and redacts that telemetry before it reaches SigNoz.

## The discriminator

`service.name` is hardcoded to `opencode` in the binary and `OTEL_SERVICE_NAME` is ignored. Do
not use it to identify this workload. Every scope clause and every query keys on:

- traces and OTLP logs: `deployment.environment = agent-sandbox`
- container-output logs (the `filelog` path): `k8s.namespace.name = agent-sandbox`

The two record shapes differ. OTLP records carry `service.name`, `deployment.environment` and
`service.namespace`, but **no `k8s.namespace.name`**: the `k8sattributes` processor cannot
associate them with a pod, because the SDK's resource has no `k8s.pod.uid`/`k8s.pod.name` to
join on. Records that come from `filelog` carry the namespace and no `service.name`. Query on
the right one, or on both.

## Verify

### 1. The pod carries the configuration

```bash
kubectl get pod -n agent-sandbox -l app=opencode-sandbox \
  -o jsonpath='{range .items[0].spec.containers[0].env[*]}{.name}={.value}{"\n"}{end}' \
  | grep -E 'OTEL_|NO_PROXY'
```

Expect `OTEL_EXPORTER_OTLP_ENDPOINT=http://10.0.0.146:4318`,
`OTEL_RESOURCE_ATTRIBUTES=deployment.environment=agent-sandbox,service.namespace=agent-sandbox`,
and a `NO_PROXY` that contains `10.0.0.146`. There must be **no** `OTEL_SERVICE_NAME`.

### 2. Spans arrive

SigNoz → Traces → filter `service.name = opencode` and
`deployment.environment = agent-sandbox`, last hour. After one agent turn there should be at
least one span, usually a `SessionRunner.runStep` or `Tool.execute` with children.

### 3. Logs arrive and are correlated

SigNoz → Logs → filter `deployment.environment = agent-sandbox`, last hour. Records should carry
a non-empty `trace_id` and `span_id`; opening one links to its trace. Startup records
(`cli starting`, `server listening`) are emitted before any span exists and therefore carry no
`trace_id` — that is expected, not a failure.

### 4. The exporter bypassed the proxy

SigNoz → Logs → filter `k8s.namespace.name = squid` and the body containing `TCP_DENIED`.
There should be **no** entry for `10.0.0.146` from this pod. A denial means the `NO_PROXY` entry
is missing or was dropped.

### 5. The policy is actually enforced

This is the check that proves the egress rule restricts rather than decorates. The probe pod
must carry `app=opencode-sandbox`, otherwise it matches `default-deny` instead of the sandbox
egress policy and proves nothing.

```bash
# A short-lived, labelled probe. It is deleted immediately; it is not a workload.
kubectl run netpol-probe -n agent-sandbox --restart=Never \
  --labels app=opencode-sandbox --image=busybox:1.36 --command -- sleep 300
kubectl wait --for=condition=Ready pod/netpol-probe -n agent-sandbox --timeout=60s

# Expected: 4318 succeeds (the collector)
kubectl exec -n agent-sandbox netpol-probe -- nc -zv -w 5 10.0.0.146 4318
# Expected: 4317 fails (not allowed by the policy)
kubectl exec -n agent-sandbox netpol-probe -- nc -zv -w 5 10.0.0.146 4317

kubectl delete pod netpol-probe -n agent-sandbox
```

If port `4317` (or any other) also succeeds, the NetworkPolicy is not being enforced for this
shape and the egress restriction must be treated as absent until it is fixed.

## Rollback

Removing three things restores the previous silent state:

1. In `manifests/cluster/agent-sandbox/sandbox.yaml`, delete the
   `OTEL_EXPORTER_OTLP_ENDPOINT` and `OTEL_RESOURCE_ATTRIBUTES` entries and remove
   `10.0.0.146` from `NO_PROXY`.
2. In `manifests/cluster/agent-sandbox/networkpolicy.yaml`, delete the egress rule for
   `10.0.0.146/32` on TCP `4318`.
3. Commit and let ArgoCD sync; the sandbox pod rolls and the exporter stops.

The collector's `transform/agent_telemetry` processor can stay: with no sandbox telemetry
arriving it matches nothing and costs one no-op rule per record. Removing it is optional and
independent.

After rollback, expect: no new spans with `deployment.environment = agent-sandbox`, no new
`agent-sandbox` log records, and the NetworkPolicy probe's `4318` check to start failing. All
other telemetry is unaffected.

## The address lives in three places

`10.0.0.146` is a **static** address, not DHCP. It appears in:

1. `nixos` → `hosts/homelab/modules/sandbox-node.nix` (the definition)
2. `homelab-cluster` → `manifests/cluster/agent-sandbox/sandbox.yaml` (the exporter endpoint)
3. `homelab-cluster` → `manifests/cluster/agent-sandbox/networkpolicy.yaml` (the egress rule)

Change all three in the same change window, or the exporter will point at a host the policy
does not allow — or the policy will allow a host that no longer exists.
