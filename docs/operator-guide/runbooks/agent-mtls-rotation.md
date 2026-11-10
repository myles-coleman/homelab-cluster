# Agent mTLS and Credential Rotation

Operator runbook for the agent broker's two long-lived credentials: the
`agent-mtls` CA key and the GitHub App private key. Everything else — sandbox
leaves, validator and publisher server/client leaves — is short-lived and is
reissued by cert-manager.

**Cadence: rotate both keys every 90 days, or immediately on suspected
exposure.** The at-rest posture that makes this cadence the compensating control
is recorded in [agent-secret-encryption.md](agent-secret-encryption.md).

The trust shape:

| Material | Where it lives | Lifetime |
|----------|----------------|----------|
| CA keypair | Secret `agent-mtls-ca-keypair` in `cert-manager` | 1 year, `rotationPolicy: Never` |
| validator-server leaf | Secret `validator-server-tls` in `agent-validator` | 30 days, renew 7 days early |
| validator-client leaf | Secret `validator-client-tls` in `agent-validator` | 30 days, renew 7 days early |
| publisher-server leaf | Secret `publisher-server-tls` in `agent-publisher` | 30 days, renew 7 days early |
| sandbox leaf | cert-manager CSI volume, never stored in a Secret | 12 hours, reissued on mount |

`agent-mtls` is the logical home of the CA, but the Secret is not there:
cert-manager resolves a ClusterIssuer's `spec.ca.secretName` only in its
cluster-resource namespace, which is `cert-manager`. See
[`manifests/cluster/agent-mtls/issuers.yaml`](../../../manifests/cluster/agent-mtls/issuers.yaml).

## CA key rotation

The CA key is the one Secret in `cert-manager` named `agent-mtls-ca-keypair`. Its
Certificate (`agent-mtls-ca`) is self-signed through the bootstrap Issuer and is
referenced by the ClusterIssuer (`agent-mtls-ca`) that every leaf uses. Nothing
may copy or mount the key elsewhere; `agent-mtls/namespace.yaml` explains why the
Secret is not in the namespace of the same name.

Rotation is manual for two reasons:

- The CA Certificate sets `privateKey.rotationPolicy: Never`. Letting
  cert-manager renew on schedule keeps the same key, which is deliberate: a
  silent CA key change would orphan every leaf that is still inside its own
  lifetime. Flipping it to `Always` is not the rotation procedure.
- cert-manager does not reissue leaves when the CA changes. A leaf is reissued
  only when it reaches `renewBefore` or its spec or Secret changes. A new CA
  therefore leaves every existing leaf signed by the old one, and verification
  breaks on the next handshake unless the leaves are reissued in order.

Rotate with overlap so there is no window in which neither CA is trusted:

1. Mint the next CA keypair under a new Secret name, leaving the current one in
   place:

   ```bash
   kubectl -n cert-manager get certificate agent-mtls-ca -o yaml \
     | sed -e 's/name: agent-mtls-ca$/name: agent-mtls-ca-next/' \
           -e 's/secretName: agent-mtls-ca-keypair$/secretName: agent-mtls-ca-keypair-next/' \
     | kubectl apply -f -
   ```

   Keep `rotationPolicy: Never` on the new Certificate. Wait for it to be Ready:

   ```bash
   kubectl -n cert-manager wait --for=condition=Ready certificate/agent-mtls-ca-next --timeout=120s
   ```

2. Repoint the ClusterIssuer at the new Secret. Edit
   `manifests/cluster/agent-mtls/issuers.yaml` so `spec.ca.secretName` is
   `agent-mtls-ca-keypair-next`, commit, and let ArgoCD sync. Do not edit the
   live object with `kubectl apply`.

3. Reissue the leaves in the order below, then redistribute trust, then remove
   the old CA. The old Certificate and Secret are deleted only after every
   consumer is on the new chain; that overlap is the reason the second CA exists.

## GitHub App key rotation

The publisher is the only workload that holds a GitHub write credential. It is
mounted from Secret `github-app` in `agent-publisher`, which External Secrets
Operator fills from two Vaultwarden secure notes: the App id and the private key
PEM. The PEM note body must be exactly the key — no label, no blank line, no
trailing newline.

1. In the GitHub App settings (owner `myles-coleman`), open **Private keys** and
   click **Generate a private key**. The `.pem` downloads once; do not paste it
   into a terminal that records history or into any file in this repository.
2. Update the Vaultwarden note that feeds `secretKey: private-key.pem` with the
   new PEM. The App id note does not change.
