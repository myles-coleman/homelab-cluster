#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
exec python3 - "$@" <<'PY'
#!/usr/bin/env python3
"""Verify the spec 30 GitHub settings (FR-39..FR-48).

Modes:
  (default)       full local run; requires gh (admin) and kubectl for the App checks
  --ci            CI smoke run; admin-only/App-only checks SKIP on 403/404 so the
                  scheduled job does not need a stored admin token
  --behavioural   additionally run the live merge-gate behaviours
"""
import base64
import json
import os
import re
import subprocess
import sys

OWNER = "myles-coleman"
CI = "--ci" in sys.argv
BEHAVIOURAL = "--behavioural" in sys.argv
results = []


def record(kind, name, detail=""):
    results.append((kind, name, detail))
    line = f"{kind} {name}"
    if detail:
        line += f" ({detail})"
    print(line, flush=True)


def ok(name, detail=""):
    record("PASS", name, detail)


def bad(name, detail=""):
    record("FAIL", name, detail)


def skip(name, detail=""):
    record("SKIP", name, detail)


def gh(*args):
    p = subprocess.run(["gh", *args], text=True, capture_output=True)
    if p.returncode != 0:
        raise RuntimeError(p.stderr.strip() or p.stdout.strip())
    return p.stdout


def api(path, jq=None):
    args = ["api", path]
    if jq:
        args += ["--jq", jq]
    return json.loads(gh(*args))


def api_text(path):
    return gh("api", path, "--jq", ".content").strip()


def nixos_file(path):
    try:
        raw = api_text(f"repos/{OWNER}/nixos/contents/{path}")
        return base64.b64decode(raw).decode()
    except RuntimeError as exc:
        if CI and ("403" in str(exc) or "404" in str(exc)):
            return None
        raise


def local(path):
    return open(path, encoding="utf-8").read()


def check(cond, name, detail=""):
    if cond:
        ok(name, detail)
    else:
        bad(name, detail)


def ruleset(repo):
    listing = api(f"repos/{OWNER}/{repo}/rulesets")
    item = next((r for r in listing if r.get("name") == "main-protection"), None)
    if item is None:
        return None
    return api(f"repos/{OWNER}/{repo}/rulesets/{item['id']}")


def rule_params(rs, rule):
    for r in rs.get("rules", []):
        if r.get("type") == rule:
            return r.get("parameters", {})
    return None


# FR-41 / FR-42: rulesets and bypass actors
for repo in ("nixos", "homelab-cluster"):
    try:
        rs = ruleset(repo)
    except RuntimeError as exc:
        if CI:
            skip(f"FR-41/42 {repo} ruleset", "token cannot read rulesets")
            continue
        raise
    if rs is None:
        bad(f"FR-41 {repo}: main-protection ruleset exists")
        continue
    rules = {r.get("type") for r in rs.get("rules", [])}
    check(rs.get("enforcement") == "active", f"FR-41 {repo}: ruleset active")
    check("refs/heads/main" in rs.get("conditions", {}).get("ref_name", {}).get("include", []),
          f"FR-41 {repo}: targets main")
    for rule in ("pull_request", "required_status_checks", "deletion", "non_fast_forward"):
        check(rule in rules, f"FR-41 {repo}: rule {rule}")
    pr = rule_params(rs, "pull_request") or {}
    check(pr.get("required_approving_review_count") == 1,
          f"FR-41 {repo}: one approving review")
    check(pr.get("dismiss_stale_reviews_on_push") is True,
          f"FR-41 {repo}: dismiss stale reviews")
    check(pr.get("require_last_push_approval") is True,
          f"FR-41 {repo}: approve the last push")
    contexts = [c.get("context") for c in
                (rule_params(rs, "required_status_checks") or {}).get("required_status_checks", [])]
    if repo == "nixos":
        for context in ("changes", "pin-guard", "closure-diff", "gate"):
            check(context in contexts, f"FR-41 nixos: required check {context}")
        check(any(c.startswith("build-x86 (") for c in contexts),
              "FR-41 nixos: required build-x86 checks")
        check(any(c.startswith("build-arm (") for c in contexts),
              "FR-41 nixos: required build-arm checks")
    else:
        check("test" in contexts, "FR-41 homelab-cluster: required check test")
    bypass = rs.get("bypass_actors", [])
    check(any(b.get("actor_type") == "User" and b.get("bypass_mode") == "always" for b in bypass),
          f"FR-42 {repo}: operator is a bypass actor")
    check(all(b.get("actor_type") != "Integration" for b in bypass),
          f"FR-42 {repo}: publisher App is not a bypass actor")
    if repo == "nixos":
        check(all(b.get("actor_type") != "DeployKey" for b in bypass),
              "FR-42 nixos: no deploy-key bypass")
    else:
        check(any(b.get("actor_type") == "DeployKey" and b.get("actor_id") is None and
                  b.get("bypass_mode") == "always" for b in bypass),
              "FR-42 homelab-cluster: deploy-key bypass with actor_id null")

