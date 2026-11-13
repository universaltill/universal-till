# Review: personal display part 3/3, the "My display" page (ut-docs#3440)

**What shipped:** operators whose role holds `personal_display` can now pick their own theme and sell-screen browsing layout on `/my-display`. The choice follows them to every till in the shop and falls back to the till/shop value when unset.

- **Migration `102_personal_display_grants.sql`:** grants the action to admin, manager and super_admin (INSERT OR IGNORE, same shape as 096). The cashier gets no row, so access fails closed. Its checksum is pinned. The migration-033 row total moves from 66 to 69.
- **`internal/pages/my_display_page.go`:** `GET /my-display`, `POST /api/my-display/theme` and `POST /api/my-display/browsing-mode`. Each POST checks in this order:
  - 403 without the grant or a signed-in operator;
  - 409 on an additional till (`user_display_settings` is admin-synced down from the main till, so a local write would be pruned);
  - 400 for a theme not in `availableThemes` or a mode outside the three `BrowsingMode` values.

  An empty value deletes the row, which reverts to the till's choice. Saves write only the signed-in operator's row; a posted `user_id` is ignored. Refusals use `httpx.RefuseText`, so the `save-error` banner shows the server's sentence.
- **Read-time fallback:** `Deps.ThemeAvailable` is wired in `pages.Init`. `ResolvedTheme` ignores a saved personal theme whose plugin is gone. A nil hook means no check.
- **Session chip:** a "My display" link (new `contrast` icon), shown only when the viewer holds the grant.
- **Supporting changes:**
  - 11 English keys with `en.meta.json` metadata;
  - help topic `web/help/en/my-display.md` plus one sentence in `display.md`;
  - docs-shots refreshed;
  - three `UserDisplayRepo` lines removed from `deadcode-baseline.txt`;
  - the three routes classified in `demoAllowedRoutes`.

**Process:** BA → Architect (Opus 5.5) → UX → Dev (Sonnet, TDD, tests failed first) → Tester → review (Opus 5.5, independent) → fixes. Notes are kept in the cycle's scratchpad.

## Tester (FAIL → fixed)

- **D1:** the refusal responses lacked `X-UT-Response: refused`, so the UI showed "Could not save" instead of the server's reason. Fixed with `httpx.RefuseText`. A test now asserts the header on 400, 403 and 409, and it failed before the fix.
- **D2:** an unstyled `<fieldset>` overflowed at 360px (scrollWidth 383). Fixed with the `.my-display-set` reset in `app.css`, using logical properties.
- **False-pass check:** the Tester disabled each production line in turn and every key test went red: cashier 403 on POST, follower 409, invalid theme and mode 400, the theme-gone fallback (common and pages tests), and the migration grant.
- **Driven run:** real binary, auth on, real admin, manager and cashier accounts.
  - The manager opened `/my-display` from the chip, saved Dark (applied after reload and on the sell screen), saved the strip layout (kept), then reverted to the till's theme.
  - The cashier kept the till theme, had no link, and got 403 on GET and on every POST.
  - Screenshots at 1024x600 (light and dark), at 360px including the drawer, and in fa (RTL mirrors correctly) were looked at.

## Review findings (Opus 5.5)

1. **High: docs-shots not regenerated** (`guard-docs-shots_test.sh` and `update-docs-shots-surface-hash_test.sh` would fail the build). Fixed: `make docs-shots` was re-run and the PNG and manifest committed.
2. **Low:** the help line said the link isn't shown on the sale screen or Menu, but on phones it is in the drawer. Fixed: reworded.
3. **Low:** the 128-character value cap had no test. Addressed in the test file (see the Dev note in the PR).
4. **Info, accepted:** for an operator with a personal theme row, every render reads the theme directory once and runs one `ListThemeEntries` query. No cost for anyone else.
5. **Info, accepted:** the cashier's 403 text is the shared "Manager or admin required" key used by every grant-gated page.
6. **Checked clean:**
   - server-side re-check on every POST;
   - own rows only;
   - `UT_AUTH=off` gives disabled controls and a 403;
   - no CSRF token, consistent with the other settings POSTs;
   - migration replay and an earlier revocation kept;
   - template escaping;
   - help route and `?` link;
   - demo classification;
   - no new file write without `MkdirAll` and no cwd-relative path.

**CI follow-up (locale-render-audit):** the German render of `/my-display` flagged the built-in theme names "Amber" and "Slate". `availableThemes` labels a built-in theme with the title-cased key and no T key, the same as Settings. Those two collide by spelling with `catalog.color.amber`/`slate`. Theme names are product names, so both are added to `e2e/tests-i18n-audit/allowlist.json` with that reason.
- Accepted trade-off: an untranslated `catalog.color.amber`/`slate` on a German page would no longer be flagged.
- Translating theme names would be a product change for Settings too.

No `settingsAudit` row is written for a personal cosmetic preference. This is a product nuance, accepted, and recorded for the owner.

## Verification

- `gofmt -l .` is clean. `go build ./...` and `go vet ./internal/pages/...` pass.
- `go test` passes for `./internal/db`, `./internal/data`, `./internal/pages/common`, `./internal/httpx` and `./web/...`, and for `./internal/pages -run 'MyDisplay|Personal|SessionChip|Permission|Demo|Icon|Help'`.
- `golangci-lint` passes for the touched packages.
- Guards: i18n, i18n-meta, help-topics, RTL logical CSS, no-showModal, no-native-confirm, data-access, page-http-error, compliance, competitor, mail, core-neutral, card-data, migration collision, fixed-dates, deadcode-growth and docs-shots.
- The full `./internal/pages` package (over an hour locally) and `guard-deadcode-baseline.sh` were left to CI, because this container's deadcode binary needs Go 1.27.

**Pre-flight:**
- help topic: updated (`web/help/en/my-display.md`, `display.md`, `make docs-shots`);
- en.json keys → `en.meta.json`: updated;
- reference page: n/a (no ut-docs reference lists till permission grants);
- README: n/a (no claim changed);
- ADR: n/a (follows the #3149 design);
- translations: n/a (Weblate, ADR-0160);
- deferred items: Backlog cards ut-docs#6192 and ut-docs#6193.

**Deferred → Backlog cards:**
- additional-till write-through to the main till, replacing the 409 → ut-docs#6192;
- the error-page theme resolver;
- a Settings-page hint after a till-theme save while a personal theme overrides it;
- whether shared screens (kitchen display, `/orders`) ignore personal themes.

The last three items are one card: ut-docs#6193.

**Verdict:** approve once the fixes above are in. They are applied in this branch.
