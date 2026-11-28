#!/usr/bin/env python3
"""Audit every rendered namespace against Pod Security Baseline and Restricted.

Two inputs, because either one alone lies:

  * the live pods, via `kubectl get pods -A -o json`, which catches pods a
    controller created from a custom resource (ClickHouse, Sandbox) and which no
    rendered manifest shows;
  * the rendered workload templates in manifests/dist/, which catch a pod that
    is not running right now but would be created on the next restart or scale.

`--check` fails when a Namespace object in manifests/dist/ is labelled weaker
than its workloads require, or when a `restricted` label is not actually
satisfied (the latent-breakage case: the label gates admission of new pods only,
so the cluster looks healthy until the next restart). That is the regression
guard: a new privileged sidecar cannot land without either fixing the workload
or making the exception explicit.

Requires PyYAML (see scripts/tools.env).
"""

from __future__ import annotations

import argparse
import glob
import json
import subprocess
import sys

import yaml

ALLOWED_CAPS = {
    "AUDIT_WRITE", "CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL",
    "MKNOD", "NET_BIND_SERVICE", "SETFCAP", "SETGID", "SETPCAP", "SETUID",
    "SYS_CHROOT",
}
SAFE_SYSCTLS = {
    "kernel.shm_rmid_forced", "net.ipv4.ip_local_port_range",
    "net.ipv4.ip_unprivileged_port_start", "net.ipv4.tcp_syncookies",
    "net.ipv4.ping_group_range", "net.ipv4.ip_local_reserved_ports",
}

# Live namespaces this repository does not own. Reported, never required to
# carry a label, because nothing here can change them.
UNMANAGED = {"argocd", "default", "kube-node-lease", "kube-public", "kube-system"}

STRICTNESS = {"privileged": 0, "baseline": 1, "restricted": 2}
LEVELS = {0: "privileged", 1: "baseline", 2: "restricted"}


def pod_specs(obj):
    kind = obj.get("kind")
    if kind == "Pod":
        yield obj.get("spec") or {}
    elif kind in ("Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job"):
        yield ((obj.get("spec") or {}).get("template") or {}).get("spec") or {}
    elif kind == "CronJob":
        spec = ((obj.get("spec") or {}).get("jobTemplate") or {}).get("spec") or {}
        yield (spec.get("template") or {}).get("spec") or {}
    elif kind == "Sandbox":
        template = (obj.get("spec") or {}).get("podTemplate") or {}
        yield template.get("spec") or template


def baseline_violations(spec):
    out = []
    if spec.get("hostNetwork"):
        out.append("hostNetwork")
    if spec.get("hostPID"):
        out.append("hostPID")
    if spec.get("hostIPC"):
        out.append("hostIPC")
    for volume in spec.get("volumes") or []:
        if "hostPath" in volume:
            out.append("hostPath:" + str(volume["hostPath"].get("path")))
    for sysctl in (spec.get("securityContext") or {}).get("sysctls") or []:
        if sysctl.get("name") not in SAFE_SYSCTLS:
            out.append("sysctl:" + str(sysctl.get("name")))
    for key in ("containers", "initContainers", "ephemeralContainers"):
        for container in spec.get(key) or []:
            sc = container.get("securityContext") or {}
            name = container.get("name", "?")
            if sc.get("privileged"):
                out.append(name + ":privileged")
            if sc.get("procMount") not in (None, "Default"):
                out.append(name + ":procMount")
            if (sc.get("seccompProfile") or {}).get("type") == "Unconfined":
                out.append(name + ":seccomp=Unconfined")
            for cap in (sc.get("capabilities") or {}).get("add") or []:
                if cap.upper() not in ALLOWED_CAPS:
                    out.append(name + ":cap:" + cap)
    return out


def restricted_violations(spec):
    out = []
    pod_sc = spec.get("securityContext") or {}
    if pod_sc.get("runAsNonRoot") is not True:
        out.append("pod:runAsNonRoot!=true")
    if (pod_sc.get("seccompProfile") or {}).get("type") not in ("RuntimeDefault", "Localhost"):
        out.append("pod:seccomp!=RuntimeDefault")
    for key in ("containers", "initContainers", "ephemeralContainers"):
        for container in spec.get(key) or []:
            sc = container.get("securityContext") or {}
            name = container.get("name", "?")
            if sc.get("allowPrivilegeEscalation") is not False:
                out.append(name + ":allowPrivilegeEscalation!=false")
            if sc.get("runAsNonRoot") is not True and pod_sc.get("runAsNonRoot") is not True:
                out.append(name + ":runAsNonRoot!=true")
            if (sc.get("seccompProfile") or {}).get("type") not in ("RuntimeDefault", "Localhost") \
                    and (pod_sc.get("seccompProfile") or {}).get("type") not in ("RuntimeDefault", "Localhost"):
                out.append(name + ":seccomp!=RuntimeDefault")
            caps = sc.get("capabilities") or {}
            if sorted(str(c).upper() for c in (caps.get("drop") or [])) != ["ALL"]:
                out.append(name + ":drop!=ALL")
    return out


