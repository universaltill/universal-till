# Registers admin page (ut-docs#651)

**Reviewer**: independent Opus subagent, fresh context, isolated worktree
(`complexity:medium` — Sonnet built it, Opus reviewed it, per the
scrum-master skill's model routing). One round — the first round found
blocking items, all fixed by the orchestrator directly (packaging/
translation/documentation fixes, not logic changes, so a second full
review round wasn't earned per the skill's "scoped to the fix" rule; the
fixes were re-verified against the full gate below instead).
**Branch**: `feat/651-registers-admin-page` (base: `main` @ `95dca79`).
**Date**: 2026-08-23.

## What shipped

`EnsureRegister` only ever created one hardcoded `reg-default` register —
no UI/API existed to create additional ones, so a real multi-till shop
couldn't reach the two-register topology ut-docs#268 already supports (a
second till resolved to the same register as the first and couldn't open
its own shift). This adds a Registers admin page, mirroring the existing
`internal/pages/locations_page.go` (stock-locations admin) pattern.

- **`internal/data/pos_repo.go`** — `CreateRegister`, `RenameRegister`,
  `SetRegisterActive`, `RegisterInUse`, `CountActiveRegisters`,
  `RegisterAdmin` + `ListRegistersForAdmin`, mirroring the `StockLocation*`
  family. `ListRegisters`/`EnsureRegister` untouched. All SQL stays in
  `internal/data`, verified by reading, not just trusting the guard.
- **`internal/pages/registers_page.go`** (new) — `registerRegisters`:
  manager-gated `GET /registers`, `POST /api/registers`,
  `POST /api/registers/{id}`, `POST /api/registers/{id}/active`, audit-
  logged. Deliberately does **not** gate deactivation on `RegisterInUse`
  (a retired till keeps its shift/sale history and stays deactivatable) —
  only on the last-active-register count, so a shop always has somewhere
  to open a shift.
- **`web/ui/pages/registers.html`** (new) — mirrors `locations.html`'s
  list+create-card structure; reuses existing classes/tokens only, no new
  colors/spacing, RTL-safe (logical properties, verified live in `fa`).
- **`internal/pages/init.go`**, **`internal/pages/menu_page.go`** — wired
  next to `registerLocations`; added a menu tile (🧮) so the page is
  reachable, matching how `/locations` is reachable.
- **`web/locales/{en,ar,fa,tr}.json`** — 18 `registers.*` keys, real
  translations in all four locales (not English placeholders — see
  Findings, this was fixed during review).
- **`web/help/{en,ar,fa,tr}/multitill.md`** — `/registers` added to
  `routes:`, a translated "Registers" section in all four locales
  (fixed during review — see Findings), and `web/help/img/**` +
  `manifest.json` regenerated (`make docs-shots`).
- **`internal/pages/ui_smoke_test.go`** — test fixture fix: the
  `seedForPages` `registers` table was missing the `UNIQUE` constraint on
  `name` that production (`001_init.sql`) has, which would have made the
  duplicate-name test a false pass.
- New tests: `internal/data/pos_repo_register_test.go` (12 cases),
  `internal/pages/registers_page_test.go` (5 cases).

## Independent review — TDD re-verification (done by the reviewer subagent, not taken on trust)

Dev/Tester claimed a template rendering bug was found and fixed during
TDD: rendering `RegisterAdmin.LocationID` (a `*string`) directly produced
a literal `&lt;nil&gt;` (unassigned) or a raw UUID (assigned) instead of a
resolved location name; fixed via a `registerView` wrapper resolving
`LocationName`. The reviewer independently reverted the wrapper and the
template cell in an isolated worktree, re-ran
`TestRegistersPageCreateRenameDeactivate`, and confirmed it fails with
exactly the claimed symptoms (`can't evaluate field LocationName` after
the first revert step; the literal `&lt;nil&gt;` and raw `loc_main` id
after the second), then restored the fix and confirmed all 5 tests pass
again. **The TDD claim is genuine.**

## Findings — all four BLOCKING items fixed before merge

