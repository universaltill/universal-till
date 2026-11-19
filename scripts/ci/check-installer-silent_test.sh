#!/usr/bin/env bash
# Fixture and `A && ok || fail` assertion style used throughout this test.
# shellcheck disable=SC2015
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
S="${ROOT_DIR}/scripts/ci/check-installer-silent.sh"
W="$(mktemp -d)"
# shellcheck disable=SC2317
cleanup() { rm -rf -- "${W:?}"; }
trap cleanup EXIT
fails=0
t() { # t <want-rc> <name> <nsi text>
  printf '%s\n' "$3" >"$W/i.nsi"; local rc=0; bash "$S" "$W/i.nsi" >/dev/null 2>&1 || rc=$?
  if [ "$rc" = "$1" ]; then echo "ok   - $2"; else echo "FAIL - $2 (rc $rc)" >&2; fails=$((fails+1)); fi
}
t 0 "user-level installer, no dialogs" 'RequestExecutionLevel user'
t 0 "MessageBox with /SD" $'RequestExecutionLevel user\nMessageBox MB_OK "x" /SD IDOK'
t 1 "MessageBox without /SD" $'RequestExecutionLevel user\nMessageBox MB_OK "x"'
t 0 "a commented MessageBox is ignored" $'RequestExecutionLevel user\n; MessageBox MB_OK "x"'
t 1 "SilentInstall normal" $'RequestExecutionLevel user\nSilentInstall normal'
t 1 "admin elevation" 'RequestExecutionLevel admin'
t 1 "no execution level" 'Name x'
t 1 "missing file" ''; rm -f "$W/i.nsi"; rc=0; bash "$S" "$W/i.nsi" >/dev/null 2>&1 || rc=$?; [ "$rc" = 1 ] || { echo "FAIL missing" >&2; fails=$((fails+1)); }
bash "$S" "${ROOT_DIR}/packaging/windows/installer.nsi" >/dev/null && echo "ok   - the real installer.nsi" || { echo "FAIL - real installer.nsi" >&2; fails=$((fails+1)); }
[ "$fails" = 0 ] || exit 1
echo "✓ check-installer-silent_test: all cases passed"
