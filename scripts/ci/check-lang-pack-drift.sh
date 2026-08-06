#!/usr/bin/env bash
#
# Core-side language-pack drift check (ut-docs#299).
#
# WHY THIS EXISTS: ut-docs#292 (and #296) gave each external
# ut-plugin-language-* repo its own ratcheting key-drift guard
# (scripts/check-key-drift.sh), triggered on that repo's own push/PR plus a
# weekly `schedule:` cron so drift *originating in core* -- a new key lands
# in web/locales/en.json, nothing in the pack repo ever changes, so its push
# trigger never fires -- still gets caught. But GitHub auto-disables a
# `schedule:`-triggered workflow in a PUBLIC repo after 60 days with no
# activity in THAT repo, and "the pack repo has gone quiet" is exactly the
# condition the cron exists to cover -- so its coverage decays to zero
# precisely when it's needed most, and the failure is silent (no run, no
# red X, nothing). See ut-docs#299 for the full writeup; ut-docs#281 is the
# same class of problem for ut-infra/uptime.yml, tracked separately.
#
# THE FIX: run each pack's own drift check from HERE instead of relying
# solely on the pack repo's idle-prone schedule. universal-till gets pushed
# to constantly, so a `push: branches: [main]` trigger here can never go
# 60 days quiet. This script does not reimplement the drift-check logic --
# it fetches each pack's OWN scripts/check-key-drift.sh (plus its locale
# JSON and baseline/allowlist files) straight from GitHub over an
# unauthenticated raw.githubusercontent.com read (same trust model the
# pack's own script already uses to fetch core's en.json: public repo,
# no token, hard-fail if unreachable) and runs that unmodified script
# against CORE'S OWN LOCAL en.json via UT_CORE_EN_JSON. One canonical
# check, exercised from two places. (Actually de-duplicating the *pack-side*
# copies of check-key-drift.sh into one shared implementation is a separate,
# larger concern -- ut-docs#312 -- and is out of scope here.)
#
# A pack going unreachable (deleted, renamed, network blip) is a HARD
# failure, never a silent skip -- same rule the pack scripts themselves
# follow, for the same reason: a guard that quietly exits 0 when it can't
# fetch its own input is the exact silent-gap failure mode this exists to
# close.
#
# Adding a third pack later is a one-line edit to PACKS below.
#
# Usage: scripts/ci/check-lang-pack-drift.sh
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CORE_EN_JSON="${ROOT_DIR}/web/locales/en.json"
[ -f "$CORE_EN_JSON" ] || { echo "check-lang-pack-drift: $CORE_EN_JSON missing" >&2; exit 1; }

RAW_BASE="${UT_RAW_BASE:-https://raw.githubusercontent.com/universaltill}"

# repo:locale-code, one per known external language-pack plugin.
PACKS=(
    "ut-plugin-language-de:de"
    "ut-plugin-language-es:es"
)

WORKDIR="$(mktemp -d)"
cleanup() { rm -rf "$WORKDIR"; }
trap cleanup EXIT INT TERM

fetch() {
    # $1 = repo, $2 = path in repo, $3 = local destination
    local repo="$1" path="$2" dest="$3"
    local url="${RAW_BASE}/${repo}/main/${path}"
    mkdir -p "$(dirname "$dest")"
    if ! curl -fsSL "$url" -o "$dest"; then
        echo "check-lang-pack-drift: FAILED to fetch ${url}" >&2
        echo "check-lang-pack-drift: cannot verify ${repo}'s key parity -- refusing to pass silently." >&2
        exit 1
    fi
}

overall_fail=0

for entry in "${PACKS[@]}"; do
    repo="${entry%%:*}"
    code="${entry##*:}"
    pack_dir="${WORKDIR}/${repo}"

    echo "check-lang-pack-drift: checking ${repo} (locale: ${code})"

    fetch "$repo" "scripts/check-key-drift.sh" "${pack_dir}/scripts/check-key-drift.sh"
    fetch "$repo" "locales/${code}.json" "${pack_dir}/locales/${code}.json"
    fetch "$repo" "i18n-baseline/${code}.untranslated.txt" "${pack_dir}/i18n-baseline/${code}.untranslated.txt"
    fetch "$repo" "i18n-baseline/${code}.same-as-en.txt" "${pack_dir}/i18n-baseline/${code}.same-as-en.txt"

    if ! UT_CORE_EN_JSON="$CORE_EN_JSON" bash "${pack_dir}/scripts/check-key-drift.sh"; then
        echo "check-lang-pack-drift: ${repo} FAILED (see above)" >&2
        overall_fail=1
    else
        echo "check-lang-pack-drift: ${repo} ok"
    fi
    echo
done

if [ "$overall_fail" -ne 0 ]; then
    echo "check-lang-pack-drift: one or more language packs have drifted from core -- see above." >&2
    exit 1
fi

echo "check-lang-pack-drift: all known language packs are in sync with core."
