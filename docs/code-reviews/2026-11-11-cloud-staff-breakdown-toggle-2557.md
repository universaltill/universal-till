# Review: manager-only toggle for staff sales in cloud reports (ut-docs#2557)

- **Card:** ut-docs#2557 (split from #2535, epic #2493, ADR-0111 §4)
- **Branch:** `feat/2557-cloud-staff-breakdown-toggle`
- **Pipeline:** BA + Architect + UX (Opus, design note), Dev (Sonnet), Tester (Opus, separate agent), Reviewer (Opus, separate agent, fresh context), review fixes (orchestrator). Unattended cloud cycle, lane `lane:cloud-41`.

Pre-flight: web/help topic — updated (`web/help/en/reports.md` new "Staff sales in cloud reports" section + keywords; docs-shots — n/a: `guard-docs-shots.sh` passes, the new card is in no captured screenshot); `reference/` — n/a (ADR-0111 §4 already describes the setting and its default; no ut-docs page described the missing UI); README — n/a (no text went stale); en.json/en.meta — updated (5 `settings.cloud_staff.*` keys with meta, `works_council` is `review: legal`; English only, ADR-0160); ADR — n/a (follows ADR-0111 §4 and the existing manager-only card pattern); plan/price, platform claim, plugin taxonomy/host function — n/a; review record — this file.

## What shipped

- `internal/pages/cloud_staff_breakdown_settings.go` (new): `POST /api/settings/cloud-staff-breakdown`. Requires the `settings` permission (flat 403 `common.error.manager_or_admin_required`, no PIN-elevation path). It stores exactly `"true"` or `"false"` through `saveShopSettings`, so an additional till's write goes to the main till, which checks the grant again. It writes the audit row `cloud_staff_breakdown_changed` and answers like the neighbouring cards (`settingsRespondSaved`).
- `web/ui/pages/settings.html`: a `#settings-cloud-staff` card, rendered only `{{ if .isManager }}`. It has a checkbox, an explicit Save button (no save-on-change, so a stray tap commits nothing), a works-council note and a 14-day note, plus `{{ helpLink "reports" }}`.
- Wiring: `uislot.CoreSettings` (Order 1450, Advanced), the nav filter hides the row from non-managers, a shop-wide lock region, the demo-mode deny list, and an explain-only `data.AgentSettings` entry (not readable, because the forbidden-keys test refuses "cloud" keys).
- `data.CloudStaffBreakdownKey` is exported and `internal/cloudsync/sales_aggregates.go` uses it instead of a private copy of the string.

## Findings (independent Opus review) and resolution

| # | Sev | Finding | Resolution |
|---|---|---|---|
| F1 | major | Help "What is sent" left out `avg_sale_minor`, `items_per_sale` and `no_sale_opens`, and did not say that a staff member who only opened the drawer still gets a bucket. A works council checks exactly this list. | Help list completed (`reports.md`) |
| F2 | minor | The new const sat between `tillKeyExpr` and its doc comment, so `go doc` showed the wrong comment | Const moved above the comment |
| F3 | minor | The 14-day re-send/clear wording was unconditional; nothing is re-sent while the subscription has lapsed (402) | Help says "at the next upload while your subscription is active" |
| F4 | nit | Any value other than on/1/true is stored as `"false"` | Accepted: fails safe and matches the telemetry handler |
| F5 | nit | The cashier half of the card-visibility test passes because the whole `/settings` page is refused | Accepted: the card's own `isManager` gate is pinned by `CoreSettingsAndFilterMatchTheRealTemplate` |

No blockers. The reviewer found no problems with authorization (cashier, additional-till proxy, demo mode), CSRF, data access, i18n, RTL or modals. It checked the 14-day claim against the till's fingerprint salting, the empty `by_cashier` when the setting is off, and ut-cloud's `SetByCashier` replace.

## Verification

- New tests: `internal/pages/cloud_staff_breakdown_settings_2557_test.go` (manager on/off with audit rows, cashier 403, card shown to managers only); a cloudsync case for an explicit `"false"`. Extended: `TestSettingsEndpoints_RoleMatrix`, `TestSettingsSwapRegionNotReload`, the settingsnav fixture.
- TDD re-verified by the Reviewer in a separate worktree: with the handler and template reverted (const kept so it compiles), the manager test got 404 instead of 204, the cashier test 404 instead of 403, and the card was missing; after restoring, all passed. The Tester broke the code nine ways in a throwaway copy, and a test failed each time.
- Real app driven in Chromium (Tester, auth on, kiosk mode): at 1024x768 the manager toggles on, sees "Saved.", and the box is still checked after a reload; turning it off stores `"false"`, with exactly 2 audit rows. The checkbox row is 46.75px high. The cashier gets 403 on the page and on a direct POST. At 1024x600 Save is reachable by scrolling. At 390px nothing scrolls sideways. In RTL (fa) the layout mirrors correctly. Touch was emulated; there was no real Pi run.
- Gates: `gofmt` clean, `go build ./...`, `golangci-lint` 0 issues, `go test ./...` green except `internal/plugins` timing out at 600s here. That package is untouched and CI runs it on its own leg. Every guard in the CI build job passes, except three that cannot run in this container: deadcode was built with go1.26, shellcheck is not installed, and catimport passes once its tags are fetched.

## Deferred

- Tester did not check: dark theme, windowed mode, the additional-till proxy and lock states in a live run (they are covered by unit tests), long-locale labels, and real touch hardware.
- Cloud-side display gate: #2536 (out of scope).
