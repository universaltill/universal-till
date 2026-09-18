# End-of-day: ÖKC Z status and device-vs-till reconciliation (ut-docs#2410)

**Date:** 2026-09-18
**Card:** ut-docs#2410 (playbook E3 leftover; child of the Turkey track)
**Complexity:** medium — Sonnet dev, Opus review
**Lane:** `lane:local`

## What shipped

Turkey's certified cash register (YN ÖKC) prints the legal receipt, and its
Z report is what the accountant reconciles against. The till's end-of-day
report now carries, for the same window, the device's evidence and the
till's own count of tenders on the device — TR device market only:

- `internal/data/fiscal_device_repo.go`: `FiscalDeviceWindow` +
  `POSRepo.FiscalDeviceWindow(ctx, methodID, from, to)` — receipts windowed
  on `fiscal_device_receipts.created_at` with the same `instantWindow`
  half-open semantics `EndOfDayInstant` uses; serial/maker from the latest
  receipt; distinct Z numbers ascending; counts by `receipt_kind`; the
  till's own `payments.method_id = "okc"` count on `status='completed'`
  sales in the same window (returns included, same scope as the Methods
  breakdown). `Empty()` distinguishes "nothing happened" from "agree".
- `internal/data/pos_repo.go`: `EODReport.FiscalDevice` (`omitempty`, nil
  for every non-TR shop — archived `content_json` for other markets is
  byte-identical).
- `internal/pages/eod_fiscal_device.go`: `attachEODFiscalDevice`, gated on
  `fiscalDeviceMarketActive`, best-effort (a query failure is logged, never
  fails the close); `localDayWindow` re-anchors the range export's
  `time.Parse`d UTC-midnight dates to the shop's local calendar days
  (matching `EndOfDayRange`'s `sales.local_date` bucketing, ut-docs#869).
- `internal/pages/eod_api.go`: wired into `generateEOD` and
  `/api/reports/eod/range`; printed Z-report footer section
  `FISCAL DEVICE (OKC)` (Serial, Maker, Z no., Mali/Iade/Bilgi/Other, Till
  OKC tenders, `MATCH` / `MISMATCH (device N vs till M)`, or a single
  `NO DEVICE ACTIVITY` line when the window is empty).
- `internal/pages/reports_page.go` + `web/ui/partials/reports_tab_eod.html`:
  the archived-report row renders the block (table, multi-Z note,
  match/mismatch as text + `aria-hidden` icon, empty state) under the same
  `canRunEOD` gate as the other breakdowns.
- 12 new `reports.eod.fiscal_device.*` keys in en/tr/fa/ar (translated in
  the same change); help topic section in all four `web/help/*/reports.md`;
  help-drift baseline entries updated honestly (counts verified); manual
  screenshots regenerated (`make docs-shots` mechanism — see below).

No new device calls; core still never talks to the device from a page.

## TDD evidence

Repo, attach, footer and reports-page tests were written first and failed
against the pre-change tree (`undefined: FiscalDeviceWindow`,
`Z-report missing "FISCAL DEVICE (OKC)"`, `expected fiscal device block
rendered ("AV0001234")`). The orchestrator re-verified by swapping the old
`eod_api.go` and `fiscal_device_repo.go` back in (build fails / tests
fail) and restoring (pass). After the review the tests were strengthened
(below) and each new assertion was shown failing before its fix.

## Orchestrator finding before review

`/api/reports/eod/range` passed `time.Parse`d dates (UTC midnight) straight
into the instant window while the rest of the range report buckets on the
shop's local day — a three-hour shift in Türkiye. Fixed with
`localDayWindow` + `TestLocalDayWindow_LocalMidnightAndExclusiveEnd`.

## Independent review (Opus, fresh context, read-only, ran the gates)

Verdict on the first draft: **not safe to merge** — 1 blocker, 5
should-fix, 3 nits. All addressed:

1. **Blocker — stale screenshot manifest.** The surface changed after the
   first `make docs-shots` run (the orchestrator's late timezone fix), so
   `guard-docs-shots.sh` failed. Regenerated after all fixes (see "Gate").
2. **Maker collected but never shown** — added the row (screen + print +
   help + i18n key), omitted when empty.
3. **Idle window rendered as "✓ match"** — `Empty()` + explicit "no device
   activity" state on screen and `NO DEVICE ACTIVITY` in print; tests for
   both.
4. **Tautological byte-identity test** (compared a render with itself) —
   replaced with an independently established baseline plus marker-absence
   assertions; a dead assertion removed.
5. **Swapped counts undetectable** — asymmetric fixtures (2/1/1, 7/2/9),
   exact rendered lines/cells and the exact `MISMATCH (device N vs till M)`
   string; lower window bound now exercised (`from − 1 min` excluded).
6. **Comparability caveat** — the `TillOKCTenders` comment now names the two
   structural mismatch sources: a zero-total return (no payments row,
   fail-closed device refund still prints, ut-docs#1561/#1788) and a sale
   moved off `status='completed'` after its receipt was recorded. A
   mismatch is a prompt to look, not a verdict.
7. `ORDER BY datetime(created_at)` for consistency with the WHERE.
8. Dead `MATCH`/`MISMATCH` assertion removed.
9. `aria-hidden="true"` on the ✓/⚠ glyphs.

Verified clean by the reviewer: `payments.method_id` is literally the
manifest key `okc`; returns write one `payments` row and land as
`status='completed'`; `instantWindow` is compatible with `created_at`'s
stored forms; translations real with `%d` order preserved; help-drift
baseline honest; logical CSS only; no file writes / cwd paths; no real shop
name or secret.

## Verified beyond automated tests

- Full `go test ./...` in the worktree (dev run) and
  `./internal/data/... ./internal/pages/...` after every fix pass.
- Guards: data-access, i18n, help-topics, help-drift, docs-shots — green.
- **Visual check attestation:** the new block was verified through the
  handler tests' rendered HTML (real mux, real archived `content_json`) and
  the regenerated manual screenshots (which do not seed a TR archived
  report, so the block itself is not in any screenshot). I did **not** look
  at the block in a browser, in dark theme, at kiosk widths, or in fa/ar
  RTL — it reuses the existing breakdown-row table pattern and logical CSS,
  but that is inference, not observation. Worth a glance on the pilot
  tablet when a TR shop first closes a day.

## Gate

`gofmt`, `go build ./...`, `go vet`, `go test ./internal/data/...
./internal/pages/... -count=1`, `guard-data-access.sh`, `guard-i18n.sh`,
`guard-help-topics.sh`, `guard-help-drift.sh`, `guard-docs-shots.sh` — all
green at the final tree. Note for the next local session: `make docs-shots`
runs `npm ci`, which on this Mac currently trips the unaccepted Xcode
licence (`sudo xcodebuild -license accept` fixes it); the capture itself
was run directly (`npx playwright test --config=playwright.docs.config.ts`
+ `node tests-docs/write-manifest.js`) with the already-installed node
modules, which is exactly what the script does after `npm ci`.

## Verdict

Safe to merge. Deferred: nothing from this card. Related open items:
ut-docs#2415 (stable refund attempt id) is the reconciliation's other
known structural source of a +1 on the device side.
