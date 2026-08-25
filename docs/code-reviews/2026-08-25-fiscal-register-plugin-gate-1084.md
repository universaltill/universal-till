# Code review: gate the fiscal-register nav tile on the German plugin, not just country (ut-docs#1084)

## Change

The §146a Abs. 4 AO "Fiscal register (Germany)" nav tile
(`internal/pages/menu_page.go`) was gated only on
`fiscal.RequiresHardGate(country)` — country == DE alone. That let the
tile appear on a shop with `store.country=DE` and **zero plugins
installed**, the literal product-owner complaint on ut-docs#1026 ("I
cannot see the installed German tax plugin — all German things should
appear with its plugin"). This card adds a second, `&&`-joined condition:
the tile now also requires the German tax plugin
(`com.universaltill.tax-de`) installed **and** active
(`fiscalRegisterPluginActive`, new helper in
`internal/pages/fiscal_register_page.go`, backed by the existing
`PluginRepo.PluginActive`).

Split out of ut-docs#1026 during this cycle's Architect pass: #1026 also
wants the register's *data* physically moved into the plugin, which
conflicts with a real, previously undocumented constraint — the only
plugin-owned storage mechanism today (`plugin_storage`) is wiped on
plugin **uninstall**, while this data must legally survive it (§146a
obligation "outlives the till itself," per migration 059's own header
comment). That data-ownership question needs its own design pass (flagged
on #1026, not solved here). This card ships only the safe, mechanical,
visible half of the complaint.

**Scope note, decided deliberately:** the underlying `/fiscal-register`
route itself is left ungated by plugin state — same as before this
change. It has never had a country gate either (a manager navigating
directly has always reached it regardless of country), so this is not a
new reachability gap. Gating the route was attempted first but reverted:
this page is one of the manual's screenshotted topics
(`web/help/*/fiscal-register.md`), and `docs-shots`' throwaway till never
has any plugin installed by design (`playwright.docs.config.ts`: "always
fresh... an installed plugin [would] silently bake into 'reproducible'
documentation screenshots"). Gating the route would make this topic
unscreenshotable without a broader change to that harness — out of scope
for an `easy`-complexity card, tracked as the remaining half of #1026
instead.

## Review process

Independent review — fresh-context Sonnet subagent (this card is
`complexity:easy`), no part in writing the change. Findings, all
confirmed against the actual code before being accepted:

1. **Real gap: no test proved the gate checks `is_active`, not mere row
   existence.** The two original tests covered "no plugin row" and "active
   plugin + non-DE," but never "installed and disabled" at country=DE — a
   query that degenerated to `SELECT COUNT(*) FROM plugins WHERE id = ?`
   (dropping the `is_active = 1` clause) would have passed both and still
   wrongly shown the tile. Fixed: added
   `TestMenuPage_FiscalRegisterTileHiddenWhenPluginDisabled` plus a
   `seedDisabledTaxDePlugin` test helper, mirroring the existing
   `installTaxDePluginDisabled` precedent in `import_page_test.go`
   (ut-docs#531).
2. **Real defect: a self-contradicting comment.** `menu_page.go`'s comment
   claimed the tile's gate "matches the page route's own gate," while
   `fiscal_register_page.go`'s comment on the same function said the
   opposite (the route is deliberately *not* gated). Fixed: reworded
   `menu_page.go`'s comment to state plainly that this is a
   visibility-only fix, not a reachability one, and to point at the other
   file's comment for why.
3. **Real, binding-rule violation: the manual went stale.** All four
   locale help topics (`web/help/{en,fa,tr,ar}/fiscal-register.md`) step 1
   still said the tile is "only shown once your shop's country is set to
   Germany" — now false. `universal-till/CLAUDE.md` is explicit:
   "Behaviour changes update the affected reference/guide in the same
   session" and the manual "is only worth having if it is never behind the
   product" (product owner standing instruction, ut-docs#324). Fixed: all
   four locale files updated to state the plugin requirement too, and
   `make docs-shots` re-run so the manifest reflects the prose change.
4. Verified as *not* problems (checked directly, not taken on faith):
   `taxDePluginID` really is `"com.universaltill.tax-de"`
   (`import_page.go:1074`); `PluginActive` really does
   `SELECT COUNT(*) FROM plugins WHERE id = ? AND is_active = 1`
   (`internal/data/plugin_repo.go`); ADR-0050 Decision 1's table really
   does place "§146a Abs. 4 AO notification data" under the country
   plugin; the `seedActiveTaxDePlugin`/`seedDisabledTaxDePlugin` test
   helpers' minimal 4-column insert against `seedForPages`'s simplified
   `plugins` fixture (no FK to `plugin_catalog`, unlike the real migrated
   schema) is accurate for what `PluginActive` actually queries, not a
   misrepresentation of production.

All three fixes applied and re-verified (tests re-run green, guards
re-run green) after the review, per this repo's standard.

## Verification

- `gofmt -l .` clean, `go build ./...` clean, `go vet ./...` clean.
- `go test ./...` — all packages pass. (`go test ./... -race` separately
  hits a pre-existing, unrelated timeout in `internal/plugins` at the 10m
  default — this PR touches nothing under `internal/plugins`, and the
  same class of race-detector-timeout flake is already tracked for a
  different package, ut-docs#1034; not introduced by this change.)
- CI-blocking guards: `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-i18n.sh`, `guard-compliance-claims.sh`,
  `guard-help-topics.sh`, `guard-docs-shots.sh`, and the rest of the
  `build` job's guard list — all pass.
- **Live-driven verification** (tester step, per this pipeline's
  "real driven run" requirement for UI/runtime-facing changes): started
  the actual server (`go run .`, `UT_AUTH=off`), set `store.country=DE` via
  `POST /api/settings/save`, confirmed `GET /menu` does **not** render the
  fiscal-register tile with no plugin row present; inserted a real
  `plugins` row (`is_active=1`) directly into the running server's SQLite
  file and re-fetched `/menu` **without restarting the server** — the tile
  appeared (`<a class="menu-tile" href="/fiscal-register">`), proving the
  gate reads live DB state, not something cached at boot.
- `web/help/img/manifest.json` updated by `make docs-shots`; only
  AA-noise-level PNG diffs (≤11 bytes, `en/users.png`, `ar/translations.png`)
  were produced and reverted per this harness's own documented convention
  (`docs-shots.spec.ts`'s comment on manifest-only commits being a
  legitimate outcome) — no `fiscal-register` PNG actually changed, since
  the page's rendered markup didn't change, only its help prose and the
  tile's visibility condition (not exercised by the docs-shots seed, which
  never sets country=DE or installs any plugin).

## Non-goals (tracked on ut-docs#1026)

- Moving `fiscal_register_de`'s table/data into `ut-plugin-tax-de`.
- Gating the `/fiscal-register` route itself (deferred — see Scope note).
- Resolving the plugin_storage-wiped-on-uninstall vs. legal-retention
  conflict.
