# Code review: declarative UI slot registry (ut-docs#1904, ADR-0088)

**Date:** 2026-09-09
**Card:** ut-docs#1904 — "Core UI structure is hardcoded Go — a plugin can
only append a tile, never remove, reorder, rename or group one, so 'the UI
design must be a plugin' is not expressible today"
**Branch:** `feat/1904-ui-slot-registry` (PR universaltill/universal-till#979)
**Design:** ADR-0088, accepted before implementation
(`ut-docs/adr/0088-declarative-ui-slot-registry.md`, PR ut-docs#1917)
**Diff:** `internal/uislot/` (new), `internal/pages/menu_page.go`,
`internal/pages/menu_layout_settings_page.go` (new),
`internal/pages/common/{deps,state}.go`, `internal/pages/init.go`,
`internal/plugins/{manifest,manifest_verifier,plugins,rollback}.go`,
`internal/data/plugin_repo.go`, `internal/db/migrations/018_*.sql`,
`internal/httpx/{httpx,icons}.go`, `plugins/layout-salon/` (new),
`scripts/ci/guard-plugin-menu-read{,_test}.sh`, `web/locales/*.json`,
`web/ui/pages/{menu,menu_layout,settings}.html`, `web/public/app.css`
**Build model:** Fable (card is `complexity:hard`)
**Reviewer:** independent (Opus, fresh context in an isolated worktree,
different model from the implementation, did not write the code and did not
see its reasoning)

## What shipped

`registerMenu`'s imperative `add(...)` sequence became a declared table
(`uislot.CoreMenu`) resolved through `uislot.Resolve`, which core's own
tiles and a `layout` plugin's amendments both go through. New canonical
type `layout` (taxonomy 21 → 22, amending ADR-0002), migration 018 widening
the `plugin_entries.type` CHECK, install-time validation on both
`PersistManifest` and `Rollback`, a "Hidden menu tiles" Settings surface
with per-entry restore, and `plugins/layout-salon` as the first real
`layout` plugin.

## Independent review: no blocker, four findings, all fixed before merge

The reviewer verified rather than accepted the claims: it re-ran the
zero-allocation benchmark itself
(`BenchmarkResolve_ZeroAmendments-12  1.987 ns/op  0 B/op  0 allocs/op`),
applied migration 018 (numbered 017 at review time) to a scratch database seeded from 001→016 and
checked rows, indexes, foreign keys and cascade behaviour survived, and
falsified three TDD claims by removing the production behaviour and
confirming the tests fail. No false-pass tests were found. It also traced
the authorization gates tile by tile against `main` and confirmed identical
tile sets for cashier, manager, manager+stock, DE and TR.

### F1 (most severe) — a protected tile could be disguised, and nothing said so

`slot.go`'s protected-key check guarded `hide` only, so ADR-0088's original
Decision E — which permitted reorder/re-label/re-group/re-icon on protected
keys because they "keep the destination visible" — left a hole the reviewer
drove end to end: an amendment of
`{"key":"/report-issue","label_key":"nav.catalog","icon":"tag","order":99999}`
installed cleanly and rendered the statutory tile as **"Catalog"** with a
tag icon in last position, while the Decision D recovery page reported
nothing amended (it lists hides). The same worked on `/fiscal-register`
(§146a Abs. 4 AO) and `/journal`.

The word "visible" was carrying weight it could not bear. **Fixed at the
root, not at the symptom:** re-label and re-icon of a protected key are now
refused at install alongside hide, with the error naming the key and what
was refused. Order and group stay permitted — they move a protected tile
without disguising it, and a vertical legitimately needs to position
statutory destinations among its own. ADR-0088 Decision E was narrowed to
match, recording why the original wording did not survive contact with the
implementation.

Proven: `TestParseMenuAmendments_ProtectedKeyCannotBeRelabelledOrReiconed`
fails when the refusal is removed;
`TestParseMenuAmendments_UnprotectedKeyStillFullyAmendable` guards against
the fix having frozen the mechanism's actual purpose.

### F2 — "re-group" did not group

The renderer emits a heading whenever *consecutive* entries change group,
and `Resolve` sorted by `Order` only — so two entries given the same group
but landing apart rendered the **same heading twice** with unrelated tiles
between them, and `ParseMenuAmendments` accepted it silently. That is
run-labelling, not grouping, and it did not match what the ADR and the
salon README advertise.

Fixed by `groupTogether`: same-group entries are gathered at the earliest
member's position, ungrouped entries keep their relative order around them,
and a slot with no grouped entries comes out byte-identical (the zero and
near-zero paths are untouched). Proven by
`TestResolve_SameGroupEntriesEndUpAdjacent` (fails without the fix) and
`TestResolve_NoGroupAmendmentLeavesOrderUntouched`.

### F3 — the refactor made an authorization regression a one-token edit, with no test

On `main` the two fiscal tiles' nesting inside the manager gate was
*structural* (`if canPerform { … if DE { add } }`) and could not be dropped
by accident. ADR-0088 turned it into a composable expression
(`v.visible("settings") && …` inside the predicate), which made it a
one-token deletion **at exactly the moment nothing covered it**: every
existing fiscal-tile test sets `UT_AUTH=off`, so the reviewer deleted the
conjunct from *both* predicates and the entire package tree stayed green.
The regression that would have shipped: a cashier on a `Country=DE` till
with the German tax plugin active sees the `/fiscal-register` tile.

Fixed by adding the missing cashier-path coverage —
`TestMenuPage_FiscalRegisterTileStaysManagerGatedForCashier` and
`…FiscalDeviceTileStaysManagerGatedForCashier`. Both were verified against
the reviewer's own mutation: they fail with the conjunct removed and pass
with it restored. Each also asserts the menu actually rendered, so neither
can pass for the wrong reason.

### F4 — two lower-severity gaps

**(a) The flagship example was a no-op in the one market with a real
merchant.** `plugins/layout-salon` shipped locales for `en/ar/fa/tr` — the
core locales the existing test globs — but German and Spanish ship as
external *language packs*, so on the German pilot till `layout.salon.services`
never resolved and the label correctly degraded to the core "Artikel". The
re-label silently did nothing, and by construction the existing test could
never catch it. Fixed by shipping `de.json`/`es.json` and adding
`TestLayoutSalonPlugin_ShipsLocalesForTheLanguagePackMarketsToo`, which
pins the language-pack locales explicitly and — per ut-docs#292's lesson —
also fails a value that is present but still English, not just a missing
key.

**(b) The CI guard could not see the read it was widened for.** The
`plugin-menu-read` guard matches `<ident>.Pm.LayoutAmendments`, but
`common.BuildMenuAmendments` reads `pm.LayoutAmendments` off a
`*plugins.Manager` **parameter**, outside the pattern. All three current
callers hold `PluginMu`, so nothing was wrong — but a fourth would not have
been caught. Widening the field pattern would have flagged the function's
own body, whose contract *is* "the caller holds the lock", so the guard now
protects the risk where it actually lives: `BuildMenuAmendments`' **call
sites**, allowlisted by file, so a new caller fails until whoever adds it
states which lock it holds. A self-test case was added and the guard was
verified to reject a planted rogue call site.

## Verified sound (reviewer, unchanged)

- No authorization regression; unknown `VisibleIf` fails closed; a plugin
  cannot influence which predicate runs (`parseAmendment` has no
  `visible_if` field and `Resolve` never touches `Entry.VisibleIf`).
- Protected keys cannot be hidden on any write path — `validateLayoutEntries`
  covers the only two callers of `ReplacePluginEntries`, the only
  `INSERT INTO plugin_entries` in the codebase, and validates the persisted
  bytes rather than the in-memory config. `loadLayoutEntries` re-parses at
  reload, so a hand-edited DB row is dropped rather than applied.
- Concurrency: every read goes through a locking accessor or sits inside a
  held write lock; no nested `RLock`→`Lock`; both loaders reassign fresh
  slices, so a snapshot handed to an unlocked caller stays valid.
- `Resolve` does not mutate the caller's backing array.
- Label fallback returns the core label, never the raw key.
- No injection surface: plugin strings reach templates as `string` through
  `html/template`; `IconSVG` comes only from the fixed `railIcons` map; the
  `?err=` value is allowlisted.
- Both new SQL statements live in `internal/data` and are parameterized.

## Not fixed here — filed instead

Non-protected restructures (a plugin re-labelling `/reports`) remain
invisible on the Settings surface, which lists hides. For protected keys
that is now moot — F1 refuses the disguise outright — and for a
non-protected tile a re-label is the mechanism doing its job. Surfacing
"what this layout plugin changed" in general is real but separate scope.

## Gate

`go build ./...`, `gofmt`, `go test ./... -race`, `make docs-shots`
(104/104), `guard-data-access.sh`, `guard-plugin-menu-read.sh` and its
self-test, all green on the merged tree. An earlier run of the gate timed
out in two packages; the goroutine dump showed a sleeping test and idle
`database/sql` cleaners with **no goroutine blocked on a mutex**, i.e.
contention with the concurrently-running Playwright screenshot harness
rather than a deadlock in the new locking path — re-run sequentially and
green.

## Tester phase (run after the review, and it found something)

The pipeline's Tester phase had been skipped on the first pass through this
card — caught by the product owner, not by the process. Run properly
afterwards, it found a real defect the Go tests and the independent review
had both missed, because neither *looks* at a rendered page.

### A visual defect on the new Settings surface

On `/settings/menu`, the per-row "Open" link rendered **inline against the
destination name**, so a merchant read `Tables & floor plan Open` as a
single string. This is exactly the shape ut-docs#300 shipped — two labels
running into one row on a page whose screenshot existed and which nobody
looked at.

Fixed by putting the link on its own line (`display: block` on the name,
`margin-block-start` on the link — logical properties, so RTL needs no
separate rule), and confirmed by looking at the re-rendered page in both
English and Farsi.

Pinned as **geometry**, not markup, per the tester skill's rule that a
visual defect fixed without a geometric assertion comes back: the new spec
asserts the link's bounding box starts at or below the label's bottom edge.
Falsified by reverting the CSS to `display: inline` — the test fails with
`label y=231.53 h=20, link y=233.53`, i.e. the same line.

### What the driven run proves that no unit test could

ADR-0088 Decision D's central claim is that hiding removes a **tile** and
never a **route**. That is a statement about the router and the renderer
deliberately disagreeing, and a rendered-HTML assertion cannot see the
router at all. The spec drives a real till with the real
`plugins/layout-salon` installed and asserts `/tables` and
`/kitchen-stations` still return **200** while absent from the menu.

Infrastructure added, following the existing `seed_faq` / `run-till-ai.sh`
precedent:

- `e2e/seed_layout_salon` — installs the **real** plugin by reading
  `plugins/layout-salon`'s own manifest and locale files, not a fixture
  copy, so the test moves with the shipped plugin instead of drifting from
  it.
- `e2e/run-till-layout.sh` + a dedicated Playwright project on port 8094 —
  isolated deliberately: installing a plugin that hides `/tables` and
  re-labels `/items` into the shared default till would move the ground
  under every other menu/nav assertion in the suite.

### Falsification

The e2e assertions were falsified as a set by removing the plugin's `hide`
amendments from its manifest and re-running: **3 of the 4 then-existing
tests failed**. The fourth (the re-label) correctly still passed, since
that amendment was left intact; it is falsified separately by the Go-level
mutation checks.

### Visual check attestation (ut-docs#301)

Looked at, at **1024×600** (the 10-inch till target):

- `/menu` with the salon layout installed, **en** — "Services" renders
  first with the scissors icon; Tables and Kitchen stations are absent;
  every other tile and icon intact.
- `/settings/menu`, **en** — before and after the "Open" fix.
- `/settings/menu`, **fa (RTL)** — `dir=rtl`, table and buttons mirror
  correctly, the link sits under the name on its own line, no horizontal
  overflow.

**Not looked at, and why:**

- **German rendering of the new `menulayout.*` strings.** German ships as
  an external *language pack*, which is not installed on the e2e till, so
  `?lang=de` correctly falls back to English there and the screenshot is
  byte-identical to the English one. The German strings themselves are
  covered by `ut-plugin-language-de` PR #219; their *layout* at the longest
  locale is **unverified on a real screen**. The spec does assert no
  horizontal overflow at `?lang=de` on `/menu`, but that is against
  English-length strings on this till, so it is weaker than it looks.
- **Dark theme** — not captured.
- **A real device.** Everything here is desktop Chromium at kiosk
  dimensions, not the TECLAST tablet or a Pi.

### Gate

`go build`, `go vet`, `gofmt`, guards `data-access` / `i18n` /
`plugin-menu-read` (+ its self-test), `make docs-shots` (104/104, surface
hash moved as expected for the CSS change), `go test` on
`internal/{uislot,plugins,data,httpx,pages,...}`, and the **full Playwright
suite: 381 passed**.

`internal/pages` under `-race` exceeds a 25-minute budget on this machine
and has its own `make test-race-pages` target (60-minute budget) for that
reason; the plain suite that CI runs is green. That is a pre-existing
property of the package, not a regression from this change — the earlier
timeout's goroutine dump showed a sleeping test and idle `database/sql`
cleaners, with no goroutine blocked on a mutex.

### One cosmetic thing left alone

The hidden-tiles table does not fill its card's width (empty space to the
trailing side). Pre-existing `.table` styling, not introduced here, and not
worth widening this card to chase.
