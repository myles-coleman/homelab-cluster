# Sandbox Agent Observability

Runbook for the OpenCode sandbox's OTLP telemetry: how to verify it is working and how to
turn it off.

The sandbox pod exports **traces and logs** over OTLP to the `k8s-infra` agent, through that
agent's **Service ClusterIP** (`10.43.78.178:4318`). One environment variable enables both
signals: `OTEL_EXPORTER_OTLP_ENDPOINT`. The agent's `transform/agent_telemetry` processor
scopes, truncates, and redacts that telemetry before it reaches SigNoz.

## Why the ClusterIP and not the node's own address

The obvious endpoint is the agent's hostPort on the sandbox node, `10.0.0.146:4318`, and it
does **not** work from a pod running on that same node:

1. the CNI portmap rules DNAT the packet to the agent pod (`10.42.5.6:4318`), same node, same
   subnet;
2. the agent's reply goes pod-to-pod over the bridge and bypasses the host's conntrack, so it is
   never un-DNAT'd back to `10.0.0.146`;
3. the client sees a SYN-ACK from an address it never dialled and drops it.

Measured: **0/10** connections from a pod on `sandbox-node`, **10/10** from a pod on `node4`
(same policy, same destination). The `tcpdump` shows the SYN leaving and no SYN-ACK returning.
A plain host-network listener on another port works, which is why a relay "fixes" it and hides
the real defect.

The Service ClusterIP path is masqueraded by kube-proxy, so the return path is correct from any
node. That is the supported shape.

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

Expect `OTEL_EXPORTER_OTLP_ENDPOINT=http://10.43.78.178:4318`,
`OTEL_RESOURCE_ATTRIBUTES=deployment.environment=agent-sandbox,service.namespace=agent-sandbox`,
and a `NO_PROXY` that contains `10.43.78.178`. There must be **no** `OTEL_SERVICE_NAME`.

Confirm the ClusterIP still matches the Service:

```bash
kubectl get svc -n signoz-k8s-infra k8s-infra-otel-agent -o jsonpath='{.spec.clusterIP}{"\n"}'
```

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
There should be **no** entry for `10.43.78.178` from this pod. A denial means the `NO_PROXY`
entry is missing or was dropped.

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
kubectl exec -n agent-sandbox netpol-probe -- nc -zv -w 5 10.43.78.178 4318
# Expected: 4317 fails (not allowed by the policy)
kubectl exec -n agent-sandbox netpol-probe -- nc -zv -w 5 10.43.78.178 4317

kubectl delete pod netpol-probe -n agent-sandbox
```

Run the probe **scheduled onto `sandbox-node`** if you are checking the same-node path; that is
the case that failed before the ClusterIP change. A probe on another node will pass even when
the same-node path is broken.

If a port other than `4318` also succeeds, the NetworkPolicy is not being enforced for this
shape and the egress restriction must be treated as absent until it is fixed.

## Rollback

Removing three things restores the previous silent state:

1. In `manifests/cluster/agent-sandbox/sandbox.yaml`, delete the
   `OTEL_EXPORTER_OTLP_ENDPOINT` and `OTEL_RESOURCE_ATTRIBUTES` entries and remove
   `10.43.78.178` from `NO_PROXY`.
2. In `manifests/cluster/agent-sandbox/networkpolicy.yaml`, delete the egress rule for the
   `signoz-k8s-infra` namespace on TCP `4318`.
3. Commit and let ArgoCD sync; the sandbox pod rolls and the exporter stops.

The collector's `transform/agent_telemetry` processor can stay: with no sandbox telemetry
arriving it matches nothing and costs one no-op rule per record. Removing it is optional and
independent.

After rollback, expect: no new spans with `deployment.environment = agent-sandbox`, no new
`agent-sandbox` log records, and the NetworkPolicy probe's `4318` check to start failing. All
other telemetry is unaffected.

## If the ClusterIP changes

The ClusterIP is assigned by the cluster, not by `nixos`. It survives agent pod rollouts but
changes if the Service is deleted and recreated. To re-derive it:

```bash
kubectl get svc -n signoz-k8s-infra k8s-infra-otel-agent -o jsonpath='{.spec.clusterIP}{"\n"}'
```

Then update the endpoint and the `NO_PROXY` entry in `sandbox.yaml` in the same commit, or the
exporter will be routed through Squid and denied.
