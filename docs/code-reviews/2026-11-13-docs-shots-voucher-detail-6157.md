# Review: vouchers help screenshot shows a voucher's detail page (ut-docs#6157)

**Change.** The manual's vouchers screenshot used to be the empty `/vouchers` list, so no picture showed the "no receipt printer" hint added in ut-docs#6152. Now:
- `e2e/seed_docs_sales` (docs till only, gated by `UT_DOCS_SHOTS_SEED_SALES=1` in `e2e/run-till.sh`) seeds one gift voucher `DOCS-GV-0001` (holder "Anna", 2500 minor units GBP, active, fixed `2025-12-10T12:00:00Z`) and its `issue` history row. Both are written through `data.POSRepo.CreateVoucher` / `RecordVoucherTransaction` in one transaction. The seed is idempotent: `ErrVoucherIDExists` skips the voucher and its history row.
- `e2e/tests-docs/docs-shots.spec.ts` captures the `vouchers` topic at `/vouchers/DOCS-GV-0001` (`routeOverride`). Before the shot it asserts that `[data-testid="voucher-slip-browser-hint"]` is visible (`mustShow`), so a 404 or a configured printer fails the capture instead of producing a wrong picture.
- `web/help/en/vouchers.md` gets one sentence that describes the picture. `web/help/img/{en,fa,ar,tr}/vouchers.png` and `manifest.json` are regenerated.

**Pipeline.** Author (Dev): Sonnet 5.5. Tester: separate agent. Reviewer: independent, different model (Opus 5.5).

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| F1 | low | The new help sentence describes the hint, but not the **Receipt printer** link the hint carries (visible in the shot). | **Fixed.** The sentence now says "…with a **Receipt printer** link to set one up." The manifest topic hash was regenerated with `node e2e/tests-docs/write-manifest.js` (hashes only, no PNG change). `guard-docs-shots.sh` and `guard-help-topics.sh` pass. |
| F2 | low | Staleness coverage: the surface hash keeps an `internal/pages` file only if it registers a screenshotted `routes[0]`. `/vouchers/{id}` is not anyone's `routes[0]`, so its handler is hashed only because it shares `vouchers_page.go` with `GET /vouchers`. If the detail handler moved to its own file, edits there would no longer mark the shot stale. `web/ui/**` templates stay hashed either way. | Accepted. This is the same model as every other topic. Noted here so a future split of `vouchers_page.go` checks it. |
| F3 | info | `routeOverride` doesn't check that the override is one of the topic's declared `routes:` (`/vouchers/{id}` is declared in `vouchers.md`). | Accepted. There is one entry, its comment points at the seed constant, and `mustShow` catches a wrong page. |
| I1 | info | Correctness checks, all OK. Idempotency: a second `seed()` adds nothing (test), and the Tester's two-run determinism was byte-identical. Locale runs: one docs till DB, and the seed skips existing rows. Repository layer: no raw SQL for vouchers, and `e2e/` is outside `guard-data-access` anyway. Money: `int64` minor units, original equals balance. Leak: the seed runs only on the docs-shots till, and no other `e2e/` spec touches it. `VoucherType`/`Purpose` take the repo defaults (`multi_purpose`). The hint condition (`not $s.printer`) holds because the docs till never sets `printer.mode`. Fixed dates: `main.go` is not scanned, and the test compares only the `2025-12` prefix, more than 60 days back. "Top of this page" is accurate because `internal/manual` inserts the figure right after the `<h1>`. No real shop or client names in the seed. | No change. |

## Verification
- Reviewer runs: `go build ./...` and `go vet ./e2e/...` are clean. `gofmt -l e2e` is empty. `go test ./e2e/seed_docs_sales/...` is ok. `guard-help-topics`, `guard-fixed-dates`, `guard-data-access`, `guard-docs-shots` and `guard-compliance-claims` exit 0 (help-topics gives only the existing non-blocking `notices` translation warnings). I looked at `en` and `ar` `vouchers.png`: the detail page shows the hint above **Print voucher slip** inside the 1024x600 frame, and RTL mirrors cleanly.
- Tester (separate agent): the false-pass check passed. With `main.go` reverted the test fails to build (`undefined docsVoucherID`). With a no-op seed it fails "got 0 vouchers, want 1". `guard-docs-shots-determinism.sh` passed: 133 files byte-identical over 2 runs. Visual check passed for en/ar/fa/tr. 13 guards passed (docs-shots, help-topics, help-drift, fixed-dates, i18n, help-breadcrumbs, help-terms, compliance-claims, competitor-naming, data-access, mail-addresses, core-neutral, card-data-schema). shellcheck was not run (not installed; no `.sh` changed).
- **Not checked:** dark theme, the `de` manual (not a docs-shots locale), and the voucher History row (below the fold, not pictured). The hint text is English in ar/fa/tr: the key is not translated yet and comes via Weblate (ADR-0160).

Pre-flight: web/help topic updated (`vouchers.md`, shots regenerated, manifest updated); reference page n/a (no behaviour change, harness and manual only); README n/a (not stale); en.json/en.meta.json n/a (no new keys); review record this file; follow-ups none (the list-view picture is a non-goal, one image per topic).

**Verdict:** approve, with F1 fixed in this branch.

**Deferred:** none.