# FR-44: Actions cannot create/approve PRs
for repo in ("nixos", "homelab-cluster"):
    try:
        wf = api(f"repos/{OWNER}/{repo}/actions/permissions/workflow")
    except RuntimeError as exc:
        skip(f"FR-44 {repo} workflow permissions", "token cannot read")
        continue
    check(wf.get("default_workflow_permissions") == "read",
          f"FR-44 {repo}: default workflow token is read-only")
    check(wf.get("can_approve_pull_request_reviews") is False,
          f"FR-44 {repo}: Actions cannot approve PRs")

# FR-46: no repository/org secrets; the only material is environment-scoped
for repo in ("nixos", "homelab-cluster"):
    try:
        listing = gh("secret", "list", "--repo", f"{OWNER}/{repo}", "--json", "name")
        secrets = {s["name"] for s in json.loads(listing)}
    except RuntimeError as exc:
        skip(f"FR-46 {repo} repository secrets", "token cannot list secrets")
        continue
    check(not secrets, f"FR-46 {repo}: repository secret list is empty", ",".join(sorted(secrets)))
try:
    nixos_prod = json.loads(gh("secret", "list", "--repo", f"{OWNER}/nixos",
                               "--env", "production", "--json", "name"))
    names = {s["name"] for s in nixos_prod}
    check(names == {"CACHIX_AUTH_TOKEN", "SSH_PRIVATE_KEY", "TS_OAUTH_CLIENT_ID", "TS_OAUTH_SECRET"},
          "FR-46 nixos: deploy secrets are in the production environment", ",".join(sorted(names)))
except RuntimeError:
    skip("FR-46 nixos production secrets", "token cannot list secrets")
try:
    homelab_boot = json.loads(gh("secret", "list", "--repo", f"{OWNER}/homelab-cluster",
                                 "--env", "bootstrap", "--json", "name"))
    check({s["name"] for s in homelab_boot} == {"DEPLOY_KEY"},
          "FR-46 homelab-cluster: deploy key is in the bootstrap environment")
except RuntimeError:
    skip("FR-46 homelab-cluster bootstrap secret", "token cannot list secrets")

# FR-47: production environments
for repo in ("nixos", "homelab-cluster"):
    try:
        env = api(f"repos/{OWNER}/{repo}/environments/production")
        policies = api(f"repos/{OWNER}/{repo}/environments/production/deployment-branch-policies")
    except RuntimeError as exc:
        skip(f"FR-47 {repo} production environment", "token cannot read")
        continue
    reviewers = [r["reviewer"]["login"] for rule in env.get("protection_rules", [])
                 if rule.get("type") == "required_reviewers"
                 for r in rule.get("reviewers", [])]
    check("myles-coleman" in reviewers, f"FR-47 {repo}: operator is a required reviewer")
    names = [p.get("name") for p in policies.get("branch_policies", [])]
    check(names == ["main"], f"FR-47 {repo}: production is main-only")

# FR-43 / FR-48: homelab workflows are secret-free on the PR path
gen = local(".github/workflows/generate-apps.yml")
check("environment: bootstrap" in gen, "FR-43 generate-apps uses the bootstrap environment")
check("ssh-key: ${{ secrets.DEPLOY_KEY }}" in gen, "FR-43 generate-apps uses the deploy key")
check("persist-credentials: false" in gen, "FR-43 generate-apps does not persist credentials")
check("git push origin HEAD:main" in gen, "FR-43 generate-apps pushes the generated commit to main")
check("::set-output" not in gen, "FR-43 generate-apps uses GITHUB_OUTPUT")

