# Review: Settings → Staff & security links Users and Permissions (ut-docs#3208)

Date: 2026-11-01 · Branch: `feat/3208-staff-security-links` · Lane: `lane:cloud-41`
Author: Sonnet (Dev subagent) + orchestrator (help topics) · Reviewer: Opus 5.5, fresh context

## What shipped

- New `#settings-staff-links` card in the **Staff & security** category
  (`web/ui/pages/settings.html`, after Auto-lock), with a `uislot.CoreSettings`
  entry (Order 1850, staff group) so the sidebar, search index and `layout`
  plugins see it.
- Two links, each gated like its page: **Users** (`/users`, PINs are set
  there) on `user_management`; **Permissions** (`/users/permissions`) on
  `permission_management`. The card and its sidebar row render only when at
  least one holds (`filterSettingsNavForRender` gains `showStaffLinks`).
  The pages themselves did not move.
- Keys `settings.staff_links.title` / `.help` in en/tr/ar/fa (+ en.meta.json);
  `settings.cat.staff.desc` reworded ("Staff accounts, PINs, permissions and
  auto-lock.") in every core locale. Pack follow-ups: ut-plugin-language-de/es/pt
  branches `i18n/3208-staff-links`.
- Help: `web/help/{en,tr,ar,fa,de}/users.md` step 1 names the new route.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | `StaffLinksNeverReachCashier` was vacuous: a cashier gets 403 on `/settings` before the gate runs, so it passed with the gate removed. A real leak path exists (cashier granted `settings`; manager with `user_management` revoked). | Fixed: two new tests (`StaffLinksHiddenForSettingsOnlyRole`, `StaffLinksHiddenWhenManagerLosesUserManagement`); cashier test kept as a labelled 403 smoke test. |
| 2 | Low | Button row used `.set-gap-s` only (top margin), so wrapped buttons had no row gap. | Fixed: `set-row set-gap-s` (page's flex/wrap/gap row). |
| 3 | Low | fa strings said "PIN" where fa.json and the fa help use "پین". | Fixed. ar "رموز PIN" accepted (PIN appears elsewhere in ar). |
| 4 | Info | "choose what each role may do" is also shown to a manager who sees only the Users button. | Accepted — describes the card's area; no button for a page they can't open. |

Gates confirmed identical to `GET /users` (`users_page.go` requireManager →
`user_management`) and `GET /users/permissions` (`lockoutAction`).

## Verified

- TDD re-verified twice: reviewer reverted the template/slot → the three
  original tests failed ("missing id=settings-staff-links"); orchestrator
  forced the card's `if` to `true` → both new leak tests failed ("leaks
  id=\"settings-staff-links\""); restored → all five pass.
- Driven run (real till, `UT_AUTH=off`, seeded demo): Settings → Staff &
  security tile shows the new one-liner; tree lists Auto-lock + Users and
  permissions; card shows Users and Permissions; Users opens `/users`.
  Looked at: en 1280×800 light, fa 1280×800 dark (RTL), tr 600×1000 light.
  Not looked at: kiosk sizes, a manager-only session in a browser (covered
  by the handler test).
- `go build ./...`, `go vet`, `gofmt -l`, `golangci-lint` (0 issues),
  `go test ./...`, and the build-job guards (i18n, i18n-meta, help-drift,
  help-breadcrumbs, help-topics, rtl-logical-css, no-showmodal,
  compliance-claims, competitor-naming, core-neutral, data-access, …): clean.

## Verdict

Safe to merge. Merge core first, then the three pack PRs (new keys →
`lang-pack-drift` on main is expected red until they land).
