# Review: layout plugin non-hide amendments become visible (ut-docs#1921)

**Card:** universaltill/ut-docs#1921 — Settings → Hidden menu tiles
(ADR-0088 Decision D's findability surface) listed only tiles a `layout`
plugin **hides**. A plugin that re-labels, re-icons, reorders or re-groups
a tile without hiding it left no trace on the page — the empty state even
read "No layout plugin is hiding any Menu tile", which reads as "this
plugin changed nothing" when it may have changed everything except
visibility.

**Build:** Sonnet (complexity:easy). **Review:** Sonnet, independent
fresh-context subagent, isolated worktree.

## What shipped

- `internal/pages/menu_layout_settings_page.go`: new `amendedMenuRow` type
  and `amendedMenuRows` function, listing every core key some active
  layout plugin restructures (re-label/re-icon/reorder/re-group) without
  hiding it. Reuses `uislot.Resolve`'s own `LabelFallback`/`IconFallback`
  fields for the "core default this replaced" values, per the card's own
  acceptance criteria, rather than re-deriving them.
- `web/ui/pages/menu_layout.html`: a second table, "Other menu tile
  changes", rendered only when `.amended` is non-empty. The empty-state
  message now only shows when BOTH `.rows` (hides) and `.amended` are
  empty — this is what actually satisfies the "distinguish no-plugin from
  installed-and-changed-non-hide-things" acceptance criterion, since both
  signals come from the same `LayoutAmendmentsSnapshot()` and the old
  logic only ever looked at the hides half.
- Restore/rehide is deliberately NOT offered on amended rows (acceptance
  criterion: restore stays hide-only) — documented inline in the template.
- 10 new locale keys added to `en/ar/fa/tr` (all four core locales); no
  `de`/`es` follow-up owed here — those live in the external
  `ut-plugin-language-{de,es}` packs, out of this repo's scope.
- `web/help/{en,de,ar,fa,tr}/menu.md` updated in the same paragraph
  structure (no new headings/bullets, so no new `guard-help-drift`
  baseline entries), and `make docs-shots` run (manifest regenerated;
  the "menu" topic's own screenshot is unaffected since it's taken from
  `/menu`, not `/settings/menu` — a pre-existing gap this card doesn't
  introduce).
- 5 new regression tests in `internal/pages/menu_layout_settings_test.go`.

## Independent review findings

### 1. Same key hidden by one plugin AND restructured by a different plugin — should-fix, FIXED
`uislot`'s cross-plugin conflict check only rejects restructure-vs-restructure
collisions on the same key; a hide by plugin A and a restructure by plugin B
on the same key installs cleanly (a real, reachable state). At render time
`uislot.Resolve` makes hide win regardless of amendment order, but the
original `amendedMenuRows` didn't check for a same-key hide from another
plugin — so the page rendered the key in **both** tables at once: hidden
(correctly) and "stays on the Menu" (the amended section's own intro copy),
directly contradicting itself on the same page.

Fixed by collecting every hidden key first and skipping any restructure
amendment whose key some amendment (own or another plugin's) hides. New
regression test:
`TestMenuLayoutSettings_KeyHiddenByOnePluginAndRestructuredByAnotherShowsHiddenOnly`
— installs a "Hider" plugin hiding `/tables` and a "Relabeler" plugin
relabelling `/tables`, and asserts `/tables` appears as hidden-by-Hider
and does NOT appear in the amended section at all.

### 2. Change-arrow (`→`) not locale-aware — nit, not fixed
The new template's "core default → new value" text hardcodes `→`
regardless of locale. U+2192 carries Unicode's `Bidi_Mirrored` property so
a compliant RTL bidi engine may auto-mirror it — not independently
confirmed in a live ar/fa render. No CSS `left`/`right` was introduced
(the acceptance criterion's actual ask), so not a blocker; worth a UX
glance on ar/fa, noted as a follow-up rather than blocking this card.

### 3. One new test is a weak pin on its own — nit, not fixed
`TestMenuLayoutSettings_AmendedRowsOfferNoRestoreAction` also passes with
the entire feature reverted (trivially — no amended section, no restore
action to find). It only earns its keep alongside the other new tests
that require the section to actually render; left as-is rather than
strengthening it, since the combination already covers the real claim.

### Checked clean
- No filesystem writes anywhere in this diff (grepped, not assumed) — the
  "missing `os.MkdirAll`"/"cwd-relative path vs `paths.Data`" bug classes
  are genuinely N/A here.
- No hardcoded secrets or real client/shop names.
- No `left`/`right` CSS or new non-logical layout (no CSS touched at all).
- ar/fa/tr translations for all 10 new keys are genuine, locale-appropriate
  (not English copy-paste), consistent register with surrounding strings.
- Empty-state template logic verified balanced (7 `{{ if/range }}` opens,
  7 `{{ end }}`s) and correct by test.
- `web/help/img/manifest.json` hash diff verified against real file
  content via `guard-docs-shots.sh`.

## TDD re-verification (actual commands, actual output)

Reviewer's own pass: reverted `internal/pages/menu_layout_settings_page.go`
+ `web/ui/pages/menu_layout.html` to `main`, kept the new tests —
`TestMenuLayoutSettings_ListsRelabelAndReiconAmendmentNamingCoreDefault`,
`TestMenuLayoutSettings_ListsReorderAndRegroupAmendment`,
`TestMenuLayoutSettings_NonHideAmendmentsSuppressEmptyState` all FAILed
with the expected "amended-tiles section missing ..." errors; restored,
all PASSed again.

Orchestrator's own independent re-verification of finding 1's fix
specifically: `git stash` on `menu_layout_settings_page.go` alone (keeping
the new test) →
`TestMenuLayoutSettings_KeyHiddenByOnePluginAndRestructuredByAnotherShowsHiddenOnly`
FAILed; `git stash pop` restored the fix → PASSed again.

## Gate (run by the reviewer, and again by the orchestrator before commit)

`gofmt -l .` (clean), `go build ./...`, `go vet ./...`, full `go test ./...`
(all packages, 0 failures), `golangci-lint run ./...` (0 issues),
`guard-i18n.sh`, `guard-data-access.sh`, `guard-kiosk-engine.sh`,
`guard-page-http-error.sh`, `guard-compliance-claims.sh`,
`guard-help-topics.sh`, `guard-help-drift.sh` (only pre-existing,
already-baselined drift unrelated to the `menu` topic), `guard-docs-shots.sh`
— all exit 0.

## Verdict

**Safe to merge.** Finding 1 (real cross-plugin contradiction bug) fixed
in this branch with a permanent regression test. Findings 2–3 are genuine
but non-blocking, noted for optional follow-up rather than blocking:
- Optional: confirm the `→` glyph renders correctly (not visually reversed)
  in a real ar/fa browser session.
