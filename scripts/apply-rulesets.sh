#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

owner="myles-coleman"
apply() {
  local repo="$1" file="$2" existing id
  existing="$(gh api "repos/${owner}/${repo}/rulesets" \
    --jq '.[] | select(.name=="main-protection") | .id' | head -1)"
  if [ -n "$existing" ]; then
    gh api --method PUT "repos/${owner}/${repo}/rulesets/${existing}" --input "$file" >/dev/null
    id="$existing"
  else
    id="$(gh api --method POST "repos/${owner}/${repo}/rulesets" --input "$file" --jq .id)"
  fi
  echo "APPLIED ${repo} ruleset=${id}"
}

verify() {
  local repo="$1" deploy="$2" tmp id
  id="$(gh api "repos/${owner}/${repo}/rulesets" \
    --jq '.[] | select(.name=="main-protection") | .id' | head -1)"
  [ -n "$id" ] || { echo "FAIL ${repo}: main-protection ruleset not found"; exit 1; }
  tmp="$(mktemp)"
  gh api "repos/${owner}/${repo}/rulesets/${id}" > "$tmp"
  python3 - "$repo" "$tmp" "$deploy" <<'PY'
import json
import sys

repo, path, deploy = sys.argv[1], sys.argv[2], sys.argv[3] == "yes"
d = json.load(open(path))
checks = []


def ok(name, condition):
    checks.append((name, bool(condition)))


ok("ruleset is active", d.get("enforcement") == "active")
ok("targets refs/heads/main",
   "refs/heads/main" in d.get("conditions", {}).get("ref_name", {}).get("include", []))

rules = {r.get("type") for r in d.get("rules", [])}
for rule in ("deletion", "non_fast_forward", "pull_request", "required_status_checks"):
    ok(f"rule {rule}", rule in rules)

pr = next((r for r in d.get("rules", []) if r.get("type") == "pull_request"), {})
params = pr.get("parameters", {})
ok("one approving review", params.get("required_approving_review_count") == 1)
ok("dismiss stale reviews on push", params.get("dismiss_stale_reviews_on_push") is True)
ok("require approval of the last push", params.get("require_last_push_approval") is True)

rc = next((r for r in d.get("rules", []) if r.get("type") == "required_status_checks"), {})
contexts = [c.get("context") for c in rc.get("parameters", {}).get("required_status_checks", [])]
if repo == "nixos":
    for context in ("changes", "pin-guard", "closure-diff", "gate"):
        ok(f"required check {context}", context in contexts)
    ok("required build-x86 checks", any(c.startswith("build-x86 (") for c in contexts))
    ok("required build-arm checks", any(c.startswith("build-arm (") for c in contexts))
else:
    ok("required check test", "test" in contexts)

bypass = d.get("bypass_actors", [])
ok("operator is a bypass actor",
   any(b.get("actor_type") == "User" and b.get("bypass_mode") == "always" for b in bypass))
ok("the publisher App is not a bypass actor",
   all(b.get("actor_type") != "Integration" for b in bypass))
if deploy:
    ok("deploy key bypass actor with actor_id null",
       any(b.get("actor_type") == "DeployKey" and b.get("actor_id") is None and
           b.get("bypass_mode") == "always" for b in bypass))

failed = False
for i, (name, condition) in enumerate(checks, 1):
    print(("PASS " if condition else "FAIL ") + f"{i}/{len(checks)} {repo}: {name}")
    failed = failed or not condition
sys.exit(1 if failed else 0)
PY
  rm -f "$tmp"
}

apply nixos scripts/rulesets/nixos-main.json
verify nixos no
apply homelab-cluster scripts/rulesets/homelab-cluster-main.json
verify homelab-cluster yes
echo "OK: rulesets applied and verified"
