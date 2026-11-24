# Review: kiosk start-screen reset honours the tender commit point (ut-docs#7022)

Date: 2026-11-24 · Lane: `lane:cloud-41` · Complexity: easy
Built by: Sonnet 5.5 (Dev) · Tested by: Opus 5.5 (separate agent) · Reviewed by: Opus 5.5 (independent agent, fresh context)

## What shipped

- `internal/pages/tender_inflight.go`: `resetUnlessCommitting(eng *pos.Service) bool`
  runs `BeginAbandon` → `Reset` → `EndAbandon` on the given engine. It skips the
  reset while a tender holds the commit point (#6923). It takes the engine, not
  `d`, so the self-order route file never names `.Engine` (kiosk isolation).
- `internal/pages/self_order_page.go`: the walk-up landing (`GET /self-order`
  without a bound table) uses it instead of `d.KioskEngine.Reset()`. The start
  screen still renders 200 either way: no 409, no modal, no toast (offline-first).
  The language-cookie clearing on `!bound` is unchanged.
- `internal/pages/pos_api.go`: the "Exception: the kiosk landing page…" sentence
  in `completeTender` is gone; the rule now holds for the kiosk too.
- `web/help/img/manifest.json`: surface hashes refreshed (no visual change).
- ut-docs `reference/payment-provider-contract.md`: the abandon list names the
  kiosk returning to its start screen (reverses captured legs while authorizing;
  skips its reset once the sale is committing).

## BA finding (scope correction)

The card's claimed harm (the tender's post-commit `Reset()` wiping the next
customer's scans) cannot happen today. The kiosk checkout holds `TryBeginTender`
and every kiosk basket edit is refused during a tender (#6926). The real defect
was a rule break: an abandon took effect inside the commit span (it rotated the
attempt id and wiped the paying customer's basket mid-commit). That was harmless
today but one engine read after the commit away from a bug.

Design choice (Architect): **refuse, not defer**. A deferred reset would either
block the customer page for up to the commit span (sign ask + `CompleteSale`,
which can exceed `tenderDrainTimeout`) or need a new pending-reset flag. If the
commit then fails (cannot-sign, or the #7021 gap), the basket is kept for a
retry and the next landing clears it. Accepted residual.

## Tests

New `internal/pages/self_order_landing_commit_7022_test.go`:
- landing during the fiscal sign ask: 200, attempt id unchanged, basket kept,
  one sale recorded, cashier engine untouched;
- next customer's basket kept after the commit (regression guard only; it
  passes without the fix, noted in the test);
- landing during authorize still abandons: basket cleared, checkout refused,
  no sale, exactly one reversal of the captured card leg (#6602);
- idle landing resets and clears the language cookie, including an unknown `?table=`;
- `TestResetUnlessCommitting`: held commit, after commit, nested abandons.

TDD re-verified twice (Tester and Reviewer, each separately): with the direct
`d.KioskEngine.Reset()` restored,
`TestSelfOrderLanding_DuringSignAskSkipsResetAndSaleRecorded` fails with "the
landing reset must be skipped while the commit point is held: attempt/basket
were wiped". Restored → green, `-count=3 -race`.

## Gate

gofmt, `go build ./...`, `go vet ./...`, golangci-lint (0 issues); `-race` on
the related `internal/pages` tests (tender, cross-till, cannot-sign, self-order,
commit point) and `internal/pos`: ok. 71 of 72 build-job guards were run
locally and passed. Shellcheck isn't installed here, so it wasn't run; no
scripts changed. A full `go test ./...` hit Go's default 10 min timeout on
`internal/pages` and `internal/plugins` under a loaded machine, with no
failures. CI runs both packages with `-timeout 20m`.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| T1 | should-fix (Tester) | Sign-ask test compared the attempt id with itself (false pass) | Fixed: captures the id before the landing |
| T2 | should-fix (Tester) | Authorize test didn't assert the reversal | Fixed: `refunds()==1` on the 7019 card-plugin setup |
| T3 | blocker (Tester) | docs-shots surface hash stale | Fixed: `update-docs-shots-surface-hash.sh`, `Docs-Shots-Unchanged: true` |
| R1 | nit | Next-customer test passes without the fix | Fixed: comment says it is a regression guard only |
| R2 | nit | `self_order_page.go` comment said landing "always" resets | Fixed |
| R3 | nit | Docs clause wording awkward | Fixed |
| R4 | nit | First run of the sign-ask test ~13.5 s (setup cost) | Accepted: passed every time; well under CI's 20 m |
| R5 | nit | Test helper returns 7 values | Accepted: test-only readability |

No blockers left. Race, deadlock, nil-engine, table-bound path and the
recurring-bug checks (MkdirAll, cwd-relative paths) are all clean.

Pre-flight: till user-visible behaviour: n/a (no visible change; same start
screen, no template/CSS/JS/i18n change), help n/a, docs-shots hash refreshed;
`reference/payment-provider-contract.md` updated (ut-docs); README n/a;
en.json n/a; ADR n/a (applies the #6923 rule); follow-ups: none new (#7021
already tracks the `CompleteSale`-after-sign gap).

Verdict: **approve**.
