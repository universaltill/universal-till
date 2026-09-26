#!/usr/bin/env bash
# Regression test for guard-pipefail-grep-q.sh (ut-docs#2946).
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GUARD="${ROOT_DIR}/scripts/ci/guard-pipefail-grep-q.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fails=0
bar='|' # fixtures are built so this file doesn't contain the pattern itself

for form in 'grep -qE foo' 'grep -Eq foo' 'grep -E -q foo' 'grep -i -q foo' 'grep --quiet foo' 'egrep -q foo' 'command grep -q foo'; do
  for sep in ' %s ' '%s'; do
    printf "#!/usr/bin/env bash\nset -euo pipefail\necho \"\$x\" ${sep} ${form} || true\n" "$bar" >"$tmp/bad.sh"
    if bash "$GUARD" "$tmp" >/dev/null 2>&1; then echo "FAIL - pipe into '${form}' (sep '${sep}') passed"; fails=$((fails+1)); else echo "ok   - pipe into '${form}' flagged"; fi
  done
done
rm -f "$tmp/bad.sh"

printf '#!/usr/bin/env bash\nset -euo pipefail\ngrep -qE foo <<<"$x" || true\n# echo %s grep -q in a comment is fine\n' "$bar" >"$tmp/good.sh"
printf '#!/usr/bin/env bash\nset -eu\necho x %s grep -q x\n' "$bar" >"$tmp/nopipefail.sh"
printf '#!/usr/bin/env bash\nset -euo pipefail\ntrue %s%s grep -q x <<<"$y"\n' "$bar" "$bar" >"$tmp/oror.sh"
if bash "$GUARD" "$tmp" >/dev/null 2>&1; then echo "ok   - here-string, comment, || and non-pipefail script pass"; else echo "FAIL - a clean directory was flagged"; fails=$((fails+1)); fi

[ "$fails" -eq 0 ] || exit 1
echo "guard-pipefail-grep-q_test: ok"
