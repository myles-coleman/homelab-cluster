# NetworkPolicy components

Four Kustomize Components a service can opt into with:

```yaml
components:
  - ../_netpols/_baseline
```

The parent service **must** set `namespace: <its namespace>`. These policies
carry no `metadata.namespace` of their own; the parent's namespace transformer
supplies it. That is what keeps one component usable by every service instead of
copying the same three policies 39 times.

| Component | Posture |
|---|---|
| `_strict` | Default-deny ingress and egress. Add explicit allows in the service. |
| `_baseline` | Default-deny, then allow in-namespace, Traefik ingress, and monitoring (SigNoz) ingress. **All egress is allowed, including DNS.** |
| `_permissive` | Default-deny, then allow ingress from anywhere in the cluster. All egress allowed. |
| `_standalone` | Only an egress allow to the API server. Combine with `_strict`; it is not a posture on its own. |

## Why `_baseline` allows all egress

A default-deny egress set without a DNS allowance takes the cluster down: no
service can resolve a name. `_baseline` therefore allows all egress, and a
service that needs a narrower egress set uses `_strict` and writes its own
allows. `agent-sandbox`, `agent-publisher` and `agent-validator` are deliberately
**not** migrated to these components: their hand-written policies encode
security decisions a generic baseline would undo (the sandbox drops DNS as an
exfiltration control, the validator has no resolver at all, and the publisher
allows egress only to GitHub). See `docs/architecture/` for the rationale.
