# Review: late tap after the till's 30 s card timeout (ut-docs#6304)

Date: 2026-11-16. Lane: lane:local. Card: universaltill/ut-docs#6304 (p1, payments).

## What shipped

- `ut-plugin-payment-stripe` v1.3.0: the Terminal flow moved to `src/terminal.go`
  (go-test covered; CI gained `go test ./...`). The customer gets 18 s, then the
  plugin cancels the reader action and the PaymentIntent and re-checks until
  24 s (no call starts after that): paid -> approve, cancelled ->
  `reader_timeout`, unknown -> `outcome_unknown`. A retry with the same till
  request id (stable per basket, ut-docs#1762) settles the earlier
  PaymentIntent before creating another (Idempotency-Key `ut-<id>-<seq>`).
- `ut-plugin-payment-sumup` v1.3.0: the reader flow moved to `src/reader.go`.
  18 s, then terminate and re-check until 24 s. A terminate 202 is not proof
  of anything: only a FAILED/CANCELLED transaction declines as
  `reader_timeout`; otherwise `outcome_unknown`, record kept. A retry settles
  the old checkout first; with no transaction it starts fresh only after 90 s
  and an ONLINE + IDLE reader status.
- Both: attempts in ONE bounded storage key (`attempts`, last 8; host cap 1024
  keys, no delete).
- `universal-till`: `web/help/en/payments.md` step 4 (reader timeout advice).
- `ut-docs`: `guides/uk-pilot-card-payments.md` timeout section.
- demo / qrpay / eposnow checked: no gap (no money, no authorize hook, no code).

## Review

Tester (Sonnet, independent) and Reviewer (Fable, independent, two rounds).

| # | Finding | Severity | Status |
|---|---|---|---|
| T-D1 | SumUp retry stuck on outcome_unknown after a plain timeout | should-fix | fixed (settle-first + 90 s/idle rule) |
| T-D2 / R-S2 | slow calls can run past the 30 s gate | should-fix | fixed as far as the guest can (24 s, no late calls, skip a read); host part -> #6439 |
| T-D3 | two SumUp mutations survived | test gap | fixed (tests added) |
| R-B1 | SumUp "no transaction" is a 404, not an empty list | blocker | fixed, fake follows docs |
| R-B2 | terminate 202 treated as "not charged" -> double charge possible | blocker | fixed |
| R-S1 | per-attempt keys hit the 1024-key storage quota | should-fix | fixed (one bounded key); pre-existing sale keys -> #6438 |
| R-S3 | retry could start a payment with no time left | should-fix | fixed (`previous_attempt_settled`) |
| R-S4 | docs overstated the guarantee | should-fix | fixed |
| R-nits | Stripe cached 500 key, cancel_action on definite refusal, codeless errors, requires_capture | nit | fixed |
| R2-1..4 | help wording, 2 SumUp test gaps, terminal_reader_timeout prompt | nit | fixed |
| R2-3 | SumUp no-tap timeout blocks card on that basket ~90 s unless SumUp records FAILED | accepted | safe, documented; sandbox check on #6440 |

Re-review verdict: **ship**.

## Verification

- TDD: old 60 s behaviour ported as a stub first; new tests failed (flow past the
  gate at 32-78 s, approve after the gate, orphan PaymentIntent), then passed.
- Mutation runs (Tester 15, Reviewer 16 + 14, orchestrator 22): every remaining
  mutation of a safety line fails a test.
- Real host run (Tester): both wasm modules driven through universal-till's
  `WasmRuntime` with a 30 s gate against a TLS fake provider: Stripe declined
  `reader_timeout` at 20.1 s with `cancel_action` + PI cancel seen; SumUp at
  24.1 s with terminate seen (pre-review timings; deadlines since tightened).
- Gates per plugin: gofmt, wasip1 go vet, go test, build, validate, package
  (+ SumUp i18n guard). universal-till help guards + manual/web tests.

Pre-flight: help topic updated (web/help/en/payments.md); docs-shots n/a (no
screen changed); reference/ n/a (no reference page covers reader timing; the
pilot guide is updated); README updated (both plugins); en.json n/a (no new
key; cashier copy for outcome_unknown -> #6439); ADR n/a (follows ut-docs#1762's
request-id contract); manifest/host functions n/a; follow-ups filed #6438,
#6439, #6440.

## Deferred

- #6438 plugin storage cap vs per-sale keys (p1).
- #6439 host per-request timeout / gate-expiry cancel; outcome_unknown cashier copy; split-tender reversal check.
- #6440 SumUp affiliate app_id, foreign_transaction_id, sandbox checks; Stripe early decline.
- Live reader runs stay on the #6304 checklist (`blocked:env`).
