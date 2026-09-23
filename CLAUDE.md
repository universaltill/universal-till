# universal-till — rules for working in this repo

The offline-first **POS host** (Go, SQLite, HTMX). Full standards: `ut-docs`
→ `reference/coding-standards.md`. Rules only here — the *why* is in the
linked ut-docs issue; full pre-2026-09-23 wording: `git log -p CLAUDE.md`.

## Data access — repository pattern (`scripts/ci/guard-data-access.sh`)
- Within `internal/`, raw SQL lives only in `internal/data` (repositories)
  and `internal/db` (migrations). One-off `main.go` seed/smoke tooling under
  `scripts/` and `e2e/` (e.g. `scripts/e2e_seed`, `e2e/seed_faq`) is
  test-support, not domain code. Passing `*sql.DB`/`*sql.Tx` through the
  domain is fine; the query text is not — add a repo method.
- Migrations (`internal/db/migrations/`) are **append-only, always** — frozen
  the moment they merge (ADR-0100). Statement edits fail
  `TestShippedMigrationsUnchanged` and would brick installed tills;
  comment-only edits are fine. Add a new `NNN_*.sql` and pin its checksum in
  `internal/db/shipped_migrations_test.go` (the failure message prints it).

## Offline-first
- Checkout is never blocked by the network; a full sale completes offline.
- Offline/sync/install state → status chips/banners, never modal blockers in
  the kiosk flow.