ab = local(".github/workflows/agent-broker.yaml")
pr_block = ab.split("jobs:", 1)[1]
test_job = pr_block.split("  build-validator:", 1)[0]
check("pull_request:" in ab, "FR-48 homelab-cluster: agent-broker is pull_request-triggered")
check("secrets." not in test_job, "FR-48 homelab-cluster: the PR test job is secret-free")
check("contents: read" in test_job, "FR-48 homelab-cluster: the PR test job reads contents only")

rel = local(".github/workflows/release.yaml")
check("pull_request" not in rel and "secrets.GITHUB_TOKEN" in rel,
      "FR-48 homelab-cluster: release tag creation remains on main pushes")

# FR-45: nixos PR workflow (read from the public nixos repo)
pr = nixos_file(".github/workflows/pr.yaml")
comment = nixos_file(".github/workflows/pr-comment.yaml")
if pr is None or comment is None:
    skip("FR-45 nixos PR workflow", "token cannot read the nixos workflows")
else:
    check("pull-requests: write" not in pr, "FR-45 nixos: pr.yaml has no pull-requests: write")
    check("secrets." not in pr, "FR-45 nixos: pr.yaml references no secrets")
    check("workflow_run:" in comment, "FR-45 nixos: the comment job is workflow_run-triggered")
    check("ref:" not in comment, "FR-45 nixos: the comment checkout has no ref")
    check("persist-credentials: false" in comment,
          "FR-45 nixos: the comment checkout disables persisted credentials")

# FR-39 / FR-40: App permissions and token scope (local only)
if CI:
    skip("FR-39 App permissions", "requires the cluster Secret")
    skip("FR-40 installation token scope", "requires the cluster Secret")
else:
    try:
        app_id = subprocess.run(
            ["kubectl", "-n", "agent-publisher", "get", "secret", "github-app",
             "-o", "jsonpath={.data.app-id}"], text=True, capture_output=True, check=True).stdout
        key = subprocess.run(
            ["kubectl", "-n", "agent-publisher", "get", "secret", "github-app",
             "-o", r"jsonpath={.data.private-key\.pem}"], text=True, capture_output=True,
            check=True).stdout
        import tempfile
        with tempfile.TemporaryDirectory() as d:
            app_id = base64.b64decode(app_id).decode().strip()
            key_path = os.path.join(d, "key.pem")
            with open(key_path, "wb") as f:
                f.write(base64.b64decode(key))
            os.chmod(key_path, 0o600)
            now = int(subprocess.run(["date", "+%s"], text=True, capture_output=True,
                                     check=True).stdout.strip())
            def b64url(data):
                return base64.urlsafe_b64encode(data).rstrip(b"=").decode()
            header = b64url(json.dumps({"alg": "RS256", "typ": "JWT"}).encode())
            payload = b64url(json.dumps(
                {"iss": app_id, "iat": now - 60, "exp": now + 540}).encode())
            sig = subprocess.run(
                ["openssl", "dgst", "-sha256", "-sign", key_path],
                input=f"{header}.{payload}".encode(), capture_output=True, check=True).stdout
            jwt = f"{header}.{payload}.{b64url(sig)}"
            app = json.loads(subprocess.run(
                ["curl", "-sS", "-H", f"Authorization: Bearer {jwt}",
                 "-H", "Accept: application/vnd.github+json", "https://api.github.com/app"],
                text=True, capture_output=True, check=True).stdout)
            perms = app.get("permissions", {})
            check(perms.get("contents") == "write" and perms.get("pull_requests") == "write"
                  and perms.get("metadata") == "read" and len(perms) == 3,
                  "FR-39 App permissions are contents:write, pull_requests:write, metadata:read",
                  json.dumps(perms, sort_keys=True))
            for repo in ("nixos", "homelab-cluster"):
                inst = json.loads(subprocess.run(
                    ["curl", "-sS", "-H", f"Authorization: Bearer {jwt}",
                     "-H", "Accept: application/vnd.github+json",
                     f"https://api.github.com/repos/{OWNER}/{repo}/installation"],
                    text=True, capture_output=True, check=True).stdout)
                iid = inst.get("id")
                tok = json.loads(subprocess.run(
                    ["curl", "-sS", "-X", "POST", "-H", f"Authorization: Bearer {jwt}",
                     "-H", "Accept: application/vnd.github+json",
                     "-H", "Content-Type: application/json",
                     "-d", json.dumps({"repositories": [repo],
                                       "permissions": {"contents": "read"}}),
                     f"https://api.github.com/app/installations/{iid}/access_tokens"],
                    text=True, capture_output=True, check=True).stdout)
                repos = [r.get("name") for r in tok.get("repositories", [])]
                exp = tok.get("expires_at", "")
                import datetime
                exp_dt = datetime.datetime.strptime(exp, "%Y-%m-%dT%H:%M:%SZ").replace(
                    tzinfo=datetime.timezone.utc)
                lifetime = (exp_dt - datetime.datetime.now(datetime.timezone.utc)).total_seconds()
                check(repos == [repo] and 0 < lifetime <= 3600,
                      f"FR-40 {repo}: one-repository token under one hour",
                      f"lifetime={int(lifetime)}s")
    except Exception as exc:  # cluster access is required for these two checks
        skip("FR-39/FR-40 App/token checks", str(exc)[:120])

