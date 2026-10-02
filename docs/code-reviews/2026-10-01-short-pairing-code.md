# Review: short typeable pairing code (ut-docs#3219)

- **Date:** 2026-10-01 · **Lane:** cloud-54 · **Branch:** `feat/3219-short-pairing-code`
- **Author:** Opus 5.5 (Dev subagent) · **Reviewer:** Fable (independent, worktree-isolated)

## What shipped
- `enrolTokens` (`internal/pages/sync_api.go`): "Show pairing code" mints a
  6-character one-time twin alongside the 128-bit long token.
  - Alphabet `23456789ABCDEFGHJKLMNPQRSTUVWXYZ`, unbiased crypto/rand, shown `XXX-XXX`.
  - Same 10-minute TTL. Using either the short code or the long token burns both.
  - One live short code per shop: issuing a new one retires the older short codes.
- `POST /api/sync/enroll` accepts the short code, with two limits:
  - 5 attempts per minute per source (429);
  - a shop-wide budget of 10 failed short-code attempts, after which the live
    short code is retired. The long twin stays usable.
- `POST /api/sync/enroll-token` shows:
  - the big code and the big main-till address;
  - the QR, which is unchanged and still carries the long code;
  - the long code inside a "Full code for copy & paste" disclosure.
- Both join forms (Tills page and first-boot wizard) get an address field.
  - `joinPrimary` builds the URL from the address. Errors are `need_address`
    and `bad_address`.
  - A 429 from the main till maps to `too_many_attempts`.
  - Long codes still work as before.
- Locales: 9 new keys and 8 reworded ones in en/ar/fa/tr. The de/es packs
  follow in their own PRs, after core.
- Help: `multitill` updated in en/de/ar/fa/tr, screenshots regenerated.
- ut-docs `architecture/lan-sync.md` now documents the code and its budget.

## Findings (Fable review)
| Severity | Finding | Outcome |
|---|---|---|
| **Major** | The budget purge also deleted every long twin. Any LAN caller could send 10 wrong codes a minute and keep QR/long-code pairing dead, which was a regression versus main. | **Fixed.** The purge now drops short codes only. Two tests were flipped to assert that the long twin survives. |
| Minor | Issuing a new code reset the budget but left older short codes live. | **Fixed.** Only one short code is live; new test `TestEnrolTokens_NewShortCodeRetiresOlderOnes`. |
| Minor | An address with a trailing `#`/`?` passed validation and then failed as "not a till". | **Fixed.** The raw text is checked; test cases added. |
| Minor | A real but expired short code counted against the budget. | **Fixed.** New test `TestEnrolTokens_ExpiredShortCodeDoesNotSpendBudget`. |
| Nit | The per-source limiter is keyed on RemoteAddr (NAT shares one bucket). The 429 is plain text. | Accepted. This is the existing `pair-request` pattern, and `completeJoin` keys on status. |
| Nit | A stale comment in `setup_page_test.go`. | Fixed. |

Things the reviewer verified OK:
- entropy and bias;
- no shape-normalisation bypass from long token to short code;
- mutex coverage and pruning;
- the approve-to-pair flow is unchanged;
- HTML escaping, RTL (`dir="ltr"` on code/address, logical properties) and i18n;
- no SSRF widening;
- no file writes and no cwd paths.

## TDD re-verification
- The reviewer mutated the budget threshold and the per-source limiter; the tests failed and passed again once restored.
- The orchestrator then reverted the purge fix and the `#?` check inline. `TestEnrolTokens_ShortCodeFailureBudget`, `TestSyncEnroll_ShortCodeFailureBudgetInvalidatesLiveCode` and `TestJoinPrimary_ShortCodeRejectsBadAddress` failed with the expected messages, and passed again once restored.

## Verified beyond unit tests
I drove a real till (`e2e/run-till.sh`, headless Chromium), clicked "Show pairing code" and opened the code tab. I looked at screenshots for these cases:
- 1024×600 en;
- 360×740 en;
- 1024×600 fa (RTL);
- 360×740 with `lang=de`. No German pack is installed in e2e, so this rendered English.

Results:
- The code and address are large and legible.
- The QR sits below them.
- The disclosure collapses.
- At 360px the address field and code field stack cleanly. The code placeholder is truncated at 360px, but the label carries the meaning.

Not checked: real touch hardware, iOS Safari, a real two-device LAN join. A two-process join is covered by `TestSyncJoin_ShortCodeWithBareAddress_FullFlow`.

## Gate
These all passed:
- `gofmt` clean, `go build ./...`, `go vet ./internal/pages/...`;
- `go test ./...` with no failures;
- `golangci-lint` with 0 issues;
- guards: i18n, data-access, help-drift, help-topics, docs-shots (surface hash refreshed after the logic-only fixes), compliance-claims, core-neutral, no-showmodal, kiosk-engine, page-http-error.

## Verdict
Safe to merge after the fixes above. Deferred items: none, since camera scan (#696) and iOS discovery (#3218) are already separate cards.