- Status, lock and exit-to-OS stay reachable on every surface, admin
  included — except self-order kiosk mode, where they must be unreachable
  (the axis is device mode, not admin-vs-sale). A full-screen dialog over the
  nav rail must keep a compact affordance for all three. Kiosk exit can't
  live only in the web UI it hides. (#1999, #1513; coding-standards §10.)

## Self-order kiosk isolation (`scripts/ci/guard-kiosk-engine.sh`)
- The kiosk basket is `common.Deps.KioskEngine`, separate from the cashier's
  `Engine` (ADR-0020, #449). `/self-order` and `/api/self-order/*` are
  auth-exempt, so a file registering those routes must never reference
  `Engine` in code — any `<var>.Engine`, whatever the receiver's name;
  comment-only lines are exempt. Reviewed exception:
  `// kiosk-engine-guard:allow <reason>`.

## Money
- Amounts are `internal/money.Money` (integer minor units). Convert only at
  DB/DTO boundaries with `money.FromMinor(x)` / `m.Minor()`. Basis-point
  rates stay `int64`.

## API, formats, i18n
- Responses `{ "data": …, "error": null }`; JSON snake_case; ISO-8601 dates.
- No hardcoded user-facing strings: templates use `{{ T "key" }}`, and every
  key exists in every `web/locales/*.json` (en.json is the base) —
  `scripts/ci/guard-i18n.sh`. Go-side menu labels are `nav.*` keys.
- Inline `<script>` text is user-facing too: use a page-local
  `var T = { key: "{{ T "some.key" }}" }` (see
  `web/ui/partials/bugreport_panel.html`). The guard also scans
  `web/public/**/*.js` (not `vendor/`). Reviewed legacy exception:
  same-line `// i18n:ignore` (#205, #453).
- RTL: `dir` comes from the locale (`httpx.IsRTL`); use logical CSS
  properties (`margin-inline-start`, `text-align: start`), never left/right.
- A new `en.json` key needs follow-up PRs in `ut-plugin-language-{de,es}`.
  `lang-pack-drift.yml` blocks on `push` to `main` and is advisory on PRs
  touching `en.json` (its `::warning::` names the pack repos — #1857); it is
  `paths:`-scoped, so its absence on other PRs is normal. Never make it a
  required check. `scripts/ci/check-lang-pack-drift.test.sh` self-tests it.
- Validate all external input (users, plugins, devices).

## Compliance wording (ADR-0040, `scripts/ci/guard-compliance-claims.sh`)
- Never claim a certification outcome ("GoBD-compliant",
  "revisionssicher"/audit-proof, "certified by the Finanzamt", or that we file
  the §146a Abs. 4 AO notification). Describe what the software does. Full
  allow/forbid list: ut-docs#667. Scans `web/locales/*.json`, `web/help/**`,
  `web/ui/**`. Reviewed exception: `compliance-claim:allow`.

## Plugins
- Installed plugins are Ed25519-verified before they run
  (`internal/plugins/manifest_verifier.go`). Never run an unverified plugin.

## Agent worktree hygiene
- `.claude/worktrees/` accumulates stale agent worktrees that pollute
  repo-wide greps (#1567). Run `make prune-worktrees` periodically or when a
  search looks off — it only removes worktrees that are merged into
  `origin/main`, clean, and older than 3 days.

## Before committing
- `gofmt -l .` (no output), `go build ./...`, `go test ./...`,
  `golangci-lint run ./...` (0 issues; only the `unused` linter is enabled;
  `cmd/unitill-desktop` excluded — #1581), `shellcheck scripts/ci/*.sh` (0
  issues; a needed suppression is a targeted `# shellcheck disable=SCxxxx`
  with the reason on the line above — no prose on the directive line, never a
  blanket suppression), and **every guard in
  `.github/workflows/ci.yml`'s `build` job** (read the job for the current
  list).
- `android-ci.yml` runs on every PR (no `paths:` filter — it was unreliable;
  it skips SDK/NDK setup internally via `git diff` unless `android/**` or
  `mobile/**` changed): Gradle build against the generated `.aar` plus
  `scripts/ci/guard-gobind-skip.sh` (#1658, #1735).
- Separate path-scoped gating workflows — never make them required checks
  (most PRs get no run):
  - `adr-taxonomy-guard` job in `ci.yml` for `CanonicalTypes` changes:
    `guard-adr-plugin-taxonomy.sh` + `guard-adr-taxonomy-drift.sh` check
    ut-docs ADRs (needs `DOCS_READ_TOKEN`; skips with a warning without it).
    Merge the ut-docs ADR-0002 PR first. Historical counts in an ADR:
    `<!-- taxonomy-count:historical -->` (#2134, #2159).
  - `docs-shots-determinism.yml` for docs-shots harness changes (paths in
    its `on:` block; + weekly cron, `workflow_dispatch`): two runs must
    produce byte-identical PNGs and `manifest.json` (#2184).
  - `locale-render-audit.yml` for i18n wiring / `e2e/tests-i18n-audit/**`:
    renders pages in German and fails on untranslated English; fails for
    real on PRs; exceptions in `e2e/tests-i18n-audit/allowlist.json`;
    `UT_LOCALE_AUDIT_STRICT=1` (`scripts/ci/audit-locale-render.sh`) makes a
    missing pack checkout a hard failure, not a skip (#2300).
- Feature branch; review record in `docs/code-reviews/<date>-<topic>.md`;
  then merge. No secrets in logs or committed files.

## Decisions & documentation (ut-docs `adr/`, ADR-0007)
- ADRs are binding; changing course needs a superseding ADR first.
  Significant choices get an ADR before code; non-trivial features start
  from a short spec in ut-docs.
- Standing decisions: in-process WASM plugins (wazero), processes only for
  hardware plugins (ADR-0001); fixed 22-type taxonomy (ADR-0002, amended by
  ADR-0010, ADR-0088 B); offline-first, vendored assets (ADR-0003);
  server-rendered HTMX, no SPA (ADR-0008).
- Behaviour changes update the affected doc (`docs/reference/`, guides,
  `architecture/plugin-architecture.md`) in the same session.
- **The user manual ships with the feature** (#324): update the `web/help/`
  topic (prose, steps, `make docs-shots` screenshots) in the same branch. A
  new page needs a topic claiming its `routes:` and a `?` link; a section
  inside a claimed page uses `{{ helpLink "topic-id" }}`.
  `guard-help-topics.sh` checks route coverage and topic consistency;
  `guard-help-drift.sh` fails when a translation's structure drifts from
  English unless recorded in
  `scripts/ci/i18n-baseline/help-drift-baseline.json` (#1962).
- Keep `README.md` current in the same session whenever a change makes it
  stale.
