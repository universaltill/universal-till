# Review: main till refuses a till name over 60 characters on enroll and pair-request (ut-docs#5875)

Date: 2026-11-08. Lane: `lane:cloud-41`. Complexity: easy.
Built by Sonnet (Dev subagent); tested by a separate Tester subagent; reviewed by an independent Opus 5.5 subagent.

## What shipped
- `internal/pages/till_name.go`: `errTillNameTooLong` sentinel (wrapped by `validateTillName`) and
  `tillNameRefusalKey(raw)`: "" for blank/acceptable, `common.error.name_invalid_characters` for the
  #5862 character rule (wins over length), `sync.error.name_too_long` for over `maxTillNameRunes` (60 runes).
- `POST /api/sync/enroll` (`sync_api.go`): uses it in the #5862 check's place, after the role check and
  before the long token or short code is consumed; answers 400 (422 stays "name taken"). Blank still
  defaults to "till".
- `POST /api/sync/pair-request` (`pairing_api.go`): same helper, before the pending row is created.
- New key `sync.error.name_too_long` (en.json + en.meta.json, English only — ADR-0160).
- `web/help/img/manifest.json`: docs-shots surface hash refreshed (no pixel changes).
- ut-docs `architecture/lan-sync.md` enrolment bullet and `reference/coding-standards.md` name bullet.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| F1 | low | The joining till maps the 400 to its generic "refused (code used or expired?)" text (`completeJoin`, `pairing_join.go`), so a too-long name still blames the code | deferred → #5898 (joining-till local length check, `maxlength`, wizard message) |
| F2 | low | A pair-request with an over-long name queued before the upgrade can be approved, then the enroll 400s | accepted: narrow window (pre-upgrade rows within `pairingRequestTTL`); the request expires on its own |
| F3 | nit | `tillNameRefusalKey`'s `default` mapped any future refusal to "too long" | fixed: explicit `errors.Is(err, errTillNameTooLong)` case; default is the characters key |
| T1 | — | Tester: docs-shots surface hash stale after `internal/pages` edits | fixed: `update-docs-shots-surface-hash.sh` |

## Verification
- TDD re-verified twice independently (Tester; Reviewer in its own worktree): with the handler hunks
  reverted, enroll (long token and short code) and pair-request tests fail with 200 instead of 400;
  restored, they pass. Tester's mutations (check after token consumption → 403; bytes for runes;
  `>=`) were each caught.
- Driven run (Tester): built binary on a temp DB; real enrol codes. 61 runes → 400 "A till name can be
  at most 60 characters." on both routes; same token then enrolled a 60-rune name (stored exactly);
  reuse → 403.
- Gate: gofmt clean; `go build ./...`; `golangci-lint run ./...` 0 issues; `shellcheck` 0; `go test ./...`
  green (`internal/plugins` needed `-timeout 45m` locally); all 139 `ci.yml` build-job guards pass after
  the hash refresh. Full-package `-race` on `internal/pages` timed out in this container (no race reported);
  targeted `-race` on the name/enroll/pair-request tests passed.

Pre-flight: help topic n/a (API-only, no visible text change on the till's own screens; the joining-side
message is #5898); `reference/` updated (lan-sync.md, coding-standards.md); README n/a; new en.json key →
en.meta.json updated; review record this file; follow-up #5898.

Verdict: ship.
