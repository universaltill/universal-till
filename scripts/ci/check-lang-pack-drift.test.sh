#!/usr/bin/env bash
# Tests for scripts/ci/check-lang-pack-drift.sh (ut-docs#299, extended
# ut-docs#1857).
#
# The real script always fetches each pack's OWN check-key-drift.sh (plus
# its locale/baseline/allowlist files) live from GitHub -- it does not
# reimplement that logic itself (see this repo's script header: "one
# canonical check, exercised from two places" -- true of the *design*, not
# of the file: each pack repo keeps its own copy, and that copy hardcodes
# its own locale variable name, see pack_script_source below). So these
# tests don't stub any of that; they stand up a throwaway local HTTP
# server serving fixture pack repos (using each pack's REAL
# check-key-drift.sh, copied verbatim from a real checkout, same as
# production always fetches) and point the script under test at it via
# UT_RAW_BASE / UT_API_BASE -- both already overridable in the real script
# for exactly this reason. Each case asserts BOTH the exit code AND that
# the failure output actually names the right pack repo(s) -- ut-docs#1857
# exists specifically because "a pack drifted" alone isn't actionable
# enough; the new "pack(s) needing a follow-up PR" block naming the exact
# repo URL(s) is what this file exists to guard.
set -euo pipefail
cd "$(dirname "$0")/../.."

REAL_SCRIPT="$(pwd)/scripts/ci/check-lang-pack-drift.sh"

# pack_script_source REPO
# The pack-side check-key-drift.sh is NOT one shared file despite the
# "one canonical check, exercised from two places" framing (ut-docs#312) --
# each pack repo's copy hardcodes its OWN locale variable (e.g.
# `DE_LOCALE="locales/de.json"` in the de pack, `ES_LOCALE="locales/es.json"`
# in the es pack), so using the wrong repo's copy as a fixture silently
# looks for the wrong locale filename. Resolve per-repo: a local sibling
# checkout if one happens to be on disk (fast path for a dev/pipeline
# session that already cloned the packs), else fetch the real file from
# GitHub the same way check-lang-pack-drift.sh itself does in production --
# this is what makes the test runnable on a bare CI checkout of just this
# repo, which has no sibling pack checkouts at all. Cached per repo per
# test run so N fixture_pack calls for the same repo across cases fetch it
# once, not N times.
pack_script_source() {
    local repo="$1"
    local cache_var="PACK_SCRIPT_CACHE_$(echo "$repo" | tr 'a-z-' 'A-Z_')"
    local cached="${!cache_var:-}"
    if [ -n "$cached" ] && [ -f "$cached" ]; then
        echo "$cached"
        return 0
    fi

    local override_var="UT_TEST_PACK_SCRIPT_$(echo "$repo" | tr 'a-z-' 'A-Z_')"
    local override="${!override_var:-}"
    if [ -n "$override" ] && [ -f "$override" ]; then
        printf -v "$cache_var" '%s' "$override"
        echo "$override"
        return 0
    fi

    # Independent review, MEDIUM: relative to this repo's own root (we
    # already `cd` there above), not hardcoded to one container's absolute
    # home layout -- covers a flat sibling checkout (`../<repo>`) and this
    # pipeline's own anonymous-read layout (`../universaltill/<repo>`)
    # without baking in a path that only this session's disk happens to have.
    local candidate
    for candidate in \
        "../${repo}/scripts/check-key-drift.sh" \
        "../universaltill/${repo}/scripts/check-key-drift.sh"; do
        if [ -f "$candidate" ]; then
            printf -v "$cache_var" '%s' "$candidate"
            echo "$candidate"
            return 0
        fi
    done

    # No local checkout (the real case on a bare CI runner) -- fetch the
    # live script, exactly like check-lang-pack-drift.sh's own `fetch`
    # helper does. A network failure here is a real "can't run this test"
    # failure, not a silent skip -- same standing rule the script itself
    # follows for its own fetches.
    local dest="${TMPDIR:-/tmp}/check-lang-pack-drift.test.${repo}.sh"
    if curl --fail --silent --show-error --location \
        --retry 3 --retry-all-errors --retry-delay 2 \
        --connect-timeout 10 --max-time 60 \
        "https://raw.githubusercontent.com/universaltill/${repo}/main/scripts/check-key-drift.sh" \
        -o "$dest"; then
        printf -v "$cache_var" '%s' "$dest"
        echo "$dest"
        return 0
    fi

    echo "check-lang-pack-drift.test.sh: cannot find or fetch a real check-key-drift.sh for ${repo} (set ${override_var}=<path> to use a local one)" >&2
    return 1
}
# Fail fast at load time if either pack's real script isn't reachable, same
# as before -- better than discovering it mid-case.
pack_script_source ut-plugin-language-de > /dev/null
pack_script_source ut-plugin-language-es > /dev/null

