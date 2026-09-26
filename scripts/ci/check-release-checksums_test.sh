#!/usr/bin/env bash
#
# Regression test for check-release-checksums.sh (ut-docs#2863): the release
# `checksums:` job must fail loudly if any published asset is missing from
# checksums.txt (or has the wrong hash) — before this, the Windows setup
# .exe and both Android .apk files were silently never checked at all (see
# the script's own header for the full story).
#
# Fixtures live under a fresh mktemp -d per scenario (never this checkout),
# same pass/fail-tally convention as windows-signing_test.sh. Behaviour
# scenarios (a)-(f) plus wiring checks (grep/awk) on release.yml.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

CHECK="${ROOT_DIR}/scripts/ci/check-release-checksums.sh"
fails=0
pass() { echo "ok   - $1"; }
fail() { echo "FAIL - $1" >&2; fails=$((fails + 1)); }

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

# --- fixtures -----------------------------------------------------------

T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT

# A complete, correct assets dir: goreleaser-style archives plus the three
# assets that used to be missing (the setup .exe, and both APK names).
build_good_fixture() {
  local dir="$1"
  mkdir -p "$dir"
  printf 'zip-%s\n' "$$" > "${dir}/unitill-pos_1.2.3_windows_amd64.zip"
  printf 'tar-%s\n' "$$" > "${dir}/unitill-pos_1.2.3_linux_amd64.tar.gz"
  printf 'exe-%s\n' "$$" > "${dir}/unitill-pos-setup-1.2.3.exe"
  printf 'apk-%s\n' "$$" > "${dir}/unitill-pos_1.2.3_android.apk"
  printf 'apk-latest-%s\n' "$$" > "${dir}/unitill-pos-android-latest.apk"
  printf 'dmg-%s\n' "$$" > "${dir}/unitill-pos-1.2.3-macOS-arm64.dmg"
  : > "${dir}/checksums.txt"
  local f name
  for f in "${dir}"/*; do
    name="$(basename "$f")"
    [ "$name" = checksums.txt ] && continue
    printf '%s  %s\n' "$(sha256 "$f")" "$name" >> "${dir}/checksums.txt"
  done
}

run_check() { # run_check <checksums-file> <assets-dir>  -> sets OUT, returns exit code
  set +e
  OUT="$(bash "$CHECK" "$1" "$2" 2>&1)"
  local rc=$?
  set -e
  return "$rc"
}

# (a) complete + correct -> exit 0
d="$T/good"
build_good_fixture "$d"
if run_check "$d/checksums.txt" "$d"; then
  pass "(a) complete + correct assets dir passes"
else
  fail "(a) complete + correct assets dir should pass: $OUT"
fi

# (b) an asset (the .exe) is missing from checksums.txt -> non-zero, names it
d="$T/missing-exe"
build_good_fixture "$d"
grep -v 'unitill-pos-setup-1.2.3.exe' "$d/checksums.txt" > "$d/checksums.txt.new"
mv "$d/checksums.txt.new" "$d/checksums.txt"
if run_check "$d/checksums.txt" "$d"; then
  fail "(b) missing .exe entry should fail, but passed"
elif grep -qF 'unitill-pos-setup-1.2.3.exe' <<<"$OUT"; then
  pass "(b) missing .exe entry fails and names the file"
else
  fail "(b) missing .exe entry failed but didn't name it: $OUT"
fi

# (c) wrong hash -> non-zero
d="$T/wrong-hash"
build_good_fixture "$d"
awk '$2=="unitill-pos_1.2.3_android.apk"{$1="0000000000000000000000000000000000000000000000000000000000000000"} {print}' \
  OFS='  ' "$d/checksums.txt" > "$d/checksums.txt.new"
mv "$d/checksums.txt.new" "$d/checksums.txt"
if run_check "$d/checksums.txt" "$d"; then
  fail "(c) wrong hash should fail, but passed"
else
  pass "(c) wrong hash fails"
fi

# (d) duplicate line for the same asset -> non-zero
d="$T/dup"
build_good_fixture "$d"
line="$(grep 'unitill-pos_1.2.3_android.apk' "$d/checksums.txt")"
printf '%s\n' "$line" >> "$d/checksums.txt"
if run_check "$d/checksums.txt" "$d"; then
  fail "(d) duplicate checksums line should fail, but passed"
else
  pass "(d) duplicate checksums line fails"
fi

# (e) phantom entry (names a file absent from assets-dir) -> non-zero
d="$T/phantom"
build_good_fixture "$d"
printf '%s  %s\n' "$(sha256 "$d/checksums.txt")" "unitill-pos-nonexistent-1.2.3.exe" >> "$d/checksums.txt"
if run_check "$d/checksums.txt" "$d"; then
  fail "(e) phantom checksums entry should fail, but passed"
elif grep -qF 'unitill-pos-nonexistent-1.2.3.exe' <<<"$OUT"; then
  pass "(e) phantom checksums entry fails and names it"
else
  fail "(e) phantom checksums entry failed but didn't name it: $OUT"
fi

# (f) no assets besides an (empty) checksums.txt -> non-zero. The empty
# checksums.txt matters: without it the checker already fails on "checksums
# file not found" and (f) would pass without exercising the empty-dir check.
d="$T/empty"
mkdir -p "$d"
: > "$d/checksums.txt"
if run_check "$d/checksums.txt" "$d"; then
  fail "(f) empty assets dir should fail, but passed"
else
  pass "(f) empty assets dir fails"
fi

# --- wiring: .github/workflows/release.yml -------------------------------

job_block() { # job_block <job-name>
  awk -v j="  ${1}:" '$0==j{on=1; print; next} on && /^  [a-zA-Z_][a-zA-Z0-9_-]*:$/{exit} on{print}' .github/workflows/release.yml
}

checksums_job="$(job_block checksums)"
if [ -n "$checksums_job" ]; then
  pass "release.yml has a checksums: job"
else
  fail "release.yml has no top-level 'checksums:' job"
fi

if grep -qF 'scripts/ci/check-release-checksums.sh' <<<"$checksums_job"; then
  pass "checksums job runs check-release-checksums.sh"
else
  fail "checksums job does not run scripts/ci/check-release-checksums.sh"
fi

# The literal ${VERSION} is intentional: matching release.yml's own
# unexpanded run: script text, not a shell expansion in this test.
# shellcheck disable=SC2016
for want in 'unitill-pos-setup-' 'unitill-pos_${VERSION}_android.apk' 'unitill-pos-android-latest.apk'; do
  if grep -qF -- "$want" <<<"$checksums_job"; then
    pass "checksums job folds in: ${want}"
  else
    fail "checksums job does not fold in: ${want}"
  fi
done

needs_block() { # needs_block <job-block-text> -- the needs: ... ] block, however indented
  awk '/^[[:space:]]*needs:/{f=1} f{print} f && /\]/{exit}' <<<"$1"
}

publish_job="$(job_block publish-release)"
if grep -qF 'checksums' <<<"$(needs_block "$publish_job")"; then
  pass "publish-release's needs includes checksums"
else
  fail "publish-release's needs does not include checksums"
fi
if grep -qF 'needs.checksums.result' <<<"$publish_job"; then
  pass "publish-release's if: checks needs.checksums.result"
else
  fail "publish-release's if: does not check needs.checksums.result"
fi

cleanup_job="$(job_block cleanup-orphaned-tag)"
if grep -qF 'checksums' <<<"$(needs_block "$cleanup_job")"; then
  pass "cleanup-orphaned-tag's needs includes checksums"
else
  fail "cleanup-orphaned-tag's needs does not include checksums"
fi

if [ "$fails" -ne 0 ]; then
  echo "${fails} check(s) failed" >&2
  exit 1
fi
echo "all release-checksums checks passed"
