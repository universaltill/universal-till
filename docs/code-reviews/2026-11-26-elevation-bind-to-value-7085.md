# Elevation approval bound to the exact to-value (ut-docs#7085)

Reviewer: independent review (Opus 5.5; code by a Sonnet developer).
Branch `fix/7085-elevation-bind-to-value` (universal-till), companion
`docs/7085-elevation-bind-to-value` (ut-docs `reference/roles-and-permissions.md`).

## What shipped

- `internal/pages/elevation_approval.go` (new): in-memory `approvalStore`
  (16-byte crypto/rand base64url token, 5 min TTL, cap 256 with
  expire-sweep then oldest-eviction, mutex; `mint`/`valid`/`consume`/`reset`)
  and `approvalBinding` (length-prefixed canonical join of version, endpoint,
  session user, sorted actions, the prompt's hidden fields and server-side
  extras). `errApprovalStale`.
- `internal/pages/elevation.go`: `checkOrElevateBound` — permission holders
  return `allowed` with no token; no PIN → prompt + fresh token; PIN with a
  missing/used/expired/other-user/edited token → `errApprovalStale` re-prompt
  **before** `verifyElevationPIN` (no lockout tick, no audit/shrinkage);
  wrong PIN re-renders the same token; token consumed only after the PIN
  elevates. `elevationErrorKey` maps it to `elevation.error_approval_changed`.
- `internal/pages/pos_api.go`: `/api/pos/line` (reduce, raise, both),
  `/api/pos/discount`, `/api/pos/remove` build the hidden-field slice before
  the check and bind it (line adds the unit price; remove adds a
  LineKey|SKU|qty|price snapshot of every matched priced line plus the
  extended value). Non-HTMX JSON 403 blocks unchanged.
- `web/locales/en.json` + `web/i18n-meta/en.meta.json`: one new key, English
  only, `review: standard`. `web/help/en/elevation.md`: one paragraph.
- Tests: `internal/pages/pos_elevation_bind_7085_test.go` (new), and the
  direct-PIN tests in `pos_api_line_reduce_gate_test.go` /
  `pos_api_shrinkage_test.go` converted to prompt-then-retry.

## Pre-flight

Pre-flight: till user-visible behaviour → `web/help/en/elevation.md` updated;
`make docs-shots` n/a (no layout change, only a new error line in an existing
prompt); ut-docs `reference/roles-and-permissions.md` updated; `README.md` n/a
(no stale text); new `en.json` key → `en.meta.json` updated (English only,
ADR-0160); ADR n/a (no architectural choice: in-memory, offline, no schema);
review record = this file; follow-ups below as Backlog items.

## Verification (run by the reviewer)

- `gofmt -l internal/pages` clean.
- `go test ./internal/pages/ -run 'ElevationBind7085|LineReduce|Shrinkage|RemoveHandler|PermissionGroup|5416|6858|6113' -count=1` → ok.
- `go test -race ./internal/pages/ -run ElevationBind7085 -count=1` → ok
  (covers the 8-goroutine consume race; dev had not run `-race`).