FAILS=0
server_pid=""
work_dir=""

cleanup() {
    if [ -n "$server_pid" ]; then
        kill "$server_pid" >/dev/null 2>&1 || true
        wait "$server_pid" 2>/dev/null || true
    fi
    [ -n "$work_dir" ] && rm -rf "$work_dir"
}
trap cleanup EXIT INT TERM

# fresh_case: tears down any previous case's server/tmp dir and stands up a
# brand-new one -- each case gets a clean fixture root and its own server
# on a fresh port (port 0 == OS-assigned, avoids clashing with a port a
# previous case's not-yet-reaped server might still hold).
#
# check-lang-pack-drift.sh resolves its OWN core en.json from its own
# on-disk location (`ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.."
# && pwd)"` -- there is no env override for this one, unlike the pack-side
# script it calls). So, same technique check-key-drift.test.sh already uses
# for that pack-side script: copy the REAL script into a throwaway
# "repo"-shaped fixture (fixture_root/scripts/ci/check-lang-pack-drift.sh +
# fixture_root/web/locales/en.json) and invoke it from there, rather than
# trying to point the real repo's copy at fake data.
fresh_case() {
    if [ -n "$server_pid" ]; then
        kill "$server_pid" >/dev/null 2>&1 || true
        wait "$server_pid" 2>/dev/null || true
        server_pid=""
    fi
    [ -n "$work_dir" ] && rm -rf "$work_dir"
    work_dir="$(mktemp -d)"
    server_root="${work_dir}/server"
    mkdir -p "$server_root"
    fixture_root="${work_dir}/fixture-repo"
    mkdir -p "${fixture_root}/scripts/ci" "${fixture_root}/web/locales"
    cp "$REAL_SCRIPT" "${fixture_root}/scripts/ci/check-lang-pack-drift.sh"
    chmod +x "${fixture_root}/scripts/ci/check-lang-pack-drift.sh"
    core_json="${fixture_root}/web/locales/en.json"
}

