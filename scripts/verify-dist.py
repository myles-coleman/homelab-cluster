#!/usr/bin/env python3
"""Verify the rendered dist/ tree against its source manifests.

Four invariants, all blocking:

  1. every image reference in dist/ is digest-pinned, except exact values listed
     in scripts/dist-allowlist.txt;
  2. for every active service, the object set of dist/<svc> equals the object set
     of `kustomize build manifests/cluster/<svc>`;
  3. every object body matches between the two renders, modulo image digests;
  4. every Namespace, PersistentVolumeClaim and PersistentVolume carries
     `argocd.argoproj.io/sync-options: Prune=false`.

Usage:
  verify-dist.py                 run all checks, exit 1 on failure
  verify-dist.py --report        print a per-service table as well
  verify-dist.py --check-sentinel  only assert no namespace is named "cluster"
  verify-dist.py --check-protected only assert Prune=false on Namespace/PVC/PV

Requires PyYAML at $PYAML_VERSION (see scripts/tools.env). No registry access,
no credentials, and nothing is written.
"""

import os
import re
import subprocess
import sys

try:
    import yaml
except ImportError:
    print("verify-dist: PyYAML is required (see scripts/tools.env)", file=sys.stderr)
    raise SystemExit(2)

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CLUSTER_KUSTOMIZATION = os.path.join(ROOT, "manifests", "cluster", "kustomization.yaml")
DIST = os.path.join(ROOT, "manifests", "dist")
ALLOWLIST = os.path.join(ROOT, "scripts", "dist-allowlist.txt")

PINNED = re.compile(r"@sha256:[0-9a-f]{64}$")
DIGEST = re.compile(r"@sha256:[0-9a-f]{64}")
PRUNE_ANNOTATION = "argocd.argoproj.io/sync-options"
PRUNE_VALUE = "Prune=false"
PROTECTED_KINDS = {"Namespace", "PersistentVolumeClaim", "PersistentVolume"}

# Growth of the allowlist is a policy decision, not a side effect of fixing one
# chart. Raising this bound is a reviewed edit that says so.
MAX_ALLOWLIST = 3

# Registry-prefixed strings that are not in an `image:` key. Longhorn passes
# `--engine-image` and `--instance-manager-image` to longhorn-manager as args, so
# a scan of `image:` keys alone reports green while those images float.
REGISTRY_PREFIX = re.compile(
    r"^(?:docker\.io|quay\.io|ghcr\.io|lscr\.io|gcr\.io|registry\.k8s\.io)/"
)


def load_allowlist():
    """Return {reference: reason}; a reason is mandatory."""
    if not os.path.exists(ALLOWLIST):
        return {}
    entries = {}
    reason = None
    for line in open(ALLOWLIST, encoding="utf-8"):
        stripped = line.strip()
        if not stripped:
            reason = None
            continue
        if stripped.startswith("#"):
            reason = stripped.lstrip("#").strip()
            continue
        entries[stripped] = reason or "NO REASON GIVEN"
        reason = None
    return entries


def active_services():
    services = []
    for line in open(CLUSTER_KUSTOMIZATION, encoding="utf-8"):
        match = re.match(r"^-\s+(\S+)\s*$", line)
        if match:
            services.append(match.group(1))
    return services


def parse(stream):
    documents = [doc for doc in yaml.safe_load_all(stream) if doc]
    return documents


