# Code review: remove now-dead `isManagerOrAuthOff` (ut-docs#721)

**Date**: 2026-08-14
**Card**: ut-docs#721
**Branch**: `pipeline/721-remove-dead-ismanagerorauthoff`
**Reviewer**: fresh-context Sonnet subagent (complexity:easy → fresh-context
same-tier review, per the `reviewer` skill's exception for easy cards).

## What this change does

Deletes `isManagerOrAuthOff` from `internal/pages/settings_page.go` — the
last piece of the `isManagerOrAuthOff` → `canPerform(d, r, action)` auth
migration tracked by the ut-docs#555 umbrella. All 5 subsystem-scoped
successor cards (#710, #706, #707, #709, #708→#712/#713) had already
converted every call site; #713 (merged as universal-till#348, just
before this cycle picked up #721) was the last one. This card's own
acceptance criteria explicitly deferred the removal to its own diff
rather than bundling it into #713's.

## Independent review — findings

Ran a fresh-context Sonnet subagent, told to verify everything itself
rather than trust the drafting description. Verdict: **PASS WITH NITS**
(one non-blocking nit, fixed before this record was written):

1. **Confirmed zero live call sites** — repo-wide grep found ~29 hits,
   every one inside a comment (mostly test files' explanatory notes,
   correctly left as historical context per the card's own non-goals).
2. **Confirmed build/test/guards green**, independently re-run (not
   trusted from the drafting session): `go build ./...`, `go test ./...`
   (including a `-count=1` targeted re-run of `internal/pages/...`, the
   changed package, bypassing cache), `guard-data-access.sh`,
   `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh` — all pass.
3. **Confirmed import hygiene** — `auth` and `os`, only used by the
   deleted function's body among the (very short) function itself, are
   both still used elsewhere in `settings_page.go` (`auth.Disabled(
   os.Getenv("UT_AUTH"))` and `auth.ErrLockedOut`), so no unused-import
   fallout.
4. **Spot-checked all 5 predecessor cards** (not just 2–3) — each is
   genuinely `status:done` with a merged PR (#346, #344, #345, #343,
   #347/#348). #721's premise checks out.
5. **Nit, non-blocking**: `internal/pages/authz.go`'s `canPerform` doc
   comment still described `isManagerOrAuthOff` as "staying in place
   until every call site has moved" — stale prose now that it's deleted.
   **Fixed**: reworded to past tense, referencing #713/#721 instead of
   describing an in-progress migration that has actually finished.

No blocking findings.

## Verification beyond the automated pass

- Confirmed personally that the diff touches exactly the two files noted
  above and nothing else (no scope creep).
- Confirmed the `#555` umbrella's own completion criterion — "all 5
  successors Done, `isManagerOrAuthOff` has no remaining callers, and it
  is removed" — is now fully met; closing #555 alongside this PR.

## Outcome

The one nit fixed on the branch. Ready to merge; closes ut-docs#721 and,
as its consequence, ut-docs#555 (the umbrella, whose own completion
criterion this PR satisfies).
