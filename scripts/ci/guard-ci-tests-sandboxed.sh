#!/usr/bin/env bash
#
# Fails if a scripts/ci *_test.sh names a fixture path in the real tree
# (a variable set to, or a cp/mv/mkdir/touch/tee/sed -i/rm/redirect writing
# to, a path under internal/, web/, plugins/, cmd/, e2e/ or tests/) without
# running inside a sandbox copy (ut-docs#4311). Such tests plant or rewrite
# files there; against the real tree they race with a concurrent
# `go test ./...` (a planted .go file compiled mid-run, a tracked file caught
# half-rewritten) and leave debris behind when killed. Each must `source`
# guard-test-sandbox.sh and call enter_guard_test_sandbox (or the i18n
# variant, i18n-guard-sandbox.sh / enter_i18n_guard_sandbox — ut-docs#4258).
#
# A best-effort static check (a path built up in pieces gets past it); the
# runtime proof is that a run leaves nothing newer in the tree. A line that
# only names a real path to READ, in a test that never writes under the tree,
# takes a reviewed same-line marker:
#   SOME_PATH="internal/x"  # guard-sandbox:allow <reason>
#
# Usage: guard-ci-tests-sandboxed.sh [DIR]   (DIR defaults to scripts/ci)
set -euo pipefail

DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)}"
TREE='(\$\{?(ROOT_DIR|REAL_ROOT|REPO_ROOT|ROOT)\}?/)?(internal|web|plugins|cmd|e2e|tests)/'
# A variable (or array element) set to a real-tree path ...
ASSIGN_RE="^[[:space:]]*((local|readonly|export|declare([[:space:]]+-[a-zA-Z]+)?)[[:space:]]+)?[A-Za-z_][A-Za-z0-9_]*=\\(?[\"']?${TREE}"
# ... or a command writing straight to one.
WRITE_RE="(>>?|\\b(cp|mv|mkdir|touch|tee|sed -i|rm))[[:space:]]+(-[a-zA-Z]+[[:space:]]+)*([^[:space:]|;&]+[[:space:]]+)*[\"']?${TREE}"
SOURCE_RE='^[[:space:]]*(source|\.)[[:space:]].*(guard-test-sandbox|i18n-guard-sandbox)\.sh'
ENTER_RE='^[[:space:]]*enter_(guard_test|i18n_guard)_sandbox([[:space:]]|$)'
FAIL=0
COUNT=0

for f in "${DIR}"/*_test.sh; do
  [ -e "$f" ] || continue
  COUNT=$((COUNT + 1))
  hits="$(grep -nE "${ASSIGN_RE}|${WRITE_RE}" "$f" | grep -v 'guard-sandbox:allow' || true)"
  [ -n "${hits}" ] || continue
  if ! grep -Eq "${SOURCE_RE}" "$f" || ! grep -Eq "${ENTER_RE}" "$f"; then
    echo "guard-ci-tests-sandboxed: $f names real-tree fixture paths but does not run in a sandbox copy (ut-docs#4311):" >&2
    printf '%s\n' "${hits}" | sed 's/^/    line /' >&2
    echo "  source guard-test-sandbox.sh and call enter_guard_test_sandbox <paths the guard reads>" >&2
    FAIL=1
  fi
done

if [ "${COUNT}" -eq 0 ]; then
  echo "guard-ci-tests-sandboxed: no *_test.sh found in ${DIR}" >&2
  exit 1
fi
[ "${FAIL}" -eq 0 ] && echo "✓ ci-tests-sandboxed guard: every fixture-planting guard test in ${DIR} runs in a sandbox copy"
exit "${FAIL}"
