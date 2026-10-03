# Code review — a resumed order discarded by New Sale leaves an audit row (ut-docs#3423)

- **Date:** 2026-10-03
- **Branch:** `fix/3423-resumed-order-discard-audit`
- **Author:** Opus 5.5 (pipeline lane `lane:cloud-54`)
- **Reviewer:** independent Fable subagent (different model from the author), one round

## What shipped

ut-docs#3423 found that a held, table or pay-at-counter order resumed into
the live basket has already left `held_sales` (`resumeHeldSale` deletes it,
or claims/tombstones it cross-till), so a **New Sale** tap
(`POST /api/pos/reset`) dropped it with no held row, no sale and no audit
entry.

- `recordResumedOrderDiscard` (`internal/pages/hold_api.go`), called by the
  reset handler (`internal/pages/pos_api.go`) before `Engine.Reset()`:
  when the basket carries a held origin and still has lines (priced or
  "add by hand"), it writes `audit_log` `held_sale` / `discard`, entity id =
  the order's held id, payload `label`, `total_minor`, `line_count`,
  `by_hand_count`, `table_id`, `display_no`, `first_parked_at` (ISO-8601).
  Best-effort: a failed write is logged and never blocks the reset
  (offline-first).
- Help: `web/help/{en,de,ar,fa,tr}/open-orders.md` — one sentence on what
  New Sale does to an opened order and where it is recorded (structure
  unchanged; `make docs-shots` re-run, manifest refreshed).
- Behaviour is otherwise unchanged: New Sale still clears the basket. The
  card's other question — an explicit, permission-gated **Cancel order** and
  re-parking on New Sale — is designed and split to ut-docs#3582 (re-parking
  first would have removed the only way to get rid of an order).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | Gate used `HasItems()` (priced lines only): a kiosk pay-at-counter order whose lines all became "add by hand" still vanished silently. | **Fixed** — gate on the snapshot's lines + add-by-hand; `by_hand_count` in payload; test `TestResetHandler_ResumedByHandOnlyOrderLeavesDiscardAudit` (seen failing first). |
| 2 | minor | Help said the order "never disappears without a trace" — overclaims (a till restart while an order is open still loses the in-memory basket). | **Fixed** — reworded to "New Sale never drops an order silently" in all five locales. |
| 3 | minor | `first_parked_at` copied held_sales' `2006-01-02 15:04:05` text; API dates are ISO-8601. | **Fixed** — re-formatted RFC3339 (raw fallback); test asserts it. |
| 4 | nit | Origin/snapshot read and `Reset()` are separate locks; two concurrent resets from two tabs could double-log. | Accepted — same pattern as the existing `TableID()`-then-`Reset()`; one `Snapshot()` call now narrows it. |
| 5 | nit | Help bolds the `kiosk.checkout_start` label. | No change — that is the button's real label in every locale. |
| — | pre-existing | Resuming another order over a by-hand-only one skips the auto-park (same `HasItems()` gap) and overwrites it. | Out of scope — filed ut-docs#3586. |

Reviewer also checked and found OK: `audit_log.actor_id` FK (`auth.UserID`
falls back to the seeded `system` user), money in minor units via
`money.Money`, no double-logging (auto-park, tender and line voids don't go
through the handler), kiosk engine untouched, no SQL outside `internal/data`,
translations match `audit.title` / `kiosk.checkout_start` in each locale.

## Verification

- TDD: the main test failed before the fix (`got 0` audit rows) — reproduced
  independently by the reviewer in a separate worktree by reverting the
  production files; the by-hand and ISO-8601 tests were also seen failing
  before their fixes.
- `go build ./...`, `go vet`, `gofmt -l` clean; full `go test ./...` green
  before the review fixes, `internal/pages` + `internal/pos` re-run after.
- Every guard in `ci.yml`'s `build` job run locally: all pass after
  `make docs-shots`, except `guard-shellcheck-version.sh` (no `shellcheck`
  binary in this container; no shell script touched).
- No UI surface changed (a backend audit write plus help prose), so no
  driven/visual run; existing e2e for New Sale is untouched.

## Verdict

Safe to merge.
