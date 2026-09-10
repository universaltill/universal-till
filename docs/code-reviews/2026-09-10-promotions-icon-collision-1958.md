# 2026-09-10 — Promotions/Items icon collision (ut-docs#1958)

## What shipped

Product-owner report on the pilot tablet: "on the left menu, there is an
ticket icon like items when I am in the items page, but it goes to
promotions." The ticket's own hypothesis (icon set from source, not yet
verified) was that `/promotions` had no declared icon and fell through to
`genericFallbackIcon`. That hypothesis was **wrong** — verified by reading
the actual render paths before touching anything:

- `web/ui/partials/session_chip.html` (the manager dropdown, rendered
  inline in the left rail) hardcoded `{{ icon "tag" }}` for `/promotions`.
- `internal/uislot/slot.go`'s `CoreMenu` declares `Icon: "tag"` for
  `/items`, rendered on the `☰ Menu` launcher (`internal/pages/menu_page.go`).

Two entirely different destinations, both reachable on the same tablet,
rendering the identical glyph — not a missing-icon/fallback bug at all.

Fix:
- Added a new `"percent"` icon (Lucide's standard glyph — a diagonal line
  + two circles) to the shared set in `internal/httpx/icons.go`.
- `session_chip.html`'s Promotions link now uses `{{ icon "percent" }}`.
- Corrected `"tag"`'s stale code comment (it wrongly said "Promotions").
- Added `internal/httpx/icons_test.go`'s `TestNoTwoNavDestinationsShareAnIcon`:
  cross-references `uislot.CoreMenu`'s declared icons against every literal
  `{{ icon "name" }}` call in `web/ui/**/*.html`, and fails if one icon name
  is claimed by two different destinations. The same destination linked
  from two chrome elements with the same icon (e.g. `/users` in both
  `CoreMenu` and `session_chip.html`) is correctly not a collision.
- Regenerated the manual's screenshots (`make docs-shots`, 28 topics × 4
  locales) — required by `guard-docs-shots.sh` since `web/ui/**` changed.
  `web/help/en/promotions.md`'s prose never described the icon, so no text
  update was needed (confirmed by reading it, not assumed).

## Independent review

Reviewed by a fresh-context Sonnet subagent (card is `complexity:easy`),
in an isolated worktree, with no visibility into the implementation
reasoning above. Findings: **no blocking issues.**

What it verified itself, not just re-read:
- `go build ./...`, `go vet ./...`, `go test ./internal/httpx/...
  ./internal/uislot/... ./internal/pages/...`, `golangci-lint run
  ./internal/httpx/...`, `gofmt -l` — all clean.
- **TDD claim, independently reproduced**: reverted `session_chip.html`
  back to `icon "tag"`, re-ran the new test — failed with the exact
  collision message naming both destinations. Restored the fix, re-ran —
  passed. Working tree confirmed clean afterward.
- `guard-docs-shots.sh`, `guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-help-drift.sh` all pass (the drift guard's pre-existing baseline
  entries, tracked separately by ut-docs#1962/#1973, are unrelated to this
  diff and were correctly not treated as new findings).
- The new `"percent"` SVG is Lucide's real, unmodified path data, follows
  the file's own wrapper convention, and no other entry/template already
  uses "percent" for something else (e.g. a tax-rate glyph).
- Independently confirmed the regression test's regex correctly excludes
  `menu.html`'s data-driven tiles (`{{ .Href }}`/`{{ .IconSVG }}`, not a
  literal `{{ icon "name" }}` call) and correctly treats the several
  `href="/"` → `"shopping-cart"` uses across `nav.html`/`menu.html`/
  `error_page.html` as one destination, not a collision.
- Opened `web/help/img/en/promotions.png` and `web/help/img/en/menu.png`
  after the screenshot regen: the two icons are visibly distinct in the
  real rendered app.

One non-blocking, out-of-scope observation: a `layout` plugin can
override an entry's icon at runtime via `Amendment.Icon` (ADR-0088), which
this test structurally cannot see (it only scans core's static
declarations + literal template calls) — a plugin could in principle
re-introduce a collision against a core destination at runtime. Pre-
existing extensibility surface, not a regression from this fix; noted here
rather than expanding this bugfix's scope.

## Verified beyond automated tests

- Real running-app screenshots (`make docs-shots`, headless Chromium)
  visually confirm the Items tile (price-tag glyph, `web/help/img/en/menu.png`)
  and the Promotions rail entry (percent glyph,
  `web/help/img/en/promotions.png`) are now unambiguously distinct — this
  is the same rail the product owner's report describes, not a synthetic
  reproduction.
- Full `go test ./...` (whole repo) run clean before this review, in
  addition to the review's own scoped re-run.
- `golangci-lint run ./...` (whole repo) clean.

## Deferred / out of scope

- Runtime plugin-amendment icon collisions (noted above) — not filed as a
  new card; a genuinely small, speculative extension of this test's scope
  rather than a known live bug.

## Verdict

**Safe to merge.**
