# Code review: role_permissions schema + Auth.Can groundwork (ut-docs#554)

## Post-merge addendum: migration renumbered 038 → 039

This PR (#313) and PR #311 (ut-docs#567, "demo customers/promos opt-in
treatment") both branched before the other existed and each independently
claimed migration version 038 — under different filenames
(`038_role_permissions.sql` vs `038_demo_customers_promos_opt_in.sql`), so
git's merge never saw a conflict. Both merged to `main` minutes apart;
`main`'s CI immediately went red on both workflow runs with
`constraint failed: UNIQUE constraint failed: schema_migrations.version`.
Fixed same-session: `038_role_permissions.sql` renamed to
`039_role_permissions.sql` (039 was free), doc comments updated, full
gate re-run, PR opened and merged, `main`'s CI re-verified green. The
review content below (everything shipped, findings, verification) still
describes the actual code — only the file's version number changed. See
`ut-docs#629`/`#630` for the standing collision-guard gap this is another
instance of.

## What shipped

Split (a) of #520 — foundational infra only, no call-site migration yet.

- `internal/db/migrations/038_role_permissions.sql` (new): a `roles` table
  seeded with `cashier|manager|admin|super_admin` (adds `super_admin` as a
  recognized role for the first time); a `permission_actions` catalog
  seeded with `refund|eod_report|cash_adjustment|price_override|void|
  user_management|settings`; and `role_permissions (role, action,
  granted)`, FK-referencing both, seeded so `manager`/`admin`/
  `super_admin` are granted every catalog action and `cashier` gets none —
  chosen to exactly replicate today's `isManagerOrAuthOff` role gate.
- `internal/data/auth_repo.go`: new `AuthRepo.HasPermission(ctx, role,
  action) (bool, error)` — a closed model: no matching row (unknown role,
  unknown action, or an unseeded pairing) means denied, not an error.
- `internal/auth/service.go`: new `Service.Can(ctx, User, action) (bool,
  error)`, a thin pass-through to `HasPermission`.
- Tests: `internal/data/auth_repo_test.go` (granted/denied/unknown-action,
  plus the kiosk-user-zero-grants regression test the acceptance criteria
  call for); `internal/auth/auth_test.go` (`Can` plumbing test, plus a
  `role_permissions` table added to the package's hand-rolled test schema).

No call site changed — `Can`/`HasPermission` are referenced only from
their own definitions and the new tests (grep-confirmed by the reviewer).
Additive/inert: nothing in production consumes this yet, so no existing
till's access changes when this lands. That's #555's job.

## Review

Independent review via an Opus subagent, isolated in a separate git
worktree (its own copy of the WIP commit) so its revert/rerun steps
couldn't touch this checkout — `complexity:medium`, so review runs at the
stronger model per the `scrum-master` skill's model-routing table.
Verdict: **safe to merge, no blockers, no majors.**

### Findings — all minor/nit, all genuine follow-ups for #555, not defects here

1. **`super_admin` is recognized only in the `roles` table.**
   `auth.User.IsManager()` (`internal/auth/service.go`) is still
   `manager||admin`, and the create-user validator
   (`internal/pages/users_page.go`) rejects `super_admin`, so no such user
   can be created through the UI today. A `super_admin` row written
   directly into the DB would be granted every catalog action by `Can()`
   while still being denied by every existing `IsManager()` gate — correct
   and deliberate (leaving `IsManager()` alone is exactly what keeps this
   card inert), but it has to be carried into #555 explicitly or the
   divergence lands silently. **Recorded here; not fixed in this card.**
2. **The `UT_AUTH=off` bypass has no equivalent in the permission model.**
   `isManagerOrAuthOff` has two arms — `auth.Disabled(os.Getenv("UT_AUTH"))`
   (grants everyone everything, for dev/CI tooling) or `u.IsManager()`. The
   seed data only replicates the second arm. Harmless today since nothing
   calls `Can()` yet, but #555 must wrap `Can()` in a helper that preserves
   the `UT_AUTH=off` bypass, or every dev/CI environment and any till
   running with auth off loses access at every migrated call site at once.
   **Flagged as a real trap for #555, not a defect in #554.**
3. **The action catalog doesn't yet cover the gated surface it will
   eventually replace.** The 7 seeded actions map to none of the
   plugin-management, printing, backup/restore, device-pairing,
   catalog-import, or sync gates that make up most of the ~87
   `isManagerOrAuthOff` call sites (#555's own scope estimate). #555 will
   need a further append-only migration extending the catalog *and*
   seeding grants for the new rows — 038's `CROSS JOIN` seed has already
   run on any till that upgrades before then, so later-added actions get
   no grants retroactively unless a later migration does the same seed
   pattern for just the new rows. Defensible for a foundational card; the
   migration's own header comment already anticipates this.
4. **Nit, fixed:** `internal/auth/auth_test.go`'s hand-rolled
   `role_permissions` table in `openAuthTestDB` omits the `roles`/
   `permission_actions` FK parents present in the real migration. It only
   ever inserts values that would be valid against them, so it wasn't
   testing a fiction, but a comment pointing at 038 was cheap insurance —
   added.
5. **Nit, no action needed:** the card's wording asks to "keep `role` a
   string FK into a `roles` table." `role_permissions.role` *is* a real
   FK into `roles`; `users.role` itself is not (SQLite can't add an FK to
   an existing column without rebuilding the table, and that rebuild is
   explicitly out of this card's scope). The migration's header states
   this and why. The requirement's actual intent — a future custom-role
   card is additive, not a rewrite — is satisfied by the `roles` table
   existing at all.
6. **Nit, no action needed:** the doc comment on `HasPermission` claims
   the closed model covers unknown role, unknown action, and an unseeded
   pairing; the shipped tests assert the latter two explicitly. The
   reviewer independently verified unknown-role behaves identically
   (`HasPermission(ctx, "totally_unknown_role", "refund")` → `false, nil`)
   — coverage tidiness, not a gap.

Findings 1–3 are follow-ups #555 needs to account for, not new backlog
cards on their own (they're scoped guidance for the very next card in
this split, already tracked as #555) — noted here so #555's Architect/Dev
pass starts from this list rather than rediscovering it.

## Verified

- `go build ./...`, `go vet ./...` — clean.
- `go test ./...` (full suite) — all packages pass, including the
  `internal/db` migration-replay tests (`barcode_seed_test.go`,
  `demo_seed_migration_test.go`) exercising 038 on both a fresh DB and an
  upgraded one.
- `bash scripts/ci/guard-data-access.sh`,
  `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`, `guard-i18n.sh` —
  all pass. (No UI/template surface touched — `ux-guidelines.md` checklist
  and `web/help/` manual updates don't apply; no plugin/self-order surface
  touched — kiosk-engine/plugin-menu-read guards are baseline
  confirmations, not meaningful new signal for this diff.)
- **TDD re-verified independently**, twice (once by Dev/Tester, once again
  by the reviewer in the isolated worktree): moved
  `038_role_permissions.sql` out, re-ran
  `TestAuthRepo_HasPermission`/`TestAuthRepo_HasPermission_KioskUserZeroGrants`,
  got a real runtime failure through the actual query path
  (`SQL logic error: no such table: role_permissions`), restored the file,
  confirmed both pass again.
- `PRAGMA foreign_keys = ON` is live at migration time — confirmed the FKs
  are real, not decorative: the reviewer's throwaway probe inserting
  `('bogus_role','refund')` and `('manager','bogus_action')` both failed
  with `FOREIGN KEY constraint failed`.
- Seed grants checked against `isManagerOrAuthOff`
  (`internal/pages/settings_page.go`) directly: its role arm is
  `u.IsManager()` = `manager||admin`; the seed grants
  `manager`+`admin`(+`super_admin`, which no user can hold today) — an
  exact match for every role a user can actually have right now.
- No real client/shop name, no secret-shaped literal anywhere in the diff.

## Safe-to-merge verdict

Yes. No blockers or majors found. One nit fixed in review (test-schema
comment); five other findings are explicitly scoped as input for #555,
recorded here rather than reworking this card's boundary.
