# Review — self-order checkout hardening: spam guard, orphaned held C-rows, C-number uniqueness (ut-docs#2714)

- **Author:** Opus 5.5 (Dev subagent + orchestrator fixes). **Reviewer:** Fable 5.1, fresh context, isolated worktree, one round. Card complexity:medium.
- **Origin:** round-2 review findings 3, 4 and 6 of #2703 (`2026-09-25-counter-orders-as-held-sales-2703.md`).

## What shipped
1. **Spam guard on `POST /api/self-order/checkout`** (counter and card paths):
   - A per-source-IP limit (`pairRateLimiter`, 20 per minute, ADR-0033 shape) returns 429.
   - A per-basket in-flight guard, keyed by the table-QR token or the walk-up kiosk basket, returns 409.
   - Both checks run before `ParseForm` and before any database, print, network or payment work.
   - A refusal carries an empty body and `HX-Reswap: none` (see finding 1).
2. **Orphaned `held` rows:**
   - A failed park deletes the counter row it just created, so the retry reuses the C-number. The delete runs detached from the request context. It is skipped when the main till refused the push, because the main then holds that number.
   - `Create` prunes `held` rows older than 90 days, in the same transaction and after its insert, so the sequence maximum never drops. `open` and `collected` rows are untouched.
3. **C-number uniqueness:**
   - Migration `044` suffixes legacy duplicate `display_no` values with `~<rowid>`; the sequence CAST still reads the leading number. It then adds a UNIQUE index. Replay-safe, checksum pinned.
   - A replica (`sync.primary_url` set) with a blank `sync.receipt_prefix` derives a prefix from `sync.till_id`.
   - `sales.display_no` stays non-unique on purpose: it is a fiscal table fed by every till, and display_no is display-only there.
   - Same-till concurrency was already safe through `_txlock=immediate` (ut-docs#311). The new concurrent test is a regression guard.
4. Help: the multi-till prefix sentence in `kiosk-counter-orders` (en/de/tr/ar/fa); screenshots and manifest regenerated.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | The kiosk page force-swaps every `/api/self-order/*` 4xx into its modal, so the plain-text 409/429 bodies would show untranslated English. A late 409 could also blank the first request's confirmation. | **Fixed**: `refuseKioskCheckout` sends an empty body with `HX-Reswap: none`. The tests were extended first and failed with `HX-Reswap = "", want none`. |
| 2 | Low | If the main till applied the push but its reply was lost, and the local fallback write then fails, the row is deleted. The retry can then park a second order under the same C-number. The `heldSaleSyncRefused` exemption rarely fires. | **Accepted**: this needs a lost network reply plus a local DB failure. It is the same ambiguity class as #2712 (ADR-0093 Amendment B), which is being addressed there. |
| 3 | Low | The rate limit is per remote IP, so guests behind a NAT or proxy share 20 per minute. | **Accepted**: same limitation as the existing table-session mint limiter. On shop LAN Wi-Fi each phone has its own address. |
| 4 | Nit | A former replica that goes standalone reads the leading digits of old derived-prefix rows (`CAST('7B2E91-1')=7`) into its maximum. | **Accepted**: numbers can only jump up, never collide. |
| 5 | Nit | The marshal-failure `DeleteHeld` used the request context; `Create` read the clock twice. | **Fixed** |

## Verification
- **TDD re-verified by the reviewer in its own worktree:** reverting each fix makes `ConcurrentSameBasketIsRefused`, `RateLimitedPerSource` and `ParkFailureLeavesNoOrphanAndReusesTheNumber` fail. The Dev's red runs are recorded on the card.
- **Reviewer probe:** migration 044 on 20k rows with 500 distinct numbers deduped in 82 ms, and MAX(CAST) was unchanged.
- **Gates:**
  - `go test ./...` passed.
  - `-race` over pages SelfOrder/Kiosk/Counter/Held/Tender/Print passed (Dev run).
  - `golangci-lint` reported 0 issues, and `gofmt` is clean.
  - Every `ci.yml` build guard passes, except `guard-shellcheck-version`, which needs a shellcheck binary this container doesn't have; no shell scripts changed.
  - e2e `kiosk-counter-order-held-2703.spec.ts` passed.
- **Visual:** no pixel-level UI change, since refusals paint nothing. Only the kiosk-counter-orders screenshots and manifest were regenerated. Other PNGs that differed byte-for-byte in this container were not committed.

## Verdict
Safe to merge.
