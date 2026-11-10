#!/usr/bin/env bash
# verify-runbooks.sh — assert the spec 30 Unit 5 operator runbooks exist, have
# the required headings, and are linked into the docs site.
#
# Requirements:
#   FR-58  the agent-mtls CA key and GitHub App private key rotation runbook,
#          including leaf reissue, trust redistribution, and expiry monitoring.
#   FR-59  the recorded answer to "is k3s secret encryption at rest enabled",
#          with the command, the observed result, and — when it is not enabled —
#          the accepted risk and a fixed rotation cadence.
#
# The docs site uses the awesome-pages plugin, so mkdocs nav is intentionally
# NOT required: reachability is proved through the .pages files instead.
#
# Usage:
#   scripts/verify-runbooks.sh
#   scripts/verify-runbooks.sh --help
#
# Exit: 0 only when every assertion passes; non-zero on the first failure.
set -euo pipefail

usage() {
  cat <<'EOF'
verify-runbooks.sh — verify spec 30 Unit 5 operator runbooks (FR-58, FR-59)

Checks:
  * docs/operator-guide/runbooks/agent-mtls-rotation.md exists and contains the
    required headings: CA key rotation, GitHub App key rotation, Leaf reissue,
    Trust redistribution, Expiry monitoring, Threat model.
  * docs/operator-guide/runbooks/agent-secret-encryption.md exists and contains
    Observed result, Command, Accepted risk, an enabled/not-enabled answer, and
    Rotation cadence.
  * Both runbooks are listed in docs/operator-guide/runbooks/.pages and the
    rotation runbook is reachable through the awesome-pages tree.
  * The repository AGENTS.md links agent-mtls-rotation.md.
  * The rotation runbook mentions GitHub App key rotation, the CA key, leaf
    expiry, and a SigNoz query containing a filter.

Options:
  -h, --help   Show this help and exit.

Exit status is 0 only when every assertion passes. The first failure exits
non-zero immediately.
EOF
}

case "${1:-}" in
  -h|--help)
    usage
    exit 0
    ;;
  "")
    ;;
  *)
    echo "verify-runbooks: unknown argument: $1" >&2
    usage >&2
    exit 2
    ;;
esac

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="${VERIFY_RUNBOOKS_ROOT:-$(cd "$SCRIPT_DIR/.." && pwd)}"

RUNBOOKS="$ROOT/docs/operator-guide/runbooks"
ROTATION="$RUNBOOKS/agent-mtls-rotation.md"
ENCRYPTION="$RUNBOOKS/agent-secret-encryption.md"
PAGES="$RUNBOOKS/.pages"

PASS=0
TOTAL=21

pass() { PASS=$((PASS + 1)); printf 'PASS %2d/%d  %s\n' "$PASS" "$TOTAL" "$*"; }
fail() { printf 'FAIL %2d/%d  %s\n' "$((PASS + 1))" "$TOTAL" "$*" >&2; exit 1; }

# has_prefix FILE PREFIX — true when any line starts with PREFIX exactly.
has_prefix() {
  local file="$1" prefix="$2"
  awk -v p="$prefix" 'index($0, p) == 1 { found = 1 } END { exit(found ? 0 : 1) }' "$file"
}

require_heading() {
  local file="$1" label="$2" prefix="$3"
  [ -f "$file" ] || fail "$label: missing file ${file#"$ROOT"/}"
  has_prefix "$file" "$prefix" \
    || fail "$label: missing heading line starting with '$prefix'"
  pass "$label: heading '$prefix'"
}

pages_has() {
  [ -f "$1" ] && grep -qF -- "$2" "$1"
}

# 1. FR-58 — the rotation runbook exists.
[ -f "$ROTATION" ] \
  || fail "rotation runbook missing: docs/operator-guide/runbooks/agent-mtls-rotation.md"
pass "rotation runbook exists"

