# Code review: hold-modal submit button duplicate accessible name (ut-docs#1628)

**Date:** 2026-09-06
**Branch:** `fix/1628-hold-modal-duplicate-accessible-name`
**Card:** universaltill/ut-docs#1628 (found reviewing PR #1625's own independent review)
**Complexity:** easy → reviewed by a fresh-context Sonnet subagent (per
`scrum-master`'s Model routing by complexity)

## What shipped

ut-docs#1625 gave `#payment-overlay`'s in-overlay Hold Sale/New Sale
duplicate buttons their own distinguishing `aria-label` so they no
longer collide with the plain-named originals in
`.tender-default-footer`. But `#hold-modal`'s own `<button
type="submit">` was also plain "Hold Sale" text with no `aria-label`,
and the dialog opens non-modally too (same on-screen-keyboard reasoning
as `#payment-overlay` — nothing outside it becomes `inert`). So once an
operator opens the dialog (from either trigger), its submit button
joined the accessible-name tree as a THIRD "Hold Sale"-named control.

Fix: added `hold.modal.confirm_action` = "Hold Sale (confirm)" (en;
ar/fa/tr translated) and `aria-label="{{ T "hold.modal.confirm_action"
}}"` on that submit button (`web/ui/pages/index.html`), following the
same "<visible text> (<context>)" convention #1625 established.

**Design note:** the issue left the exact qualifier as an Architect/UX
call. No existing "(confirm)"-shaped qualifier convention was found
elsewhere in the codebase to reuse verbatim, so `(confirm)` was chosen
as a short, WCAG-friendly qualifier describing the button's role
within the open dialog — the same shape as #1625's own
"(Tender panel)" qualifier, just naming a different context.

## TDD

Wrote the e2e spec (`hold-modal-duplicate-accessible-name-1628.spec.ts`)
first, ran it against the unfixed code to confirm it failed with
"Expected: 1, Received: 2" on the plain-"Hold Sale" accessible-name
count (proving the exact collision the issue describes), then applied
the fix and confirmed it passed. Re-ran it alongside every sibling
#1625/#1542/#1386/#1385 spec (12 total) to confirm no regression.

## Independent review

Dispatched to a fresh-context Sonnet subagent in an isolated git
worktree, briefed with the issue, the #1625 precedent, and an explicit
instruction to do its own revert-then-restore verification.

**Verdict: SAFE TO MERGE, no blocking issues.**

What it verified, independently:
- The aria-label landed on exactly the right button
  (`web/ui/pages/index.html:92-93`), nothing else in the dialog's
  markup changed, and the two #1625-fixed overlay-copy buttons plus
  `internal/pages/catalog/handlers.go` are untouched.
- All four locale files valid JSON, `guard-i18n.sh` passes, and it
  checked the ar/fa/tr translations semantically against the existing
  `hold.action`/`tender.overlay_hold_action` keys in each locale —
  each reuses the identical base phrase with a plausible
  "(confirm)"-shaped qualifier.
- Ran the new spec and all 4 sibling specs itself (12/12 passed).
- Independently reverted the aria-label, re-ran the new spec, confirmed
  it fails with the exact expected symptom, restored, confirmed it
  passes again.
- `bash scripts/ci/guard-docs-shots.sh` passes; spot-checked
  `web/help/img/{en,ar}/sell.png` — correct rendering, no regression.
  Noted the diff also touches `till-designer.png`/`multitill.png`
  (unrelated pages) — confirmed pixel-identical modulo PNG re-encoding,
  a side effect of `make docs-shots` regenerating the whole surface,
  not a real change.
- `gofmt`, `go build ./...`, `go vet ./...` (implicit via build),
  `golangci-lint run ./...` (0 issues), `go test ./...` all clean.
- No secret-shaped literals or real client/shop names in the diff.

## Verified beyond automated tests

- Full `go test ./...` and `golangci-lint run ./...` run personally
  before handing off to review.
- `make docs-shots` run personally; regenerated screenshots reviewed
  (`web/help/img/en/sell.png`) — correct layout, no visual regression
  (expected, since this is an aria-only change).

## Safe-to-merge verdict

Yes. Small, well-scoped, TDD'd, independently reviewed with its own
verification, full gate green, no deferred items.

## Non-goals (per the issue)

- Does not re-open #1625's own scope (the overlay-vs-original pair it
  already fixed).