# fixture_pack REPO CODE MODE
# Populates the fixture server root for one pack repo. MODE is:
#   sync    -- every core key present, translated to a value that is
#              neither empty nor byte-identical to core's (real "in sync",
#              not just same-content -- an identical value would itself
#              trip the untranslated-but-present check, which is a
#              different, correct signal this fixture must not confuse
#              with the thing under test)
#   drift   -- locale file missing one key core has (real new drift, not
#              in any baseline)
#   unreachable -- don't write this repo's raw-file fixtures (script/
#              locale/baseline/allowlist), so the script's `fetch` 404s and
#              hard-fails for this repo. The commits/main API fixture is
#              still written (independent review, LOW: modeling "repo
#              deleted" would need to skip that too, but the outcome is
#              identical either way -- that call is best-effort and never
#              gates the check).
# All fixtures get empty baseline/allowlist files (ratcheting behavior is
# tested by the pack-side check-key-drift.test.sh, not re-tested here).
fixture_pack() {
    local repo="$1" code="$2" mode="$3"
    local base="${server_root}/${repo}"
    local script_source
    script_source="$(pack_script_source "$repo")"

    mkdir -p "${base}/commits"
    printf '{"sha":"test-%s"}' "$repo" > "${base}/commits/main"

    [ "$mode" = "unreachable" ] && return 0

    mkdir -p "${base}/main/scripts" "${base}/main/locales" "${base}/main/i18n-baseline"
    cp "$script_source" "${base}/main/scripts/check-key-drift.sh"
    : > "${base}/main/i18n-baseline/${code}.untranslated.txt"
    : > "${base}/main/i18n-baseline/${code}.same-as-en.txt"

    if [ "$mode" = "sync" ]; then
        python3 - "$core_json" "${base}/main/locales/${code}.json" <<'PY'
import json, sys
core = json.load(open(sys.argv[1]))
# A real, non-empty, non-identical "translation" of every core key -- full
# parity without tripping the empty-value or identical-to-English checks.
pack = {k: f"[xx] {v}" for k, v in core.items()}
json.dump(pack, open(sys.argv[2], "w"))
PY
    elif [ "$mode" = "drift" ]; then
        python3 - "$core_json" "${base}/main/locales/${code}.json" <<'PY'
import json, sys
core = json.load(open(sys.argv[1]))
# Same "real translation" as the sync case, MINUS one key -- new,
# un-baselined drift, and nothing else the guard could also flag.
dropped = sorted(core.keys())[0]
pack = {k: f"[xx] {v}" for k, v in core.items() if k != dropped}
json.dump(pack, open(sys.argv[2], "w"))
PY
    else
        echo "fixture_pack: unknown mode $mode" >&2
        return 1
    fi
}

start_server() {
    # Independent review, MEDIUM: this used to background `( cd X && python3
    # ... )`, which captures $! of the WRAPPER SUBSHELL, not of python3 --
    # every case's `kill "$server_pid"` was therefore killing a process
    # that either already exited or was never python, leaking one orphaned
    # `http.server` per case (41 counted across a review + my own runs,
    # confirmed by pid/ppid mismatch). `--directory` (Python 3.7+) serves
    # the fixture root without a `cd`, so `$!` here is python3's own real
    # pid.
    python3 -m http.server 0 --bind 127.0.0.1 --directory "$server_root" \
        > "${work_dir}/server.log" 2>&1 &
    server_pid=$!
    # python3 -m http.server logs its actual bound port to stderr on
    # startup ("Serving HTTP on 127.0.0.1 port NNNNN") -- port 0 above asks
    # the OS for a free one so parallel test runs never collide.
    local port=""
    for _ in $(seq 1 20); do
        port="$(grep -oE 'port [0-9]+' "${work_dir}/server.log" 2>/dev/null | grep -oE '[0-9]+' || true)"
        [ -n "$port" ] && break
        sleep 0.2
    done
    if [ -z "$port" ]; then
        echo "check-lang-pack-drift.test.sh: fixture HTTP server never reported its port" >&2
        cat "${work_dir}/server.log" >&2 || true
        exit 1
    fi
    server_base="http://127.0.0.1:${port}"
}

run_check() {
    UT_RAW_BASE="$server_base" UT_API_BASE="$server_base" \
        bash "${fixture_root}/scripts/ci/check-lang-pack-drift.sh"
}

assert_pass() {
    local name="$1" out rc
    set +e
    out="$(run_check 2>&1)"
    rc=$?
    set -e
    if [ "$rc" -ne 0 ]; then
        echo "FAIL [$name]: expected exit 0, got $rc. Output:"
        echo "$out"
        FAILS=$((FAILS + 1))
        return
    fi
    if echo "$out" | grep -q "pack(s) needing a follow-up PR"; then
        echo "FAIL [$name]: exit 0 but still printed a follow-up-PR block. Output:"
        echo "$out"
        FAILS=$((FAILS + 1))
        return
    fi
    echo "ok   [$name]"
}

