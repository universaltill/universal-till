# Code review — till names are single-line, no hidden formatting characters (ut-docs#5862)

**Date:** 2026-11-08 · **Branch:** fix/5862-till-name-chars-v2 (+ ut-docs `docs/5862-till-name-chars`) · **Reviewer:** independent subagent (Opus 5.5; code by Sonnet)
**Precedent:** ut-docs#5858 (`data.CheckSingleLineName`, `common.error.name_invalid_characters` on admin names), ut-docs#557 (refuse before the elevation gate)

## What shipped
- `validateTillName` (wizard, cloud first-till pair, cloud `rename_till`, LAN-reported joined-till name) now delegates
  the character rule to `data.CheckSingleLineName` and returns `ErrNameInvalidCharacters` itself, so operator-facing
  callers map it to `common.error.name_invalid_characters` (blank/too long keep `keyTillNameRequired`).
- Settings → Till name (`POST /api/settings/till-name`): 422 `RefuseText` into `#till-name-msg` before the name-taken
  check and the elevation gate.
- Joining till: `completeJoin` (both `/api/sync/join` and `/api/setup/join`) and `pairStartHandler`
  (`/api/sync/pair-start`, `/api/setup/pair-start`) refuse locally before any request to the main till.
- Main till (D1): `/api/sync/enroll` → 400 before the one-time token/short-code limiter; `/api/sync/pair-request` → 400
  before a pending row exists. Never 422 (that means `name_taken` to `completeJoin`); an older joining till gets the
  generic `joinErrRefused`.
- `RenameRegisterToFollowTill` skips a refused name (`false, nil`, best-effort kept); `CreateRegisterForEnrolment`
  refuses (D2), the enrol handler's existing fail-open branch enrols without a register.
- Docs: `web/help/en/multitill.md` step 9; `en.meta.json` description of the reused key; ut-docs
  `reference/coding-standards.md` single-line-name bullet.

## Findings
| # | Severity | Finding | Resolution |
|---|---|---|---|
| MED-1 | Medium | `POST /api/settings/upsert` (the All-settings raw editor) accepted `till.name` / `sync.till_name` with any characters, so an operator could still store a name the Till name card refuses — an operator-typed entry point the change missed. A cashier's bad value even reached the PIN prompt. | Fixed (TDD): `TestSettingsUpsert_TillNameInvisibleCharsRefused` was red (200 PIN prompt / stored), now green. Upsert refuses both keys with 400 `RefuseText` + the shared key before the elevation gate; a balanced-isolate name still saves. |
| LOW-1 | Low | The new test file held raw U+202E, U+2028, U+200B, U+2066/2067/2069, U+200F and U+200D inside string literals — invisible in review and editors (Trojan-Source hygiene). | Fixed: rewritten as `\uXXXX` escapes; behaviour unchanged. (Older tests such as `store_name_3115_test.go` still carry raw ones — not touched.) |
| LOW-2 | Low | `data` import placed inside the stdlib group in `pairing_join.go` and `setup_cloud_pair.go`. | Fixed: moved to the module group. |
| INFO-1 | Info | Settings rename truncates to 60 runes before checking, so a bad character past rune 60 is cut off rather than refused, and invalid UTF-8 longer than 60 runes becomes U+FFFD. The field's `maxlength` stops a browser there; the architect accepted this. | Accepted. |
| INFO-2 | Info | `TestApplyReportedTillName_…` calls the hook twice but does not assert "warned once" (behaviour pre-existing, unchanged). | Accepted. |

Reviewer checked and found no problem in: C0/C1 (`unicode.IsControl`), U+2028/2029, bidi embeddings/overrides
U+202A-202E, ZWSP and other Cf runes, unbalanced isolates (all via `CheckSingleLineName`); RTL names with RLM/ZWJ/ZWNJ
and balanced isolates still accepted; the enroll check sits before token consumption and the short-code limiter (a
retry with the same token works — tested); pair-start answers 200 with the error view (htmx swap); `completeJoin`'s
422→`name_taken` mapping untouched; register-follow stays best-effort (no error, no audit); no new en.json key, no
hardcoded strings; offline-first unaffected (pure local checks); no SQL outside `internal/data`.

## Verification
- TDD: Dev ran the new tests red before the fix. Tester mutation-checked every production change (till_name.go,
  settings_page.go, pos_repo.go, and the join/pair-start/enroll/pair-request checks) — each mutation failed the matching
  tests, restore passed. Reviewer: MED-1 test red before the fix, green after.
- Driven run (Tester, real binary, `UT_AUTH=off`): Settings → Tills → Till name with RLO, tab and ZWSP each → 422
  `x-ut-response: refused`, inline message under Save, no modal, stored name unchanged on a fresh read; a valid rename
  → 204 "Saved."; checked in fa (RTL), tr and en at 1024–1280 px. Wizard/pairing/join screens covered by httptest only.
- Reviewer gate after fixes: gofmt clean, `go build ./...`, `go vet ./internal/pages ./internal/data`,
  golangci-lint 0 issues; guards i18n, i18n-meta, help-topics, data-access, fixed-dates, page-http-error, help-drift
  rc=0; `en.meta.json` parses; `go test ./internal/pages ./internal/data` (non-race) ok (475 s / 78 s). The `-race`
  run of the same packages is run separately by the orchestrator.

Pre-flight: help topic updated (`multitill.md`; no screenshot changes — text-only refusal on an existing inline
message slot); reference updated (ut-docs `coding-standards.md`); en.json n/a (key reused), en.meta.json description
updated; README, ADR, plugin/manifest n/a; my./ut-cloud mirror deferred → follow-up card (below).

## Verdict
APPROVE after MED-1/LOW-1/LOW-2 fixes. Deferred (Backlog cards to open):
- ut-cloud: `claims.RenameDeviceQueued` and the heartbeat-reported name still accept bidi/invisible characters, so a
  queued `rename_till` with one now fails on the till — mirror the rule (BA non-goal).
- `POST /api/settings/upsert` also lets `store.name` bypass `config.NormalizeStoreName`'s checks (pre-existing).
- `/api/sync/enroll` does not apply the 60-rune limit to the enrolling name (pre-existing).
