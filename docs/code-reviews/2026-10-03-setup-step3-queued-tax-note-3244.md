# Code review — setup step 3 keeps the queued tax note without a catalog match (ut-docs#3244)

- **Date:** 2026-10-03
- **Branch:** `fix/3244-setup-step3-queued-tax-note`
- **Author:** pipeline build lane (lane:cloud-54), Opus 5.5 (inline; card is `complexity:easy`)
- **Reviewer:** independent subagent, Fable (different model from the author)

## What shipped

Finding 2 of the #3210 review. #3210 keeps a consented `{tax, de}` spec on the #591 pending list when the catalog is reachable but publishes no DE tax listing. The wizard's step-3 tile is rendered only from a catalog match, and the pending note sat inside `{{ if .installableTaxPlugin }}`, so a German operator resuming step 3 saw nothing about the fiscal plugin.

- `internal/pages/setup_tax_catalog.go`: new `setupTaxPluginQueued` — true when the country maps to a tax locale, no active local tax plugin serves that market, and the pending list holds `{tax, <locale>}`. Read errors log a warning and return false (it only adds a note; the Settings chip stays the durable signal).
- `internal/pages/setup_page.go`: called only when there is no tile (`taxPlugin == nil`).
- `web/ui/pages/setup.html`: `{{ else if .taxPluginQueued }}` renders the tile's title and the existing `setup.tax_plugin.install_pending` note, with no Install button (nothing to install; the consent is already queued). No new locale keys, so no language-pack follow-ups.
- Help: `web/help/{en,de}/users.md` gain one sentence; docs-shots manifest regenerated (no PNG changed — the users topic shot doesn't show step 3).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `setupTaxPluginQueued` swallowed read errors silently, unlike its sibling which logs. | **Fixed** — `Warnf` on both error paths. |
| 2 | minor | "Still installing … in the background" is loose when the catalog simply has no listing. | Accepted: deliberate key reuse; per-reason copy is ut-docs#3243 (comment added there naming this second call site). |
| 3 | nit | `HasActiveEntryTypeForMarket` runs twice per first-boot render when nothing is installed. | Accepted — negligible, bounded to the wizard. |
| 4 | nit | Exact `Locale` match vs `EqualFold` in `taxSpecSatisfiedLocally`. | Accepted — specs are written from the same `countryTaxLocale` map. |
| 5 | nit | Offline tile still shows "Install when online" for a till that already queued the spec (pre-existing #1512 behaviour). | Out of scope; noted on ut-docs#3243. |

## Verification

- TDD: `TestSetupGETShowsQueuedTaxNoteWhenCatalogHasNoListing` (4 subtests: pending tax spec / nothing pending / only a language spec / pending but sideloaded tax plugin active). The `pending tax spec` subtest failed before the fix (`queued note shown = false, want true`) and passes after — confirmed by the author and independently by the reviewer (reverted non-test files, saw red, restored, saw green).
- `gofmt`, `go build ./...`, `go vet`, full `go test ./...`, `golangci-lint` 0 issues; every guard in `ci.yml`'s build job passed (shellcheck-version skipped locally: no shellcheck binary; no shell changes).
- Driven run: real till binary on a fresh data dir, a fake reachable catalog with no listings, a pending `{tax,de}` spec seeded; GET `/setup` step 3 shows "Germany tax plugin — Still installing the tax plugin in the background — you can continue." Screenshots looked at in en/de @1024×600 and en/ar @360×800: note visible, no horizontal scroll, RTL renders correctly, styling matches the existing tile.

## Verdict

Safe to merge.
