# Code review — buildKitchenTargets / receiptDesignFromSettings settings-read errors (ut-docs#1533)

- **Date:** 2026-09-05
- **Branch:** `fix/1533-kitchen-receiptdesign-settings-read-errors`
- **Reviewer:** independent read via a fresh-context Sonnet subagent
  (complexity:easy → reviewer runs at Sonnet, per the
  `reviewer`/`scrum-master` skills' model-routing table), no shared
  context with the implementation, run in an isolated worktree.
- **Verdict: SAFE TO MERGE.** Zero blocking findings.

## What shipped

Residual follow-up to ut-docs#1153 (`printerConfigChecked`/
`kitchenPrintingEnabledChecked`, merged to `main`): two settings-read call
sites #1153 explicitly scoped out still discarded the error from
`d.Settings.Get`, unable to tell "key not set" (`ok=false, err=nil`) from
a genuine read failure (`err!=nil`).

- `buildKitchenTargets` (`internal/pages/kitchen_print.go`) now calls the
  checked `printerConfigChecked` instead of the plain `printerConfig`, and
  returns the error immediately — same shape as its existing
  `GetSaleDetail`/`ResolveKitchenStations` error returns right above it.
  No new failure-recording code was needed: `printKitchen`'s existing
  `err != nil` return, `printKitchenAsync`'s existing audit +
  `SetKitchenPrintFailed` branch, and the manual `POST /api/print/kitchen`
  endpoint's existing `kitchen.print.failed` branch all already handle a
  non-nil `buildKitchenTargets` error generically.
- `receiptDesignFromSettings` (`internal/pages/print_api.go`): doc-comment
  only, recording the reviewed conclusion that a read failure here is
  cosmetic-only (header/footer/logo/toggle formatting) and never turns
  into a missed/failed receipt — `buildReceiptDoc`'s callers already gate
  on a *different*, already-checked printer-config read before ever
  reaching this one, so there is no failure-reporting obligation to add.
  Zero logic change.
- New test `TestBuildKitchenTargets_SurfacesSettingsReadError`
  (`internal/pages/kitchen_print_test.go`): drops the `settings` table
  (a genuine read error, not just an unset key — same technique #1153's
  own tests used) and asserts `buildKitchenTargets` now returns a non-nil
  error where it previously silently defaulted.

## Review findings

No correctness, concurrency, money, repository-pattern, or i18n issues.
The reviewer traced every caller in the chain
(`buildKitchenTargets` → `printKitchen` → `printKitchenAsync` /
`registerKitchenPrintAPI`'s manual endpoint) and confirmed each already
handles a non-nil error generically — no swallowing, no double-audit, no
panic. It also independently traced *why* the manual print endpoint was
the call path most exposed pre-fix: it gates only on the unchecked
`kitchenPrintingEnabled`, so before this change a genuine settings-read
failure there fell through to silently defaulted config — worse than a
simple false negative, since a wrong charset could reach a live ticket
undetected, not just a skipped print.

The reviewer also went one step further than the ticket text on
`receiptDesignFromSettings`'s deferral: `buildReceiptDoc`'s own
`printerConfig` read (a *different* unchecked call, same function) is
technically also unchecked, but both of `buildReceiptDoc`'s real callers
(`printReceiptAsync` via `printerConfigChecked`, and the manual reprint
endpoint via `cfg.Enabled()`) already gate on a checked read before ever
calling it. Since `printer.*` and `receipt.*` settings live in the same
table and a real failure mode (dropped table, disk I/O, lock) is
table-wide rather than per-row, the reviewer judged the residual risk on
`receiptDesignFromSettings` itself to be a design defect at worst (wrong
footer/tax/logo), never a silently-missed print — confirming the
deferral is reasoned, not a gap.

One non-blocking process note: the reviewer initially looked for
#1153's own review record in the wrong repo (`ut-docs`) and couldn't
find it there — it lives at
`universal-till/docs/code-reviews/2026-09-04-async-print-settings-read-failure-1153.md`
(this product keeps review history in the product repo itself, not
`ut-docs`). No code action needed; noted here so a future reader isn't
confused by the same wrong-repo lookup.

## Verification beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l internal/pages/kitchen_print.go
  internal/pages/kitchen_print_test.go internal/pages/print_api.go` — clean.
- `go test ./internal/pages/...` and full `go test ./...` — green.
- `guard-data-access.sh`, `guard-i18n.sh` — pass.
- Targeted `-race` run on the touched print/kitchen/async test family —
  clean, no data races (run by Dev/Tester before handoff; independently
  re-run by the reviewer without `-race` due to time budget, standard
  full suite green either way).
- **TDD claim independently re-verified by the reviewer, not just
  asserted:** hand-reverted only the `buildKitchenTargets` hunk back to
  the unchecked `printerConfig` call, confirmed
  `TestBuildKitchenTargets_SurfacesSettingsReadError` fails with the
  exact predicted symptom (`"expected buildKitchenTargets to surface the
  settings read error, got nil"`), then restored the real fix and
  confirmed all tests pass again.
- No real client/shop name or secret-shaped literal introduced (new test
  uses the same synthetic `itm-steak`/receipt-number pattern already used
  throughout this test file).

Refs: ut-docs#1153 (prior fix this follows up), ut-docs#1533 (this card).