# 2-7. FR-58 — required rotation headings.
require_heading "$ROTATION" "rotation runbook" "## CA key rotation"
require_heading "$ROTATION" "rotation runbook" "## GitHub App key rotation"
require_heading "$ROTATION" "rotation runbook" "## Leaf reissue"
require_heading "$ROTATION" "rotation runbook" "## Trust redistribution"
require_heading "$ROTATION" "rotation runbook" "## Expiry monitoring"
require_heading "$ROTATION" "rotation runbook" "## Threat model"

# 8. FR-59 — the at-rest record exists.
[ -f "$ENCRYPTION" ] \
  || fail "at-rest record missing: docs/operator-guide/runbooks/agent-secret-encryption.md"
pass "at-rest encryption record exists"

# 9-13. FR-59 — required at-rest headings and the recorded answer.
require_heading "$ENCRYPTION" "at-rest record" "## Observed result"
require_heading "$ENCRYPTION" "at-rest record" "## Command"
require_heading "$ENCRYPTION" "at-rest record" "## Accepted risk"

grep -qi 'enabled' "$ENCRYPTION" \
  || fail "at-rest record: no 'enabled' / 'not enabled' answer"
pass "at-rest record: records the enabled/not-enabled answer"

require_heading "$ENCRYPTION" "at-rest record" "## Rotation cadence"

# 14-15. Both runbooks are listed in the runbooks .pages file.
[ -f "$PAGES" ] \
  || fail "runbooks .pages missing: docs/operator-guide/runbooks/.pages"

grep -qF 'agent-mtls-rotation.md' "$PAGES" \
  || fail ".pages does not list agent-mtls-rotation.md"
pass ".pages lists agent-mtls-rotation.md"

grep -qF 'agent-secret-encryption.md' "$PAGES" \
  || fail ".pages does not list agent-secret-encryption.md"
pass ".pages lists agent-secret-encryption.md"

# 16. Awesome-pages reachability. mkdocs.yaml nav is not required because the
#     site uses the awesome-pages plugin; prove the path instead.
pages_has "$PAGES" 'agent-mtls-rotation.md' \
  && pages_has "$ROOT/docs/operator-guide/.pages" 'runbooks' \
  && pages_has "$ROOT/docs/.pages" 'operator-guide' \
  || fail "agent-mtls-rotation.md is not reachable through the .pages tree (awesome-pages)"
pass "rotation runbook is reachable from the .pages tree (awesome-pages; mkdocs nav not required)"

# 17. The repository AGENTS.md links the runbook.
grep -qF 'agent-mtls-rotation.md' "$ROOT/AGENTS.md" \
  || fail "AGENTS.md does not link agent-mtls-rotation.md"
pass "AGENTS.md links agent-mtls-rotation.md"

# 18. FR-58 — the rotation runbook covers the GitHub App private key rotation.
grep -qi 'github app' "$ROTATION" \
  && grep -qiE 'key rotation|private key' "$ROTATION" \
  || fail "rotation runbook does not describe GitHub App key rotation"
pass "rotation runbook mentions GitHub App key rotation"

# 19. FR-58 — the rotation runbook covers the CA key.
grep -qi 'CA key' "$ROTATION" \
  || fail "rotation runbook does not mention the CA key"
pass "rotation runbook mentions the CA key"

# 20. FR-58 — the rotation runbook covers leaf expiry.
grep -qi 'leaf' "$ROTATION" \
  && grep -qi 'expir' "$ROTATION" \
  || fail "rotation runbook does not mention leaf expiry"
pass "rotation runbook mentions leaf expiry"

# 21. FR-58 — the runbook documents a SigNoz query with a filter.
awk 'tolower($0) ~ /signoz/ && (tolower($0) ~ /filter/ || index($0, "|") > 0) { found = 1 }
     END { exit(found ? 0 : 1) }' "$ROTATION" \
  || fail "rotation runbook has no line containing a SigNoz query with a filter"
pass "rotation runbook documents a SigNoz query with a filter"

[ "$PASS" -eq "$TOTAL" ] \
  || { echo "verify-runbooks: internal error: ran $PASS of $TOTAL checks" >&2; exit 1; }

printf '\nAll %d checks passed.\n' "$TOTAL"
