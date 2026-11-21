# Review: restart-orphaned Tap to Pay attempts need attention (ut-docs#6795, backend slice)

Branch `fix/6795-tap-attempt-attention` (Dev commit `4dca79d8e` on `origin/main` `4f64cab0b`), plus
the ADR-0198 §6.8 implementation note on ut-docs `docs/6795-adr0198-restart-note`. Part of
ut-docs#6795, which this slice closes: the manager screen, status chip and help topic are the follow-up ut-docs#6848.

Pre-flight: branch `fix/6795-tap-attempt-attention`, merge-base = `origin/main` `4f64cab0b`; read BA,
Architect, Dev and Tester notes and both diffs in full. Before and after the fixes: `gofmt -l .`
empty, `go build ./...` ok, `golangci-lint run ./...` 0 issues, `guard-i18n`, `guard-i18n-meta`,
`guard-core-neutral`, `guard-data-access`, `guard-kiosk-engine`, `guard-fixed-dates` and
`run-docs-shots-guard.sh` green (the last one after the hash refresh below). No migration, no new
page, so there is no help topic to change. README unaffected.

## What shipped

- Boot (`internal/app/tap_attempts_boot.go`, called right after `jumpNumbersAfterRestore`, after the
  data-dir flock and migrate, before any server or plugin starts): every `pending` attempt becomes
  `unknown`/`till_restarted`. Best effort with a 5 s timeout. A failure is logged and boot goes on.
  No broker call.
- `data.PaymentAttempt.NeedsAttention`, `MarkPendingUnknownAtBoot`, `ListNeedingAttention` (raw
  SQL only in `internal/data`; the deadline is compared in Go, not lexically in SQL).
- `tapAttentionGate` in `completeTender`, before reader resolution, fiscal start and any authorize:
  while an attempt on a provider needs attention, a new tap on that provider is refused (the
  start_failed panel, `tap.attention.start_refused`). The register's own locked leg is exempt.
- `GET /api/pay/attempts/attention` (permission `refund`) and `POST …/resolve` (role plus a fresh
  PIN via `checkStepUp`, which also checks same-origin; broker asked first; audited `tap_resolve`
  with `context: needs_attention`). Both are in `demoDeniedRoutes`.
- `charged_no_sale` = state `approved` plus that reason. No sale is invented.

## Findings

