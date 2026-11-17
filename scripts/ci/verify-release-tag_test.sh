#!/usr/bin/env bash
# Self-test for verify-release-tag.sh (ut-docs#6410): the tag must be a plain
# vX.Y.Z that exists and whose commit is on main; prints the commit SHA.
set -euo pipefail
S="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/verify-release-tag.sh"
fails=0
ok() { echo "ok   - $1"; }
bad() { echo "FAIL - $1"; fails=$((fails + 1)); }
[ -f "$S" ] || { echo "FAIL - verify-release-tag.sh missing"; exit 1; }
t="$(mktemp -d)"; trap 'rm -rf "$t"' EXIT
cd "$t" && git init -q -b main . && git config user.email t@example.invalid && git config user.name t
git commit -q --allow-empty -m a && git tag v1.2.3
a="$(git rev-parse HEAD)"
git checkout -q -b side && git commit -q --allow-empty -m b && git tag v1.2.4
git checkout -q main && git commit -q --allow-empty -m c
git tag -a v1.2.5 -m x
run() { MAIN_REF=main bash "$S" "$1" 2>&1; }
out="$(run 1.2.3)" && [ "$out" = "$a" ] && ok "tag on main prints its commit" || bad "on-main tag: $out"
run 1.2.4 >/dev/null && bad "off-main tag accepted" || ok "tag not on main refused"
run 9.9.9 >/dev/null && bad "missing tag accepted" || ok "missing tag refused"
for v in v1.2.3 1.2 1.2.3-rc1 '1.2.3;id' ''; do run "$v" >/dev/null && bad "bad version '$v' accepted" || ok "bad version '$v' refused"; done
out="$(run 1.2.5)" && [ "$out" = "$(git rev-parse 'v1.2.5^{commit}')" ] && ok "annotated tag peels to the commit" || bad "annotated tag: $out"
[ "$fails" = 0 ]
