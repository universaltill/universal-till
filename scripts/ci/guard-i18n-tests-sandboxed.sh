#!/usr/bin/env bash
#
# Fails if guard-i18n_test.sh or any guard-i18n_*_test.sh in scripts/ci does not run
# inside the i18n-guard sandbox (ut-docs#4258). Those tests plant fixtures and rewrite
# locale files; against the real tree they race with concurrent
# `go test ./...` (a half-written web/locales/ar.json, a planted .go file).
# Each must `source` i18n-guard-sandbox.sh and call enter_i18n_guard_sandbox.
#
# Usage: guard-i18n-tests-sandboxed.sh [DIR]   (DIR defaults to scripts/ci)
set -euo pipefail

DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)}"
FAIL=0
COUNT=0

for f in "${DIR}"/guard-i18n_test.sh "${DIR}"/guard-i18n_*_test.sh; do
  [ -e "$f" ] || continue
  COUNT=$((COUNT + 1))
  if ! grep -Eq '^[[:space:]]*(source|\.)[[:space:]].*i18n-guard-sandbox\.sh' "$f" ||
    ! grep -Eq '^[[:space:]]*enter_i18n_guard_sandbox([[:space:]]|$)' "$f"; then
    echo "guard-i18n-tests-sandboxed: $f must source i18n-guard-sandbox.sh and call enter_i18n_guard_sandbox (ut-docs#4258)" >&2
    FAIL=1
  fi
done

if [ "${COUNT}" -eq 0 ]; then
  echo "guard-i18n-tests-sandboxed: no guard-i18n_test.sh and guard-i18n_*_test.sh found in ${DIR}" >&2
  exit 1
fi
exit "${FAIL}"