- TDD red: not re-run by revert (the tester shares the tree and source edits
  were forbidden this run); accepted the dev's captured red log
  (`scratchpad/7085/red.txt`: every tamper/replay/other-user/expiry test
  failed "not refused: 200", unchanged-retry failed "prompt carries no
  approval field"), which matches the tests' assertions.
- No test in `internal/pages` calls `t.Parallel()`, so the process-global
  store and its `reset()` in `bindEnv` cannot cross-talk between tests.

## Bypass hunt (all held)

- **Unbound form fields.** `/api/pos/line`: the binding uses the *computed*
  `qty` (after `delta`/`qty` parsing) and computed `discount`, and from-values
  read from the engine (`cur.Qty`, `cur.LineDiscount`), not the posted
  `from_*`; adding `delta`, dropping `from_qty`/`from_discount` or posting an
  unparseable discount all change the binding → refused. `reason` is bound
  whenever it is used (reduction). `/api/pos/discount`: `discount` and the
  engine's `from` bound. `/api/pos/remove`: reason, key-or-code and the
  matched-line snapshot bound. The only read-but-unbound field is `code` when
  `key` is also posted (see F2).
- **Action set.** A reduce-only approval reused for reduce+raise has a
  different sorted action list → refused.
- **HTMX vs non-HTMX.** Non-HTMX lacking the permission still gets the 403
  JSON before any PIN on line/discount; on remove (no 403 pre-check, as
  before) a direct `override_pin` post now needs a minted token, so it is
  strictly tighter.
- **Auth off / permission holders.** `canPerform` returns true under
  `UT_AUTH` disabled and for holders → `allowed`, no token, no PIN read.
- **Replay / race.** `consume` is check-and-delete under one lock; the
  `-race` run passes. A captured request (PIN in body) replayed later is
  refused (token gone). Double-tap is already dropped by the dialog's
  `hx-sync="this:drop"`.
- **Lockout/audit ordering.** Stale/missing token is refused before
  `verifyElevationPIN`; the test does 6 wrong-PIN attempts without a valid
  token and then a fresh prompt + correct PIN still elevates, proving no
  lockout tick; `noRows` asserts zero audit/shrinkage rows.
- **Leakage.** Token is a hidden field in the prompt only; bound to the
  session user, useless without the PIN; not logged.
- **Memory.** Bounded at 256 entries, swept on every mint.
- **Test quality.** Tests assert non-application (basket snapshot of every
  line's qty/discount and the sale discount, audit and shrinkage row counts),
  not just status codes.

## Findings

| # | Severity | Where | Finding | Status |
|---|---|---|---|---|
| F1 | Low | `internal/pages/pos_api.go:2256-2266` | Code-addressed `/api/pos/line` binds the posted `code` and unit price but not the resolved line identity (`cur.LineKey`). Scenario: prompt for code-addressed line A; A is removed and a new line with the same SKU/ItemID/VariantID, qty, discount and price is added; the retry's approval applies to the new line. Same value, so no extra loss, but the approval is not for "the exact line". Fix: add `cur.LineKey` (and `cur.SKU`) to the extras. | Accepted (no value at risk); one-line follow-up |
| F2 | Low (pre-existing) | `internal/pages/pos_api.go:2337`, `:1968-1975` | When both `key` and `code` are posted, `key` wins and `code` is unbound, yet `code` is written into the elevated audit row's detail map. A cashier can put arbitrary text in an approved row's `code` field. Fix: write `code` to the detail only when `key == ""` (or bind it). | Accepted; follow-up |
| F3 | Nit | `internal/pages/elevation.go:175-180` | Two concurrent retries with the same live token both run `verifyElevationPIN` before one loses `consume`; the loser re-prompts with "doesn't match the request any more" though the edit applied. UI already drops the duplicate (`hx-sync`), so only a scripted client sees it. Fix if wanted: consume before the PIN check and re-mint on a wrong PIN. | Accepted |
| F4 | Nit | `internal/pages/elevation_approval.go:59-68` | A cashier spamming >256 prompts evicts an older pending approval on the same till (self-DoS, re-prompt). Acceptable at a single till; noted only. | Accepted |
| F5 | Low (out of scope) | `internal/pages/pos_api.go:2689`, `internal/pages/hold_api.go:926`, `:3828`, `refund_page.go:741` | Other PIN gates (`/api/pos/reset` void, held-order void, refunds) still take an unbound, reusable `override_pin`: a request captured with the PIN in its body can be replayed while `from_lines`/`from_total` still match. BA scoped reset out (it has no to-value); single-use is still missing there. | Deferred → Backlog card (generalise `checkOrElevateBound` to the remaining sell-screen PIN gates) |

No blocker or high finding.

## Verdict

**Approve.** The three gates now apply only the values the approver saw,
once, for the same session user; refusal happens before the PIN lookup and
writes nothing; permission holders and auth-off are untouched; tests assert
non-application. F1/F2 are cheap hardening follow-ups, F5 a Backlog card.

## Deferred

- F1 + F2: bind `cur.LineKey` for code-addressed lines; stop writing an
  unbound `code` into audit detail (epic checklist line, <1 day).
- F5: single-use binding for `/api/pos/reset`, held-order void and refund PIN
  gates (Backlog card).
- e2e Playwright specs that type the PIN into the real dialog
  (`prompt-line-reduction-hint-5417.spec.ts`,
  `new-sale-approved-closes-payment-overlay-5416.spec.ts`) carry the token in
  the DOM and should pass, but were not run in this review; the tester/CI run
  confirms.
