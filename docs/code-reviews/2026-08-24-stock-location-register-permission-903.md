# 2026-08-24 — ut-docs#903: dedicated `stock_location_management` permission action

**Card:** [ut-docs#903](https://github.com/universaltill/ut-docs/issues/903)
**Complexity:** easy — **Build model:** Sonnet (inline) — **Review model:** Sonnet, fresh-context worktree-isolated subagent

## Why

Follow-up from ut-docs#901's independent review (`docs/code-reviews/
2026-08-23-locations-registers-auth-off-901.md`): `locations_page.go` and
`registers_page.go` gated on the generic `"settings"` permission action —
correct at the time (#901 only needed the raw `IsManager()` check migrated
onto `canPerform` so `UT_AUTH=off` worked, and reusing `"settings"` was the
smallest fix for that) but semantically broad. `"settings"` also covers
`settings_page.go`, `receipt_designer.go`, `print_api.go` and
`menu_page.go`'s tile visibility generally, so a super_admin editing that
one row in `role_permissions` (runtime-editable via
`permission_settings_page.go`) moved stock-location/register
administration in lockstep with every other settings-gated surface, with
no way to grant or withhold it independently — something the old raw
`IsManager()` check could never do either way.

## What shipped

- `internal/db/migrations/060_stock_location_register_management_permission.sql`
  — one combined action, `stock_location_management` (covering both
  locations and registers, not two separate actions — the original card
  explicitly allowed either shape, and this codebase already treats the
  two as one admin surface: same `multitill.md` help topic, same menu
  section). Seeded identically to `"settings"` (manager/admin/super_admin
  granted, cashier denied) — same precedent as migrations 043
  (`plugin_management`), 057 (`tax_code_management`), etc.: a dedicated
  action per subsystem rather than overloading `"settings"`.
- `locations_page.go` / `registers_page.go`: `requireManager` switched
  from `canPerform(d, r, "settings")` to
  `canPerform(d, r, "stock_location_management")`.
- `menu_page.go`: the single nav-tile gate block (previously one
  `canPerform(d, r, "settings")` covering `/users`, `/locations`,
  `/registers`, `/kitchen-stations`, `/tables`, `/country-settings`,
  `/translations`, `/report-issue`) split so `/locations`/`/registers`
  gate on the new action while everything else stays on `"settings"` —
  otherwise the tile/page desync #901 fixed once already would reappear
  immediately.
- `web/locales/{en,ar,fa,tr}.json`: new `permissions.action.
  stock_location_management` key ("Locations & registers" / "المواقع
  والصناديق" / "مکان‌ها و صندوق‌ها" / "Konumlar ve kasalar") — the
  super_admin permission-editor page (`permission_settings_page.go`)
  reads the action catalog dynamically (`ListRolePermissionMatrix`'s
  `CROSS JOIN permission_actions`), so the new action shows up there with
  no extra Go wiring, same as every prior dedicated-action migration.
- `internal/pages/ui_smoke_test.go`'s `seedForPages` test fixture (a
  hand-maintained mirror of the real migration catalog, documented as
  needing to stay in sync with every migration that adds an action) got
  the new action appended.
- New/updated tests in `locations_page_test.go`, `registers_page_test.go`,
  `import_ask_menu_manager_gate_test.go` proving the new action's
  independence from `"settings"` in both directions: a role granted
  `"settings"` but not `stock_location_management` is denied; a role
  granted `stock_location_management` but not `"settings"` is allowed and
  sees the tiles.

## Independent review (Sonnet, fresh-context, worktree-isolated)

**Verdict: safe to merge. No BLOCKER or MAJOR findings.**

Verified the grant set against a real migrated SQLite DB (not just reading
SQL): `stock_location_management` ends up granted to exactly
manager/admin/super_admin, cashier denied — identical to `"settings"`'s
grant set, confirming no existing till's access changes on upgrade.
Confirmed via repo-wide search that `menu_page.go`'s split is complete —
nothing else in the repo gates `/locations`/`/registers` reachability on
`"settings"` specifically. Independently re-verified all three new tests'
teeth: reverted `locations_page.go`/`registers_page.go` (and separately
`menu_page.go`) to their pre-change state and confirmed the corresponding
new tests fail red for exactly the reason claimed, then confirmed restoring
makes them green again. Ran the full `go test ./...` suite to completion —
all green. Confirmed the locale translations are plausible and genuinely
translated, not copy-pasted. One informational MINOR (comment length,
matches the existing 043/057 style — no change needed).

Also independently noted the same pre-existing, out-of-scope gap this
diff's author had already found: migration 057's `tax_code_management`
action has no locale key and no explicit Go-side reference in
`permission_settings_page.go` — unrelated to and unaffected by this diff;
tracked as a new backlog card (ut-docs#942) rather than fixed here.

## What was verified beyond automated tests

- `gofmt -l .`, `go build ./...` — clean.
- `go test ./internal/pages/... -run 'Locations|Registers|Menu' -v` — all
  pass, including the three new independence tests.
- `go test ./internal/data/... -run 'Permission|Auth' -v` — all pass (23
  tests), including `TestAuthRepo_ListRolePermissionMatrix`'s full
  role×action grid check, unaffected by the new action.
- `go test ./...` — full suite, both this session's own run and the
  reviewer's independent run, all green.
- `bash scripts/ci/guard-i18n.sh`, `bash scripts/ci/guard-data-access.sh`,
  `bash scripts/ci/guard-help-topics.sh` — all pass; no new user-facing
  string outside the locale-key pattern, no SQL outside
  `internal/data`/`internal/db`, no route/manual-topic changes needed
  (this is a permission-model change, not a new page or route).
- No real client/shop name or secret-shaped literal anywhere in the diff.

## Verdict

**Safe to merge.** Both acceptance criteria from the original card met:
a product/engineering decision was made (build it — the codebase's own
established precedent for a subsystem this size made the "not worth it"
outcome the wrong call here) and, having been built, the new action is
migrated in, `menu_page.go` and both pages updated together, no
tile/page gating mismatch introduced, no existing till's access changed.

## Explicitly deferred (new Backlog card filed)

- **ut-docs#942** — `tax_code_management` (migration 057) has no locale
  key in `web/locales/*.json` and no explicit reference in
  `permission_settings_page.go`, unlike every other dedicated action;
  independently spotted by both this diff's author and its reviewer,
  pre-existing and unrelated to this change.
