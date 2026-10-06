# Review — check-lang-pack-drift.sh header comment (ut-docs#3579, item 3)

**PR:** universal-till#1712 (branch `docs/3579-lang-pack-drift-header`)
**Author model:** Sonnet 5 · **Reviewer:** Opus 5.5 (different model), lane:cloud-54 PR-sweep

## What shipped
`scripts/ci/check-lang-pack-drift.sh`'s header said "Adding a third pack
later is a one-line edit to PACKS below", but `PACKS` already lists three
packs (de, es, pt). The comment now says PACKS lists every known external
pack and that adding one is a one-line edit — no count hard-coded, so it
can't go stale again on the next pack. Comment-only; no logic changed.

## Findings
- None blocking. The author's own earlier review already removed a
  re-hardcoded "(de, es, pt)" list (would have gone stale the same way).
- Consistent with `CLAUDE.md`'s rule to never hard-code pack names outside
  the script's `PACKS` array.

## Verified
- Diff vs merge-base is exactly the one comment block (2 lines → 3 lines).
- `bash -n` clean; `scripts/ci/check-lang-pack-drift.test.sh` passes
  (all cases) on the branch after merging `origin/main`.
- `shellcheck` not installed in this container — CI's `build` job runs it.

## Verdict
Safe to merge once CI is green on the refreshed head (the previous run's
`compile`/`lang-pack-drift`/`playwright` were *cancelled*, not failed).

## Deferred
Nothing. Items 1–2 of ut-docs#3579 landed in ut-docs#3722.