def collect(specs, prefix):
    result = {}
    for ns, name, spec in specs:
        result.setdefault(ns, {"baseline": [], "restricted": []})
        result[ns]["baseline"].extend(f"{prefix}/{name}:{v}" for v in baseline_violations(spec))
        result[ns]["restricted"].extend(f"{prefix}/{name}:{v}" for v in restricted_violations(spec))
    return result


def live_specs():
    raw = subprocess.check_output(["kubectl", "get", "pods", "-A", "-o", "json"])
    for pod in json.loads(raw)["items"]:
        yield pod["metadata"]["namespace"], pod["metadata"]["name"], pod["spec"]


def rendered_specs(base):
    for path in glob.glob(base + "/*/*.yaml"):
        with open(path) as fh:
            docs = list(yaml.safe_load_all(fh))
        for doc in docs:
            if not doc:
                continue
            for spec in pod_specs(doc):
                ns = (doc.get("metadata") or {}).get("namespace") or "cluster"
                name = f"{doc.get('kind')}/{(doc.get('metadata') or {}).get('name')}"
                yield ns, name, spec


def rendered_labels(base):
    result = {}
    for path in glob.glob(base + "/*/Namespace_cluster_*.yaml"):
        with open(path) as fh:
            for doc in yaml.safe_load_all(fh):
                if not doc or doc.get("kind") != "Namespace":
                    continue
                name = (doc.get("metadata") or {}).get("name")
                labels = (doc.get("metadata") or {}).get("labels") or {}
                result[name] = labels.get("pod-security.kubernetes.io/enforce")
    return result


def merge(a, b):
    result = dict(a)
    for ns, value in b.items():
        slot = result.setdefault(ns, {"baseline": [], "restricted": []})
        slot["baseline"].extend(value["baseline"])
        slot["restricted"].extend(value["restricted"])
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base", default="manifests/dist")
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()

    found = merge(collect(list(live_specs()), "live"),
                  collect(list(rendered_specs(args.base)), "dist"))
    labels = rendered_labels(args.base)

    namespaces = sorted(set(found) | set(labels))
    clean, dirty = [], []
    for ns in namespaces:
        (dirty if found.get(ns, {}).get("baseline") else clean).append(ns)

    if not args.check:
        print(f"baseline-clean: {len(clean)}   baseline-dirty: {len(dirty)}")
        print()
        for ns in clean:
            print(f"  baseline    {ns}")
        print()
        for ns in dirty:
            print(f"  privileged  {ns}")
            for item in found[ns]["baseline"][:6]:
                print(f"                {item}")
            if len(found[ns]["baseline"]) > 6:
                print(f"                ... and {len(found[ns]['baseline']) - 6} more")
        return 0

    failures = 0
    managed = [n for n in namespaces if n not in UNMANAGED]
    for ns in managed:
        actual = labels.get(ns)
        if actual is None:
            print(f"FAIL  {ns}: no pod-security enforce label")
            failures += 1
            continue
        if actual not in STRICTNESS:
            print(f"FAIL  {ns}: unknown pod-security level {actual!r}")
            failures += 1
            continue
        actual_rank = STRICTNESS[actual]
        base_ok = not found.get(ns, {}).get("baseline")
        restricted_ok = not found.get(ns, {}).get("restricted")
        if actual_rank == 0:
            if base_ok:
                print(f"FAIL  {ns}: labelled privileged but its workloads satisfy baseline")
                failures += 1
        elif actual_rank == 1:
            if not base_ok:
                print(f"FAIL  {ns}: labelled baseline but workloads violate baseline")
                for item in found[ns]["baseline"][:6]:
                    print(f"        {item}")
                failures += 1
        else:
            if not restricted_ok:
                print(f"FAIL  {ns}: labelled restricted but workloads violate restricted")
                for item in found[ns]["restricted"][:6]:
                    print(f"        {item}")
                failures += 1

    if failures:
        print(f"\naudit-pss: FAIL ({failures} namespace(s))")
        return 1
    print(f"audit-pss: PASS ({len(managed)} managed namespaces)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
