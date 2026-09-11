# Code review: inventory & locations manual content (ut-docs#330)

**Date**: 2026-09-11
**Author**: Farshid Mirza (autonomous SDLC pipeline, `lane:cloud-54`) — Dev
phase delegated to a fresh-context Sonnet subagent (implement + verify,
no commit), orchestrated and committed by this session.
**Reviewer**: independent fresh-context Opus subagent (read-only pass, no
prior context on the change — per `complexity:medium` model routing)
**Ticket**: universaltill/ut-docs#330
**Branch**: `docs/330-inventory-locations-manual`

## What changed

Expanded `web/help/en/inventory.md` from 35 to ~85 lines to cover every
surface the card lists: the receive/adjust popup (goods-in, adjustments,
and the location-mismatch pitfall that creates a duplicate stock row), the
manager override panel, processing a return from `/inventory`, how a sale
and a refund change stock, low-stock alerts, and stock locations
(create/rename/activate/deactivate, moving stock between them).
`scripts/ci/i18n-baseline/help-drift-baseline.json` gained 4 entries
(ar/de/fa/tr × topic `inventory`) recording the resulting translation
drift, citing this card and deferring to ut-docs#341 — the same convention
already used for `till-designer`/`catalog`. `web/help/img/manifest.json`'s
`inventory.en` freshness hash was regenerated via `make docs-shots`.

Every step was verified by actually driving the live till
(`e2e/run-till.sh`, Playwright/Chromium against real UI), not written from
source alone, per the card's own explicit instruction.

## Verification performed (this session, independent of both subagents)

- Re-ran `scripts/ci/guard-help-topics.sh`, `guard-help-drift.sh`,
  `guard-compliance-claims.sh`, `guard-docs-shots.sh` myself — all green,
  both before and after the review-driven fixes below.
- Read the full diff personally before and after the fixes.
- Ran `make docs-shots` myself to regenerate the freshness hash after the
  post-review prose edits; reverted the three unrelated PNGs it
  regenerated with pure anti-aliasing noise (`catalog.png` en/ar/tr,
  `sell.png` ar — zero related content, confirmed via `git diff
  --stat`/`manifest.json`'s unchanged hashes for those topics), keeping
  only the `inventory.en` hash update.

## Independent review findings and disposition

The Opus review independently verified all six of the Dev subagent's
live-till findings against the actual Go source (`pos_repo.go`,
`inventory_api.go`, `locations_page.go`, the low-stock query, the return
form's template) and found no blockers — all six held. It raised:

1. **[should-fix, fixed] S1 — every sale/refund/import draws from a single
   hardcoded "Main" location**, not "the location the sale was rung up
   against" as the first draft said. `EnsureStockLocation`
   (`internal/data/pos_repo.go`) resolves one hardcoded row; there is no
   per-till/per-sale location choice anywhere in the sale, refund, kiosk
   or import paths. Reproduced live by the reviewer (stock split across
   two locations; a sale reduced Main only). This directly undercut the
   new multi-location content, so it's the one fix that mattered most —
   corrected the "How a sale and a refund change stock" section to state
   this plainly and warn about the resulting trap for a shop stocking a
   non-Main location.
2. **[should-fix, fixed] S2 — Activate is also main-till-only**, not just
   create/rename/deactivate as the first draft implied (`requirePrimary`
   runs before the activate/deactivate branch in
   `locations_page.go`). Fixed step 5's wording.
3. **[should-fix, done] S3 — file follow-up tickets** for the four real
   product defects the manual now documents rather than fixes (a
   documentation card correctly doesn't fix them, but they need their own
   record beyond the manual prose): ut-docs#2064 (return panel sends no
   lines), ut-docs#2065 (reorder level has no UI/import entry point),
   ut-docs#2066 (a used location can never be deactivated), ut-docs#2067
   (S1 itself — no per-till/per-sale stock location).
4. **[nit, fixed] N1** — the **+** button opens a *blank* dialog, not a
   prefilled one; only a row tap prefills. Corrected.
5. **[nit, fixed] N2** — "Process a return" refuses with "original sale
   not found" for an unrecognised receipt, not "at least one line
   required" (that message is specific to a receipt it *does* find).
   Corrected.
6. **[nit, not applied] N3** — the override panel's `qty_requested` field
   is rendered but never parsed/persisted server-side. Left the existing
   wording ("the quantity you found... and a reason") rather than
   describing a field that's silently discarded — simplifying to what's
   actually recorded is the more honest fix, and N3 is now moot since the
   authorizing-quantity phrase was dropped in the S1/S2 edit pass.
7. **[nit, fixed] N4** — the ⚠ low-stock chip is on the Stock Levels card
   heading here, and the Reports page's own header there — not "this
   page's header" for both. Corrected.
8. **[nit, fixed] N5** — deactivating the last active location is refused
   with a *different* message than the in-use refusal, and the in-use
   check fires first on any used location. Corrected.
9. **N6** — the refund wording ("same stock row the original sale took it
   from") only held because the refund path *also* hardcodes Main; this
   fell out naturally once S1 was corrected to name Main explicitly.
10. **N7** — informational only (the `/api/inventory/low-stock` route is
    correctly denylisted from `routes:` coverage; the surface is covered
    in prose). No action needed.

All should-fix items and applicable nits are applied on this branch.
Guards re-verified green after the edits (structural drift-baseline
counts unchanged by the prose-only fixes; the freshness hash was
regenerated to cover the additional edits).

## Verdict

**Approved**, with the should-fix items folded in before this commit and
four follow-up cards filed for the real product defects the manual now
accurately documents (ut-docs#2064, #2065, #2066, #2067). No blockers.
