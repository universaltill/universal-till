# Code review: guard-docs-shots.sh failure message accuracy (#417)

**Date:** 2026-08-23
**Card:** universaltill/ut-docs#417
**Branch:** `fix/417-guard-docs-shots-message`

## What shipped

`scripts/ci/guard-docs-shots.sh`'s surface-staleness failure message named
only `web/ui/**` and `internal/pages/**.go` as the hashed "app surface,"
omitting `web/public/**` even though the script's own `surface_hash()`
function hashes all three (per the script's own "HASH ALGORITHM" comment
block: "a theme/app.css change is exactly as visible in a screenshot as a
template change"). A CSS-only change to `web/public/app.css` correctly
fails the guard but the message didn't say why — this plausibly
contributed to a wrong assumption during #413 that a CSS-only diff needed
no `make docs-shots` regen.

Fixed both places in the file that stated the hashed fileset incompletely:
- The `print()` failure message (the reported bug).
- The top-of-file header comment, same omission, same root cause — found
  by the independent review below and fixed in the same commit since it's
  a one-line, same-file, same-category fix.

No change to the guard's actual hashing/comparison logic — message/
comment text only.

## Independent review

Reviewed by a fresh-context Sonnet subagent (complexity:easy →
Sonnet-builds/Sonnet-reviews per the pipeline's model routing), with no
visibility into the implementation reasoning.

**Verdict: SAFE TO MERGE.**

What it verified:
- Diff scope: only the print string changed in the reviewed commit (no
  control-flow/hashing logic touched).
- Message accuracy: cross-checked the new text against `surface_hash()`'s
  actual fileset loop (`("web/ui", False), ("web/public", False),
  ("internal/pages", True)`) — matches exactly, same order, nothing
  omitted or invented.
- Ran `scripts/ci/guard-docs-shots_test.sh` — all 6 cases pass (the test
  asserts on the substring "the app surface," preserved).
- Manually reproduced the original bug: appended a comment to
  `web/public/app.css`, ran the guard, confirmed the corrected message
  fires and names `web/public/**`; reverted, confirmed clean tree.
- `bash -n` syntax check clean; `gofmt -l .` clean (no `.go` files
  touched).
- No secret-shaped literals or real client/shop names in the diff.
- Manual-topic-update rule (product-owner standing instruction,
  ut-docs#324) doesn't apply — this is a CI script's own stderr message,
  not anything a shop owner sees or does.

**Finding raised and fixed in this same commit:** the top-of-file header
comment (separate from the print message) had the identical `web/public/**`
omission. Same root cause as the reported bug, one line, same file — fixed
alongside rather than filed as a separate follow-up.

## Verified beyond automated tests

- Live-reproduced the exact #413 scenario (CSS-only `web/public/app.css`
  change) against the fixed script and confirmed the message now names the
  actual reason for the failure.
- Re-ran the full `guard-docs-shots_test.sh` suite after the second
  (header-comment) fix, not just after the first — all 6 cases still pass.

## Safe-to-merge verdict

Yes. Message/comment-accuracy-only change, zero logic delta, full existing
test suite green, bug scenario manually reproduced and confirmed fixed.

## Explicitly deferred

Nothing — both instances of the same omission in this file are fixed by
this change. No further follow-up needed.
