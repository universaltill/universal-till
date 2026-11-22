# Review: one tender at a time per till, and basket controls can't abort it (ut-docs#6909)

Date: 2026-11-22. Lane: `lane:cloud-41`. Author model: Sonnet (Dev, two rounds).
Independent reviewer: Opus 5.5, fresh context, in its own worktree (round 1 on
the full diff, round 2 on the fixes only). Verdict: **ship** (round 2).

## What shipped

`/api/pos/tender` had no in-flight guard, and about 40 basket controls carry
`hx-sync="#basket:replace"`, so a scan or a second tap during a card wait
aborted the tender request in the browser while the server (since #6805) kept
running it. Three failures followed: a second Pay reused request id R and its
deferred reversal could refund the payment of the sale request 1 had just
recorded; a line scanned mid-tender was wiped by the final `engine.Reset()`;
and the browser never showed the result of the sale.

- `internal/pos/service.go`: `TryBeginTender` / `EndTender` / `TenderInFlight`
  on the engine. No `Reset*`, settle or lock path touches the flag.
- `internal/pages/pos_api.go`: the tender handler takes the guard as its first
  statement with a deferred release (every return and panic). A second tender
  gets **409** `tender_in_flight` (`{data:null,error:{code,message}}`,
  `HX-Reswap: none`, `HX-Trigger: tender-in-flight`) from the new
  `internal/pages/tender_inflight.go`, before reading the body or touching
  engine state, so the refusal has no side effects. The kiosk keeps its own
  per-basket guard (KioskEngine, ADR-0020) and is untouched.
- `scan` is refused while a tender runs: the unchanged basket with a toast
  (`pos.tender.scan_refused`), like the item-not-found reply.
- Tap (follow-up from the #6908 review): `/pay/tap/check`, the manager resolve
  and the attention resolve take a per-leg guard before their detached work,
  and check/resolve also refuse while the engine has a tender in flight
  (check: the existing "still checking" panel, no broker ask; resolve: 409
  `tap.resolve.in_progress`).
- `web/public/app.js`: an `htmx:confirm` gate (fires before htmx's sync-abort in
  the vendored 1.9.12) holds every `#basket` request while the tender xhr or
  the split-modal fetch is pending, shows `#tender-busy-notice` (a `role=status`
  banner inside the basket, no modal), sets `aria-busy`, and keeps the scanned
  code in the scan box (`window.utTenderPending`). Only `/pay/tap/remote*` is
  exempt; status, lock and exit don't target `#basket`. After a lost tender
  response (abort/error/timeout: `detail.error` or `xhr.status === 0`) it
  re-reads `/ui/basket`, repeating every 1.5 s (bounded) while the server
  answers `X-UT-Tender-In-Flight`.
- i18n: `pos.tender.in_flight`, `pos.tender.busy`, `pos.tender.scan_refused`,
  `tap.resolve.in_progress` (English + `en.meta.json`, review standard).
- Help: `web/help/en/sell.md` "While a payment is running".

No ADR change: ADR-0136 (card tender), ADR-0198 and ADR-0020 still hold; the
guard follows the kiosk's existing `selfOrderCheckoutInFlight` pattern.

## Tests (TDD)

`internal/pos/service_tender_inflight_test.go`,
`internal/pages/tender_inflight_test.go`, `internal/pages/tap_inflight_test.go`,
`e2e/tests/tender-in-flight-gate-6909.spec.ts`.

Red before the fix, re-verified by the reviewer in its own worktree:
- `TestTender_SecondConcurrentTenderIs409`: "second tender did not return 409
  while the first waits (2 authorize calls)" (round 1 hung instead; fixed, n1).
- `TestScan_RefusedWhileTenderInFlight`: fails at the scan-refused assertion.
- `TestTapCheck_ConcurrentSameLegOneBrokerAsk`: "broker asked 2 times while a
  check runs, want 1".
- `TestTapCheck_RefusedWhileTenderInFlight` /
  `TestTapResolve_RefusedWhileTenderInFlight`: 2 broker calls / 200 before the
  round-2 guard.
- Playwright: the gate test (scan during a held-back tender sends no request,
  notice shown, code kept, no modal, receipt view after) and the abort-resync
  test (`route.abort('connectionreset')` → `/ui/basket` re-read). The resync
  test failed on round 1 (M1) and passes after the fix.

## Findings

Round 1 (on 59cc4cf1):

| # | Sev | Finding | Outcome |
|---|---|---|---|
| M1 | major | Lost-response resync never ran: htmx 1.9.12 fires `afterRequest` (with `detail.error`, not `failed`) before `sendAbort`/`sendError`, so the gate released without re-reading. The Tester found the same thing (D1) independently | fixed |
| M2 | major | Exemption `/pay/tap/` let tap "Check" abort a running tender; server check/resolve could run beside a tender retry on the same leg | fixed: exempt only `/pay/tap/remote*`; check/resolve refuse while `TenderInFlight()` |
| M3 | major | Scan code still cleared on the normal Pay path (only the split modal set the flag); contradicted the help text. Tester D2 | fixed: `window.utTenderPending`, e2e asserts the field value |
| m1 | minor | Recheck counter not reset after its limit | fixed |
| m2 | minor | Attention resolve took the leg guard after its state checks | fixed: guard right after `repo.Get` |
| n1 | nit | 409 test failed by hanging, not by assertion | fixed |
| n2 | nit | Other basket mutations refused only client-side; scan check vs `AddLine` not atomic | accepted → #6919 |
| n3 | nit | Clear / New sale / Hold also wait during the tender | accepted (BA default), one help sentence added |

Round 2 (on the fixes only, 88a0dc15): all seven fixed, no regression.
Checked against the vendored htmx 1.9.12: `detail.error` is set only on a real
loss, so a 409/5xx still releases normally and the following `sendAbort` finds
no xhr (no double resync); the recheck loop is bounded (40 × 1.5 s) and
releases if the resync itself fails; the check/resolve refusals run before the
throttle stamp, the leg guard, any broker ask, unlock or attempt-id rotation;
the tender's own tap retry calls the service directly, so it isn't blocked.
TDD: removing only the `/pay/tap/check` tender guard fails
`TestTapCheck_RefusedWhileTenderInFlight` ("check asked the broker beside a
running tender (1 -> 2 calls)").

| # | Sev | Finding | Outcome |
|---|---|---|---|
| m3 | minor | Reverse race: a tender started while a Check/resolve is already running server-side isn't refused (`tap_pending.go:329`, `:427`) | deferred → #6919 |
| n4 | nit | No client timeout on the tender request: a silently dead connection holds the basket until reload (lock/exit stay reachable) | accepted, pre-existing |
| n5 | nit | Recheck gives up after ~60 s, a tap can take 120 s; the basket unblocks but the server still refuses scans and a second Pay | accepted |

## Verification beyond unit tests

- Tester, real till in Chromium with the tender held back: a scan during the
  payment sent no request, the notice showed, the basket was marked busy, no
  modal opened; after release the receipt appeared and a new sale scanned
  normally. Looked at by eye at 1280×800 and 1024×600 (light theme): a readable
  two-line banner at the top of the basket, no overlap or clipping. Not
  checked: dark theme, RTL, German, phone/kiosk widths.
- Regression: `card-reader-picker-6566.spec.ts` 11/11; `internal/pos` and
  `internal/pages` full suites pass; `-race` on `internal/pos` and on pages
  `-run 'Tender|Tap|Scan|Attention'` pass; every other package passes; 68/70
  build-job guards pass (the two misses are sandbox-only: catimport tags not
  fetched, no shellcheck binary); golangci-lint 0 issues.

Pre-flight: help topic `web/help/en/sell.md` updated; `make docs-shots` n/a
(the notice is hidden by default, no screen changes in the shots); new
`en.json` keys have `en.meta.json` entries (English only, Weblate does the
rest); `reference/` n/a (no reference page describes tender concurrency);
README n/a; ADR n/a (no decision changed); follow-ups → ut-docs#6919.

## Deferred

ut-docs#6919: server-side refusal of every basket mutation while a tender
runs, a "last completed sale" marker so the resync shows the receipt, the
engine guard on the kiosk checkout, and a neutral name for the keyed
in-flight set.
