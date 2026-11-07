# Kubernetes (k3s) Upgrade Runbook

## Overview

Walk the six-node k3s cluster one minor version at a time, with a backup before each hop and a
gate after every node. Written from the 1.33 → 1.36 upgrade (spec 28) and its three failures, so
it encodes what actually broke rather than an idealised checklist.

**Read the rollback section before starting.** The datastore is single-node SQLite behind one
control plane; some mistakes here are not fixed by re-deploying.

## Topology facts that drive the procedure

| Fact | Consequence |
| --- | --- |
| `node0` is the only server (single control plane) | every `node0` restart is a real API outage |
| kube-vip holds `10.0.0.200` on `node0` in ARP mode | the VIP dies with k3s and returns only when k3s is schedulable; agents point at it |
| Longhorn has 2 replicas per volume (one legacy volume has 1) | exactly **one** agent may be down at a time |
| `node0`–`node3` run a 16K-page kernel | their closures miss upstream caches and build from source; their deploys are slow |
| `node4` is the only GPU node and runs 4K pages | its deploys are fast; never assume `node0`–`node3` behave like it |
| `sandbox-node` is a microvm on `homelab` with `restartIfChanged = false` | a `homelab` deploy installs its definition but **does not restart it** |
| The guest is not reachable from the LAN except through `homelab` | the gate needs a jump host (`K3S_GUEST_JUMP`) |
| ArgoCD is installed by hand (helm), not by ArgoCD | its supported k8s range must be checked before a hop |

## Prerequisites

- Repos: `myles-coleman/nixos` (the pin and the scripts) and `myles-coleman/homelab-cluster`
  (ArgoCD Applications and app manifests).
- `gh` authenticated, `kubectl` on the workstation pointed at the VIP.
- SSH to `pi@10.0.0.140`–`144` and `bee@10.0.0.150` + `bee@10.0.0.146` (via the jump).
- The age identity that encrypts backups (`~/.config/age/keys.txt`). **Without it the artifacts are
  unreadable.**

## 1. Choose the target and check ArgoCD

```bash
nix eval --raw .#nixosConfigurations.node0.config.services.k3s.package    # current pin
nix eval --raw .#nixosConfigurations.node0.pkgs.k3s_1_36.version          # candidate
```

K3s supports **one minor at a time**, servers before agents. Check the running ArgoCD against
its tested-versions table (`argo-cd.readthedocs.io` → *Tested Kubernetes versions*): if the target
minor is outside its range, upgrade ArgoCD first. It is a manual `helm upgrade --reuse-values`
because nothing in the repos describes that release.

## 2. Take and verify a rollback point

The scripts live in `myles-coleman/nixos/scripts/`.

```bash
scripts/k3s-backup.sh --minor <current-minor> --recipient <age-recipient>
scripts/k3s-backup.sh --verify ~/k3s-backups/<artifact>.tar.age
```

- `--minor` is the minor the datastore is **being taken on** — that is the label rollback uses.
- Expect `integrity_check: ok`, a sha256, and a token member reported by length only.
- Record the manifest (path, size, sha256, minor, token-generation hash) in the artifacts vault
  under `nixos/docs/specs/`. Never commit the artifact itself.
- Nothing deletes these automatically. Keep the artifact for the hop you are about to do until its
  soak finishes.

## 3. Change the pin (one PR per hop)

| File | Repo | What it holds |
| --- | --- | --- |
| `modules/aspects/k3s-common.nix` | nixos | `services.k3s.package = lib.mkDefault pkgs.k3s_1_<N>;` |
| `hosts/homelab/modules/sandbox-node.nix` | nixos | an optional plain pin that holds the guest one minor behind |

- The pin must stay `lib.mkDefault` so hosts can override it.
- Do **not** move `flake.lock`: the minor comes from the `k3s_1_3X` attributes only. `pin-guard`
  keeps root nixpkgs aligned with `nixos-raspberrypi`.
- While a hop is in flight, pin the guest one minor **behind** the server. `homelab` auto-deploys
  on every merge to `main`, so without an explicit pin a guest restart can put a newer kubelet in
  front of an older apiserver — the exact skew that is not allowed.

Verify before merging:

```bash
nix run nixpkgs#alejandra -- <changed files>
nix flake check --no-build
nix eval --raw .#nixosConfigurations.node0.config.services.k3s.package
nix eval --raw '.#nixosConfigurations.homelab.config.microvm.vms.sandbox-node.config.config.services.k3s.package'
```

Merge only when `pin-guard`, `build-arm` (node0–node4), `closure-diff` and `gate` are green.

## 4. Roll out, one dispatch per node

k3s nodes are **manual `workflow_dispatch` only** and must never join the auto-deploy set.

```bash
gh workflow run deploy.yaml --ref main -f hosts=node0
gh run watch <run-id>
scripts/k3s-upgrade-check.sh --expect-minor <target> --node node0
```

Then, **only if the gate passes**, `node1`, `node2`, `node3`, `node4` — one at a time, gate
between each. Stop the rollout at the first failure; do not dispatch the next node "to finish the
set".

Finally the guest:

