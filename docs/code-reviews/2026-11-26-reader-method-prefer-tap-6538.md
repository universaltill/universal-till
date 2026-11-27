# Review record: approving a phone reader fails when several payment methods have a readers hook (ut-docs#6538)

Date: 2026-11-26. Author: Claude (Sonnet 5.5) with the owner testing live.
Reviewer: none independent (small fix, urgent live test, `[no-subagent]` tag). An independent Reviewer pass is owed in the next batch review.

## What shipped
`internal/pages/reader_identity.go` `resolveReaderMethod`: when several active payment methods declare a `payment.<method>.readers` hook, the single entry marked as a tap entry (config `{"tap":true}`, `plugins.IsTapEntryConfig`) is the method a phone reader takes taps for. Zero, or several with no single tap entry, is still refused (`errNoReaderMethod`). Core names no provider.

## Why
Live on the owner's Mac (shop with SumUp, Stripe server-driven card, and Stripe Tap to Pay, all active): approving the phone's reader request returned 409 `readerErrNoMethod` ("could not approve") because three methods declared a readers hook and the rule demanded exactly one. The Stripe plugin itself declares two readers hooks (`stripe` and `stripe_tap`), so the shipped plugin could never pair a phone reader, only the test fakes could.

## Evidence
- Mac DB copy (read-only): plugin_entries payment rows sumup, stripe, stripe_tap all active; plugin_hooks `payment.sumup.readers`, `payment.stripe.readers`, `payment.stripe_tap.readers`; `stripe_tap` config `{"provider":"stripe","tap":true}`; 4 `card_reader` rows still `pending`, 0 reader credentials.
- TDD: `TestResolveReaderMethodPrefersTheSingleTapEntry` fails on main ("one tap entry among several reader methods must resolve, got no single reader-capable payment method"), passes with the fix. It also asserts three methods without a tap entry are refused and two tap entries are refused. `go test ./internal/pages -run 'Reader|Pairing'` passes.

## Findings
- None open. The same function is used at approve and at the phone's poll (`completeReaderPairing`), so both stay consistent.
- Follow-up: with Stripe + SumUp both reader-capable the manager has no choice of which tap method a phone uses; today the tap entry wins. SumUp Tap to Pay (card #6746) will need an explicit choice.

Pre-flight: no UI strings, no migration, no SQL outside data, no money, no new route.

Verdict: approve for the dev build; independent review owed.
