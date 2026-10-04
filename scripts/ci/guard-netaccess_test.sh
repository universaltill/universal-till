#!/usr/bin/env bash
#
# Regression test for guard-netaccess.sh (ADR-0113 §1.6, ut-docs#2795):
# plants disposable fixture files under internal/pages (always cleaned up via
# trap, even on failure), runs the real guard against them, and asserts it
# rejects every forbidden construction form and ignores the exempt ones. Also
# asserts the guard passes on the real, unmodified codebase.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-netaccess.sh"
FIXTURE_DIR="internal/pages"
OUT="$(mktemp)"
FAIL_COUNT=0

fixtures=()
# Invoked indirectly via `trap ... EXIT`, not a direct call -- shellcheck cannot see that (SC2317 false positive).
# shellcheck disable=SC2317
cleanup() {
  local status=$?
  if [[ ${#fixtures[@]} -gt 0 ]]; then
    for f in "${fixtures[@]}"; do
      [[ -n "${f}" && -f "${f}" ]] && rm -f "${f}"
    done
  fi
  rm -f "${OUT}"
  exit "${status}"
}
trap cleanup EXIT

# plant <name> <go statement>: one fixture file whose function body is the
# given statement, in the internal/pages package.
plant() {
  local name="$1" stmt="$2"
  local path="${FIXTURE_DIR}/zz_netaccess_guard_${name}.go"
  fixtures+=("${path}")
  printf 'package pages\n\nfunc zzNetaccessGuard%s() {\n\t%s\n}\n' "${name}" "${stmt}" >"${path}"
}

unplant() {
  rm -f "${FIXTURE_DIR}/zz_netaccess_guard_$1.go"
  fixtures=()
}

expect_fail() {
  local label="$1"
  if bash "${GUARD}" >"${OUT}" 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat "${OUT}" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
}

expect_pass() {
  local label="$1"
  if bash "${GUARD}" >"${OUT}" 2>&1; then
    echo "✓ guard correctly ignored ${label}"
  else
    echo "❌ FAIL: expected guard to ignore ${label} (false positive), but it rejected it" >&2
    cat "${OUT}" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

# Every forbidden form. The bodies are never compiled (the guard is a grep),
# so they need not type-check — only look like the real thing.
for case in \
  'PtrLiteral:_ = &http.Client{Timeout: 5 * time.Second}' \
  'ValueLiteral:c := http.Client{}; _ = &c' \
  'EmptyPtrLiteral:_ = &http.Client{}' \
  'Dial:_, _ = net.Dial("tcp", "example.com:80")' \
  'DialTimeout:_, _ = net.DialTimeout("tcp", "example.com:80", time.Second)' \
  'DialUDP:_, _ = net.DialUDP("udp", nil, nil)' \
  'DialerPtr:_ = (&net.Dialer{Timeout: time.Second}).DialContext' \
  'DialerValue:d := net.Dialer{}; _ = d' \
  'AllowWithoutReason:_ = &http.Client{} // netaccess:allow'
do
  name="${case%%:*}"
  stmt="${case#*:}"
  plant "${name}" "${stmt}"
  expect_fail "${stmt}"
  unplant "${name}"
done

# Exempt forms: a full-line comment, a reviewed same-line exception with a
# reason, and the sanctioned constructor itself.
for case in \
  'Comment:// a seam over net.Dialer.DialContext; was &http.Client{} before' \
  'Allowed:_, _ = net.Dial("udp", "224.0.0.251:5353") // netaccess:allow UDP route lookup, sends nothing' \
  'Constructor:_ = netaccess.NewClient(5 * time.Second)'
do
  name="${case%%:*}"
  stmt="${case#*:}"
  plant "${name}" "${stmt}"
  expect_pass "${stmt}"
  unplant "${name}"
done

# A _test.go file is out of scope.
test_fixture="${FIXTURE_DIR}/zz_netaccess_guard_fixture_test.go"
fixtures+=("${test_fixture}")
printf 'package pages\n\nvar _ = &http.Client{}\n' >"${test_fixture}"
expect_pass "&http.Client{} in a _test.go file"
rm -f "${test_fixture}"
fixtures=()

# Baseline: the guard must pass on the real, unmodified codebase.
if ! bash "${GUARD}" >"${OUT}" 2>&1; then
  echo "❌ FAIL: guard rejects the clean codebase" >&2
  cat "${OUT}" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
else
  echo "✓ guard passes on the clean codebase"
fi

if [[ "${FAIL_COUNT}" -gt 0 ]]; then
  echo "❌ guard-netaccess_test.sh: ${FAIL_COUNT} case(s) failed" >&2
  exit 1
fi

echo "✓ guard-netaccess_test.sh: all cases passed"
exit 0
