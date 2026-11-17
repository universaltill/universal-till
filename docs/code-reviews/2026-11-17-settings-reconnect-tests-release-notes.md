# Review: settings Reconnect tests vs release notes (#5145)

**Scope:** branch `fix/5145-settings-tests-release-notes`, uncommitted diff:
`internal/pages/reconnect_5145_test.go` only (new const `reconnectButton`;
five negative `strings.Contains(body, "Reconnect this till")` checks in three
tests now look for the button markup `hx-post="/api/enrol/reconnect"`).

**Author:** different model. **Reviewer:** Sonnet 5.5 (independent).

## What shipped

Main went red at merge 246d262ec, which added only
`web/release-notes/en/v0.36.1.md`. Its text says "a manager sees Reconnect
this till or Register as a new shop". `/settings` renders the current
version's What's new (`web/ui/pages/settings.html` ~495), so "no Reconnect
offered" checks that matched the label text matched the prose. The checks now
assert on the button's markup, which only the real button emits.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Info | Diagnosis confirmed. With the fix reverted, the three tests fail (lines 132, 141, 155) and a dumped `/settings` body has "Reconnect this till" at the release-notes sentence (`...a manager sees Reconnect this till or Register as a new shop and confirms it.`) and no `hx-post="/api/enrol/reconnect"`. | Confirmed |
| 2 | Info | Still catches a regression. Template broken so the panel and button always render (`{{ with $.reconnectPanel }}`, `{{ if true }}`): all three tests fail (lines 138, 147, 161). Template restored with `git checkout`. | Confirmed |
| 3 | Info | Positive checks still use the label text where the panel is expected; unchanged and correct. The `data-testid="reconnect-panel"` half of the claimed/no-lock-out checks is unchanged, so panel presence stays covered too. | No change |
| 4 | Info | Other absence assertions: `register_blocked_5145_test.go`, the other `reconnect_5145_test.go` negatives ("Reconnected.", "Registered as a new shop", "Register as a new store", "no longer in the old shop"), `diagnostics_settings_test.go:347`, `country_settings_page_test.go:167` checked against all of `web/release-notes/en/`: none appears in any release note. The chip test hits `/ui/cloud-auth-chip`, which does not render notes. | No change |
| 5 | Low (latent) | Any future negative text check on `/settings` can collide with release notes the same way. Not fixed here; use markup/testid for absence checks. | Accepted |

## Verification

- (a) and (b) as above, each reverted in the same command.
- `go vet ./internal/pages`: clean.
- `go test ./internal/pages -count=1`: ok (441.8 s).
- No commit or push made by the reviewer.

## Verdict

**PASS.** Test-only fix, correct diagnosis, regression coverage intact.

## Deferred

None. Finding 5 is advice, not a card.

Pre-flight: help topic `n/a` (test-only); `reference/` page `n/a`;
`README.md` `n/a`; `en.json`/`en.meta.json` `n/a` (no keys); ut-cloud locales
`n/a`; ADR index `n/a`; plugin taxonomy/mirrors `n/a`; review record
`updated` (this file); follow-ups none.