# assert_fail_repos NAME EXPECTED_REPO [EXPECTED_REPO...]
# Asserts non-zero exit AND that the "pack(s) needing a follow-up PR:"
# block names EXACTLY the given repos (as full GitHub URLs) -- no more, no
# fewer. A block naming an unaffected pack would send someone to open a PR
# against a repo that was never actually in drift.
assert_fail_repos() {
    local name="$1"
    shift
    local out rc
    set +e
    out="$(run_check 2>&1)"
    rc=$?
    set -e
    if [ "$rc" -eq 0 ]; then
        echo "FAIL [$name]: expected non-zero exit, got 0. Output:"
        echo "$out"
        FAILS=$((FAILS + 1))
        return
    fi
    # Independent review, MEDIUM: this file runs under `set -euo pipefail`
    # (top of file). A `grep -oE` with no match exits 1, which under
    # pipefail kills the WHOLE TEST SCRIPT right here -- confirmed live: a
    # real revert of the fix under test produced exactly one "ok" line then
    # a bare exit 1, with cases 2-4's own FAIL/want/got diagnostics never
    # printed at all. `|| true` in a subshell keeps a real "no match" from
    # ever reaching pipefail.
    local got
    got="$( (echo "$out" | grep -oE 'https://github\.com/universaltill/[A-Za-z0-9_.-]+' || true) | sort -u)"
    local want
    want="$(printf '%s\n' "$@" | sort -u)"
    if [ "$got" != "$want" ]; then
        echo "FAIL [$name]: follow-up-PR repo list mismatch."
        echo "  want: $(echo "$want" | paste -sd, -)"
        echo "  got:  $(echo "$got" | paste -sd, -)"
        echo "Full output:"
        echo "$out"
        FAILS=$((FAILS + 1))
        return
    fi
    echo "ok   [$name]"
}

# --- Case 1: both packs in sync -- clean pass, no follow-up-PR block ---
fresh_case
printf '{"a.b":"Hello","c.d":"World"}' > "$core_json"
fixture_pack ut-plugin-language-de de sync
fixture_pack ut-plugin-language-es es sync
start_server
assert_pass "both packs in sync"

# --- Case 2: only de has new drift -- follow-up block names de ONLY ---
fresh_case
printf '{"a.b":"Hello","c.d":"World"}' > "$core_json"
fixture_pack ut-plugin-language-de de drift
fixture_pack ut-plugin-language-es es sync
start_server
assert_fail_repos "only de drifts" "https://github.com/universaltill/ut-plugin-language-de"

# --- Case 3: both packs have new drift -- follow-up block names both ---
fresh_case
printf '{"a.b":"Hello","c.d":"World"}' > "$core_json"
fixture_pack ut-plugin-language-de de drift
fixture_pack ut-plugin-language-es es drift
start_server
assert_fail_repos "both packs drift" \
    "https://github.com/universaltill/ut-plugin-language-de" \
    "https://github.com/universaltill/ut-plugin-language-es"

# --- Case 4: es is unreachable (simulates a deleted/renamed/network-down
# pack repo) -- this must be a hard failure that STILL names the repo in
# the follow-up block, same as a real drift failure. This is the fetch-
# failure branch (not the check-key-drift.sh branch) -- ut-docs#1857's
# FAILED_REPOS tracking covers both, and this case is what pins that. ---
fresh_case
printf '{"a.b":"Hello","c.d":"World"}' > "$core_json"
fixture_pack ut-plugin-language-de de sync
fixture_pack ut-plugin-language-es es unreachable
start_server
assert_fail_repos "es unreachable" "https://github.com/universaltill/ut-plugin-language-es"

echo
if [ "$FAILS" -ne 0 ]; then
    echo "check-lang-pack-drift.test.sh: $FAILS case(s) FAILED"
    exit 1
fi
echo "check-lang-pack-drift.test.sh: all cases passed"
