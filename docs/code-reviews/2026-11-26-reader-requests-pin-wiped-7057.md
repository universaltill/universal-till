# Review record: reader-request PIN wiped by the 3s poll (part of ut-docs#7057)

Date: 2026-11-26. Author: Claude (Sonnet 5.5) with the owner testing live.
Reviewer: none independent (one-file fix, urgent live test, `[no-subagent]` tag); the owner re-tests on a dev build. A fresh Reviewer pass is still owed in the next batch review.

## What shipped
`web/public/pairing-stream.js`: `window.utPairingBusy()` now checks both `#pending-pairings` and `#reader-requests` (focus inside, typed PIN, changed role select). Before, only `#pending-pairings` was checked, but the Card reader requests box (`web/ui/partials/tills_reader_requests.html`) polls every 3 s behind the same guard, so its outerHTML swap wiped a half-typed manager PIN and the Approve feedback.

## Evidence
- Live: 4 `card_reader` rows in `pending_pairings` (status pending, 0 rows in `card_reader_credentials`) after repeated attempts by the owner; the owner could not complete the PIN.
- TDD: `TestPairingStreamJS_BusyGuardCoversReaderRequests` fails on main (message: utPairingBusy must guard #reader-requests), passes with the fix. `go test ./internal/pages -run Pairing` and `go test ./internal/httpx` pass.

## Findings
- None open. Known limit: the guard is JS, not browser-tested here (the string-level test mirrors the existing pairing-stream tests); verified by the owner on the Mac dev build.
- Follow-ups on #7057: one pending row per device; one Tills page.

Pre-flight: no UI strings, no migration, no data access, no money, no new route.

Verdict: approve for the dev build; independent review owed.
