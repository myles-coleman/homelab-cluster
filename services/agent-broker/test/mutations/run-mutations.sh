#!/usr/bin/env bash
#
# Mutation proof for spec 30 Unit 5 (FR-51, FR-52, FR-53/54, FR-58, FR-59).
#
# Each mutation is applied to a disposable copy of HEAD, the target test is run,
# and the copy is restored. "caught" means the test exited non-zero. The script
# exits non-zero if any mutation is missed, so a reviewer can re-run it instead
# of trusting the proof prose.
#
# Usage: ./test/mutations/run-mutations.sh [--list]
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../../../.." && pwd)"
GO_ROOT="$(nix build --no-link --print-out-paths nixpkgs#go)"
GO_BIN="$GO_ROOT/bin/go"
export PATH="$(dirname "$GO_BIN"):$PATH"
export CGO_ENABLED=0

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

H="$WORK/repo"
mkdir -p "$H"
git -C "$REPO" archive HEAD | tar -x -C "$H"
cp -a "$H" "$WORK/pristine"

caught=0
missed=0
rows=()

restore() { rsync -a --delete "$WORK/pristine/" "$H/"; }

run() {
  local id="$1" desc="$2" snippet="$3" cmd="$4"
  restore
  python3 - "$H" <<PY
import sys
root = sys.argv[1]
$snippet
PY
  if ( cd "$H" && eval "$cmd" >"$WORK/out" 2>&1 ); then
    missed=$((missed + 1))
    rows+=("$id|$desc|missed|$(grep -m1 -E 'PASS|ok ' "$WORK/out" | head -c 100)")
  else
    caught=$((caught + 1))
    rows+=("$id|$desc|caught|$(grep -m1 -E 'FAIL|--- FAIL|expected|want|not ' "$WORK/out" | head -c 120)")
  fi
}

if [ "${1:-}" = "--list" ]; then
  grep -oE '^run M[0-9]+ "[^"]+"' "$0" | sed 's/^run /  /'
  exit 0
fi

run M1 "Redact returns input unchanged" '
p=root+"/services/agent-broker/internal/audit/audit.go"
s=open(p).read().replace("\tif text == \"\" {\n\t\treturn \"\"\n\t}", "\treturn text\n\tif text == \"\" {\n\t\treturn \"\"\n\t}")
open(p,"w").write(s)
' 'cd "$H/services/agent-broker" && "$GO_BIN" test ./internal/audit'

run M2 "Reason is not redacted" '
p=root+"/services/agent-broker/internal/audit/audit.go"
s=open(p).read().replace("\tr.Reason = Redact(r.Reason)\n", "")
open(p,"w").write(s)
' 'cd "$H/services/agent-broker" && "$GO_BIN" test ./internal/audit'

run M3 "reason tag renamed to detail" '
p=root+"/services/agent-broker/internal/audit/audit.go"
s=open(p).read().replace("`json:\"reason\"`","`json:\"detail\"`")
open(p,"w").write(s)
' 'cd "$H/services/agent-broker" && "$GO_BIN" test ./internal/audit'

run M4 "two OTLP log records per request" '
p=root+"/services/agent-broker/internal/audit/otlp.go"
s=open(p).read().replace("\traw, err := json.Marshal(payload)", "\tpayload.ResourceLogs[0].ScopeLogs[0].LogRecords = append(payload.ResourceLogs[0].ScopeLogs[0].LogRecords, payload.ResourceLogs[0].ScopeLogs[0].LogRecords[0])\n\traw, err := json.Marshal(payload)")
open(p,"w").write(s)
' 'cd "$H/services/agent-broker" && "$GO_BIN" test ./internal/audit'

run M5 "service.name attribute dropped" '
p=root+"/services/agent-broker/internal/audit/otlp.go"
s=open(p).read().replace("\t\t\t\tstringAttr(\"service.name\", e.Service),\n","")
open(p,"w").write(s)
' 'cd "$H/services/agent-broker" && "$GO_BIN" test ./internal/audit'

run M6 "local store never prunes" '
p=root+"/services/agent-broker/internal/audit/store.go"
s=open(p).read().replace("if rec.Time.Before(cutoff) {","if false && rec.Time.Before(cutoff) {")
open(p,"w").write(s)
' 'cd "$H/services/agent-broker" && "$GO_BIN" test ./internal/audit'

run M7 "MIME body base64 unwrapping removed" '
p=root+"/services/agent-broker/internal/scan/scan.go"
s=open(p).read().replace("""\tfor _, body := range mimeBodies(s) {
\t\tif b, ok := decodeBase64Loose(body); ok {
\t\t\tout = append(out, b)
\t\t}
\t}
""","")
open(p,"w").write(s)
' 'cd "$H/services/agent-broker" && "$GO_BIN" test ./test/redteam'

run M8 "one named-class fixture deleted" '
import os
os.remove(root+"/services/agent-broker/test/redteam/fixtures/hex-encoded-credential.json")
' 'cd "$H/services/agent-broker" && "$GO_BIN" test ./test/redteam'

run M9 "API rejection stops recording Reason" '
p=root+"/services/agent-broker/internal/api/patches.go"
s=open(p).read().replace("\t\trec.Reason = reason\n","")
open(p,"w").write(s)
' 'cd "$H/services/agent-broker" && "$GO_BIN" test ./internal/api'

run M10 "publisher skips its audit write" '
p=root+"/services/agent-broker/internal/publish/bundle.go"
s=open(p).read().replace("if s.Audit != nil {","if false && s.Audit != nil {")
open(p,"w").write(s)
' 'cd "$H/services/agent-broker" && "$GO_BIN" test ./internal/publish'

run M11 "runbook heading removed" '
p=root+"/docs/operator-guide/runbooks/agent-mtls-rotation.md"
s=open(p).read().replace("## Trust redistribution\n","")
open(p,"w").write(s)
' 'cd "$H" && ./scripts/verify-runbooks.sh'

run M12 ".pages entry removed" '
p=root+"/docs/operator-guide/runbooks/.pages"
s=open(p).read().replace("- agent-mtls-rotation.md\n","")
open(p,"w").write(s)
' 'cd "$H" && ./scripts/verify-runbooks.sh'

run M13 "recorded at-rest answer flipped" '
p=root+"/docs/operator-guide/runbooks/agent-secret-encryption.md"
s=open(p).read().replace("k3s secret encryption at rest is **not enabled**","k3s secret encryption at rest is **enabled**")
open(p,"w").write(s)
' 'cd "$H" && ./scripts/verify-runbooks.sh'

printf '\n%-4s %-48s %-7s %s\n' ID MUTATION RESULT DETAIL
for row in "${rows[@]}"; do IFS='|' read -r id desc res det <<<"$row"; printf '%-4s %-48s %-7s %s\n' "$id" "$desc" "$res" "$det"; done
printf '\ncaught=%d missed=%d total=%d\n' "$caught" "$missed" "$((caught + missed))"
[ "$missed" -eq 0 ]
