# Code review: catalog numeric field upper bounds

**Date:** 2026-08-09
**Card:** universaltill/ut-docs#276 — "No upper bound on per-item numeric
catalog fields (lead_time_days, cost_price)"
**Complexity:** easy
**Reviewer:** fresh-context Sonnet subagent (independent, isolated worktree)

## What shipped

`internal/pages/catalog/handlers.go`'s two mutation handlers,
`POST /api/catalog/item-cost` and `POST /api/catalog/item-lead-time`,
validated only the low end of their numeric input server-side (`< 0`);
the client-side `min="0"` attribute bounded nothing on the server. An
absurdly large `lead_time_days` (e.g. `999999999`) was accepted verbatim
and made the inventory-page/reports/digest `DaysLeft <= leadTimeDays`
warning fire permanently — a real, if self-inflicted, footgun.

Fix: widened each existing validation condition to also reject values
above a sanity ceiling, reusing the exact same error branch (same status
code, same message) as the pre-existing negative-value rejection:

- `item-cost`: `f < 0` → `f < 0 || f > 1_000_000` (major currency units,
  checked before minor-unit conversion).
- `item-lead-time`: `n < 0` → `n < 0 || n > 365` (days).

Both ceilings are sanity bounds, not real limits on shop pricing/lead
time — picked as routine engineering judgement (not a product/business
call) per the BA step, matching the issue's own framing.

## What the independent review found

**Verdict: clean, no blocking or non-blocking findings.**

Checked, explicitly:
- **TDD re-verification (done for real, not taken on trust):** reverted
  only the two condition lines (test file untouched), re-ran
  `TestItemCost_ValidationAndClear` / `TestItemLeadTime_ValidationAndClear`,
  confirmed both fail specifically on the new `1000001`/`366` cases:
  ```
  handlers_coverage_test.go:118: cost="1000001": want 400, got 200
  --- FAIL: TestItemCost_ValidationAndClear (0.01s)
  handlers_coverage_test.go:148: leadTimeDays="366": want 400, got 200
  --- FAIL: TestItemLeadTime_ValidationAndClear (0.01s)
  ```
  Restored the fix, both pass again; full `TestItemCost*`/`TestItemLeadTime*`
  suite (7 tests) green.
- **Boundary inclusivity:** `cost=1000000` and `leadTimeDays=365` both
  accepted (200 OK) — confirmed the checks are `>`, not `>=`.
- **Overflow risk:** `internal/httpx/currency.go`'s currency table tops
  out at 2 decimals (fallback also 2); `1,000,000 * 10^2 = 100,000,000`,
  nowhere near `int64` overflow. No known or fallback currency risks
  wraparound.
- **Condition placement:** both ceilings folded into the existing
  boolean, same branch — no separate/unreachable branch introduced.
- **File-write bug classes** (missing `os.MkdirAll`, cwd-relative path
  instead of `paths.Data(...)`): not applicable — neither touched
  handler does file I/O. (The file's separate thumbnail-upload handlers
  do, correctly, and are untouched by this diff.)
- **Repository pattern:** no SQL text in `handlers.go`; confirmed
  compliant with `guard-data-access.sh`'s scope.
- **i18n:** `"invalid cost"` / `"invalid lead time"` are raw
  `http.Error` bodies, not template-rendered — zero matches in
  `web/ui/**/*.html`; not a `guard-i18n.sh` surface, consistent with the
  pre-existing negative-value branch this widens.
- **Manual/help docs:** `web/help/en/catalog.md` doesn't describe the
  old unbounded behaviour as a feature; no stale prose, no update
  needed — this is server-side-only validation per the task's own
  non-goal of leaving client-side `min`/`max` untouched.
- **Clear-field edge case:** empty `raw` still bypasses both checks
  unchanged (`if raw != ""` guards the whole block); existing
  set-then-clear assertions pass unchanged.
- **Demo data / secrets:** none introduced.

## Verified beyond automated tests

- Full gate run before review: `go build ./...`, `go test ./...` (all
  packages), `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh` — all green.
- Independent reviewer additionally ran its own throwaway boundary-value
  requests (`1000000`, `365`) confirming inclusive acceptance, beyond
  what the committed test table covers.

## Safe to merge

Yes. No findings to fix, no deferred items.