| # | Severity | Finding | Resolution |
|---|---|---|---|
| R1 | **Major** (money) | The design's claim that "an orphan's key can never be reused by a tender" is false for unattended engines. A table-QR basket's engine (`SelfOrderSessions`) was not in `allLiveLockedKeys`, so its locked leg was listed and resolvable while that engine still held the key. `SettleTender` also keeps the attempt id after a later leg's start failure. After a manager resolve as `charged_refund_at_provider`, a retry with the same key hit `tapAuthorize`'s "already approved, same amount" short cut (or `ConfirmTap`'s `PendingAlreadySettled` branch) and **completed a sale on the payment the manager was told to refund**. | Fixed. `tapAuthorize` refuses a key whose row is `approved`/`charged_no_sale` before any plugin runs (`tapStartFailedError{NeedsAttention}`). TDD: `TestTapAuthorize_ChargedNoSaleKeyNeverConfirmsASale` failed first (`conf=&{pi_x}`, err nil), then passed. |
| R2 | Moderate (Tester O1) | The gate exempts only the calling engine's locked legs, while list and resolve excluded the kiosk engine's live legs too. A walk-up kiosk's locked tap leg therefore refused cashier taps on that provider, the manager list was empty, and resolve answered "in progress". That is a dead end until the kiosk idle reset (default 60 s; forever with the reset set to 0). Exempting other engines' legs in the gate is **not** safe: readers of kind `phone` are kiosk-eligible (`readerEligible`), so a cashier tap with a new key on the same phone could release the kiosk's attempt. | Fixed the other way round. Only the cashier engine's locked leg is excluded from list and resolve (its sale screen's pending panel owns it). Kiosk and table-QR legs are listed and resolvable: their customer was sent to the counter, and R1 makes a retry on that engine safe. TDD: `TestTapAttention_KioskLockedLegListedAndResolvable` failed first (`Count:0`), then passed. |
| R3 | Moderate (race) | `NeedsAttention` counted a `pending` row as orphaned the instant its deadline passed. `CollectTap`'s deadline is the collect's own deadline, and after it the same request still runs `ConfirmTap` / `tapCancelThenAssess` (up to `paybroker.Timeout` = 15 s per call). A manager resolve in that window races the till's own confirmation. With "charged" plus the till's own `Mark(approved)`, the result is a sale plus a provider refund. | Fixed. Added `data.AttemptSettleGrace` (60 s). A pending row needs attention only once its deadline plus the grace has passed. TDD: the new "pending at deadline" and "pending within settle grace" cases in `TestPaymentAttempt_NeedsAttention` failed first, then passed. `TestListNeedingAttention_FiltersAndOrders` seeds now sit past the grace. |
| R4 | Minor (Tester O2) | `tapEntry` turns a plugin-list read error into "not a tap", so the gate skipped the leg (fail open), contrary to its own comment. Today the authorize loop re-reads the list and fails closed on the same error. But a transient error that clears between the two reads would start a tap past the gate. | Fixed: fail closed. `tapEntryErr` reports the error, and the gate refuses the tender with `paymentDeclinedError`, the same answer the authorize loop gives for that error today (cash included, so behaviour on a broken plugin table is unchanged). TDD: `TestTapAttentionGate_PluginListErrorFailsClosed` failed first, then passed. |
| R5 | Nit | `ListNeedingAttention` orders by `created_at` in SQL. RFC3339Nano strings misorder rows inside the same second (`.5Z` sorts before `Z`). The order is only for display, and the predicate is already done in Go. | Accepted. |
| R6 | Info | The docs-shots surface hash is stale on this branch. I checked the orchestrator's premise: on a `git archive origin/main` tree, `run-docs-shots-guard.sh` is **green** (surface `4c97ee39…`), so the staleness comes from this diff, not from main. The Tester's "pre-existing" check archived the branch commit `HEAD`, not main. The surface changes cannot alter a rendered pixel: a `basket.html` branch reached only for reason `needs_attention`, which no screenshot seeds, and Go code behind a gate no docs-shots fixture reaches. | Ran `scripts/ci/update-docs-shots-surface-hash.sh` (`web/help/img/manifest.json`, surface `ccc5d383…`). The commit must carry the `Docs-Shots-Unchanged: true` trailer. |

Checked with no finding:
- Authz: GET checks sign-in plus `canPerform(refund)`. POST runs `checkStepUp` (same-origin, session
  plus permission, PIN with a shared lockout) before any read, and the broker is never asked before
  the PIN is proven (tested).
- Demo deny: both routes are in `demoDeniedRoutes` (`TestDemoRouteClassification` green).
- Kiosk-engine guard: the file registers no `/self-order` route.
- Boot vs a running process: `AcquireDataDirLock` (flock) is taken before `db.Open`, so no second
  process can share the DB. The call sits before any collect can start in this process.
- Offline-first: boot never blocks (5 s timeout, error logged). The resolve needs no network except
  the broker, and an override with a reason works offline. Cash is never refused by the gate.
- Card data: only brand, last4 and the PI id are returned (ADR-0127). Money: `amount_minor` goes
  through `money.FromMinor(...).Minor()` at the DTO.

## ADR note accuracy

The ut-docs note claimed "Cash and every other tender are never refused" and quoted the panel text
as "Could not start … Nothing was charged". I corrected both against the code: the real
`tap.attention.start_refused` text; the rule never refuses another tender, and only an unreadable
plugin list refuses one, as authorize already does. I also added the settle grace, the kiosk and
table-QR legs (refused like orphans and settled by a manager), and "an attempt settled as charged
with no sale is never accepted as a sale's payment". `docs/data-model.md` is updated the same way.

## Tester evidence (summary)

PASS from the Tester. Eight mutations (boot no-op, gate on cash, no own-leg exemption, list not
excluding live keys, pending always gating, no broker-says-paid refusal, no step-up, gate call
removed) were each caught by a named test. I re-ran the gate-removed mutation myself, inline:
`_RefusedWhileAttemptNeedsAttention` and `_RefusedAfterNewSaleOrPark` fail, and both pass once it is
restored. Driven run on a real binary with auth on, across two restarts: the boot log line appears,
the row flips to `unknown`/`till_restarted`, an in-flight row is not listed, manager 200 / cashier
403 / anonymous 401, cash completes with an orphan present, and resolve gives 403 without a PIN,
422 without a reason, 200 with `customer_left`, then 409 / 404. `go test -race` over the touched
packages is clean.

## Verification after the fixes

`go test ./internal/pages/... -run 'Tap|Attention|Attempt|DemoRoute|Tender|SelfOrder|Kiosk'` ok;
`go test -race ./internal/pages -run 'TapAttention|TapGate|TapAuthorize'` ok;
`go test ./internal/data/... ./internal/app/... ./internal/pos/...` ok; gofmt, build, lint and the
guards listed in Pre-flight are green.

## Accepted gaps / follow-up

- G1 No test asserts that `app.Run` calls `markOrphanedTapAttempts`. The driven run covers it.
- G2 `tellNativeResolver` on the attention resolve is not asserted (best effort by design).
- G3 "Zero `reader.collect` frames" on a phone reader is not asserted directly. The gate returns
  before `resolveReaderLegs`.
- Resolving a kiosk or table-QR leg leaves that basket locked. After "not charged" its retry starts
  a fresh collect. After "charged" its retry is refused (R1) until its idle reset or session end. A follow-up card should have the manager screen reset that basket. The
  same card covers the screen, chip, help topic and per-reader gating (`reader_ref`).

Verdict: **approve after the fixes above** (made in the working tree, not committed by the reviewer).

## Round 2 (Fable, fix-scoped)

Scope: `git diff 4dca79d8e..HEAD` only (the R1-R4 fixes). Each round-1 test was re-run with its fix
reverted in a throwaway worktree and failed with the claimed message (R1 `conf=&{pi_x}`, R2
`Count:0`, R3 the two grace cases, R4 "let a tender through"); the worktree was removed.

| # | Severity | Finding | Resolution |
|---|---|---|---|
| R2-1 | **Major** (money, race) | R1 guards only `tapAuthorize`'s entry. R2 made a kiosk or table-QR basket's locked leg resolvable while that engine can still retry it (its own locked key is exempt from the gate, and `Begin` leaves an `unknown` row unchanged, so the row stays resolvable for the whole retry). Manager settles it `charged_refund_at_provider` after the retry passed the entry check; the plugin then fails (its `Begin` now answers `ErrAttemptSettled`, or any timeout or decline); `tapVerifyFailure` (`internal/pages/tap_gate.go`, the `PendingAlreadySettled` branch) read the approved row as "paid" and **completed the sale on the payment the manager was told to refund**. | Fixed. That branch reads the row again: `approved`/`charged_no_sale` is refused like R1 (`tapStartFailedError{NeedsAttention}`), an unreadable row is pending. TDD: `TestTapAuthorize_ChargedNoSaleSettledMidFlightNeverConfirmsASale` failed first (`conf=&{pi_x}`, err nil), then passed. |
| R2-2 | Moderate (double charge) | A tap leg whose plugin exits `outcome_unknown` returned from `tapVerifyFailure` with its row still `pending`. With R3 a pending row gates nothing until deadline + 60 s (and never did before the deadline), so "plugin times out, cashier presses New sale, rings again and taps" started a second tap on the provider with a new key, the case the gate exists for. | Fixed at the source: that branch marks the row `unknown` (`not_confirmed`) before returning, so no finished request leaves a `pending` row and the grace only ever covers a request still running. TDD: `TestTapGate_PluginOutcomeUnknownThenNewSaleStillGates` failed first (row `pending`), then passed. |

Checked with no finding:
- R1, other paths: the plugin-approved path ends in `ConfirmTap`, which answers `already_settled` as
  pending (no sale); the retry is then refused at entry. `/pay/tap/check` and
  `/manager/pay/tap/resolve` act on the cashier engine's locked leg only, which the attention resolve
  refuses, so they cannot meet a `charged_no_sale` key. A cashier key kept after a lock drop is
  unlocked, so the gate refuses its retry until it is settled, and R1 after that.
- R2: the cashier lock is held for the whole retry and check, so its leg stays hidden and
  unresolvable; a row only turns `unknown` at the end of its request, milliseconds before the lock.
- R4: correct. The refusal is `paymentDeclinedError`, as the authorize loop answers.
- The R1 refusal goes through `SettleTender`, so a kiosk basket is unlocked and its next try is a
  fresh attempt, not "refused until idle reset" as the gaps list says. Harmless: the first payment
  is refunded at the provider, the second pays the sale.

Verification: `go test ./internal/pages/... -run 'Tap|Attention|Attempt|Tender|SelfOrder|Kiosk|Reader'`,
`go test ./internal/data/... ./internal/pos/...`, `gofmt -l .` empty, `go build ./...` ok. The
docs-shots surface hash was refreshed again (Go failure paths only, no rendered pixel).
`docs/data-model.md` updated. Not committed by the reviewer.

Verdict: **approve with the two fixes above.**
