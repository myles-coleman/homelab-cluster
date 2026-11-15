# Gateway API cutover runbook

Operator procedure for moving a host from a Traefik `IngressRoute` to a Gateway API `HTTPRoute`
attached to the chart-rendered `Gateway/traefik-gateway`, and for rolling the change back.

Spec: `04-spec-gateway-api-cutover`. The Gateway itself is owned by the Traefik Helm chart
(`gateway.listeners` in `manifests/cluster/traefik/values.yaml`), not by a hand-written manifest.

## Preconditions

- `kubectl get gateway traefik-gateway -n traefik` reports `PROGRAMMED=True`.
- `kubectl get gatewayclass traefik` reports `ACCEPTED=True`.
- The Gateway has listeners `web` (8000/HTTP) and `websecure` (8443/HTTPS) with
  `allowedRoutes.namespaces.from: All`.

## Canary check

`gateway-canary.cowlab.org` exists only to prove the Gateway path before a production host is
touched. It is deleted at the end of the migration.

```bash
kubectl -n homepage get httproute gateway-canary \
  -o jsonpath='{range .status.parents[*].conditions[*]}{.type}={.status} {end}'; echo
curl -s -o /dev/null -w '%{http_code}\n' https://gateway-canary.cowlab.org
```

Expected: `Accepted=True ResolvedRefs=True` and `200`. A `404` here means the route did not attach;
check `allowedRoutes` and the `parentRefs.sectionName` before continuing.

## Per-host swap

One commit per host, three paths:

1. Add `manifests/cluster/<service>/httproute.yaml`.
2. Delete `manifests/cluster/<service>/ingress.yaml`.
3. Update `manifests/cluster/<service>/kustomization.yaml`.

The route shape is fixed:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: <service>
  namespace: <namespace>
spec:
  parentRefs:
    - name: traefik-gateway
      namespace: traefik
      sectionName: websecure
  hostnames:
    - <host>.cowlab.org
  rules:
    - backendRefs:
        - name: <service>
          port: <port>
```

Notes:

- No `tls` block and no `certResolver`. The `websecure` listener terminates TLS with
  `cowlab-production-tls`.
- Omit `matches` unless the old rule had a real path prefix. An `HTTPRoute` rule with no match
  defaults to `PathPrefix("/")`.
- Keep non-ingress annotations (for example `gethomepage.dev/*`), drop
  `kubernetes.io/ingress.class`.

## Per-host verification

All three checks must pass before the next host in the batch:

```bash
kubectl -n <namespace> get httproute <name> \
  -o jsonpath='{range .status.parents[*].conditions[*]}{.type}={.status} {end}'; echo
kubectl -n traefik port-forward deploy/traefik 18080:8080 &
curl -s http://127.0.0.1:18080/api/http/routers \
  | python3 -c "import json,sys;print([r['name'] for r in json.load(sys.stdin) if '<host>' in r['rule']])"
curl -s -o /dev/null -w '%{http_code}\n' https://<host>.cowlab.org
```

The router of interest must end in `@kubernetesgateway`, and there must be **no** `@kubernetescrd`
router for the same hostname. The status code must equal the one captured immediately before the
swap — not an assumed `200`. `argocd` and `longhorn` redirect; `dex` returns `404` at `/`.

## Drawdown rule

No more than **5** hosts may sit in a mixed migrated/unmigrated state at a batch boundary, and that
state must never be left overnight. Check at the start of every batch:

```bash
kubectl get httproute -A --no-headers | wc -l    # migrated
kubectl get ingressroute -A --no-headers | wc -l # remaining
```

If a batch cannot be finished, revert the remaining hosts of that batch rather than stopping
part-way through it.

## Rollback

Two levels, and they are not interchangeable:

- **One host**: `git revert <host commit>`. That restores the `IngressRoute` and removes the
  `HTTPRoute`. The Gateway is untouched.
- **The whole Gateway**: revert the Gateway commit **and every host migrated after it**. Reverting
  the Gateway commit alone leaves live `HTTPRoute`s parented to a missing Gateway, which serves
  `404` everywhere.

Rehearse the per-host rollback once during batch 1 and record the time-to-recover.

If chart rendering of the Gateway regresses, use the documented escape hatch in
`values.yaml`: `gateway.enabled: false` plus a hand-written `Gateway` in the same kustomization.
Never delete the `GatewayClass` while a `Gateway` references it — the
`gateway-exists-finalizer` blocks deletion and hangs the sync.

## ArgoCD CLI

The gRPC-over-ingress rule is intentionally not migrated: Gateway API expresses it as
`appProtocol: kubernetes.io/h2c` on the ArgoCD-owned Service, which this repository does not manage.
Use port-forward instead:

```bash
kubectl port-forward svc/argocd-server -n argocd 8080:443
argocd --port-forward --port-forward-namespace argocd app list
```

## Post-cutover steady state

```bash
grep -rn "kind: IngressRoute" manifests/cluster     # no matches
kubectl get ingressroute -A                          # no items
kubectl get httproute -A                             # exactly the 20 production hosts
kubectl get svc -A -o jsonpath='{range .items[?(@.spec.type=="ExternalName")]}{.metadata.namespace}/{.metadata.name}{"\n"}{end}'
grep -nE "kubernetesingress|allowExternalNameServices=true|insecureSkipVerify" manifests/cluster/traefik/helm-chart.yaml
```

`providers.kubernetesCRD` stays enabled permanently: it is still needed for `Middleware` as
`ExtensionRef`. A cluster that serves only Gateway API routes but keeps the CRD provider is the
intended end state, not a half-finished migration.
