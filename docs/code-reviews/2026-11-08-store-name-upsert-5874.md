# Review: the All-settings upsert applies the shop-name rule to `store.name` (ut-docs#5874)

Date: 2026-11-08. Lane: `lane:cloud-54`. Complexity: easy (security).
Built by Sonnet (Dev subagent). Tested by a separate Sonnet Tester subagent. Reviewed by an independent Opus 5.5 subagent.

## What shipped
- `internal/pages/settings_page.go`, `POST /api/settings/upsert`: a `key == common.KeyStoreName` branch sits right after
  the #5862 till-name block. It comes before the #244 value validators and before `checkOrElevate`. It runs
  `data.CheckSingleLineName` and then `config.NormalizeStoreName`.
  - Invalid characters from either check return `common.error.name_invalid_characters`.
  - A blank, placeholder or over-80-rune name returns the Shop name card's own keys (`storeNameErrorKey`).
  - Every refusal is a translated text/plain 400, sent before any PIN prompt.
  - The normalised name is what gets stored.
- Behaviour change, by design: the raw editor can no longer blank `store.name` or set a placeholder name. The Shop
  name card couldn't do either already.
- No new `en.json` keys.
- New tests in `internal/pages/store_name_upsert_test.go`:
  - invisible/bidi characters, for both manager and cashier;
  - blank, long and placeholder names;
  - stored-normalised equivalence with the Shop name card;
  - the localised refusal, using `?lang=fa`.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| F1 | medium | `TestSettingsUpsert_StoreNameRefusalLocalised` always `t.Skip`ped, because the de pack is a separate repo and not in the test binary | fixed by the Tester: the test uses `?lang=fa` (core ships fa), runs, and asserts the Persian store-name messages |
| F2 | low | `StoredNormalised` is an equivalence pin, not a red/green test: the handler already trims, so storing the raw value is an equivalent mutant today | fixed: a comment on the test says so |
| F3 | low | The upsert audits as `setting_upserted`, not `store_name_changed`, and has no short-circuit for an unchanged name | accepted: this is the raw editor's own audit trail |
| F4 | low | The Shop name card still accepts U+E0100–E01EF; the wizard, first login, `/api/sync/settings` and the cloud `set_setting` directive write `store.name` without the single-line rule | deferred → ut-docs#5894 |
| F5 | nit | The refusal resolves the locale with `ResolveLocale` | accepted: same as the #5862 till-name precedent |

## Tester verification
- **Mutation checks**, each run against the real handler:
  - Removing the branch fails the invisible-characters, blank/long/placeholder and localised tests.
  - Dropping `CheckSingleLineName` fails the U+2028 and U+E0100 cases.
  - Dropping `NormalizeStoreName` fails the LRM, blank/long/placeholder and localised cases.
  - Moving the branch after `checkOrElevate` fails the cashier cases.
- **Driven run** of the real till on a temp DB, port 8171, killed afterwards:
  - U+202E in `store.name` returned 400 with the English invalid-characters text.
  - A name of 81 runes with `?lang=fa` returned 400 with the Persian too-long text.
  - `"   Test 5874 Shop   "` returned 204, and the Settings page then showed `Test 5874 Shop`.
- **Not checked:** API-only change, so no visual surface; the raw editor already shows any 4xx text/plain body in
  `#settings-upsert-msg`. No checkout or basket file was touched, so offline selling is unaffected.

## Gate
- gofmt clean; `go build ./...`; `go vet ./internal/pages/`; golangci-lint 0 issues. `go test ./...`: 108 packages ok. `internal/plugins` hit the 10-minute package timeout on this loaded 4-core box; it is untouched by this diff, so CI is the check.
- Every guard in `ci.yml`'s `build` job ran locally and passed, except two that need tooling this box lacks: `guard-deadcode-baseline` (deadcode built with go1.26, repo needs go1.27) and `guard-shellcheck-version` (no shellcheck). CI runs both.

Pre-flight:
- Till user-visible behaviour: `web/help/` n/a (no topic documents the All-settings editor's `store.name` rules;
  `multitill.md` mentions the editor only for other keys). `reference/` n/a. README n/a.
- New `en.json` key: n/a (no new keys).
- my./admin UI: n/a.
- Follow-up filed: ut-docs#5894.

Verdict: approve after fixes. F1 and F2 are fixed and re-verified.