3. Force the ExternalSecret to refresh instead of waiting for its one-hour
   interval:

   ```bash
   kubectl -n agent-publisher annotate externalsecret github-app \
     force-sync="$(date +%s)" --overwrite
   kubectl -n agent-publisher get externalsecret github-app \
     -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}{"\n"}'
   ```

   Expect `True`. Compare the mounted key's fingerprint with the new note's
   without printing either value:

   ```bash
   kubectl -n agent-publisher get secret github-app \
     -o jsonpath='{.data.private-key\.pem}' | base64 -d \
     | openssl pkey -pubout -outform der | sha256sum
   ```

4. Restart the publisher so it loads the key at startup:

   ```bash
   kubectl -n agent-publisher rollout restart deploy/publisher
   kubectl -n agent-publisher rollout status deploy/publisher --timeout=120s
   ```

5. Revoke the old private key in the GitHub App settings. Revoking before the
   publisher has the new key breaks `publish`; revoking after it works is safe
   and leaves a single valid key.
6. Verify `publish` still works. Drive one publish through the validator (the
   publisher is not reachable from the LAN) and confirm the audit record is an
   `accept` with a real commit SHA, and that the branch appears on the
   repository. Alternatively, watch the publisher log for a successful token
   exchange on the next real request; an authentication failure means the key
   did not reach the pod or was revoked too early.

## Leaf reissue

Leaves are reissued after a CA change, and can be reissued on demand at any
time. `cmctl renew` triggers reissuance without deleting the Secret; deleting the
Secret also works because cert-manager recreates it, and the leaves set
`rotationPolicy: Always`, so a new leaf key is generated either way.

Order matters. The publisher must trust the new CA before the validator presents
a client leaf signed by it, and the validator must trust the new CA before a
sandbox connects:

1. `publisher-server` — supplies both the publisher's serving leaf and its client
   CA (`--client-ca-file=/certs/ca.crt`) for validator-client.

   ```bash
   cmctl renew -n agent-publisher publisher-server
   cmctl status certificate -n agent-publisher publisher-server
   ```

2. `validator-server` — supplies the validator's serving leaf and its client CA
   for sandbox leaves.

   ```bash
   cmctl renew -n agent-validator validator-server
   cmctl status certificate -n agent-validator validator-server
   ```

3. `validator-client` — the identity the validator presents to the publisher.
   Reissue only after step 1 has loaded the new CA.

   ```bash
   cmctl renew -n agent-validator validator-client
   cmctl status certificate -n agent-validator validator-client
   ```

If `cmctl` is unavailable, delete each Secret instead and wait for the
replacement:

```bash
kubectl -n agent-publisher delete secret publisher-server-tls
kubectl -n agent-validator delete secret validator-server-tls
kubectl -n agent-validator delete secret validator-client-tls
```

Then restart both servers so a cached verifier pool cannot hold the old chain:

```bash
kubectl -n agent-validator rollout restart deploy/validator
kubectl -n agent-publisher rollout restart deploy/publisher
```

The sandbox leaf is not in this list. It is issued at mount time by the CSI
driver from the ClusterIssuer, so after the CA switch a new Sandbox Pod gets the
new chain automatically. A running sandbox keeps its old leaf until the volume is
re-mounted; delete the sandbox Pod to force a re-mount (the workspace PVC is
unaffected). Never copy a sandbox leaf into a Secret.

## Trust redistribution

After the CA changes, every consumer of trust must see the new CA and no consumer
may keep the old one. The consumers are:

- **CA bundle ConfigMap** — the trust copy in namespace `agent-mtls` for clients
  that cannot mount a Secret or a CSI volume. Regenerate it from the new CA's
  `ca.crt` and commit the manifest; this is public material, not the key.
  Confirm no other ConfigMap carries a copy:

  ```bash
  kubectl get configmap -n agent-mtls -o name
  ```

- **CSI volume consumers** — the sandbox's inline `identity` volume names the
  ClusterIssuer `agent-mtls-ca`. It follows the ClusterIssuer, so no manifest
  change is needed; the proof is a new leaf whose issuer is the new CA.
- **`GIT_SSL_CAINFO`** — the sandbox sets `GIT_SSL_CAINFO=/certs/ca.crt`, and
  that file is the CSI mount's `ca.crt`. It follows the volume. Verify inside the
  Pod that `git` still clones, rather than editing the variable.
- **Publisher client CA** — the publisher's `--client-ca-file=/certs/ca.crt` is
  the `ca.crt` inside `publisher-server-tls`, which is why that leaf is reissued
  first. The validator-client leaf must be signed by the same CA.