```bash
# after the fleet is on the target
gh workflow run deploy.yaml --ref main -f hosts=homelab     # installs the new VM definition
ssh bee@10.0.0.150 'sudo systemctl restart microvm@sandbox-node'
```

The restart is an explicit step: `restartIfChanged = false` means the deploy alone will leave the
VM on its old k3s. When the fleet has landed, remove the guest's pin so it inherits the shared one.

## 5. The gate

`scripts/k3s-upgrade-check.sh` is the only thing that enforces "verify between each node" — the
deploy workflow's post-deploy check proves SSH, and `fail-fast: false` lets a failed `node0` be
followed by agent deploys.

```bash
# per node during a hop
scripts/k3s-upgrade-check.sh --expect-minor 1.36 --node node0

# whole fleet
K3S_GUEST_JUMP=bee@10.0.0.150 scripts/k3s-upgrade-check.sh --expect-minor 1.36

# no cluster access (runs in pr.yaml as pin-guard)
scripts/k3s-upgrade-check.sh --static

# after a known-good state, to accept it as the drift baseline
scripts/k3s-upgrade-check.sh --record-baseline
```

Exit codes: `0` pass, `1` a named assertion failed, `2` usage/environment.

It asserts, per node: `k3s` active, the minor parsed from systemd's **`ExecStart` store path** (never
`k3s --version`), `cgroup2fs`, node `Ready` with a kubelet matching its own `ExecStart`. Fleet-wide:
`/readyz` and `/version` through the VIP with real credentials, the `kube-vip` pod on `node0`, and
no **new** ArgoCD `OutOfSync`/`Unknown` against the baseline. Deprecated-API metric series are
printed; `kubent` runs when installed and records `SKIPPED` otherwise.

`--static` needs no cluster and fails if an unpinned `k3s`/`kubectl` reappears or if
`k3s-server` stops using `config.services.k3s.package`.

## 6. Rollback

**Restore first, re-pin second.** Once the newer binary has started and migrated the datastore,
starting the older minor against it is not supported.

| Condition | Path |
| --- | --- |
| k3s never became `active`, `state.db` mtime unchanged | roll the NixOS generation back — the datastore was never migrated |
| k3s became active at least once | restore the artifact from the minor you came from, then roll the generation back |

The full procedure (stop k3s, replace `state.db` **and remove the stale `-wal`/`-shm`**, restore
the token, start, gate, then bring agents back one at a time) lives in the artifacts vault:
`nixos/docs/specs/28-spec-k3s-1-36-upgrade/28-proofs/runbook-rollback.md`.

Two things that are easy to get wrong:

- `10.0.0.200` can be unreachable while `node0` answers on `10.0.0.140`. Use
  `--server=https://10.0.0.140:6443` rather than concluding the cluster is broken.
- Longhorn tolerates one agent down. Recover one node fully before touching the next.

## 7. Known traps (all of these happened)

1. **`cred/passwd newer than datastore`** — k3s refuses to start after a restart rewrites
   `server/cred/passwd` with content the datastore does not hold (the `--agent-token-file`
   interaction). It cost 13 minutes of API outage. Now defused by an `ExecStartPre` in
   `k3s-token-sops.nix` that moves the file aside before start. If it ever reappears, that guard
   did not run.
2. **`iscsid.socket` fails during activation on an agent** and deploy-rs rolls the whole deploy
   back. It is a race: `node1` succeeded with 13 iSCSI sessions, `node3` failed with 3. Retry once;
   if it repeats, set `systemd.services.iscsid.restartIfChanged = false` (the socket has no
   equivalent option) so activation never restarts a daemon holding live Longhorn sessions.
3. **A stalled `nix copy`** looks like a slow deploy: `nix-store --serve --write` alive, new system
   absent from the target store, `bytes_received` frozen. Cancel and retry; do not wait it out.
4. **ArgoCD reports `OutOfSync` with no diff.** Under `ServerSideApply`, fields the API server
   defaulted or dropped cause a perpetual diff: `creationTimestamp: null` in a rendered pod
   template, `status` inside a StatefulSet's `volumeClaimTemplates`. Diagnose with
   `kubectl exec -n argocd deploy/argocd-server -- argocd app diff <app>` (the CLI is in the pod,
   so no UI needed) and fix the manifest at source rather than adding a blanket ignore.
5. **A `homelab` deploy does not restart the guest.** See §4.
6. **DNS can take the control plane's tooling down without touching Kubernetes.** `homelab`
   resolved everything through Tailscale MagicDNS, which forwards to `protecli-vault`; when that
   gateway's tunnel wedged, a deploy died on `Could not resolve host: cache.nixos.org`. The k3s
   nodes are immune because `k3s-common` sets `--accept-dns=false`.

## 8. Finish

- Run the full gate on the fleet and record the assertion count.
- Soak: the spec for the 1.33→1.36 walk asked for 24 h per hop with explicit signals — all nodes
  `Ready`, no new ArgoCD drift, no k3s restarts, no anomalous `server/db` growth.
- Put the hop record (dispatch order, run IDs, gate transcripts, backup sha256, incidents) in the
  artifacts vault. Proof lives outside the repos on purpose.
