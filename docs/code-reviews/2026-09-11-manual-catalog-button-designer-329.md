# Manual content: catalog, variants, modifiers & the button designer (ut-docs#329)

**Date:** 2026-09-11
**Card:** universaltill/ut-docs#329 — "Manual content: catalog, variants,
modifiers & the button designer"
**Complexity:** medium

## What the card asked for

Write the manual topics for catalog/variants/barcodes/modifiers/import-export
and the button designer, to the standard set by ut-docs#324/#325/#327:
description, step-by-step, screenshots via `make docs-shots`, error/empty
states covered, steps verified against a running till.

## What was actually found (BA/Architect pass before writing)

`web/help/en/catalog.md` (topic `catalog`, routes `/catalog /import /items
/modifiers`) already existed and was already extremely thorough — items,
variants, barcodes, import/export, option sets and customization (modifier)
groups were all already covered in depth by an earlier cycle. Re-writing it
would have been busywork with real regression risk (accidentally dropping
accurate detail).

`web/help/en/till-designer.md` (topic `till-designer`, route `/designer` —
the "button designer" the card asks for) existed but was thin: 3 bare steps,
no "what can go wrong" section, no error/empty states — well short of the
`catalog.md` bar.

The card's "Catalog cleanup / obsolete items" surface turned out to already
be documented, accurately, in `web/help/en/display.md` step 14 (Settings →
Data → Catalog cleanup) — a different topic than `catalog.md`, since that
screen lives under `/settings`, a route already claimed by `display.md`
(confirmed no second topic can claim the same route —
`guard-help-topics.sh`).

So the real, bounded gap was: expand `till-designer.md` to the same
description/steps/errors depth as `catalog.md`, and make the two topics
cross-reference each other for discoverability, rather than rewrite content
that was already correct.

## What shipped

- `web/help/en/till-designer.md`: rewritten with a "what it is" intro, 4
  numbered steps (open / add via search / reorder / remove) and a new "Good
  to know" section covering the primary-till-only gating, the empty state
  ("No products yet."), and what happens when a reorder fails to save.
- `web/help/en/catalog.md`: one wording correction on the delete-item bullet
  (it previously implied reactivation was generally available; it's only
  reachable via the still-open edit form's own save — see review below), and
  one new "Good to know" bullet cross-linking Settings → Data → Catalog
  cleanup (`/help/display`) for permanently clearing out old deactivated
  items.
- `web/help/img/manifest.json`: regenerated via `make docs-shots` (112/112
  screenshot tests green) — topic-content hashes updated; the two topics'
  actual PNGs came back byte-different only by the documented
  anti-aliasing-noise margin (ut-docs#930), so those PNG diffs were reverted
  per that note's own instruction ("regenerated PNGs that differ only by AA
  noise should be reverted, not committed").
- `scripts/ci/i18n-baseline/help-drift-baseline.json`: 8 new/updated
  reviewed-drift entries (`catalog` ar/de/fa/tr, `till-designer` ar/de/fa/tr)
  — `guard-help-drift.sh` requires either a matching translation or a
  reviewed baseline entry for any structural change to an English topic, and
  #329's own instructions are explicit: "English only on this card —
  translation is a separate card" (ut-docs#341, "Manual: translate into fa /
  ar / tr", already on the Backlog). The baseline entries record that
  deferral by issue number rather than translating ad hoc.

## Verified against the running app

- Booted a throwaway till (`e2e/run-till.sh`, port 8091, demo catalog
  seeded) and drove `/designer` directly: confirmed the 3-character search
  minimum and its exact hint text, a no-match search, and that a tapped
  result adds immediately with no separate save step (`node` +
  `e2e/node_modules/playwright`, `/opt/pw-browsers/chromium`).
- Read `internal/pages/buttons_api.go`, `web/ui/partials/buttons_admin.html`
  and the `designer.*` keys in `web/locales/en.json` to confirm every quoted
  string and every behavioural claim (edge-tile button disabling, the
  click-burst ordering guarantee, the primary-till gate, the reload-on-save-
  failure behaviour) against source, not assumption.
- Read `internal/pages/catalog/handlers.go` (`UpdateItemReturningWasActive`,
  `/api/catalog/item/deactivate`) and `internal/pages/data_api.go`
  (`ListObsoleteItems`/`CleanupObsoleteItems`/`cleanup-catalog`,
  `checkStepUp`/ADR-0087) plus `web/ui/pages/settings.html` before writing
  the two catalog.md edits.
- `bash scripts/ci/guard-help-topics.sh`, `guard-docs-shots.sh`,
  `guard-help-drift.sh`, `guard-i18n.sh`: all green. `gofmt -l .` empty,
  `go build ./...` and `go vet ./internal/pages/...` clean (no Go source
  touched by this change; run as a sanity check anyway).

## Independent review

A fresh-context Opus subagent (complexity:medium → Opus review, per
`scrum-master`'s model-routing table) reviewed the diff independently,
checking every quoted string and behavioural claim against source rather
than trusting the diff's own wording. Found 3 real inaccuracies in the first
draft, all fixed before this record was written:

1. **CI-blocking**: the first draft never ran `guard-help-drift.sh`, which
   failed on the new English structure with no baseline/translation. Fixed
   via the baseline entries described above.
2. `till-designer.md`'s "if a reorder can't be saved (for example, a
   connection drop)" had the example backwards: `persistOrder()`'s
   `.catch()` (a dropped connection) explicitly skips the reload, per its
   own comment, since the tile already visibly moved; the reload only fires
   on a non-2xx HTTP response. Reworded to describe both cases correctly.
3. `till-designer.md` step 1 described the grid as being above the search
   box; the real DOM order (`buttons_admin.html`) is search box first, grid
   below. Fixed.

One more minor point (catalog.md's "Preview shows exactly what would go"
overstating the 200-row preview cap when the actual removal is uncapped) was
also fixed. Everything else — every quoted locale string, the 3-char search
minimum, edge-button disabling, the primary-till gate, the
`wasActive`/reactivation mechanism, the Catalog-cleanup criteria and PIN
gate, and the `/help/display` cross-link target — was confirmed correct
against source.

## Scope / non-goals

- Not rewriting `catalog.md`'s already-accurate content (see BA note above).
- Not translating the new English content into ar/de/fa/tr — out of scope
  per the card's own instruction; deferred to ut-docs#341 via reviewed
  baseline entries (see above), which is what the repo's own guard mechanism
  exists for.
- No Go/HTML/CSS change — content-only, so no behaviour to regression-test
  beyond the guard scripts above.

## Verdict

Safe to merge. `guard-help-topics.sh`, `guard-docs-shots.sh`,
`guard-help-drift.sh` and `guard-i18n.sh` all green; independent review
findings fixed and re-verified. Closes universaltill/ut-docs#329.
