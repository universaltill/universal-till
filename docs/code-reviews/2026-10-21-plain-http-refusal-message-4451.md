# Review: a plain-HTTP refusal says what to do (ut-docs#4451)

Follow-up to ut-docs#4411 (findings 1 and 4 of `2026-10-15-lan-tls-full-closure-4411.md`). Built by Sonnet 5.5 (lane:cloud-41, complexity:easy). Reviewed by Opus 5.5 in a fresh context, in an isolated worktree at the pre-review snapshot.

## What shipped
- The main till keeps its 403 and body text on every plain-HTTP refusal. It now also sends a stable `X-UT-Plain-Refused` header:
  - `tls-only` from `refusePlainSyncWhenClosed`, the closed fleet case. That covers enroll, pair-request, the pair-request poll and primary-proof.
  - `satellite-tls-only` or `satellite-no-tls` from both satellite branches of `POST /api/sync/enroll`. The second value is used when `lantls.Serving() == nil`.
  - All three sites go through `writePlainRefusal`, so they cannot drift.
- The joining till's `plainRefusalKind` maps a 403 that carries one of those three values to a new join error kind (`joinErrTLSOnly`, `joinErrSatelliteTLSOnly`, `joinErrSatelliteNoTLS`).
  - `completeJoin`, `pairStartHandler` and `pairStatusHandler` show its translated message.
  - Any other 403 is still generic: `joinErrRefused`, `tills.pairing.error.refused` or `unexpected_response_status`. That includes "invalid or expired enrolment token" and a 403 with no header or an unknown value.
- New keys `tills.join_error.{tls_only,satellite_tls_only,main_no_tls}` are added in en/ar/fa/tr, with i18n metadata.
- The multitill help topic, step 5 (en/de/ar/fa/tr), now says what to do on each message.

## Findings (Opus)
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | medium | "Update this till" is wrong advice for a current joining till. Such a till only reaches plain HTTP when its TLS attempt failed, not because it is old. The person would update an up-to-date till and get the same message. | Fixed. The message and help now say: update this till if it runs an older version than the main till, otherwise restart both tills, then try again. All four locales and all five help languages are updated. |
| 2 | low | "Restart the main till" only fixes a transient key error. A demo till or a persistently unreadable key never serves TLS. | Accepted. Restarting is the right first step, and naming logs on a shop-floor message helps nobody. |
| 3 | low | The three new en.json keys need follow-up PRs in the language packs. | The de/es/pt pack PRs land in this cycle, after the core merge, because new keys can't land in a pack first. |
| 4 | low | The poll subtests send `satellite-*` headers, which the main till never sends on that route. | Accepted. The test exercises the mapping, and it's harmless. |
| 5 | info | The ar help step mixes "الجهاز الرئيسي" (older text) with "الصندوق الأساسي" (the new sentence, which matches the ar locale strings). | Accepted; the wording is pre-existing. |

The reviewer also checked these, and found no problem:
- **Header spoofing:** a device that can answer at that address could already return any status. The header only selects which message is shown; every path still errors out.
- **Status order in `completeJoin`:** 404, 429 and 422 are still checked first, and no other 403 sets the header.
- **Header timing:** the header is set before the status is written.
- **Repo rules:** there are no file writes, no SQL, and no CSS, and offline-first is unaffected.

## Verified beyond the unit tests
- The reviewer re-verified TDD with a stub that keeps the consts but returns false from `plainRefusalKind` and writes no header. These tests then fail:
  - `TestPlainRefusalKind`
  - `TestCompleteJoin_PlainRefusalHeaderMapsToItsKind` (kind 4, want 13/14/15)
  - six `TestPairJoin_PlainRefusalHeaderShowsActionableMessage` subtests
  - `TestPlainSyncClosure_ClosedRefusesPlainHandshakesWithoutBurningTheToken` (header "" want "tls-only", ×4)
  - both existing satellite enroll tests
  - `TestEnroll_SatelliteOnPlainMainWithoutTLSSaysSoInTheHeader`

  With the fix restored, they all pass.
- The author ran the full `go test ./internal/pages/`, which passed. After the wording fix, the targeted tests were re-run and passed.
- `go build ./...` passes. golangci-lint reports 0 issues. All `ci.yml` build-job guards pass locally except two that this container can't run: `guard-deadcode-baseline` (its tool binary is older than go.mod's Go) and `guard-shellcheck-version` (no shellcheck installed). No Go or shell code changed in their scope, and CI runs both.
- No visual surface changed. The message renders in the existing error slot under the join or pairing form, with the same template and no new layout, so no screenshot was taken.

## Verdict
Safe to merge.
