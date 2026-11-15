# Review — till reads `store.had_main_till` (ut-docs#5332)

Date: 2026-11-04. Branch: `feat/5332-pair-had-main-till`. Code by Sonnet;
independent review by Opus 5.5 (Reviewer role).

## What changed
- `internal/enroll/pair.go`: `PairProfile.HadMainTill`, decoded in
  `UnmarshalJSON` from the `had_main_till` key. Only a JSON `true` counts;
  absent, `null` or a non-bool is false and never fails the decode.
- `internal/pages/setup_page.go`: in the cloud branch, with no `replaced`
  block and `had_main_till` true, `sync.receipt_prefix` is set to
  `data.ReplacementMainTillPrefix(deviceID, "")` at the same point as T4,
  whatever the commit's result. No new network call, no new i18n key.
- `web/help/{en,de,tr,ar,fa}/multitill.md`: one sentence in "When the main
  till was replaced"; structure unchanged.
- Contract: ADR-0166 amendment 2026-11-04 (ut-docs). ut-cloud's guard
  `TestPairHadMainTillFieldMirrorsPOS` greps the `HadMainTill
  json.RawMessage` line in `aux`; after review it is a hard check, so this
  PR merges **before** ut-cloud's.

## Tests (written first, seen failing)
- `TestPairProfileHadMainTill` (enroll).
- `TestCloudSetupAfterRemoveGivesANewReceiptPrefix` (commit ok and commit
  failed), `TestCloudSetupHadMainTillFalseKeepsNoReceiptPrefix` (false,
  absent) (pages).
- Existing replace tests unchanged and green. AC4.4/AC4.5 stay pinned by
  `internal/data/display_no_replacement_prefix_test.go` and
  `internal/db/restore_identity_test.go`.

## Review findings
| # | Severity | Finding | Status |
|---|---|---|---|
| 1 | Low | The prefix `Settings.Set` failure only logs `Warnf` and setup completes, so numbering would start at `000000001` (false negative). This is the same pre-existing behaviour as T4; a local SQLite write failing here means the seed writes around it are failing too. | Accepted |
| 2 | Info | Can the prefix land on a till that already has receipts? No. The branch is only reachable through the `NeedsFirstBoot`-gated wizard (no active user with a PIN, so no sale), and it runs before the session is usable. | No action |
| 3 | Info | Retried wizard: the same held pairing gives the same `DeviceID`, so the same prefix (deterministic). A re-pair with a new device id gives a different prefix, but still before any sale. | No action |

Checked with no finding:
- Repository pattern: no raw SQL; the Settings repo is used.
- Offline-first: no new call; a pairing failure still completes the setup and selling.
- i18n: no new keys.
- Help: the sentence reuses each locale's real `setup.join.role_main` label (en/tr/ar/fa checked against `web/locales`, de against the existing de help text) and is accurate in all 5 locales.
- No file writes and no new paths (the two recurring bugs: n/a).

## Extra verification
- Tester mutations M5 (`== nil && h` → `== nil`), M6 (branch disabled) and M8 (prefix whenever `Store != nil`) were each caught. M7 (tag rename) was caught by the cloud guard. Full `go test ./...` passed (105 packages), golangci-lint reported 0 issues, and all CI guards were green (scratchpad `5332/tester.md`).
- Reviewer re-run:
  - `gofmt` clean, `go build ./...` OK, and `go vet` (enroll, pages) OK.
  - `go test -count=1`: `./internal/enroll`, `./internal/pages -run 'CloudSetup|ReceiptPrefix|Replace'` and `./internal/data -run Prefix` all pass.

## Verdict
**Approve.**

## Accepted / deferred
- A removed M-prefixed replacement colliding with the new prefix: 2^-32 (N3).
- Settings "Pair with a shop" does not read the key (N1).

Pre-flight: help topic — `web/help/*/multitill.md` updated (5 locales); docs-shots — n/a (no screen changed); `reference/` — ut-docs design-5058 note + ADR-0166 amendment updated; README — n/a (not stale); en.json keys — n/a (none); review record — updated (this file).
