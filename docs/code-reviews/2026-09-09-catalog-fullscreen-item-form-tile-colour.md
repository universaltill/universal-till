# Code review: full-screen item form + tile colour (ut-docs#1901)

**Date:** 2026-09-09
**Branch:** `feat/1901-fullscreen-item-form-tile-colour`
**Card:** ut-docs#1901 — "Add-item is a cramped 360px side panel, not a
full-screen form — no tile colour, no modifier attachment"
**Complexity:** medium (Dev at Sonnet, review at Opus)

## What shipped

- The catalog add/edit-item form converted from a permanently-visible
  360px sticky side panel into a near-full-screen `<dialog>`
  (`.item-form-modal`), reclaiming the catalog list's width (confirms
  ut-docs#1842's row-action fix stays reachable).
- A fixed, curated 8-swatch tile colour on items (new `items.color`
  column, migration `019_items_color.sql`), server-side allowlisted
  (`catalogtypes.ItemColors()` / `ValidItemColor()`) since the stored
  value flows into a CSS custom property downstream — a real security
  control, not just UX. Rendered as the sale-screen tile's solid
  background for a photo-less item (`ButtonVM.Color`, `product-tile` in
  `buttons.html`) and a small dot in the catalog list.
- Category field carried into the new dialog; image/label-printing/
  keypad-mapper sections moved inside it.
- 13 new i18n keys across all 4 core locales (ar/en/fa/tr), real short
  translations.
- Help manual (`web/help/en/catalog.md`) updated for the new flow;
  screenshots regenerated.
- **Deliberately out of scope**: modifier-group attachment (one of the
  card's three ACs) — depends on ut-docs#1899 (modifiers admin UI),
  still in-progress under a different lane with no merged PR at the time
  this card was picked up. Split to ut-docs#1909, documented on the
  issue.

## What the independent review found

Independent review at Opus (fresh-context subagent, isolated worktree —
complexity:medium routing). Full report: see the PR description /
session log for the complete writeup; summary below.

**Verdict: no blockers.** The review verified, empirically rather than
by reading alone:
- The colour allowlist is airtight on both write paths (create/update)
  and defence-in-depth even if bypassed (Go's `html/template` CSS-value
  filter neutralises an injection attempt).
- Contrast: white-on-swatch is ≥5.05:1 (WCAG AA) across all 8 colours.
- The `showModal()` → `.show()` non-modal fix (below) correctly follows
  this codebase's own established ut-docs#1385 pattern
  (`#hold-modal`/`#pfand-modal`/`#elevation-modal`/`#table-add-modal`/
  `.payment-overlay`) — verified geometry (OSK clearance), z-index
  layering, and no leftover modal-only markup/comments.
- Both JS races the auto-close guard's comments claim to prevent
  (reopening for a different item; typing a second new item into the
  same still-open dialog) are genuinely prevented — traced through the
  code.
- The full colour data chain (`items.color` → repo → `Button`/`ButtonVM`
  → template) is wired with no silent drop; photo always wins over
  colour, both directions tested.
- Neither of the two recurring bug classes (missing `os.MkdirAll`, a
  cwd-relative path where `paths.Data(...)` belongs) apply — no file I/O
  in this diff at all.
- Modifier-group deferral is clean — no half-built UI, no dangling
  references.

**Should-fix findings, all folded into this branch before merge:**
- **S1** — `resetBtn` ("＋ New") didn't bump the `formSession` guard
  counter, the one remaining hole in the auto-close race guard's own
  stated invariant (a reset within the 1.5s post-save window could get
  the operator's freshly-reset form yanked shut). Fixed.
- **S2** — a comment claimed the auto-close delay matched the toast's
  ~2.5s auto-expire; actual delay is 1.5s, deliberately shorter.
  Comment corrected (no behavior change).
- **S4** — `web/help/{ar,fa,tr}/catalog.md` prose still described the
  old 360px panel, self-contradictory against this PR's own regenerated
  screenshots of the new full-screen dialog. Translated the same delta
  already applied to the English manual.
- Reviewer's own comment-only fix across 7 e2e spec files: the
  `showModal()`→`.show()` switch (mid-branch) left several e2e-fix
  comments claiming "inert" when the real reason a close is needed is
  now stacking (a `position:fixed` dialog covering the target), not
  inertness. Corrected for accuracy — verified zero non-comment lines
  changed.

**Nits folded in:** N1 (explicit `background` on `.item-form-modal`,
matching `.payment-overlay`'s own precedent) and N3 (removed a dead
`width` restatement in a media query). N2/N4 noted, not changed
(cosmetic, no user impact).

**Deferred as separate backlog cards, not blocking this merge:**
- ut-docs#1928 — a latent, unobserved e2e flake risk between the
  close-button click and the auto-close timer (race-tolerant close
  helper needed).
- ut-docs#1929 — `lang-pack-drift` for 22 `categories.*` keys, found
  during this close-out but pre-existing and unrelated (`universal-
  till#981` shipped without its own pack follow-up).

## Verified beyond automated tests

- Full `go test ./...` (52+ packages, twice), `golangci-lint run ./...`
  (0 issues), `gofmt -l .` (empty), `go vet ./...` (clean).
- Every CI-blocking guard (`guard-data-access.sh`, `guard-i18n.sh`,
  `guard-help-topics.sh`, `guard-docs-shots.sh`, and the rest of the
  `build` job's list) green.
- Full e2e Playwright suite (`e2e/`, 376 tests) green — the dialog
  conversion initially broke 20 tests across 8 spec files (they filled/
  clicked catalog form fields assuming the form was always visible);
  all fixed by opening the dialog first, plus a genuine second-order fix
  (non-modal dialog stacking, not inertness) once the OSK-reachability
  fix landed.
- Live manual verification (Playwright-driven, screenshotted): the
  dialog in English, Turkish (longest strings) and Farsi (RTL) at the
  1024×600 kiosk floor; the colour swatch picker selecting and
  persisting; the auto-close timing (dialog stays open at 600ms, closed
  by 1800ms) and its race guard (reopening for a new item inside the
  close window correctly keeps it open); the full colour pipeline
  end-to-end (created a coloured photo-less item, added it as a
  shortcut button via the designer, confirmed it renders as a solid
  coloured tile on the real sale screen).
- A real, severe defect found and fixed mid-cycle (not by the
  independent reviewer — by re-running the existing OSK e2e suite
  against the dialog): the initial `showModal()` implementation made
  the on-screen keyboard completely unreachable inside the dialog on
  kiosk hardware — the same bug class already hit and fixed five times
  elsewhere in this codebase (ut-docs#1385). Fixed by switching to the
  established `.show()` non-modal pattern before this PR ever reached
  review.

## Safe to merge

Yes. No blocking findings from either the live-driven testing pass or
the independent review. All should-fix findings folded in; two
legitimate follow-ups filed as separate Backlog cards (ut-docs#1928,
ut-docs#1929) rather than widening this PR.
