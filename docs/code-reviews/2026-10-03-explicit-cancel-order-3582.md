# Code review — explicit Cancel order for held/table/counter orders; New Sale re-parks (ut-docs#3582)

- **Date:** 2026-10-03
- **Branch:** `feat/3582-explicit-cancel-order` (rebased onto `origin/main` `fed6e32` for this review; base was `b44a834`)
- **Author:** Opus 5.5 (pipeline lane `lane:cloud-41`), two Tester passes before this review
- **Reviewer:** independent Fable subagent (different model from the author), working in a separate filesystem copy of the repo, one round

## What shipped

ut-docs#3582 (split from #3423, which only audited the drop): a cashier can
now get rid of a held, table or pay-at-counter order on purpose, and can no
longer lose one by accident.

- **`POST /api/pos/held/cancel`** (`internal/pages/hold_api.go`,
  `cancelHeldSale`): gated on `void_comp_waste` through the existing
  `checkOrElevate` (same PIN prompt as a priced line void). The order is
  **claimed** off the shop's authority with `heldSaleClaimForResume`
  (cross-till safe, same as resume), its local copy dropped, its table
  claim released, an `audit_log` `held_sale`/`cancel` row written in the
  #3423 payload shape (`InsertAuditElevated` with approver + blocked cashier
  when a PIN cleared the gate), and **`fiscal.order.cancel`** dispatched.
  The order currently open in the basket is refused untouched; an id
  another till already took is refused as not found with no audit row.
- **`fiscal.order.cancel`** (ADR-0138 D2): new `plugins.FiscalOrderCancelEvent`,
  `dispatchFiscalOrderCancel` mirroring `dispatchFiscalOrderStart`
  (HasSubscribers fast path, known-offline short-circuit, goroutine under
  `d.AsyncWork`, `fiscal_order_starts` read inside the goroutine,
  best-effort). Payload `{order_id, tx_id?, tx_revision?, cancelled_at}`,
  `tx_*` omitted when no start was captured. Fifth member of
  `FiscalSignExclusiveEvents`, joined in the change that first dispatches
  it exactly as ADR-0138 D2 says.