- **Validator client CAs** — `--client-ca-file=/certs/ca.crt` (sandbox leaves)
  and `--publisher-ca-file=/publisher-client/ca.crt` (publisher-server) come from
  `validator-server-tls` and `validator-client-tls` respectively.

Close the rotation by deleting the previous CA Certificate and Secret
(`agent-mtls-ca`, `agent-mtls-ca-keypair`, or the `-next` pair once the names are
swapped), then grep the repository for the old Secret name so no manifest keeps a
stale reference.

## Expiry monitoring

Three lifetimes expire on different schedules: the CA in about a year, the three
service leaves in 30 days, and the sandbox leaf in 12 hours. A leaf that expires
is a handshake failure, so alert before it lapses.

In SigNoz → Metrics, filter
`certmanager_certificate_expiration_timestamp_seconds{namespace=~"agent-validator|agent-publisher"}`
and graph it as time minus now; alert when any series is under seven days. Add a
second panel filtering
`certmanager_certificate_expiration_timestamp_seconds{name="agent-mtls-ca"}`
with a 30-day threshold. If cert-manager is not scraped in your tenant, use the
logs query `k8s.namespace.name=agent-validator` filtered to `certificate`
renewal events as the fallback and fix the scrape rather than relying on it.

The direct checks:

```bash
kubectl get certificate -A | grep -E 'agent-(mtls|validator|publisher)'
cmctl status certificate -n agent-validator validator-server
cmctl status certificate -n agent-validator validator-client
cmctl status certificate -n agent-publisher publisher-server
kubectl -n cert-manager get certificate agent-mtls-ca -o jsonpath='{.status.notAfter}{"\n"}'
```

`kubectl get certificate` shows `READY` and `AGE`; `cmctl status` shows the
not-after date and the renewal time. For a raw check:

```bash
kubectl -n agent-publisher get secret publisher-server-tls \
  -o jsonpath='{.data.tls\.crt}' | base64 -d | openssl x509 -noout -dates
```

## Threat model

The broker splits three responsibilities across three identities, and each one
is distrusted by the others.

**Validator.** It parses hostile certificates and patches, so it runs under
gVisor, non-root, with a read-only root filesystem, `RuntimeDefault` seccomp, all
capabilities dropped, and no GitHub credential. Its egress is three peers: the
Kubernetes API on 6443, the publisher on 8443, and the OTLP collector on 4318. It
has no DNS and no internet path.

Its Kubernetes grant is deliberately tiny and is the whole of its cluster
authority — Role `validator-identity-reader`, namespace-scoped to `agent-sandbox`,
bound to ServiceAccount `validator` in `agent-validator`:

| API group | Resource | Verbs |
|-----------|----------|-------|
| `""` (core) | `pods` | `get`, `list` |
| `agents.x-k8s.io` | `sandboxes` | `get` |

There are no Secret or ConfigMap reads, no write verbs, no wildcards, and no
ClusterRole. See
[`manifests/cluster/agent-validator/rbac.yaml`](../../../manifests/cluster/agent-validator/rbac.yaml).
The grant exists because the validator must turn a leaf's URI SAN into a Pod and
then into that Pod's Sandbox owner (FR-4). `list` on Pods is required because
the Pod name is not in the SAN; `get` on Sandboxes resolves the owner. A
compromise of the parser therefore yields Pod metadata in one namespace, not
credentials, not other namespaces, and not the ability to mutate anything. If
the grant ever needs widening, treat it as a security change: any Secret read
would expose the GitHub App key or the CA key to the component that parses
attacker-controlled input.

**Publisher.** It holds the only GitHub write credential, so it never runs on the
gVisor node, has no toleration for the sandbox taint, mounts no ServiceAccount
token, and its egress excludes the whole cluster, the LAN, and the tailnet. A
compromised validator can submit a patch but cannot read the key; a compromised
publisher cannot pivot to in-cluster services. Its audit records go to stdout and
ship over the existing filelog path, not OTLP, to keep that boundary intact.

**CA key.** It lives only in `cert-manager`, is never mounted into a workload,
and is rotated manually on the cadence at the top of this page. There is no
automated signer exposed to the sandbox; leaves are issued by cert-manager from
the ClusterIssuer.

**At rest.** k3s secret encryption is not enabled on this cluster; the recorded
evidence and the accepted risk are in
[agent-secret-encryption.md](agent-secret-encryption.md). That is why the CA key
and the App key rotate every 90 days (or immediately on suspected exposure).
