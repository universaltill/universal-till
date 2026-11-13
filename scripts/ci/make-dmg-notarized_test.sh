#!/usr/bin/env bash
#
# Behaviour test for ut-docs#6025: packaging/macos/make-dmg.sh with
# MACOS_REQUIRE_NOTARIZED=1 refuses (before building, or after signing but
# without notary credentials) instead of producing an ad-hoc / un-notarized
# release .dmg; without the flag the fork/local behaviour is unchanged.
#
# Runs a mktemp copy of packaging/macos/ with stub macOS tools first on PATH
# (hdiutil, codesign, xcrun, spctl), so it works on Linux and never touches
# the real tree (#4311). A mutation self-test deletes each refusal branch in
# a further copy and requires the matching case to go red.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fails=0
pass() { echo "ok   - $1"; }
fail() { echo "FAIL - $1" >&2; fails=$((fails + 1)); }

# Stub tools: record nothing, succeed; hdiutil creates its last argument.
mkdir -p "$WORK/bin"
cat > "$WORK/bin/hdiutil" <<'SH'
#!/bin/sh
for a; do last="$a"; done
: > "$last"
SH
cat > "$WORK/bin/codesign" <<'SH'
#!/bin/sh
exit 0
SH
cat > "$WORK/bin/spctl" <<'SH'
#!/bin/sh
exit 0
SH
cat > "$WORK/bin/xcrun" <<'SH'
#!/bin/sh
case "$1 $2" in
  "notarytool submit") echo '{"id":"12345678-aaaa-bbbb-cccc-1234567890ab"}' ;;
  "notarytool wait") echo '{"status":"Accepted"}' ;;
esac
exit 0
SH
chmod +x "$WORK/bin/"*

# new_tree <name>: a copy of packaging/macos with a fake app; echoes its root.
new_tree() {
  local t="$WORK/$1"
  mkdir -p "$t/packaging" "$t/dist/Universal Till.app"
  cp -R "$ROOT_DIR/packaging/macos" "$t/packaging/macos"
  echo "$t"
}

# run_case <tree> <want-rc> <want-dmg yes|no> <label> [VAR=val ...]
run_case() {
  local t="$1" want="$2" dmg="$3" label="$4" rc=0 out found=no
  shift 4
  rm -f "$t"/dist/*.dmg
  out="$(env -i PATH="$WORK/bin:$PATH" HOME="$WORK" TMPDIR="$WORK" "$@" \
    bash "$t/packaging/macos/make-dmg.sh" v0 "$t/dist" 2>&1)" || rc=$?
  ls "$t"/dist/*.dmg >/dev/null 2>&1 && found=yes
  if [ "$rc" -ne "$want" ]; then
    fail "${label}: exit ${rc}, want ${want} (${out##*$'\n'})"
  elif [ "$want" -ne 0 ] && ! grep -q 'ut-docs#6025' <<<"$out"; then
    fail "${label}: refusal does not cite ut-docs#6025"
  elif [ "$dmg" = no ] && [ "$found" = yes ] && [ "$want" -ne 0 ] && [ "$label" != "${label#early}" ]; then
    fail "${label}: a .dmg was produced"
  else
    pass "$label"
  fi
}

# all_cases <tree> <prefix>: the three behaviours under test.
all_cases() {
  local t="$1"
  run_case "$t" 1 no "early refusal: REQUIRE=1, no identity" MACOS_REQUIRE_NOTARIZED=1
  run_case "$t" 1 yes "REQUIRE=1, identity, no notary credentials" MACOS_REQUIRE_NOTARIZED=1 MACOS_SIGN_IDENTITY=Id
  run_case "$t" 1 yes "REQUIRE=1, identity, partial API key" MACOS_REQUIRE_NOTARIZED=1 MACOS_SIGN_IDENTITY=Id MACOS_NOTARY_KEY_ID=k
  run_case "$t" 0 yes "REQUIRE=1, identity, full API key notarizes" MACOS_REQUIRE_NOTARIZED=1 MACOS_SIGN_IDENTITY=Id \
    MACOS_NOTARY_KEY_P8=p MACOS_NOTARY_KEY_ID=k MACOS_NOTARY_ISSUER_ID=i
  run_case "$t" 0 yes "no flag, no identity: ad-hoc build still works"
  run_case "$t" 0 yes "no flag, identity, no notary: signed only" MACOS_SIGN_IDENTITY=Id
}

REAL="$(new_tree real)"
all_cases "$REAL"
# the early refusal must not have produced a .dmg
rm -f "$REAL"/dist/*.dmg
env -i PATH="$WORK/bin:$PATH" HOME="$WORK" TMPDIR="$WORK" MACOS_REQUIRE_NOTARIZED=1 \
  bash "$REAL/packaging/macos/make-dmg.sh" v0 "$REAL/dist" >/dev/null 2>&1 || true
if ls "$REAL"/dist/*.dmg >/dev/null 2>&1; then
  fail "REQUIRE=1 without identity left a .dmg"
else
  pass "REQUIRE=1 without identity produces no .dmg"
fi

# Mutation self-test: each refusal branch deleted must turn a case red.
mutant_fails() { # <tree> <label>; true when a case failed in this tree
  local before="$fails"
  all_cases "$1" >/dev/null 2>&1 || true
  [ "$fails" -gt "$before" ]
}
mutate() { # <name> <python-snippet operating on s>
  local t
  t="$(new_tree "$1")"
  python3 - "$t/packaging/macos/make-dmg.sh" "$2" <<'PY'
import re, sys
p, kind = sys.argv[1], sys.argv[2]
s = open(p).read()
if kind == "early":
    s2 = re.sub(r'if \[ "\$\{MACOS_REQUIRE_NOTARIZED:-\}" = 1 \] && \[ -z "\$\{MACOS_SIGN_IDENTITY:-\}" \]; then\n.*?\nfi\n', '', s, count=1, flags=re.S)
else:
    s2 = re.sub(r'  elif \[ "\$\{MACOS_REQUIRE_NOTARIZED:-\}" = 1 \]; then\n.*?\n(?=  else\n)', '', s, count=1, flags=re.S)
if s2 == s:
    sys.exit("mutation did not apply")
open(p, "w").write(s2)
PY
  echo "$t"
}
for m in early elif; do
  t="$(mutate "mut-$m" "$m")"
  before="$fails"
  all_cases "$t" >/dev/null 2>&1
  if [ "$fails" -gt "$before" ]; then
    fails="$before"
    pass "mutant (${m} refusal deleted) is caught"
  else
    fail "mutant (${m} refusal deleted) is NOT caught"
  fi
done

if [ "$fails" -ne 0 ]; then
  echo "${fails} check(s) failed" >&2
  exit 1
fi
echo "all make-dmg.sh notarization-required checks passed"
