#!/usr/bin/env bash
#
# Run the advisory Conftest suite.
#
#   --fixtures   assert each rule fires on its fail fixture and passes on its
#                pass fixture. This is the anti-vacuity check and is the part
#                that should never be skipped.
#   --report     run the policies over manifests/dist/ and print findings.
#                Advisory: the first run is expected to be noisy.
#   --all        both, in that order (default).
#
# `--combine` is required: the rules index `input[i].contents` and the
# NetworkPolicy rule is a cross-document assertion. Without it, the policies
# match nothing and every run is green.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

# shellcheck disable=SC1091
. ./scripts/tools.env

if ! command -v conftest >/dev/null 2>&1; then
  echo "test-conftest: conftest not found. Install ${CONFTEST_VERSION} (scripts/tools.env)." >&2
  exit 1
fi

have="$(conftest --version 2>/dev/null | sed -n 's/^Conftest: //p')"
if [ "$have" != "$CONFTEST_VERSION" ]; then
  echo "test-conftest: conftest ${CONFTEST_VERSION} required, found ${have:-none}" >&2
  exit 1
fi

POLICY="tests/policy"

run_fixture() {
  local rule="$1" expect="$2" dir="$3" marker="$4" out rc
  out="$(conftest test --combine --policy "$POLICY" "$dir" 2>&1)" && rc=0 || rc=$?
  if [ "$expect" = "pass" ]; then
    if [ "$rc" -ne 0 ]; then
      printf 'FAIL  %-9s pass fixture was denied:\n%s\n' "$rule" "$out"
      return 1
    fi
    printf 'PASS  %-9s pass fixture\n' "$rule"
    return 0
  fi
  if [ "$rc" -eq 0 ]; then
    printf 'FAIL  %-9s fail fixture passed vacuously\n' "$rule"
    return 1
  fi
  if ! printf '%s' "$out" | grep -q "$marker"; then
    printf 'FAIL  %-9s fail fixture denied for the wrong reason (wanted %q):\n%s\n' \
      "$rule" "$marker" "$out"
    return 1
  fi
  printf 'PASS  %-9s fail fixture denied for the right reason\n' "$rule"
  return 0
}

fixtures() {
  local fail=0
  run_fixture images    pass tests/fixtures/images/pass    ""                   || fail=1
  run_fixture images    fail tests/fixtures/images/fail    "unpinned image"     || fail=1
  run_fixture privilege pass tests/fixtures/privilege/pass ""                   || fail=1
  run_fixture privilege fail tests/fixtures/privilege/fail "is privileged"      || fail=1
  run_fixture netpol    pass tests/fixtures/netpol/pass    ""                   || fail=1
  run_fixture netpol    fail tests/fixtures/netpol/fail    "no NetworkPolicy"   || fail=1
  run_fixture secret    pass tests/fixtures/secret/pass    ""                   || fail=1
  run_fixture secret    fail tests/fixtures/secret/fail    "populated data"     || fail=1
  if [ "$fail" -ne 0 ]; then
    echo "test-conftest: fixtures FAIL"
    return 1
  fi
  echo "test-conftest: fixtures PASS"
}

report() {
  local out rc
  out="$(conftest test --combine --policy "$POLICY" manifests/dist/*/*.yaml 2>&1)" && rc=0 || rc=$?
  printf '%s\n' "$out"
  if [ "$rc" -eq 0 ]; then
    echo "test-conftest: report CLEAN"
    return 0
  fi
  echo "test-conftest: report found findings (advisory; not a gate)"
  return 1
}

mode="${1:---all}"
case "$mode" in
  --fixtures) fixtures ;;
  --report) report ;;
  --all)
    fixtures
    report || true
    ;;
  *)
    echo "usage: $0 [--fixtures|--report|--all]" >&2
    exit 2
    ;;
esac
