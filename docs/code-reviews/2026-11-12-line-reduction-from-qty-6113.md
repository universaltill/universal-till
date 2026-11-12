# Review: line-reduction PIN approval replays `from_qty` (ut-docs#6113)

Date: 2026-11-12 · Lane: `lane:cloud-54` · Complexity: easy
Author: Sonnet (Dev subagent) · Reviewer + Tester: Opus 5.5 (independent subagent, separate worktree)

## What shipped

- `/api/pos/line` (`internal/pages/pos_api.go`): the manager-PIN prompt for a
  line reduction now replays a hidden `from_qty` (the qty shown in "Reduce X
  from 3 to 1"). An approved retry whose `from_qty` no longer matches the
  line (or whose line is gone) is refused **before** `checkOrElevate` with
  the existing 409 + `basket.line_changed` note: nothing applied, no
  shrinkage or audit row, PIN never checked. An unparseable / NaN / Inf /
  negative `from_qty` is a 400. Posts without `from_qty` (steppers, qty
  field) are unchanged; the #5417 compare-and-set still covers the
  read→write race.
- `/api/pos/remove` replays no qty and its summary shows none: unchanged.
- `web/help/en/sell.md`: one sentence on what happens when the line changes
  before the PIN is entered.
- Tests (`pos_api_line_cas_test.go`): prompt carries `from_qty`; stale
  approval with a valid PIN → 409, line stays 5, 0 shrinkage / 0 audit
  rows; matching approval still applies; invalid `from_qty` → 400; stale
  approval with a wrong PIN → 409, not a re-prompt; code-addressed stale
  approval → 409 with the note.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| F1 | minor | Code-addressed (no key) approval whose line was removed got a 409 with no note (`LineChangedKey` = ""). API clients only; the basket UI posts `key`. | Fixed: `LineChangedKey = "code:"+code` renders the basket-level note; test added (failed first: `want 409 with the note, got 409`). |
| F2 | minor | No test pinned "stale retry is refused before the PIN check". | Fixed: wrong-PIN stale-retry test added (regression guard for the ordering). |
| F3 | nit | Double-submitting the same approval: second gets "line changed". Nothing applied; prompt buttons already disable in flight (#5288). | Accepted. |
| F4 | nit | Code-addressed lines compare qty, not identity (same SKU+qty replacement). Pre-existing, unreachable from the UI. | Accepted, no card. |

Also checked by the reviewer: weighed qtys round-trip exactly through
`FormatFloat('f',-1)`/`ParseFloat` (0.235, 0.1+0.2, 1/3, 2.675, 1e-07, …);
a forged `from_qty` can only refuse, never skip the gate (cashier 5→1 with
`from_qty=5` still prompts); 409 HX retarget headers asserted.

## TDD re-verification (reviewer, own worktree)

With `pos_api.go` reverted to `origin/main`: `ElevationReplaysFromQty`
failed (`from_qty = "", want 3`), `StaleApprovalRefused…` failed (`want 409,
got 200`), `InvalidFromQtyIs400` failed (`want 400 …, got 200`); restored →
all `TestLineHandler_*` pass.

## Gate

gofmt, `go build ./...`, `go vet`, `golangci-lint run ./...`, every guard in
`ci.yml`, full `go test ./...` — see the PR. Not run: Playwright e2e and
`make docs-shots` (no client-side change; the 409 render path is #5417's,
already e2e-covered; help edit is prose only).

Pre-flight: Till user-visible behaviour — `web/help/en/sell.md` updated;
screenshots n/a (prose only); `reference/` n/a (no ut-docs page describes
this prompt); README n/a. en.json n/a (reuses `basket.line_changed`).
ADR / plugin / plan rows n/a. Follow-ups: none.

## Verdict

Approve (after F1, F2).