1. **`guard-docs-shots.sh` would have failed CI** — the diff touched
   `web/ui/pages/registers.html`, handler Go files, and all four
   `multitill.md` topics with no regenerated screenshots. **Fixed**: ran
   `make docs-shots` (84/84 passed); `web/help/img/multitill.png` in all
   four locales is byte-identical to what was already committed (the
   screenshot captures `/tills`, the topic's `routes[0]` — unaffected by
   this page, matches this diff's own scope). `alerts.png`/`designer.png`
   also changed in all four locales as an incidental side effect of this
   run (the tool's own known, non-fatal Chromium-version-pin drift
   warning, ut-docs#622 — not something this diff caused or could scope
   around; a single deterministic tool invocation's full output was
   committed as-is rather than hand-picking files out of it).
2. **The whole new page was untranslated in ar/fa/tr** — all 18
   `registers.*` keys were byte-identical English (passed `guard-i18n.sh`,
   which only checks key-set parity, not translation content — exactly
   the gap that guard can't see). **Fixed**: real translations in all
   three locales, matching this repo's existing `locations.*`/
   `settings.tills.register_*` terminology (register → `صندوق`/`kasa`).
3. **The manual's translated topics had an English section pasted in
   verbatim** — `web/help/{ar,fa,tr}/multitill.md`'s new "## Registers"
   section was untranslated English inside an otherwise fully-translated,
   RTL (ar/fa) document. **Fixed**: translated in all three, matching the
   corrected English prose from finding 4.
4. **The help prose sent the reader to the wrong page for the step that
   actually matters, and to a page path that doesn't exist.** The
   original text said to visit "Settings → Registers" on a newly-joined
   till to "pick an existing register" — the Registers page has no
   picker (create/rename/deactivate only), and "Settings → Registers" is
   not a real nav path (`/registers` is a menu tile, not a Settings
   section). The actual picker is **Settings → Tills → "This till's
   register"** (`web/ui/pages/settings.html`, already correctly
   documented elsewhere in `display.md`/`reports.md`). As written, a shop
   owner following this step would create a second register and stop —
   precisely the state that makes `pos.ResolveTillRegisterID` return
   `ErrRegisterIdentityAmbiguous`, leaving the second till exactly as
   broken as before this card. **Fixed** in all four locales: "open
   **Registers** from the menu" (mirrors the house phrasing in
   `kitchen-stations.md`) to create the register, then "**Settings →
   Tills** → **This till's register**" to assign it.

Re-ran the full gate after all four fixes: `gofmt -l .` clean,
`go build ./...` clean, `go vet ./...` clean,
`go test ./internal/data/... ./internal/pages/...` all `ok`,
`guard-data-access.sh`/`guard-i18n.sh`/`guard-help-topics.sh`/
`guard-compliance-claims.sh`/`guard-docs-shots.sh` all ✓.

## Non-blocking, explicitly deferred (not fixed in this PR)

- **`RegisterInUse` ships as unreferenced production code** — the
  deliberate design decision (a retired till keeps its history and stays
  deactivatable, so this method is informational only) is sound and
  documented in three places, but nothing calls it. Accepted as-is;
  filing a follow-up card to either surface it as an "in use" hint next
  to Deactivate, or remove it.
- **A register's `location_id` can't be changed after creation** — create
  accepts a location, rename/reactivate don't touch it, and there's no
  delete. A genuine gap (not a mirroring miss — `locations_page.go` has
  no analogue to deviate from). Filing a follow-up card.
- **Deactivating (or creating) a second register can silently strand a
  till's `sync.till_register_id` binding** (`internal/pos/
  register_identity.go`, `internal/pages/shifts_api.go` — a stale/
  ambiguous binding refuses cash payouts with a 409) — the new page gives
  no in-page warning of this. Related to the help-prose fix above (which
  now correctly documents the manual step), but a stronger in-product
  nudge (a `{{ helpLink }}` hint near the create form) is real follow-up
  work, not blocking this card.
- Minor, not filed as separate cards: partial 403 test coverage on the
  rename/active-toggle routes (both verified manually to call
  `requireManager`, just untested directly); one avoidable extra stock-
  locations query in the render path; the rename error message now
  matches create's "(name already used?)" wording (fixed, trivial); no
  empty-state row on the registers table (consistent with `locations.html`,
  same as that page).
- **Pre-existing terminology overlap in ar/fa/tr, not introduced here**:
  this product's existing translations already reuse the same word for
  "till" (the `/tills` device-list page, `tills.title`) and "register"
  (`settings.tills.register_label`) in all three locales — `registers.title`
  necessarily collides with `tills.title`'s translation as a result. A
  full terminology audit is out of this card's scope.

## Verified beyond automated tests

- Driven live: real first-boot setup wizard + PIN login (not a test
  harness shortcut) against a temp SQLite DB, real browser (Playwright/
  Chromium) — create with a location, rename, and RTL (`fa`) layout, all
  screenshotted and visually checked (see Tester's report on the issue).
- Repository-pattern compliance verified by reading (not just the guard):
  every new SQL string lives in `internal/data/pos_repo.go`; the handler
  has zero SQL.
- SQL injection: all six new queries are parameterized, no string
  concatenation.
- Manager-gating verified on all four new routes by reading (all four
  call `requireManager` as their first statement).
- Last-active-register guard boundary verified: blocks at exactly 1
  active register, allows at 2+ (proven by the test suite, re-run
  directly, not just trusted).
- `location_id` empty-string vs `NULL` handling verified consistent
  between create and read-back; the FK to `stock_locations` is real and
  enforced (`foreign_keys` PRAGMA on in production).
- No real client/shop name anywhere in the diff — generic names only
  ("Front Till", "Back Till", "Test Runner Cafe", etc.).

## Safe-to-merge verdict

**Yes**, after the four blocking fixes above. The underlying Go
implementation was sound from the first pass (correct repository-pattern
layering, correctly gated, parameterized, TDD claim genuine under
independent re-verification) — every blocking finding was in translation/
documentation packaging, not logic, and all four are now fixed and
re-verified against the full gate.