- **Surfaces:** an icon-only 46px trash button on every held row of
  `/open-orders` and the sell screen's parked-orders popup, `aria-label`
  and `title` naming the order, native `hx-confirm` stating the order and
  that it cannot be undone (catalog delete's pattern). Popup rows became a
  fixed three-column grid so Cancel and Move table line up; the popup
  widened to 42rem; a 40rem breakpoint stacks the controls under the order.
  Legacy counter rows (never held sales) get a spacer, no Cancel.
- **New Sale on a resumed order with items (priced or add-by-hand) re-parks
  it** under its own identity via `parkCurrentBasket` (keeps its table
  claim, fires `held-changed`, toast `hold.toast.reparked`); an empty
  resumed order is still just cleared. `recordResumedOrderDiscard` is gone.
- Demo mode allows the route; help topic `open-orders` updated in en/de/ar/fa/tr
  (step 5 + rewritten New Sale note, drift baseline updated in lockstep);
  8 new locale keys in en/ar/fa/tr (German is the separate language pack);
  `make docs-shots` re-run; e2e drain helper now cancels instead of
  resume-then-reset (which would re-park forever).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major (coverage) | The "order currently open in the basket is refused" branch (`cancelFailed`, locale key `open_orders.cancel.error.live`) had **no test** on either surface — the branch could be deleted and the suite stayed green (confirmed: mutation M4 below was only caught by the new test). | **Fixed** — `TestHeldCancel_OrderOpenInBasketIsRefused` (`internal/pages/held_cancel_live_test.go`): page redirect carries the live-order `?err=`, popup body carries the refusal with no `held-changed`, row + table claim + live basket untouched, no audit row. Seen failing with the check removed, passing restored. |
| 2 | minor (coverage) | AC asked for "e2e covering cancel"; only `open-orders-row-geometry-2147` measured the button and the drain helper exercised the route over HTTP — nothing drove the browser flow (native confirm → `HX-Retarget` from the hint onto the popup body → toast → badge; page `HX-Redirect` → one-shot banner → empty state). | **Fixed** — `e2e/tests/open-orders-cancel-3582.spec.ts` (2 tests): dismissing the confirm cancels nothing; accepting removes exactly that order with the toast in the body (hint empty) and badge `data-count` following; on `/open-orders` the redirect lands on the banner and the tab's empty state. Elevation stays covered at handler level (the prompt is the shared modal). |
| 3 | accepted (shared limitation) | On a **replica whose primary is unreachable**, `heldSaleClaimForResume` falls back to the local row (claimed=false) and the cancel proceeds locally: the audit row and `fiscal.order.cancel` go out, but the primary still holds the order and it reappears after the outage (a second cancel then writes a second audit row). This is exactly resume's documented ADR-0093 bounded-outage limitation (resume+tender leaves the same stale primary row) and the design asked for "same authority handling as resume". For a genuinely outage-taken order (`primary_synced=0`) the local cancel is fully correct. | Accepted; `cancelHeldSale`'s comment ("two tills can never both act on it") slightly overclaims for this window — worth a follow-up card: refuse or flag a cancel of a `primary_synced` mirror while the primary is unreachable. |
| 4 | nit | `heldOrderAuditPayload`'s comment still describes the discard row as a live writer; the discard path no longer exists. | No change (historically accurate, one reader). |
| 5 | nit | `tx_revision,omitempty` drops a captured revision of `0` while `tx_id` is sent — same as `started_tx_revision,omitempty` on `fiscal.sign.start`; TSE revisions start at 1. | No change, consistent with the precedent. |
| — | pre-existing (Tester follow-up pass) | Tapping the same spot twice to close an opened Move-table `<details>` on the popup can land on the resume button that stretched into that spot. Confirmed pre-existing on `origin/main` (`web/public/app.css:3684/3690`: `.parked-orders-list > li { display:flex; flex-wrap:wrap }` + `.parked-move[open] { flex-basis:100% }` wraps the open details to its own line and `flex:1` stretches the resume button across the gap). The branch's grid keeps the same behaviour (`grid-row: 2` for `[open]`, column 2 collapses). | Out of scope — follow-up card. |

Security / authorization walk (read in full, attacker's view), all OK:
`id` trimmed and only ever used as a key; `tab` and `view` normalised to
the two known values before being echoed into `HX-Redirect`, so no open
redirect / injection; `QueryMsgKey` validates the banner key against the
catalog. The permission gate is the same `checkOrElevate(d, r,
"void_comp_waste", override_pin)` as `/api/pos/remove`; the prompt's
summary reads the order label from the authority (`heldSaleForResume`,
non-destructive), never from the request. No other route deletes a held
row for a cashier: `/api/pos/reset` now re-parks when anything is in the
basket (emptying a resumed order first needs the same `void_comp_waste`
line voids); resume, tender and sync reconcile are the legitimate removers.
The live-basket check runs **before** the claim, so a refusal costs nothing;
the claim is `ClaimAndTombstone` (atomic) on the primary and the
primary-first claim on a replica, so cancel vs resume on two tills can never
both win and the audit row is written only after the claim took. Audit and
dispatch are best-effort after the authority write, same posture as every
sale-screen audit. `d.Engine` is referenced only in non-kiosk files. No SQL
outside `internal/data`; money stays `money.Money`/minor units; no new
`showModal`; logical CSS properties only (`border-bottom` is block-axis).

ADR check: ADR-0138 D2 reserved exactly this wire shape and pre-authorised
the exclusivity-group join on first dispatch; the ut-docs contract
(`reference/contracts/fiscal-sign-ask.md` 1.13.0) already documents the
event, the `tx_*` omission rule and the open SIGN DE `Bestellung-V1`
cancel question. ADR-0138's own "named, not built" paragraphs are now
stale — a revision note in ut-docs is a follow-up, not a contradiction.

## Verification

- **Rebase:** `origin/main` had moved 8 commits (telemetry credential
  #3561, asset pruner #3607, autoUpdateBusy #3596) since the branch's base;
  rebased, one conflict (`web/help/img/manifest.json` `surface_sha256`
  only) resolved and the hash recomputed with
  `scripts/ci/update-docs-shots-surface-hash.sh` on the final head (main's
  own changes were hash-only refreshes; this branch's pixel changes already
  carry their `make docs-shots` run). Supersession check: no other lane
  touched `hold_api.go`, `open_orders_page.go`, `pos_api.go`,
  `fiscal_sign_hook.go` or the two templates on main.
- **Gate on the rebased head:** `go build ./...`, `go vet ./...`, `gofmt -l .`
  clean; `go test ./...` all packages ok; `golangci-lint run ./...` 0 issues;
  every guard in `ci.yml`'s `build` job run locally: all pass
  (`guard-commit-attribution.sh` with CI's `git log` input: all 10 commits
  attributable). `guard-deadcode-baseline.sh` fails identically on a clean
  `origin/main` worktree (`internal/logging` `timestampWriter.Write`; that
  step runs in the GTK/cgo job in CI) — environment, not this branch.
  `shellcheck` not installed here; no shell script changed.
- **TDD / mutation checks (reviewer's own, beyond the two Tester passes),
  each: break → targeted test fails with the quoted reason → restore → passes:**
  M1 re-park gate uses `HasItems()` (drops add-by-hand) → `TestResetHandler_ResumedByHandOnlyOrderIsReParked`;
  M2a never re-park → `TestResetHandler_ResumedOrderWithItemsIsReParked`;
  M2b always re-park (empty too) → `TestResetHandler_ResumedEmptyOrderIsStillDiscarded`;
  M3 `tx_id` without `omitempty` → `TestFiscalOrderCancel_NoCapturedStartOmitsTx`;
  M4 live-basket refusal removed → new `TestHeldCancel_OrderOpenInBasketIsRefused`;
  M5 `fiscal.order.cancel` dropped from `FiscalSignExclusiveEvents` → `TestFiscalSignExclusiveEvents_IsExactlyTheADR0077And0137Group` + every-directional-pair test;
  M6 demo allow entry removed → `TestDemoRouteClassification`;
  M7 approver/actor swapped in `InsertAuditElevated` → `TestHeldCancel_CashierWithManagerPINRecordsApprover`;
  M8 table claim never released → `TestHeldCancel_ManagerCancelsTableOrderFromPopup`.
- **e2e (Playwright, default project, real till, Chromium):** the 10 spec
  files touching open orders / parked orders / the drain helper plus the
  new `open-orders-cancel-3582.spec.ts` — 23 tests, all pass (54s).
- Pre-existing-hazard framing checked against `origin/main`'s CSS (above).
- Not re-done here (done and screenshotted by the Tester passes, no real
  touch hardware in this container): 1024×600 / 360–390px layout, keyboard
  focus through the confirm, RTL.

## Verdict

Safe to merge, with the two coverage additions above committed on the
branch. Deferred: follow-up cards for the replica/unreachable-primary
cancel window (finding 3) and the pre-existing Move-table double-tap
hazard; ut-docs revision note on ADR-0138's "named, not built" wording;
German language-pack keys land in the same cycle (new `en.json` keys →
`lang-pack-drift` goes red on `main` as expected).
