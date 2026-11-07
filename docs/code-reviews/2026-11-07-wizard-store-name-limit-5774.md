# Review — setup wizard shop-name limit uses the shared 80-rune constant (ut-docs#5774)

Date: 2026-11-07 · Lane: lane:cloud-24 · Complexity: easy
Built by: Sonnet (Dev subagent) · Tested by: Sonnet (separate Tester subagent) · Reviewed by: Opus 5.5 (fresh subagent, isolated worktree)

## What shipped

- `internal/pages/setup_page.go`: the wizard's private `maxStoreNameRunes = 60` is gone;
  `isRefusedStoreName` checks `config.MaxStoreNameRunes` (80, the same limit as
  `config.NormalizeStoreName` in Settings and ut-cloud `claims.MaxStoreNameRunes`).
  Its three callers (wizard POST `/api/setup`, bare `/api/auth/setup`, cloud-pair prefill
  in `setup_cloud_pair.go`) now all accept 61–80 runes. Side effect: a cloud shop name of
  61–80 runes is now prefilled on pairing instead of being dropped.
- `internal/httpx/httpx.go`: `maxStoreNameRunes` template func; the `store_name` inputs in
  `setup.html`, `login.html` and `settings.html` use it for `maxlength` (one constant).
- The 60-rune **till** name limit (`till_name.go`, `till_name` inputs) is unchanged on purpose.
- `web/help/img/manifest.json`: docs-shots surface hash regenerated (no pixel changes).

## Tests (written first, seen failing)

- `TestSetupWizardShopNameLimitMatchesConfig` — 80 ASCII / 80 multibyte runes accepted, 81 refused, nothing saved on refusal.
- `TestShopNameInputsCarryConfigMaxlength` — rendered `/setup` carries `maxlength` = the constant.
- `TestBareFirstBootSetupAcceptsSharedShopNameLimit` (review fix) — bare first boot accepts 80 runes and saves them.
- `TestBareFirstBootSetupRequiresShopName` — the over-limit sample and maxlength echo now derive from the constant.

## Findings (Opus 5.5)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Bare `/api/auth/setup` only tested for refusal at 81; a 60 cap re-added there would pass | **Fixed** — new accept test; seen failing against the old `setup_page.go` (`80-rune name: code=200, want 303`) |
| 2 | minor | No test for the cloud-pair prefill of a 61–80-rune name | Accepted — same `isRefusedStoreName` call covered by the table test; not worth a cloud-pair fixture for an easy card |
| 3 | nit | Maxlength test looped over `{"/setup"}` only | **Fixed** — loop dropped; login covered by the bare-setup echo assertion |
| 4 | nit | `max :=` shadowed the builtin | **Fixed** — renamed `limit` |
| 5 | nit (pre-existing) | Browser `maxlength` counts UTF-16 units, so emoji count double | Accepted — browser stricter than server, never looser |

Reviewer re-verified the TDD claim in its own worktree: reverting the production files makes the three tests fail; restoring makes them pass. Orchestrator re-verified finding 1's new test the same way.

## Extra verification

- Tester: full gate — gofmt, build, vet, golangci-lint (0 issues), `go test ./...` (all packages ok except `internal/plugins`, which timed out at 10 min on a 4-core box; it does not import the changed packages), every runnable guard in `ci.yml`'s build job (`guard-shellcheck-version` not runnable: no shellcheck binary).
- Tester driven run: real binary on a fresh data dir, Chromium 1024×600 — the wizard's shop-name input has `maxlength=80`, typing 81+ keeps 80, an 80-char POST is accepted (303 → `/import?welcome=1`), 81 is refused; screenshot checked, long name clips inside the input, layout intact.

Pre-flight: help topic n/a (no help page states a shop-name length); docs-shots surface hash updated, screenshots n/a (no pixel change); en.json / i18n-meta n/a (no new keys); reference/ n/a; README n/a; review record — this file; follow-ups — none.

## Verdict

Approve. Possible overlap with open PR universal-till#2411 (it also edits `auth_page_test.go`; different lines) — whichever merges second rebases.
