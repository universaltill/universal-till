# Review: phone reader learns denied/expired; next_pin in the reader hello (ut-docs#6796)

Pre-flight: BA → Architect (Opus 5.5) → Dev (Sonnet 5.5) → Tester (Opus 5.5, separate agent) → Review (Opus 5.5, separate agent, different model from the author). UX skipped: the only UI change is one new Android string on the existing pairing-failed screen.

## Change

- `GET /api/sync/pair-requests/{id}`: the request secret is always compared in constant time (an unknown id compares against `unknownCommitment`, as `sync_link.go` does). A **card_reader** request answers the holder of the matching secret `410 {"error":"pairing_denied"}` or `410 {"error":"pairing_expired"}` (denied wins). Every caller without the secret, a pending request and every **till** request get today's identical 404.
- `PairingRepo.Deny` keeps a `denied` tombstone (token cleared, `expires_at` unchanged, swept as before) for reader rows; till rows are still deleted. New `GetByIDForPoll` (returns an expired row with a flag; used only after the secret matched). `Consume` refuses an expired approval itself. Approve on a non-pending row → 409. The pending-link resolver maps a denied row to `PairingDenied`.
- `ReaderHello.NextPin` (`next_pin`, omitempty) from `lantls.Serving().NextPin()`; fixture `testdata/reader_hello_main_rotating.json`. The phone ignores it for now (follow-up).
- Android: `PairPoll.Denied`; a 410 maps by its code (denied → Denied, expired → Expired, other → refused); string `reader_denied_request`.
- Help: `web/help/en/multitill.md` says what a denied phone shows; docs-shots regenerated.

## Findings (independent review, Opus 5.5)

| # | Finding | Severity | Outcome |
|---|---|---|---|
| 1 | Rotating hello fixture wrote `next_pin` as `sha256:<hex>`; real pins are bare hex | should-fix | fixed (fixture + test) |
| 2 | ADR-0033 amendment cites ADR-0198 §6.15, which did not list it | should-fix | fixed in the ut-docs PR |
| 3 | Manual did not mention the denied message | should-fix | fixed |
| 4 | `Consume` had no expiry check of its own | nit | fixed |
| 5 | Deny after a successful Consume still audits `pairing_denied` | nit, pre-existing | not changed |
| 6 | `ReaderNextPin` split the `OnFrame`/`OnFrameEvery` comment | nit | fixed |
| 7 | denied+expired test case builds an unused row | nit | cleaned up by Tester |

Tester found the Dev's matrix used a wrong header name (`X-Pair-Request-Secret`), so the header path was untested: fixed, with wrong-header and right-header modes.

## Verified

- False-pass: reverting `pairing_api.go`, `Deny`, `reader_hub.go` or the `omitempty` each fails the new tests (Tester).
- Driven run on the real binary with loopback TLS: reader denied → 410 `pairing_denied` (header or query secret), 404 for wrong/missing/unknown; expired → 410 `pairing_expired`; approve on denied → 409; till role unchanged (404 pending/denied, token once approved).
- `go build ./...`, `golangci-lint` clean; `go test ./...` 123 packages ok (internal/plugins timed out under load, passes alone); touched packages re-run after the review fixes: ok; `guard-docs-shots` green after regeneration.
- Not run: Kotlin unit tests (no Android SDK in this container); a real phone; `next_pin` across a real key rotation.
