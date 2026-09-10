#!/usr/bin/env bash
#
# `gomobile bind`/`gobind` can silently drop an exported function from the
# generated Java/Kotlin API when one of its parameter/return types isn't
# declared in the bound package itself (`./mobile`) — instead of failing,
# it emits a plain `//` comment in the generated source, e.g.:
#
#   // skipped function SetBluetoothBridge with unsupported parameter or return types
#
# ...and still EXITS 0. android-ci.yml's `compile` job (`./gradlew
# assembleDebug`) is a compile-only gate and cannot see this: the Kotlin
# side never references the dropped method, so nothing there fails either.
# The original real occurrence was ut-docs#1721: mobile.SetBluetoothBridge
# took a parameter type declared in internal/bluetooth (an unbound
# package), so gobind quietly built a green .aar missing the one method
# Kotlin actually needed. That review fixed the immediate bug (moved
# BluetoothBridge into ./mobile itself — see mobile/mobile.go) but flagged
# the class of failure as unguarded and filed it as ut-docs#1735: "even
# with [the mobile/** path filter], gobind reports a type it silently
# can't bind as a comment in its generated output and still exits 0 — a
# compile-only gate cannot see that class of failure." This script is that
# guard — it runs gobind itself and inspects its generated output for the
# skip comment, so a future unbound-type regression fails CI with the
# skipped function's name, instead of shipping a phone build silently
# missing a method.
#
# Two modes (same convention as guard-htmx-loaded.sh and
# guard-android-external-links.sh):
#
#   - No arguments (real CI use): requires `gobind` on PATH, runs
#     `gobind -lang=java -outdir=<tmp> ./mobile` from the repo root, checks
#     gobind's own exit code, then scans every file under <tmp>/java/ for
#     the skip-comment shape. Cleans up its temp dir on exit either way.
#
#   - One argument, a directory (test/fixture mode): skips running gobind
#     entirely and scans the given directory's **/*.java files for the same
#     pattern. This is what guard-gobind-skip_test.sh drives against a
#     throwaway fixture tree, so the regression test doesn't need
#     gobind/network access to prove the *scanning* logic works.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# The literal gobind wording (verified empirically, see mobile/mobile.go's
# BluetoothBridge doc comment and the ut-docs#1721 review) is:
#   // skipped function <Name> with unsupported parameter or return types
# Matched generously enough to survive a minor gobind wording change
# (function/method/etc, and some trailing variation after "with
# unsupported"), but anchored on an actual `//` comment start plus gobind's
# distinctive "with unsupported ... types" phrase specifically — NOT a bare
# `skip` substring search. That anchoring matters here: this repo's own
# generated BluetoothBridge.java carries a Javadoc paragraph (copied
# verbatim from mobile/mobile.go's doc comment on BluetoothBridge)
# that itself *talks about* this exact bug — it contains the words
# `gobind emits "skipped function SetBluetoothBridge...` inside ordinary
# prose, never as a real `//`-prefixed skip comment. A bare substring match
# on "skip" would false-positive on that file forever; anchoring on the
# comment-start plus the full distinctive phrase does not.
SKIP_PATTERN='^[[:space:]]*//[[:space:]]*skipped [A-Za-z]+ .* with unsupported'

# Extracts the skipped function/method name from one matched line, e.g.
# "// skipped function SetBluetoothBridge with unsupported ..." -> "SetBluetoothBridge".
# Falls back to printing the whole matched line if the shape doesn't parse
# cleanly, so a future gobind wording drift still surfaces *something*
# actionable instead of an empty name.
extract_name() {
  local line="$1"
  if [[ "$line" =~ skipped[[:space:]]+[A-Za-z]+[[:space:]]+([A-Za-z0-9_.]+)[[:space:]]+with[[:space:]]+unsupported ]]; then
    printf '%s' "${BASH_REMATCH[1]}"
  else
    printf '%s' "$line"
  fi
}

# scan_dir DIR: greps every .java file under DIR for the skip pattern and
# reports each hit. Returns non-zero (via the `found` flag below, checked
# by the caller) when at least one skip comment was found. Also sets the
# global SCANNED_COUNT to the number of .java files actually examined, so
# callers can fail closed on a vacuous scan (same "checked == 0" convention
# guard-htmx-loaded.sh uses) instead of reading "nothing scanned" as "nothing
# wrong" — a guard whose whole point is catching gobind's own silent green
# must not have a silent-green mode of its own.
SCANNED_COUNT=0
scan_dir() {
  local dir="$1"
  local found=0
  local file line name

  while IFS= read -r -d '' file; do
    SCANNED_COUNT=$((SCANNED_COUNT + 1))
    while IFS= read -r line; do
      name="$(extract_name "$line")"
      echo "❌ gobind-skip guard: ${file} — gobind silently dropped a binding: ${name}" >&2
      echo "   (${line#"${line%%[![:space:]]*}"})" >&2
      found=1
    done < <(grep -E "$SKIP_PATTERN" "$file" || true)
  done < <(find "$dir" -name '*.java' -print0)

  return "$found"
}

