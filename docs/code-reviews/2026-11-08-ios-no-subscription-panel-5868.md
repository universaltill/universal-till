# Review — iOS: leave the closed #subscription-panel dialog out of base.html (ut-docs#5868)

Date: 2026-11-08 · Complexity: easy
Built by: Sonnet (Dev) · Tested + reviewed by: Opus 5.5 (independent subagent, detached worktree at the WIP snapshot). Reviewer model ≠ author model.

## What shipped

- `internal/httpx/httpx.go`: new seam `PlanPurchaseAllowed` (default `true`) and the
  `planpurchaseallowed` template func in `baseFuncs` (so `FuncsFor` and the fragment
  renderers both carry it). httpx cannot import `internal/pages`, so the seam follows the
  `UpdateAvailable` / `UpdateInstallBridge` pattern.
- `internal/pages/plan_purchase.go`: `init()` sets `httpx.PlanPurchaseAllowed = planPurchaseAllowed`
  — the one predicate (reads `planPurchasePlatform`, so tests flipping the platform flip the layout).
- `web/ui/layouts/base.html`: the `#subscription-panel` dialog, its `#subscription-panel-loading`
  template and its opener script are wrapped in `{{ if and (allowed "settings") planpurchaseallowed }}`;
  comment cites #5868 / #3434.
- `internal/httpx/state_snapshot.go`: `SnapshotStateForTests` captures/restores the new seam.
- No change on linux/windows/darwin/android: a manager still gets the dialog, a cashier still doesn't.

## Tests

- `TestPlanPurchase5868_SubscriptionPanelDialogInDOM` (Dev, `plan_purchase_3434_test.go`): `/settings`
  as manager on `ios` has no `id="subscription-panel"`, no `subscription-panel-loading` (template and
  script both use it), no "Renew or change plan"; on linux/windows/darwin/android all three are present.
- `TestSnapshotState_RestoresEveryPublishedGlobal` extended (review fix, see finding 1).

## TDD re-verification (reviewer, own worktree)

- Reverted `internal/httpx`, `internal/pages/plan_purchase.go`, `web/ui/layouts/base.html` to `HEAD~1`:
  `go test ./internal/pages/ -run PlanPurchase` → FAIL, exactly
  `iOS manager page still contains "id=\"subscription-panel\""` / `"subscription-panel-loading"` /
  `"Renew or change plan"`. Restored → PASS.
- Finding 1's test: with `state_snapshot.go` reverted → `round 1: platform seams not restored`; restored → PASS.

## Hunt results (no defect found)

- **FuncMap coverage:** every template set that parses `base.html` (`httpx` page renderer/`ClonedTemplate`,
  `internal/ui` `NewRenderer`/`NewBasketView`/`NewAdminTreeView`/`JournalView`, httpx tplcache/rail/nav tests)
  gets its funcs from `FuncsFor`, which copies `baseFuncs`; `base.html` already needed `allowed` from the same
  map. `internal/recovery` and `cmd/unitill-desktop` parse their own standalone templates, not base.html.
  `go test ./internal/httpx/ ./internal/ui/... ./internal/pages/` green confirms no parse failure.
- **Init order:** `pages` imports `httpx`, so httpx's default is set first and pages' `init` overrides it;
  the func is a closure read at execute time, so cached parsed templates still see the current value.
  The iOS gomobile build (`GOOS=ios go list -deps ./mobile/...`) links `internal/pages`, so the override
  runs on iOS.
- **Parallelism:** `withPlanPurchasePlatform` callers are serial tests; Go runs top-level parallel tests
  only after serial ones finish, and the helper restores via `t.Cleanup`. Same hazard profile as before.
  Passed with `-shuffle=on -count=3` and under `LANG=en_GB.UTF-8` / `de_DE.UTF-8`.
- **Openers on iOS:** both `[data-sub-panel-open]` openers are already gated off on iOS
  (`subscription_chip.html` `.openPanel` and `subscription_block.html` `.CanRenew`, both `&& planPurchaseAllowed()`),
  and the opener script itself is now absent, so no click path can target the missing dialog (no JS error).
  `POST /api/subscription/panel*` already `refusePlanPurchase` on iOS.
- **Other `subscription.panel.*` wording:** only `base.html` (now gated) and `partials/subscription_panel.html`
  (served only by the refused endpoints) use those keys.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `TestSnapshotState_RestoresEveryPublishedGlobal` didn't cover the new `PlanPurchaseAllowed` seam — dropping its restore line would pass CI | **Fixed** — seam added to the test; seen failing against the pre-change `state_snapshot.go` |
| 2 | minor | `web/help/img/manifest.json` `surface_sha256` not refreshed: `run-docs-shots-guard.sh` exits 1 off-PR, so main would go red right after merge (PRs only warn) | **Fixed** — `scripts/ci/update-docs-shots-surface-hash.sh` (a027261b… → 28ad89ac…); no pixel change: docs-shots run on linux where the DOM is unchanged and the dialog is closed |
| 3 | nit | Test checks the opener script's absence only via the shared `subscription-panel-loading` string | Accepted — the script and template sit in one `{{ if }}` block; the string is in both |

## Extra verification (Tester)

- `gofmt -l .` empty; `go build ./...` ok; `go vet ./internal/httpx/ ./internal/pages/` ok;
  `golangci-lint run ./...` 0 issues; `shellcheck scripts/ci/*.sh` 0 issues (shellcheck 0.11.0 from a scratch venv).
- `go test ./internal/httpx/ ./internal/ui/... ./internal/pages/ -count=1`: all ok (pages 413 s).
- Every guard in `ci.yml`'s `build` job run directly: all pass, except `guard-catimport-version.sh`
  (environment: the worktree shares the main checkout's `.git`, so catimport tags were not fetched into it;
  the diff touches nothing under `catimport/`, `go.mod` or `go.sum`). Guard `_test.sh` regression tests not
  re-run (no guard script changed).
- Driven run: not done for iOS (no iOS simulator in this environment); the linux surface is visually
  unchanged (closed dialog, identical DOM), covered by the handler test on four non-iOS GOOS values.
  Surfaces looked at: none visually; accepted gap for an invisible DOM-only change.

Pre-flight: help topic n/a (no visible change on any platform; the dialog was never painted on iOS); docs-shots surface hash updated, screenshots n/a (no pixel change); en.json / i18n-meta n/a (no keys added or changed); reference/ n/a; README n/a (no platform/feature claim changed); ADR n/a (follows ADR-less #3434 seam pattern); review record — this file; follow-ups — none.

## Verdict

Approve, with findings 1 and 2 fixed in the worktree (`internal/httpx/state_snapshot_test.go`, `web/help/img/manifest.json`).
