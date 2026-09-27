#!/usr/bin/env bash
# Regression test for guard-pipefail-grep-q.sh (ut-docs#2946).
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GUARD="${ROOT_DIR}/scripts/ci/guard-pipefail-grep-q.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fails=0
bar='|'    # fixtures are built so this file doesn't contain the pattern itself
dollar='$' # likewise for the fixture's own "$x"

fixture() { # fixture <file> <set-line> <body-line>...
  local f="$1"
  shift
  printf '%s\n' '#!/usr/bin/env bash' "$@" >"$f"
}

for form in 'grep -qE foo' 'grep -Eq foo' 'grep -E -q foo' 'grep -i -q foo' 'grep --quiet foo' 'egrep -q foo' 'command grep -q foo'; do
  for sep in " ${bar} " "${bar}"; do
    fixture "$tmp/bad.sh" 'set -euo pipefail' "echo \"${dollar}x\"${sep}${form} || true"
    if bash "$GUARD" "$tmp" >/dev/null 2>&1; then
      echo "FAIL - pipe into '${form}' (sep '${sep}') passed"
      fails=$((fails + 1))
    else
      echo "ok   - pipe into '${form}' flagged"
    fi
  done
done
rm -f "$tmp/bad.sh"

fixture "$tmp/good.sh" 'set -euo pipefail' "grep -qE foo <<<\"${dollar}x\" || true" "# echo ${bar} grep -q in a comment is fine"
fixture "$tmp/nopipefail.sh" 'set -eu' "echo x ${bar} grep -q x"
fixture "$tmp/oror.sh" 'set -euo pipefail' "true ${bar}${bar} grep -q x <<<\"${dollar}y\""
if bash "$GUARD" "$tmp" >/dev/null 2>&1; then
  echo "ok   - here-string, comment, || and non-pipefail script pass"
else
  echo "FAIL - a clean directory was flagged"
  fails=$((fails + 1))
fi

[ "$fails" -eq 0 ] || exit 1
echo "guard-pipefail-grep-q_test: ok"
