#!/usr/bin/env python3
"""Split a multi-document Kubernetes manifest stream into one file per object.

Usage: split-manifests.py <input.yaml> <output-dir>

Filenames follow `<Kind>_<namespace>_<name>.yaml`, which is what makes a rendered
tree reviewable without opening files. The escaping rule is chosen so the mapping
is injective: see ESCAPE below.

Text is copied verbatim, so the output keeps exactly what `kustomize build`
emitted. Parsing is used only to read the identifying fields, never to
re-serialize.

Standard library only: this runs in CI next to the renderer, and adding a YAML
dependency here would make the renderer's output depend on a library version.
"""

import os
import re
import sys

# Objects with a colon in the name exist in this repo (cert-manager and metallb
# RBAC roles such as `metallb:controller`). Writing the colon through to a
# filename works on Linux but makes the tree unclonable on Windows and awkward in
# URLs, so it is escaped.
#
# `_` -> `__` must be applied before `:` -> `_s_`, otherwise an existing `_` and
# this escape sequence would be indistinguishable. The result is injective: no
# escaped segment contains a lone `_` that is not part of `__` or `_s_`, so a
# single `_` can only be the field separator between kind, namespace and name.
ESCAPE = (("_", "__"), (":", "_s_"))


def escape(value: str) -> str:
    for needle, replacement in ESCAPE:
        value = value.replace(needle, replacement)
    return value


TOP_KIND = re.compile(r"^kind:\s*(\S+)\s*$")
META_START = re.compile(r"^metadata:\s*$")
TOP_KEY = re.compile(r"^[A-Za-z_][A-Za-z0-9_.-]*:")
FIELD = re.compile(r"^\s+(name|namespace):\s*(.+?)\s*$")


def identify(document: str):
    """Return (kind, namespace, name) for one YAML document, or None."""
    kind = None
    namespace = None
    name = None
    in_meta = False

    for line in document.split("\n"):
        if kind is None:
            match = TOP_KIND.match(line)
            if match:
                kind = match.group(1).strip("\"'")
            continue

        if META_START.match(line):
            in_meta = True
            continue

        if in_meta:
            if TOP_KEY.match(line):
                # A new top-level key means metadata has ended.
                break
            match = FIELD.match(line)
            if match:
                key, value = match.group(1), match.group(2).strip("\"'")
                if key == "name" and name is None:
                    name = value
                elif key == "namespace" and namespace is None:
                    namespace = value

    if not kind or not name:
        return None
    return kind, namespace, name


def main() -> int:
    if len(sys.argv) != 3:
        print("usage: split-manifests.py <input.yaml> <output-dir>", file=sys.stderr)
        return 2
    src, out_dir = sys.argv[1], sys.argv[2]
    os.makedirs(out_dir, exist_ok=True)

    text = open(src, encoding="utf-8").read()

    # Split on separator lines that start a new document. Content inside a block
    # scalar is indented, so a `---` at column 0 is always a separator.
    documents = []
    current = []
    for line in text.split("\n"):
        if re.match(r"^---\s*$", line):
            if current:
                documents.append("\n".join(current))
            current = []
        else:
            current.append(line)
    if current:
        documents.append("\n".join(current))

    written = 0
    seen = {}
    for document in documents:
        if not document.strip():
            continue
        ids = identify(document)
        if ids is None:
            print(f"split-manifests: could not identify an object in {src}", file=sys.stderr)
            return 1
        kind, namespace, name = ids
        # Cluster-scoped objects have no namespace. The literal sentinel keeps the
        # filename shape uniform; the verifier asserts no namespace is actually
        # named "cluster", so the collapse cannot hide a collision.
        segment = escape(namespace) if namespace else "cluster"
        filename = f"{kind}_{segment}_{escape(name)}.yaml"
        if filename in seen:
            print(
                f"split-manifests: filename collision in {src}: "
                f"{filename} from both {seen[filename]} and {(kind, namespace, name)}",
                file=sys.stderr,
            )
            return 1
        seen[filename] = (kind, namespace, name)

        body = document.rstrip("\n") + "\n"
        with open(os.path.join(out_dir, filename), "w", encoding="utf-8") as handle:
            handle.write(body)
        written += 1

    print(f"split-manifests: wrote {written} object(s) to {out_dir}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
