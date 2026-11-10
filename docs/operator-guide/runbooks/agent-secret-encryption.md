# Agent Secret Encryption at Rest

Recorded answer (spec 30, FR-59) to the question: **is k3s secret encryption at
rest enabled for this cluster?** It is not. This page records the evidence, the
command that produced it, the accepted risk, and the compensating rotation
cadence.

## Observed result

k3s secret encryption at rest is **not enabled**.

On node0 (the k3s server):

- `/var/lib/rancher/k3s/server/cred/` contains **no** `encryption-config.json`.
  k3s writes that file only when encryption is turned on, so its absence means
  the API server has no encryption provider configured.
- The k3s server process command line is exactly
  `/nix/store/…/bin/k3s server` — no `--secrets-encryption` flag (and no
  `--encryption-provider-config` pointing at a file).
- `/etc/rancher/k3s/config.yaml` does not exist, so the flag is not being supplied
  through the config file either.

Both supported switches are absent, so API server Secret values are stored in the
datastore without encryption and are readable by anything that can read the
datastore's files on node0.

## Command

Read-only inspection from a privileged debug Pod on node0 with the host
filesystem mounted. `kubectl debug node/...` creates a privileged Pod with the
node's root at `/host`; it is deleted when the session ends. Nothing below writes
to the node.

```bash
kubectl debug node/node0 -it --image=busybox:1.36 -- chroot /host sh -c '
  echo "== cred directory =="
  ls -la /var/lib/rancher/k3s/server/cred/
  echo "== k3s server cmdline =="
  pid=$(pgrep -f "/bin/k3s server" | head -1)
  tr "\0" " " < /proc/"$pid"/cmdline; echo
  echo "== config file =="
  ls -l /etc/rancher/k3s/config.yaml 2>&1
'
```

The three checks map one-to-one to the observed result: the `cred` listing shows
no `encryption-config.json`, the `/proc/<pid>/cmdline` dump has no
`--secrets-encryption`, and the `ls` on the config file reports it missing.

Re-run this command after any k3s upgrade or config change; the answer is
expected to change only through a deliberate, reviewed change to the node image.

## Accepted risk

Without encryption at rest, Secret values in the k3s datastore are plaintext on
node0's disk. The effective boundary is therefore **root on node0** (or anyone
who can mount its filesystem, including a privileged hostPath Pod): such an
actor can read every Secret in the cluster without going through the API server.

For the agent broker specifically, the exposed high-value material is:

- the GitHub App private key, mounted only by the publisher;
- the `agent-mtls` CA key, mounted by no workload;
- the leaf private keys, which are short-lived and reissued.

This is accepted because node0 is a single-tenant control-plane host, the CA key
is never mounted into a workload, the App key reaches exactly one Pod, and both
long-lived keys are rotated on a fixed cadence rather than relied on
indefinitely. The rotation cadence is the compensating control, not a claim that
the datastore is safe.

**Revisit this decision if k3s secret encryption is enabled** (add
`secrets-encryption: true` to the k3s config or `--secrets-encryption` to the
server command, confirm `encryption-config.json` appears, then update this page
with the new observation). Also revisit on any suspected node compromise: an
attacker with node0 root during the exposure window may already hold the keys,
so rotation must happen immediately rather than on schedule.

## Rotation cadence

The CA key and the GitHub App private key are rotated **every 90 days, or
immediately on suspected exposure**. The CA key rotation is manual (cert-manager
does not reissue leaves when the CA changes) and is documented, together with the
GitHub App key procedure, leaf reissue order, and trust redistribution, in
[agent-mtls-rotation.md](agent-mtls-rotation.md).

When encryption at rest is eventually enabled, keep the 90-day cadence until this
page has been updated with the new evidence and the accepted risk is re-reviewed.