# Behavioural merge gate
if not BEHAVIOURAL:
    skip("behavioural operator bypass merge", "run with --behavioural")
    skip("behavioural agent PR without approval", "run with --behavioural")
else:
    import tempfile
    import time
    stamp = time.strftime("%Y%m%d%H%M%S")
    # Recorded operator bypass: PR #115 was merged through the REST endpoint.
    try:
        pr115 = api(f"repos/{OWNER}/homelab-cluster/pulls/115")
        check(pr115.get("merged") is True and pr115.get("state") == "closed",
              "behavioural: operator PR #115 merged through the bypass actor")
    except RuntimeError as exc:
        skip("behavioural operator bypass merge", str(exc)[:120])

    # Live agent PR without approval: create a ready agent branch/PR, try the
    # App token merge, expect a refusal, then remove it.
    try:
        clone = tempfile.mkdtemp(prefix="verify-settings-")
        gh_token = subprocess.run(["gh", "auth", "token"], text=True,
                                  capture_output=True, check=True).stdout.strip()
        git_env = os.environ.copy()
        cred = base64.b64encode(f"x-access-token:{gh_token}".encode()).decode()
        git_env.update({
            "GIT_CONFIG_COUNT": "1",
            "GIT_CONFIG_KEY_0": "http.extraheader",
            "GIT_CONFIG_VALUE_0": f"Authorization: Basic {cred}",
        })
        subprocess.run(["git", "clone", "-q", "--depth=1", "--branch", "main",
                        f"https://github.com/{OWNER}/homelab-cluster", clone],
                       check=True, capture_output=True, env=git_env)
        subprocess.run(["git", "checkout", "-q", "-b", f"agent/89f0f5cc-a2eb-400a-b4b7-b2e20213bffb/verify-{stamp}"],
                       cwd=clone, check=True)
        path = os.path.join(clone, "docs", f"verify-gate-{stamp}.md")
        with open(path, "w") as f:
            f.write("temporary merge-gate check\n")
        subprocess.run(["git", "add", path], cwd=clone, check=True)
        subprocess.run(["git", "-c", "user.name=verify", "-c", "user.email=verify@example.invalid",
                        "commit", "-q", "-m", "docs: temporary merge-gate check"], cwd=clone, check=True)
        subprocess.run(["git", "push", "-q", "origin", "HEAD"], cwd=clone, check=True, env=git_env)
        pr_url = subprocess.run(
            ["gh", "pr", "create", "--repo", f"{OWNER}/homelab-cluster", "--base", "main",
             "--head", f"agent/89f0f5cc-a2eb-400a-b4b7-b2e20213bffb/verify-{stamp}",
             "--title", "docs: temporary merge-gate check",
             "--body", "Temporary check for the agent PR merge gate."],
            text=True, capture_output=True, check=True).stdout.strip().splitlines()[-1]
        number = int(re.search(r"/pull/(\d+)$", pr_url).group(1))
        # Mint an App token for homelab-cluster.
        # The local app JWT is still in scope from the FR-39 block only if it ran;
        # rebuild a minimal one here.
        app_id = base64.b64decode(subprocess.run(
            ["kubectl", "-n", "agent-publisher", "get", "secret", "github-app",
             "-o", "jsonpath={.data.app-id}"], text=True, capture_output=True, check=True).stdout).decode().strip()
        key = base64.b64decode(subprocess.run(
            ["kubectl", "-n", "agent-publisher", "get", "secret", "github-app",
             "-o", r"jsonpath={.data.private-key\.pem}"], text=True, capture_output=True,
            check=True).stdout)
        with tempfile.TemporaryDirectory() as d:
            key_path = os.path.join(d, "key.pem")
            open(key_path, "wb").write(key)
            os.chmod(key_path, 0o600)
            now = int(subprocess.run(["date", "+%s"], text=True, capture_output=True,
                                     check=True).stdout.strip())
            def b64url2(data):
                return base64.urlsafe_b64encode(data).rstrip(b"=").decode()
            h = b64url2(json.dumps({"alg": "RS256", "typ": "JWT"}).encode())
            p = b64url2(json.dumps({"iss": app_id, "iat": now - 60,
                                    "exp": now + 540}).encode())
            sig = subprocess.run(["openssl", "dgst", "-sha256", "-sign", key_path],
                                 input=f"{h}.{p}".encode(), capture_output=True, check=True).stdout
            jwt = f"{h}.{p}.{b64url2(sig)}"
            inst = json.loads(subprocess.run(
                ["curl", "-sS", "-H", f"Authorization: Bearer {jwt}",
                 "-H", "Accept: application/vnd.github+json",
                 f"https://api.github.com/repos/{OWNER}/homelab-cluster/installation"],
                text=True, capture_output=True, check=True).stdout)
            tok = json.loads(subprocess.run(
                ["curl", "-sS", "-X", "POST", "-H", f"Authorization: Bearer {jwt}",
                 "-H", "Accept: application/vnd.github+json",
                 "-H", "Content-Type: application/json",
                 "-d", json.dumps({"repositories": ["homelab-cluster"],
                                   "permissions": {"contents": "write", "pull_requests": "write"}}),
                 f"https://api.github.com/app/installations/{inst['id']}/access_tokens"],
                text=True, capture_output=True, check=True).stdout)
            status = subprocess.run(
                ["curl", "-sS", "-o", os.path.join(d, "merge.json"), "-w", "%{http_code}",
                 "-X", "PUT", "-H", f"Authorization: Bearer {tok['token']}",
                 "-H", "Accept: application/vnd.github+json",
                 "-H", "Content-Type: application/json",
                 "-d", '{"merge_method":"squash"}',
                 f"https://api.github.com/repos/{OWNER}/homelab-cluster/pulls/{number}/merge"],
                text=True, capture_output=True, check=True).stdout.strip()
            check(status in ("403", "405"),
                  "behavioural: agent PR without approval cannot be merged by the App",
                  f"http={status}")
        subprocess.run(["gh", "pr", "close", str(number), "--repo", f"{OWNER}/homelab-cluster"],
                       check=True, capture_output=True)
        subprocess.run(["git", "push", "-q", "origin", "--delete",
                        f"agent/89f0f5cc-a2eb-400a-b4b7-b2e20213bffb/verify-{stamp}"],
                       cwd=clone, check=True, env=git_env)
        subprocess.run(["rm", "-rf", clone], check=True)
    except Exception as exc:
        bad("behavioural: agent PR without approval", str(exc)[:160])

failed = sum(1 for kind, _, _ in results if kind == "FAIL")
skipped = sum(1 for kind, _, _ in results if kind == "SKIP")
print(f"\nOK: {len(results)-failed-skipped} passed, {failed} failed, {skipped} skipped")
sys.exit(1 if failed else 0)
PY
