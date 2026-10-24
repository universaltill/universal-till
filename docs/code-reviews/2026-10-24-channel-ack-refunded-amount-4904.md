# Review: channel SDK Ack carries the refunded amount (ut-docs#4904)

SDK-only change in `sdk/plugin/channel`, the guest side of the ut-cloud change.
The full review record is ut-cloud `docs/code-reviews/2026-10-24-refund-ack-amount-4904.md`
(Sonnet 5.5 built it; Opus 5.5 reviewed it independently).

- `Ack.RefundedAmountMinor *int64` (`refunded_amount_minor,omitempty`): on a
  `refund.create` answer, the refund money the channel moved. Unset means
  omitted, so nothing changes for an adapter that never sets it.
- `FakeHost`'s `outbox_ack` now decodes strictly (it refuses unknown fields, like
  the cloud host) and refuses an amount that is negative, above 2^50 or sent
  beside an error (review finding 3). An adapter's unit tests therefore see the
  same `-4` the real host answers.
- Tests: `TestAckRefundedAmountMinorIsOptionalOnTheWire` and
  `TestFakeHostRefusesARefundedAmountTheCloudHostRefuses`. The second fails on
  all three cases with the old `fakehost.go` and passes with the new one
  (checked by hand).
- Gates: `gofmt -l`, `go build ./...`, `go vet` and `go test` in `sdk/plugin`,
  `guard-sdk-hostfns.sh` and `guard-mail-addresses.sh` all pass.
- No host function changed, so the `abicontract` mirror is unaffected.

Verdict: safe to merge.
