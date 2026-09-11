# Code review: Catalog top action row icon buttons (ut-docs#2092)

**Shipped:** `web/ui/pages/catalog.html`'s top action row drops the two
rail-duplicate buttons (Modifiers, Option sets — both already reachable as
`/items` left-rail sections since ut-docs#1950) and converts the remaining
five controls (Add item, Import, Tax codes, Export, Barcode backfill) to
icon-only `.btn-icon` controls (`aria-label` + `title`, reusing each
control's pre-existing i18n key — no new locale key needed for the buttons
themselves). Four new Lucide icons added to `internal/httpx/icons.go`
(`upload`, `download`, `landmark`, `scan-barcode`). Explicit non-goals:
Import/Tax-codes keep their current full-navigation mode (converting them
to an in-panel/full-screen surface is ut-docs#2095's separate, already-filed
scope); Export/Barcode-backfill's interaction mode is unchanged (neither
creates a new full-viewport surface, so ut-docs#1999's freshly-decided
persistent status/lock/exit-affordance rule for full-screen surfaces isn't
triggered by this diff).

A real, pre-existing-but-newly-exposed narrow-width bug was found and fixed
while driving the change at 360x800: `.page-head`'s shared, unscoped rule
(`display:flex` with no wrap) squeezed catalog's title + action row onto
one non-wrapping line, leaving the row only ~245px to work with — well
under what even a properly-shrunk search input needs. Fixed with a
page-scoped `.page-head-wrap { flex-wrap: wrap; }` opt-in class, applied
only to `catalog.html`'s own `.page-head` div; the shared `.page-head` rule
every other ~44 page-head usage relies on is untouched.

## Independent review

Opus (different model from the Sonnet implementation), isolated worktree
(`/home/user/ut-docs/.claude/worktrees/review-2092`, detached at a WIP
commit, `/home/user/universal-till` never touched). Ran the full gate
itself rather than trusting the implementer's report: `go build/vet/test`,
`gofmt`, `golangci-lint` (0 issues), all 7 relevant guard scripts,
independently re-verified the TDD claim by reverting `icons.go` +
`catalog.html` and confirming both new/changed Go tests fail for the exact
claimed reason before restoring, independently re-verified the narrow-width
CSS claim (including a fa/RTL half of the bug the implementer's own report
never mentioned — the search input ran off the *left* edge in RTL), fetched
the four new icon paths from the `lucide-static` npm package directly and
confirmed byte-for-byte match against upstream, and read the regenerated
`fa`/`ar` screenshots to confirm RTL mirroring.

**Found and fixed before this push:**

1. **Stale help-doc captions for 3 of the 5 icon-ized buttons.** The card's
   own change already fixed this for the two *removed* buttons, but missed
   that "Add item" and "Backfill barcodes from SKU" are also no longer
   visible captions — a shop owner on the touchscreen till has no hover to
   reveal `title`. Fixed in all 5 shipped locales
   (`web/help/{en,ar,fa,tr,de}/catalog.md`): both sentences now describe
   the control by its icon (e.g. "tap the **+** button (Add item)") rather
   than assuming a visible caption.
2. **Dead i18n key.** `catalog.option_sets.button`'s only consumer was the
   deleted top-row anchor. Removed from all 4 core locales
   (`web/locales/{en,ar,fa,tr}.json`) *and* both external packs
   (`ut-plugin-language-{de,es}`, which still carried a translated copy) —
   confirmed via each pack's own `scripts/check-key-drift.sh` run locally
   against the updated core `en.json`: `0 drift, 0 orphans` on both,
   before any push. `bash scripts/ci/guard-i18n.sh` re-run clean on core.
3. **Non-load-bearing CSS rule with an undeclared side effect.** The
   original fix added `#catalog-search { flex: 1 1 10rem; min-inline-size:
   0; }` alongside `.page-head-wrap`. Independent verification showed
   `.page-head-wrap` alone fixes the 360px overflow (measured, both
   fixes/one fix removed); the `#catalog-search` rule's `min-inline-size:
   0` on an ID selector was instead silently beating the shared
   `.page-head input[type="search"] { min-width: 260px; }` floor —
   measured 245px at the kiosk floor (1024x600), 15px under the rule that
   floor exists to enforce, in both `en` and `fa`. Removed; the shared
   260px floor is what governs `#catalog-search` again.
4. **Test asserted more than it checked.** The 360x800 e2e test was titled
   "...or overlaps" but only ever checked each control against the
   viewport edges, never against each other — `list-and-dialog-pattern.md`
   pins real geometry with an explicit no-overlap check, and this test
   didn't have one. Added a real pairwise bounding-box overlap check
   across every control in the row.
5. **Geometry coverage gap.** The same 360px test never included the
   Import/Tax-codes anchors, so 2 of the 5 converted controls had no
   narrow-width geometry assertion (they were covered for accessible names
   only). Added to the same test.
6. **Over-broad Go assertion.** `TestCatalogPage_TopRowHasNoRailDuplicateButtons`
   grepped the *entire* rendered page body for the removed hrefs.
   `catalog_variants.html` legitimately contains its own
   `href="/catalog/option-sets"` link elsewhere on the page (the "apply an
   option set" flow inside the item-variants panel) — it doesn't render on
   a bare `GET /catalog` today, but if it ever does, this test would
   wrongly blame the top row. Scoped the check to the `.page-head` block
   only.

**Noted, not fixed (informational, not blocking):**
- Import (`upload`) and Export (`download`) are the same tray glyph
  differing only in arrow direction, at 1.3rem, with no hover on a
  touchscreen till — worth the `ux` role's opinion on a follow-up, not a
  merge blocker for this card.
- `reference/list-and-dialog-pattern.md`'s icon vocabulary table wasn't
  extended with the 4 new icon names — that doc is scoped to the
  list/dialog pattern specifically (catalog hasn't adopted it, per that
  doc's own "Follow-ups" section), so arguably out of scope here, but
  it's the natural place a future author would check for a collision.

## Verified beyond automated tests

- `go build/vet/test ./...`, `gofmt -l .`, `golangci-lint run ./...` (0
  issues) — all clean, both before and after the review's fixes.
- `guard-i18n.sh`, `guard-data-access.sh`, `guard-help-topics.sh`,
  `guard-help-drift.sh`, `guard-docs-shots.sh`, `guard-emoji-font.sh`,
  `guard-page-http-error.sh` — all green, after the review's fixes.
- TDD re-verified twice, independently, via real revert→run→restore (once
  by Dev, once by the independent reviewer in an isolated worktree): both
  new/changed Go tests fail for the exact claimed reason pre-fix.
- Real driven Playwright run (`e2e/tests/catalog-toprow-icons-2092.spec.ts`,
  4/4) plus the full `catalog-*`/`categories-record-dialog-2010` regression
  suite (84/84) — both before and after the review's fixes, on a live
  till server.
- `make docs-shots` regenerated all 120 screenshots (30 topics × 4
  locales); the `en`/`fa`/`ar`/`tr` `catalog.png` shots were read and
  visually checked (row fits one line, RTL mirrors correctly, nothing
  overlaps or clips) at whatever viewport `docs-shots.spec.ts` uses, across
  all 4 built-in themes (amber/fresh/monarch/slate — driven manually via
  `POST /api/settings/theme`, screenshotted, and read).
- Not verified on a physical pilot tablet or kiosk Pi — no device
  available in this cloud sandbox; the 1024x600/360x800 viewport checks
  above are the closest equivalent.

## Safe-to-merge verdict

Safe to merge. All independent-review findings were fixed in this same
push and re-verified; no open findings block merge.