if [ "$#" -gt 0 ]; then
  # --- Fixture/test mode: scan a given directory, don't run gobind. ---
  FIXTURE_DIR="$1"
  if [ ! -d "$FIXTURE_DIR" ]; then
    echo "❌ gobind-skip guard: not a directory: ${FIXTURE_DIR}" >&2
    exit 1
  fi

  if scan_dir "$FIXTURE_DIR"; then
    echo "✓ gobind-skip guard: no skipped bindings found under ${FIXTURE_DIR}"
    exit 0
  else
    echo "❌ gobind-skip guard: gobind would silently drop the binding(s) named above." >&2
    echo "   The affected type must be declared IN the bound package (./mobile), not" >&2
    echo "   merely referenced from it — see mobile/mobile.go's BluetoothBridge doc" >&2
    echo "   comment for the pattern (ut-docs#1721, ut-docs#1735)." >&2
    exit 1
  fi
fi

# --- Real CI mode: run gobind against ./mobile and inspect its output. ---
cd "$ROOT_DIR"

if ! command -v gobind >/dev/null 2>&1; then
  echo "❌ gobind-skip guard: \`gobind\` not found on PATH. Install it with:" >&2
  echo "     go install golang.org/x/mobile/cmd/gobind@latest" >&2
  echo "   (same install this workflow's own \"Install gomobile/gobind\" step already runs)." >&2
  exit 1
fi

OUTDIR="$(mktemp -d)"
# Invoked indirectly via `trap ... EXIT`, not a direct call -- shellcheck cannot see that (SC2317 false positive).
# shellcheck disable=SC2317
cleanup() {
  rm -rf "$OUTDIR"
}
trap cleanup EXIT

# GOOS=android/GOARCH=arm64 matches what android/app/build.gradle.kts's
# generateAar task actually binds against (`gomobile bind -target=android/
# arm64,android/arm`, which itself shells out to gobind under those build
# constraints) — gobind's own type-eligibility analysis is Go-build-tag-
# aware, so a hypothetical mobile/*_android.go file could reference an
# unbound type invisible to a host-GOOS run. Today mobile/ has no
# platform-tagged files, so this is a no-op in practice (verified: linux
# and android/arm64 generated output are byte-for-byte identical), but
# pinning it costs nothing and closes that gap for whenever one is added.
if ! GOOS=android GOARCH=arm64 gobind -lang=java -outdir="$OUTDIR" ./mobile; then
  echo "❌ gobind-skip guard: \`gobind -lang=java -outdir=${OUTDIR} ./mobile\` itself failed" >&2
  echo "   (non-zero exit) — this is a real gobind failure, not a skipped-binding one;" >&2
  echo "   see gobind's own output above." >&2
  exit 1
fi

JAVA_DIR="${OUTDIR}/java"
if [ ! -d "$JAVA_DIR" ]; then
  echo "❌ gobind-skip guard: gobind exited 0 but produced no ${JAVA_DIR} directory —" >&2
  echo "   its output layout may have changed; this guard needs updating." >&2
  exit 1
fi

SCAN_RESULT=0
scan_dir "$JAVA_DIR" || SCAN_RESULT=$?

# Fail closed: a scan that examined zero .java files is not a clean bind —
# it's a guard that stopped guarding (gobind's output layout changed, the
# package produced no Java surface, etc.). Silently passing here would be
# exactly the "exits 0 having done nothing real" failure mode this whole
# card exists to eliminate, just one layer up. (Fixture mode is exempt —
# a mid-test fixture's file count is asserted by the test itself.)
if [ "$SCANNED_COUNT" -eq 0 ]; then
  echo "❌ gobind-skip guard: gobind exited 0 and produced ${JAVA_DIR}, but it contains" >&2
  echo "   zero .java files — nothing was actually scanned. Either ./mobile's bound" >&2
  echo "   surface is now empty, or gobind's output shape changed; this guard needs" >&2
  echo "   updating rather than silently passing." >&2
  exit 1
fi

if [ "$SCAN_RESULT" -eq 0 ]; then
  echo "✓ gobind-skip guard: gobind bound ./mobile with no silently skipped functions (${SCANNED_COUNT} file(s) scanned)"
  exit 0
else
  echo "❌ gobind-skip guard: gobind silently dropped the binding(s) named above from" >&2
  echo "   ./mobile's generated Java/Kotlin API (exit code was 0 — this is exactly the" >&2
  echo "   failure mode ut-docs#1735 exists to catch). The affected parameter/return" >&2
  echo "   type must be declared IN ./mobile itself, not merely referenced from it —" >&2
  echo "   see mobile/mobile.go's BluetoothBridge doc comment for the pattern" >&2
  echo "   (ut-docs#1721)." >&2
  exit 1
fi
