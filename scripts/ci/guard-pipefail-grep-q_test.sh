#!/usr/bin/env bash
# Regression test for guard-pipefail-grep-q.sh (ut-docs#2946, ut-docs#2983).
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GUARD="${ROOT_DIR}/scripts/ci/guard-pipefail-grep-q.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fails=0
bar='|'    # fixtures are built so this file doesn't contain the pattern itself
dollar='$' # likewise for the fixture's own "$x"
mkdir "$tmp/scripts" "$tmp/wf"

fixture() { # fixture <file> <set-line> <body-line>...
  local f="$1"
  shift
  printf '%s\n' '#!/usr/bin/env bash' "$@" >"$f"
}

expect() { # expect pass|fail <label>
  local want="$1" label="$2" out
  if out="$(bash "$GUARD" "$tmp/scripts" "$tmp/wf" 2>&1)"; then got=pass; else got=fail; fi
  if [ "$got" = "$want" ]; then
    echo "ok   - ${label}"
  else
    echo "FAIL - ${label}: guard said ${got}, want ${want}"
    printf '%s\n' "$out"
    fails=$((fails + 1))
  fi
}

# --- scripts/ci: every early-exit reader is flagged ------------------------
for form in 'grep -qE foo' 'grep -Eq foo' 'grep -E -q foo' 'grep -i -q foo' 'grep --quiet foo' \
  'grep --silent foo' 'egrep -q foo' 'command grep -q foo' \
  'grep -l foo' 'grep -m1 foo' 'grep -m 1 foo' 'grep -oE -m1 foo' 'grep --max-count=1 foo' \
  'grep --files-with-matches foo' 'head -1' 'head -n 1' 'head -n1' 'head' 'head -c 10'; do
  for sep in " ${bar} " "${bar}"; do
    fixture "$tmp/scripts/bad.sh" 'set -euo pipefail' "x=\"${dollar}(echo \"${dollar}y\"${sep}${form} || true)\""
    expect fail "pipe into '${form}' (sep '${sep}') flagged"
  done
done
rm -f "$tmp/scripts/bad.sh"

fixture "$tmp/scripts/good.sh" 'set -euo pipefail' \
  "grep -qE foo <<<\"${dollar}x\" || true" \
  "# echo ${bar} grep -q in a comment is fine" \
  "echo x ${bar} grep -c foo || true" \
  "echo x ${bar} grep -oE foo ${bar} sed -n 1p || true" \
  "echo x ${bar} grep -n foo || true" \
  "echo x ${bar} headless || true" \
  "x=\"${dollar}(echo x ${bar} head -1 || true)\" # pipefail-reader:allow output is complete"
fixture "$tmp/scripts/nopipefail.sh" 'set -eu' "echo x ${bar} grep -q x" "echo x ${bar} head -1"
fixture "$tmp/scripts/oror.sh" 'set -euo pipefail' "true ${bar}${bar} grep -q x <<<\"${dollar}y\""
expect pass "here-string, comment, full readers, allow marker, || and non-pipefail script pass"

# --- workflows: run steps under pipefail are flagged, with the real line ---
wf_step() { # wf_step <file> <shell-line or ''> <run-line>
  {
    printf '%s\n' 'on: push' 'jobs:' '  a:' '    runs-on: ubuntu-latest' '    steps:' '      - name: s'
    [ -z "$2" ] || printf '        %s\n' "$2"
    printf '%s\n' '        run: |' '          echo start' "          $3"
  } >"$1"
}

wf_step "$tmp/wf/bad.yml" 'shell: bash' "printf x ${bar} grep -qE y"
out="$(bash "$GUARD" "$tmp/scripts" "$tmp/wf" 2>&1 || true)"
if grep -qF "file=${tmp}/wf/bad.yml,line=10::" <<<"$out"; then
  echo "ok   - shell: bash step flagged at its real line"
else
  echo "FAIL - shell: bash step not reported at bad.yml line 10"
  printf '%s\n' "$out"
  fails=$((fails + 1))
fi

wf_step "$tmp/wf/bad.yml" '' "set -euo pipefail; printf x ${bar} head -1"
sed -i 's/^          set -euo pipefail; /          set -euo pipefail\n          /' "$tmp/wf/bad.yml"
expect fail "step whose body sets pipefail flagged"

wf_step "$tmp/wf/bad.yml" 'shell: bash' "security find-identity ${bar} grep -q 'Developer ID'"
expect fail "pipe from a command into grep -q flagged"

wf_step "$tmp/wf/bad.yml" '' "printf x ${bar} grep -qE y"
expect pass "implicit default shell (bash -e, no pipefail) passes"

wf_step "$tmp/wf/bad.yml" 'shell: bash' "grep -qE y <<<\"${dollar}x\""
expect pass "here-string under shell: bash passes"

printf '%s\n' 'jobs: [unclosed' >"$tmp/wf/bad.yml"
expect fail "unparseable workflow fails the guard"
rm -f "$tmp/wf/bad.yml"

[ "$fails" -eq 0 ] || exit 1
echo "guard-pipefail-grep-q_test: ok"
