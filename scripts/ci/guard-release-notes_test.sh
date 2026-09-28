#!/usr/bin/env bash
# Regression test for scripts/ci/guard-release-notes.sh (ut-docs#3091):
# each case builds a throwaway notes tree and asserts the exit code, and
# for the header mode that the Release body carries the note's text.
set -euo pipefail
cd "$(dirname "$0")/../.."
GUARD="$(pwd)/scripts/ci/guard-release-notes.sh"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail=0

write_note() { # dir tag version date body
    mkdir -p "$1/en"
    printf -- '---\nversion: %s\ndate: %s\n---\n%b' "$3" "$4" "$5" > "$1/en/$2.md"
}

expect() { # name want-exit args...
    local name="$1" want="$2"; shift 2
    local got=0
    UT_RELEASE_NOTES_DIR="$DIR" bash "$GUARD" "$@" >"$TMP/out" 2>&1 || got=$?
    if [ "$got" != "$want" ]; then
        echo "FAIL: ${name}: exit ${got}, want ${want}"; cat "$TMP/out"; fail=1
    else
        echo "ok: ${name}"
    fi
}

DIR="$TMP/good"; write_note "$DIR" v1.2.3 v1.2.3 2026-09-28 '## New\n\n- A thing.\n'
expect "note present" 0 v1.2.3
expect "missing note refuses" 1 v1.2.4
expect "non-semver tag refuses" 1 1.2.3
expect "no tag is a usage error" 2

DIR="$TMP/mismatch"; write_note "$DIR" v1.2.3 v1.2.2 2026-09-28 '## New\n\n- x\n'
expect "version mismatch refuses" 1 v1.2.3
DIR="$TMP/baddate"; write_note "$DIR" v1.2.3 v1.2.3 28/09/2026 '## New\n\n- x\n'
expect "bad date refuses" 1 v1.2.3
DIR="$TMP/empty"; write_note "$DIR" v1.2.3 v1.2.3 2026-09-28 '\n\n'
expect "empty note refuses" 1 v1.2.3
DIR="$TMP/nofront"; mkdir -p "$DIR/en"; printf '## New\n- x\n' > "$DIR/en/v1.2.3.md"
expect "no front-matter refuses" 1 v1.2.3

# The Go loader accepts CRLF and trailing blanks; the guard must too.
DIR="$TMP/crlf"; mkdir -p "$DIR/en"; printf -- '---\r\nversion: v1.2.3 \r\ndate: 2026-09-28\r\n---\r\n## New\r\n\r\n- x\r\n' > "$DIR/en/v1.2.3.md"
expect "CRLF + trailing blank passes" 0 v1.2.3

DIR="$TMP/good"
expect "header written" 0 v1.2.3 --header "$TMP/header.md"
if ! grep -qx "## What's new" "$TMP/header.md" || ! grep -qx '### New' "$TMP/header.md" \
   || ! grep -qx -- '- A thing.' "$TMP/header.md" || grep -q '^version:' "$TMP/header.md"; then
    echo "FAIL: header content"; cat "$TMP/header.md"; fail=1
else
    echo "ok: header content"
fi

# The real tree: the newest shipped note must pass (keeps the seed honest).
DIR="$(pwd)/web/release-notes"
latest="$(find "$DIR/en" -name 'v*.md' -exec basename {} .md \; | sort -V | tail -n 1)"
expect "newest shipped note (${latest}) passes" 0 "$latest"

exit "$fail"
