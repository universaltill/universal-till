# Code review — nightly asset-prune loop exits 1 when the last asset is kept (ut-docs#2360)

- **Date:** 2026-09-17
- **Ticket:** ut-docs#2360 (`complexity:easy`)
- **Branch:** `feat/2360-nightly-asset-prune-exit1`
- **Reviewer:** independent pass, fresh-context Sonnet subagent working in
  an isolated worktree (per this card's `complexity:easy` routing —
  `MODEL-ROUTING.md`'s "different model relaxes to different instance"
  rule for easy cards).
- **Verdict: SAFE TO MERGE**, with one should-fix folded in before merge
  (below) rather than deferred, since it was a ~15-line change to the
  exact function the reviewer had just verified.

## The bug

`.github/workflows/nightly.yml`'s publish step pruned stale release
assets with:
```bash
[ "$keep" = 0 ] && gh release delete-asset nightly "$name" --yes
```
inside a `for` loop under `set -euo pipefail`. When the LAST asset
iterated is one to KEEP, `[ "$keep" = 0 ]` is false, the `&&` list
short-circuits to exit status 1, and with nothing after it to reset `$?`,
that 1 becomes the `for` loop's own exit status — `set -e` then fails the
whole step, even though every upload had already succeeded. Confirmed
from real `Nightly` run logs (run 35199965466, 2026-09-17 08:29 UTC): all
four builds green, tag moved, all four archives uploaded, then
`Process completed with exit code 1` right after the asset list — this
had been happening on every nightly since the prune step landed.

## What shipped

- `scripts/ci/prune-nightly-assets.sh` (new): extracts the loop into a
  `prune()` function using `if`/`fi` instead of the `&&`-guard (the fix);
  `list_assets`/`delete_asset` are separately-named functions so a test
  can redefine them and exercise `prune()` with no live `gh`/network call.
  Guarded by `if [ "${BASH_SOURCE[0]}" = "${0}" ]` so sourcing it for
  tests never touches `dist/`/a real release.
- `scripts/ci/prune-nightly-assets_test.sh` (new): sources the script,
  fakes `list_assets`/`delete_asset`, and asserts the exact regression
  shape (last asset is a keeper → must still exit 0), a normal mixed
  prune, an empty-KEEP-list prune, a genuinely empty release, and that a
  `list_assets` failure itself propagates as a real failure (see below).
- `.github/workflows/nightly.yml`: the inline loop replaced with
  `bash scripts/ci/prune-nightly-assets.sh`.
- `.github/workflows/ci.yml`: the new test wired into the main `build`
  job, alongside the pre-existing `prune-stale-worktrees_test.sh` /
  `retry-with-backoff_test.sh` (same rationale already established there:
  hermetic, no network, cheap enough to run on every PR rather than only
  once a day when the real nightly workflow fires).

## What the independent review found

Ran the full gate itself, not on trust: `bash
scripts/ci/prune-nightly-assets_test.sh`, `shellcheck` on both new files
(0 issues, version 0.9.0 — confirmed against `guard-shellcheck-version.sh`'s
pinned `BASELINE_VERSION="0.9.0"`, an exact match, so no version-drift
blind spot), `go build ./...` (unaffected, confirms nothing broke), and
independently re-parsed both touched YAML files.

**TDD re-verification, done for real by the reviewer independently**:
reverted `prune()`'s `if`/`fi` back to the literal buggy
`[ "${keep_this}" = 0 ] && delete_asset "${name}"` pattern, reran the
test, watched 2 of 3 cases fail with exactly the predicted regression
message, restored the fix, confirmed all cases pass again with a clean
diff.

One finding, folded in before merge:

1. **should-fix, folded in** — `list_assets` was read directly off a
   `done < <(list_assets)` process substitution. A process substitution's
   exit status is invisible to `set -e`/the parent shell, so a genuine
   `gh release view` failure (network blip, rate limit) would have looked
   identical to "the release has zero assets" and the step would have
   silently, wrongly succeeded having pruned nothing. Not a new
   regression — the *original* inline loop had the identical gap for the
   same reason — but the whole point of this card is correct `set -e`
   failure semantics, so it's fixed in the same diff rather than carried
   forward: `prune()` now does `assets="$(list_assets)"` first (a real
   command substitution, whose failure DOES propagate under `set -e` when
   the call is a bare top-level statement, which is exactly the shape the
   real script uses) and reads from `<<<"${assets}"` instead.

   Verifying this needed its own small correction to the review's first
   draft test: `list_assets() { return 1; }; prune ... || status=$?` looks
   like it tests the failure path, but empirically does not — `set -e` is
   disabled for the **entire execution** of a command that is the left
   side of `||` (or the tested part of `if`), including every statement
   inside a function it calls, transitively. Confirmed with a throwaway
   repro (`f() { x="$(false)"; echo reached; }; f || true` prints
   "reached" and returns 0 — the internal failure never fires errexit).
   The real script never calls `prune` that way (it's a bare statement
   inside `if [ "$BASH_SOURCE" = "$0" ]; then ... fi`, where errexit is
   fully live), so the test now spawns a real child `bash -c` process for
   this one case — a fresh top-level `set -e` context that actually
   matches the real invocation shape — and captures *that* process's exit
   code instead. Confirmed failing before the `assets="$(list_assets)"`
   fix landed and passing after.

Also checked, no action needed: `RELEASE="${RELEASE:-nightly}"` is live
(not dead code) but currently always resolves to the default, since
nothing in `nightly.yml` sets it — an override knob nobody exercises yet.
An asset filename containing a space is handled correctly by the new
`while IFS= read -r` + array-based KEEP comparison (the *original*
unquoted `for name in $(...)` would have word-split such a name into
bogus iterations — an incidental fix, not something asked for). No real
client/shop name or secret-shaped literal in the new fixtures.

## Specific checks the review ran

- **Placement of the new test in `ci.yml`'s main `build` job**: confirmed
  appropriate, not just accepted on the implementer's say-so — it follows
  an established, pre-existing convention in this exact file
  (`prune-stale-worktrees_test.sh`/`retry-with-backoff_test.sh` sit right
  next to it for the identical reason), and catches a regression at PR
  time instead of only once a day when the real `nightly.yml` next fires.
- **Recurring bug classes**: n/a — no file writes, and both the workflow's
  `bash scripts/ci/...` call and the test's own `$(dirname
  "${BASH_SOURCE[0]}")/../..` root resolution are consistent with how
  every other step in this repo resolves paths; nothing cwd-fragile.
- **Backend/infra-only, confirmed**: pure CI/bash, no template/`web/`/
  locale file touched — the UX-guidelines checklist and help-topic-update
  requirement don't apply.

## Explicitly deferred (not this card)

- None — the one finding was folded in before merge.
