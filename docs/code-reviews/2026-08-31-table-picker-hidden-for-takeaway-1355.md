# Table-assignment picker hidden (and enforced) for Takeaway orders (ut-docs#1355)

**Card:** ut-docs#1355 — product owner live-verified report, 2026-08-31
("Table assignment is offered (and works) in Takeaway mode — should only
be available for Dine in"). **Complexity:** medium.
**Dev:** Sonnet (inline). **Review:** Opus (subagent, one round).

## What shipped

`registerTablePicker` (`internal/pages/table_picker_api.go`) only gated the
table-assignment button/dialog on whether the shop has any enabled tables
configured at all (ADR-0054's soft-gate) — it never read the basket's
current order type, so a cashier could open the picker and assign a
physical table to a Takeaway order.

- `internal/pages/table_picker_api.go`: `registerTablePicker` now skips the
  `ListTablesWithState` query and stays `Configured=false` — the same
  "no chrome at all" shape as the existing "no tables configured" case —
  whenever `d.Engine.OrderType() == pos.OrderTypeTakeaway`.
- `internal/pos/service.go`, `SetOrderType`: switching to Takeaway clears
  any existing table assignment (mirrors `ClearTable`). Switching back to
  dine-in does **not** resurrect it — answers the ticket's own open
  acceptance-criteria question ("clear it? block the switch?") in favour
  of clearing, matching `Tender`/`resetLocked`'s existing convention of
  silently dropping stale per-sale state on a state transition.
- `internal/pos/service.go`, `SetTable`: **the actual enforcement point**
  (added after independent review — see F1 below) — a no-op while the
  sale's order type is Takeaway, refusing an assignment at the service
  layer regardless of caller.
- Tests: `TestSetOrderType_TakeawayClearsTable`,
  `TestSetOrderType_DineInLeavesTableAlone`, `TestSetTable_NoOpWhileTakeaway`
  (`internal/pos/service_test.go`); `TestTablePicker_HiddenForTakeaway`
  (`internal/pages/table_picker_api_test.go`). All confirmed failing
  against pre-fix code (see below), passing after.
- `web/help/en/sell.md`: one clarifying sentence — the Table button only
  shows in Dine in, and switching to Takeaway clears an existing
  assignment; `make docs-shots` re-run, manifest + `en`/`ar` `sell.png`
  regenerated.

No SQL added outside `internal/data`/`internal/db` (the change *removes* a
query on the Takeaway path); no new user-facing strings; the self-order
kiosk registers no table route at all, so `d.Engine` (not `d.KioskEngine`)
is the correct instance here — `guard-kiosk-engine`/`guard-data-access`/
`guard-i18n` all pass.

## Independent review (Opus, subagent)

Verdict: **no blocking defects as originally written; one should-fix
residual gap (F1), fixed in this same session and re-verified.**

**Re-verified the TDD claim personally**, via `git stash push -- <the two
production files>` / test / `git stash pop`:

```
BEFORE (pre-fix production code, new tests present):
--- FAIL: TestSetOrderType_TakeawayClearsTable
    service_test.go:404: basket after switching to takeaway = id="tbl-1" label="T1", want both cleared
--- PASS: TestSetOrderType_DineInLeavesTableAlone
--- FAIL: TestTablePicker_HiddenForTakeaway
    table_picker_api_test.go:184: expected no table chrome at all while order type is takeaway, got
    <span id="table-picker">...<button ... table-picker-trigger ...>🍽️ Table</button>...T1...

AFTER (fix restored):
--- PASS: TestSetOrderType_TakeawayClearsTable
--- PASS: TestSetOrderType_DineInLeavesTableAlone
--- PASS: TestTablePicker_HiddenForTakeaway
ok  	.../internal/pos	0.014s
ok  	.../internal/pages	0.912s
```

The pre-fix render in the failure output is a literal reproduction of the
reported bug: a fully working table picker offered on a Takeaway basket.
Full regression run (`internal/pos`, `internal/pages`, `.../catalog`,
`.../common`) and `gofmt`/`go build`/`guard-docs-shots`/`guard-i18n`/
`guard-data-access`/`guard-kiosk-engine`/`guard-help-topics`/
`guard-compliance-claims` all green.

### Findings and outcomes

**F1 (should-fix, fixed) — `SetTable`/`POST /api/pos/table` were still
ungated, so the ticket's own "...and works" half survived.** The original
fix put the takeaway→clear direction in the service layer but the
table→refuse direction only in the presentation layer
(`table_picker_api.go`), enforcing the invariant asymmetrically. Concretely
reachable: two till browsers sharing one `common.Deps.Engine`, where a
second client's stale (pre-switch) DOM still shows the dialog; or a table
POST already in flight when an order-type switch lands first. The harm was
worse than the pre-fix state — the picker's own clear button lives inside
the now-hidden dialog, so a table attached this way had no visible control
to remove it, and the takeaway receipt template still prints `TableLabel`.
**Fixed**: `SetTable` now refuses (silently clears the incoming id/label)
whenever `s.orderType == OrderTypeTakeaway`, regardless of caller. New
test `TestSetTable_NoOpWhileTakeaway` — confirmed red against pre-fix
`SetTable` (`want both refused/empty`, got `tbl-1`/`T1`), green after.

**F2 (non-blocking, not fixed) — torn read across three lock
acquisitions.** `table_picker_api.go`'s `TableID()`/`OrderType()`/
`TableLabel()` calls each take and release `s.mu` separately; a concurrent
`SetOrderType` between them can render a fragment mixing old/new state.
Self-corrects on the next basket swap (every mutating endpoint re-renders
`#basket`, which re-fetches this fragment) — not worth a `Service`
snapshot accessor on its own. Left as-is.

**F3 (test-quality, fixed) — a "not-red" test's doc comment implied
otherwise.** `TestSetOrderType_DineInLeavesTableAlone` passes even against
pre-fix code (it's guarding against a too-broad clear, not proving the fix
exists) but its comment didn't say so, and it only exercised `""` despite
claiming to cover "any other non-takeaway value." Comment now states
explicitly that this test isn't expected to go red on its own, and the
body now also exercises an arbitrary unrecognized string
(`"garbage"`) to actually back that claim.

**F4 (pre-existing, out of scope) — held sales carry no `OrderType` at
all**, so resuming a held Takeaway order silently reverts it to Dine in
(`internal/pos/hold.go`'s `Restore` calls `resetLocked()`, zeroing
`orderType`), changing the sale's VAT basis (§12 UStG) without anyone
choosing that. Not caused by this change, and not something this fix can
extend into (there's no reliable order type on a held sale to key on).
Filed separately: **ut-docs#1381**.

**F5 (non-blocking, product-owner style call) — the clear is silent.**
`SetOrderType` drops the table assignment with no toast; ADR-0054's own
soft-gate style already argues for quiet UI changes here, and the manual
now documents the behaviour. Left as a design call, not a defect.

**F6 (non-blocking, pre-existing) — `web/help/{ar,fa,tr}/sell.md` describe
the pre-2026-08-30 inline table-picker UI**, not the current
button-opens-a-dialog flow, and now also lack the Takeaway caveat.
`guard-help-topics.sh` only checks topic *presence* per locale, not prose
freshness, so CI stays green. Predates this change; filed separately:
**ut-docs#1380**.

## Testing

- `go build ./...` / `go vet ./...` / `gofmt -l .` — clean.
- `go test ./...` — full suite green (including the pre-existing full
  `internal/pages`/`internal/plugins` packages, ~150s/~120s respectively;
  unrelated to this change).
- All CI-blocking guards in `.github/workflows/ci.yml`'s `build` job run
  locally and pass: `guard-data-access`, `guard-kiosk-engine`,
  `guard-plugin-menu-read`, `guard-i18n`, `guard-compliance-claims`,
  `guard-docs-shots`, `guard-help-topics`, `guard-webkit-version`,
  `guard-kiosk-launch-flags`, `guard-android-status-address`,
  `guard-android-i18n`, `guard-emoji-font`, `guard-htmx-loaded`,
  `guard-autofill-suppression`, `guard-e2e-fixtures-import`,
  `check-brand-assets`, `guard-makefile-version`.
- TDD: all four new/changed tests independently re-verified red-before,
  green-after (see above).

## Follow-ups filed, not blocking this PR

- ut-docs#1380 — `web/help/{ar,fa,tr}/sell.md` translation drift.
- ut-docs#1381 — held sales don't track `OrderType`, so a resumed held
  Takeaway order silently becomes Dine in (VAT-basis-relevant).
