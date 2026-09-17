#!/usr/bin/env bash
# ut-docs#2360: prunes every asset on the `nightly` GitHub release that
# isn't one of today's freshly-uploaded files (today's filenames embed
# today's version/date/commit, so a previous run's differently-named
# assets are never overwritten by `gh release upload --clobber` and would
# otherwise accumulate forever).
#
# Extracted from .github/workflows/nightly.yml's former inline loop after
# a `set -e` + trailing `&&`-guard combination made the whole publish step
# fail whenever the LAST asset iterated was one to KEEP:
#   [ "$keep" = 0 ] && gh release delete-asset nightly "$name" --yes
# When $keep is 1, that `&&` list short-circuits and evaluates to 1 without
# running anything after it — and with nothing to reset $? afterward, that
# 1 becomes the `for` loop's own exit status, which `set -e` then treats as
# the whole step failing, even though every upload had already succeeded.
#
# list_assets/delete_asset are their own named functions (not inlined into
# prune()) so prune-nightly-assets_test.sh can redefine them after
# `source`-ing this file and exercise the real prune() loop against fake
# data, with no live release or network involved.
set -euo pipefail

RELEASE="${RELEASE:-nightly}"

list_assets() {
  gh release view "${RELEASE}" --json assets --jq '.assets[].name'
}

delete_asset() {
  gh release delete-asset "${RELEASE}" "$1" --yes
}

# prune KEEP...: deletes every asset list_assets() reports that isn't one
# of the KEEP names passed as positional arguments. An `if`, never `&&`, so
# this loop's own exit status is never anything but 0 from a normal pass —
# the bug this script exists to fix.
#
# list_assets is captured into a variable first, not read straight off a
# `done < <(list_assets)` process substitution (independent review,
# ut-docs#2360) — a process substitution's exit status is invisible to
# `set -e`/the parent shell, so a genuine `gh release view` failure
# (network blip, rate limit) would otherwise look identical to "the
# release has zero assets" and prune silently, wrongly, succeeds having
# done nothing. Assigning `assets="$(list_assets)"` is a real command
# substitution, whose failure DOES propagate under `set -e`.
prune() {
  local -a keep=("$@")
  local name keep_this k assets
  assets="$(list_assets)"
  while IFS= read -r name; do
    [ -z "${name}" ] && continue
    keep_this=0
    for k in "${keep[@]}"; do
      if [ "${name}" = "${k}" ]; then
        keep_this=1
        break
      fi
    done
    if [ "${keep_this}" = 0 ]; then
      delete_asset "${name}"
    fi
  done <<<"${assets}"
}

# Only run when executed directly, never when sourced (the test file
# sources this to reach prune()/list_assets()/delete_asset() directly).
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  mapfile -t KEEP < <(ls dist)
  prune "${KEEP[@]}"
fi