def source_render(service):
    result = subprocess.run(
        ["kustomize", "build", os.path.join(ROOT, "manifests", "cluster", service)],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        raise RuntimeError(f"kustomize build failed for {service}: {result.stderr.strip()}")
    return parse(result.stdout)


def dist_render(service):
    directory = os.path.join(DIST, service)
    result = subprocess.run(
        ["kustomize", "build", directory], capture_output=True, text=True
    )
    if result.returncode != 0:
        raise RuntimeError(f"kustomize build failed for dist/{service}: {result.stderr.strip()}")
    return parse(result.stdout)


def object_id(obj):
    api = obj.get("apiVersion", "")
    group = api.split("/")[0] if "/" in api else ""
    metadata = obj.get("metadata") or {}
    return (group, obj.get("kind"), metadata.get("namespace"), metadata.get("name"))


def walk_images(obj, trail=""):
    """Yield (path, value) for every `image` key with a string value."""
    if isinstance(obj, dict):
        for key, value in obj.items():
            path = f"{trail}.{key}" if trail else key
            if key == "image" and isinstance(value, str):
                yield path, value
            else:
                yield from walk_images(value, path)
    elif isinstance(obj, list):
        for index, value in enumerate(obj):
            yield from walk_images(value, f"{trail}[{index}]")


def walk_registry_strings(obj, trail=""):
    """Yield registry-prefixed strings outside `image` keys (args, command, env)."""
    if isinstance(obj, dict):
        for key, value in obj.items():
            path = f"{trail}.{key}" if trail else key
            if key == "image":
                continue
            if isinstance(value, str) and REGISTRY_PREFIX.match(value):
                yield path, value
            else:
                yield from walk_registry_strings(value, path)
    elif isinstance(obj, list):
        for index, value in enumerate(obj):
            yield from walk_registry_strings(value, f"{trail}[{index}]")


def mask(value):
    """Replace digest suffixes so two renders can be compared for identity."""
    if isinstance(value, str):
        return DIGEST.sub("@sha256:MASKED", value)
    if isinstance(value, dict):
        return {key: mask(item) for key, item in value.items()}
    if isinstance(value, list):
        return [mask(item) for item in value]
    return value


def main(argv):
    check_sentinel = "--check-sentinel" in argv
    report = "--report" in argv

    failures = []
    allow = load_allowlist()

    if len(allow) > MAX_ALLOWLIST:
        failures.append(
            f"allowlist has {len(allow)} entries, above MAX_ALLOWLIST={MAX_ALLOWLIST}"
        )
    for reference, reason in allow.items():
        if reason == "NO REASON GIVEN":
            failures.append(f"allowlist entry has no reason: {reference}")

    if "--check-protected" in argv:
        # A separate mode so the workflow step that fails names the property that
        # broke. Bundling it into the main report would make "unpinned image" and
        # "prunable namespace" arrive as one undifferentiated failure.
        unprotected = []
        for service_dir in sorted(os.listdir(DIST)):
            directory = os.path.join(DIST, service_dir)
            if not os.path.isdir(directory):
                continue
            for name in sorted(os.listdir(directory)):
                if not name.endswith(".yaml"):
                    continue
                for obj in parse(open(os.path.join(directory, name), encoding="utf-8")):
                    if obj.get("kind") not in PROTECTED_KINDS:
                        continue
                    annotations = (obj.get("metadata") or {}).get("annotations") or {}
                    if annotations.get(PRUNE_ANNOTATION) != PRUNE_VALUE:
                        unprotected.append(f"{name} ({object_id(obj)})")
        if unprotected:
            print(f"verify-dist: {len(unprotected)} protected-kind object(s) are prunable:", file=sys.stderr)
            for entry in unprotected[:10]:
                print(f"  - {entry}", file=sys.stderr)
            return 1
        print("verify-dist: protected-object check PASSED")
        return 0

    # `manifests/dist` sits beside `manifests/cluster`, so a service could in
    # principle list the rendered tree as a resource: either a cycle, or generated
    # output laundered back into authored input. Fail loudly instead.
    for root, _, files in os.walk(os.path.join(ROOT, "manifests", "cluster")):
        for name in files:
            if not name.endswith(("kustomization.yaml", "kustomization.yml")):
                continue
            path = os.path.join(root, name)
            for lineno, line in enumerate(open(path, encoding="utf-8"), 1):
                stripped = line.strip()
                if stripped.startswith("#") or "dist" not in stripped:
                    continue
                if stripped.startswith("- ") or stripped.startswith("path:"):
                    failures.append(
                        f"{os.path.relpath(path, ROOT)}:{lineno} references the rendered "
                        f"tree: {stripped!r}"
                    )

    services = active_services()
    if not services:
        failures.append("no services found in manifests/cluster/kustomization.yaml")

    # The `cluster` sentinel collapses "no namespace" with "namespace literally
    # named cluster". Assert the second cannot happen, or the filename scheme
    # would not be injective.
    namespaces = set()
    rows = []
    for service in services:
        try:
            source = source_render(service)
            rendered = dist_render(service)
        except RuntimeError as error:
            failures.append(str(error))
            continue

        for obj in source:
            metadata = obj.get("metadata") or {}
            if obj.get("kind") == "Namespace" and metadata.get("name"):
                namespaces.add(metadata["name"])

        source_ids = {object_id(obj) for obj in source}
        dist_ids = {object_id(obj) for obj in rendered}
        missing = source_ids - dist_ids
        extra = dist_ids - source_ids
        if missing:
            failures.append(
                f"{service}: {len(missing)} object(s) missing from dist/, e.g. {sorted(missing)[:3]}"
            )
        if extra:
            failures.append(
                f"{service}: {len(extra)} object(s) in dist/ absent from source, e.g. {sorted(extra)[:3]}"
            )

        source_by_id = {object_id(obj): obj for obj in source}
        rendered_by_id = {object_id(obj): obj for obj in rendered}
        body_mismatch = sum(
            1
            for identity in source_ids & dist_ids
            if mask(source_by_id[identity]) != mask(rendered_by_id[identity])
        )
        if body_mismatch:
            failures.append(
                f"{service}: {body_mismatch} object(s) differ beyond image digests"
            )

        unpinned = []
        for obj in rendered:
            for path, value in walk_images(obj):
                if not PINNED.search(value) and value not in allow:
                    unpinned.append(f"{object_id(obj)} {path}={value}")
            for path, value in walk_registry_strings(obj):
                if not PINNED.search(value) and value not in allow:
                    unpinned.append(f"{object_id(obj)} {path}={value}")
        if unpinned:
            for entry in unpinned[:5]:
                failures.append(f"{service}: unpinned image reference: {entry}")
            if len(unpinned) > 5:
                failures.append(f"{service}: and {len(unpinned) - 5} more unpinned references")

        # Structural sanity for Deployments. There is no schema validator in this
        # pipeline, so an invalid-but-well-formed manifest passes every other
        # check: the source and dist renders agree, the images are pinned, and the
        # object set matches. This caught exactly that after a misplaced
        # `strategy:` block produced `spec.strategy.matchLabels` and a null
        # selector, which ArgoCD reports as ComparisonError on every sync.
        malformed = []
        for obj in rendered:
            if obj.get("kind") != "Deployment":
                continue
            spec = obj.get("spec") or {}
            selector = spec.get("selector") or {}
            strategy = spec.get("strategy") or {}
            if not (selector.get("matchLabels") or selector.get("matchExpressions")):
                malformed.append(f"{object_id(obj)} has no spec.selector.matchLabels or matchExpressions")
            unexpected = set(strategy) - {"type", "rollingUpdate"}
            if unexpected:
                malformed.append(f"{object_id(obj)} has unexpected spec.strategy key(s): {sorted(unexpected)}")
            rolling = strategy.get("rollingUpdate") or {}
            unexpected = set(rolling) - {"maxSurge", "maxUnavailable"}
            if unexpected:
                malformed.append(f"{object_id(obj)} has unexpected spec.strategy.rollingUpdate key(s): {sorted(unexpected)}")
        if malformed:
            for entry in malformed[:5]:
                failures.append(f"{service}: malformed Deployment: {entry}")

        unprotected = []
        for obj in rendered:
            if obj.get("kind") not in PROTECTED_KINDS:
                continue
            annotations = (obj.get("metadata") or {}).get("annotations") or {}
            if annotations.get(PRUNE_ANNOTATION) != PRUNE_VALUE:
                unprotected.append(object_id(obj))
        if unprotected:
            failures.append(
                f"{service}: {len(unprotected)} protected-kind object(s) lack {PRUNE_ANNOTATION}: "
                f"{unprotected[:3]}"
            )

        rows.append((service, len(source), len(rendered), not missing and not extra, body_mismatch == 0))

    # The sentinel check answers one question, so it must not inherit failures
    # from the invariants above: a report of "sentinel FAILED" caused by an
    # unpinned image would send a reader looking in the wrong place.
    sentinel_collision = "cluster" in namespaces
    if check_sentinel:
        print(
            "verify-dist: sentinel check",
            "FAILED (a namespace is named 'cluster')" if sentinel_collision else "PASSED",
        )
        return 1 if sentinel_collision else 0

    if "cluster" in namespaces:
        failures.append("a namespace is literally named 'cluster', which breaks the filename sentinel")

    if report:
        print(f"{'service':26s} {'src':>4s} {'dist':>5s} {'set':>5s} {'body':>5s}")
        for service, source_count, dist_count, set_ok, body_ok in rows:
            print(
                f"{service:26s} {source_count:4d} {dist_count:5d} "
                f"{'PASS' if set_ok else 'FAIL':>5s} {'PASS' if body_ok else 'FAIL':>5s}"
            )

    if failures:
        print(f"\nverify-dist: {len(failures)} failure(s)\n", file=sys.stderr)
        for failure in failures:
            print(f"  - {failure}", file=sys.stderr)
        return 1

    print(f"verify-dist: PASS ({len(services)} services, {len(allow)} allowlisted reference(s))")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
