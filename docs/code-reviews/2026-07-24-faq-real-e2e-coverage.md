# 2026-07-24 — Real browser e2e coverage for the FAQ plugin page

## Context
Spec-audit gap (`ut-docs/QUEUE.md`): "FAQ e2e tests are boilerplate, not real
coverage" — `ut-plugin-faq/tests/e2e/example.spec.ts` hit route `/faq` (the
real registered route is `/plugin/faq`) and POSTed to a nonexistent `/faq`
REST API (this plugin is `runtime: "none"`, asset-only, ADR-0001 — there is
no server to POST to). Never wired into CI either. No locale/RTL/fallback/
search test existed anywhere, despite `plugin_page_test.go` already covering
the server-side rendering logic thoroughly (locale-fallback matching,
checksum verification, keyword-haystack construction).

## Design
Real render-path coverage belongs where the renderer lives — this repo, not
`ut-plugin-faq`. Removed the boilerplate suite from `ut-plugin-faq` (its own
review doc) and added two things here instead:

1. **`e2e/seed_faq/main.go`** — installs the *real* FAQ plugin into the
   throwaway till `e2e/run-till.sh` boots: inserts `plugin_catalog`/
   `plugins`/`plugin_entries` rows, then copies real `content/en-US.json` +
   `content/fa-IR.json` and real `locales/en.json` + `locales/fa.json` (all
   four copied verbatim from `ut-plugin-faq`, byte-identical — verified) into
   the seeded install directory. The locale overlay copy matters: without it
   `plugin.faq.menu` resolves to the raw key (no `internal/plugins.Manager
   .syncLocales()` source), not the real "Help / FAQ" label a production
   install would show.
2. **`e2e/tests/faq.spec.ts`** — 4 Playwright tests against the real booted
   till: English rendering (entry count + real translated `<h1>`), Persian
   RTL (`dir="rtl"` on `.plugin-content`), the client-side search JS actually
   filtering the DOM (only a real browser can prove this — Go tests can only
   assert the `data-search` attribute is correct, not that the script runs),
   and the locale-fallback notice for an unsupported locale.
3. **`internal/pages/plugin_page_test.go`**: new
   `TestPluginPage_RTLBundleSetsDirAttribute` — every existing fixture in
   this file used `"rtl":false`; RTL rendering itself had zero coverage at
   any layer before this.
4. `e2e/run-till.sh`: one line, `go run ./e2e/seed_faq`, before boot.

## Independent review
8-angle review (`/code-review medium`), verified against the actual repo
state rather than taken at face value. Findings and disposition:

**Fixed:**
- Two independent angles (altitude, conventions) flagged that
  `e2e/seed_faq/main.go`'s raw `INSERT` statements technically break
  `CLAUDE.md`'s literal text ("Raw SQL lives only in `internal/data` and
  `internal/db`") even though `scripts/ci/guard-data-access.sh` doesn't
  catch it (its grep is scoped to `internal/`). Real gap: the rule's prose
  didn't match its own enforcement scope or existing precedent
  (`scripts/e2e_seed/main.go` already does the same thing, unremarked).
  Clarified the rule text in `CLAUDE.md` to state the `internal/`-only scope
  explicitly and name both seed scripts as the intentional exception,
  instead of either leaving the ambiguity or mis-architecting throwaway
  test-fixture inserts into `internal/data` as fake repository methods.
- Line-by-line angle caught that `plugin.faq.menu` (the plugin's page label)
  has no translation anywhere the seeded install could reach — real
  installs get it from `ut-plugin-faq`'s `locales/*.json` via
  `Manager.syncLocales()`, which reads `<plugins-dir>/<id>/<version>/locales/`.
  The seeder only copied `content/`, so the page fell back to rendering the
  raw key, and `faq.spec.ts`'s `<h1>` assertion (`toContainText(/faq/i)`)
  passed only because the literal string "plugin.faq.menu" happens to
  contain "faq" — not because translation was verified. Fixed: seeder now
  also copies real `locales/en.json` + `locales/fa.json` into
  `<install>/locales/`; the spec's assertion tightened to
  `toHaveText('Help / FAQ')`, the actual production string. Re-verified
  end-to-end (curl + full Playwright run) that this now resolves through the
  overlay mechanism for real, not by coincidence.
- Two simplification-angle findings, applied: `selfDir()` (a 9-line
  `runtime.Caller(0)` indirection plus the extra import) was solving a
  problem that doesn't exist — the only caller (`run-till.sh`) always `cd`s
  to the repo root first — replaced with a plain relative path; `copyFile`'s
  manual `os.Open`/`os.Create`/`io.Copy` shrunk to `os.ReadFile`/
  `os.WriteFile` (fixture files are a few KB, streaming buys nothing).

**Refuted:**
- Line-by-line angle claimed `pluginVersion = "0.2.3"` and the fixture
  checksums were fabricated/mismatched against the real plugin, citing this
  repo's own `data/plugins/com.universaltill.ut-faq/0.2.2/` as the source of
  truth. That directory is a stale, gitignored, uncommitted local artifact
  (`data/plugins/` — `.gitignore` line 75) left over from manual testing,
  not the source of truth. Re-verified directly against the actual
  `ut-plugin-faq` repo: manifest version `0.2.3`, and `diff` confirms all
  four seeded fixtures (2 content bundles, 2 locale overlays) are
  byte-identical to what's really shipping there today.

**Considered, deliberately not changed:**
- *The seeder bypasses the real install pathway* (raw `INSERT`s instead of
  `internal/plugins.PersistManifest`, so Ed25519 signature verification and
  compatibility checks are never exercised by this suite). Correct
  observation — a regression in the real installer gets no signal here. The
  alternative (driving the actual signed-bundle install flow, which would
  need a mock marketplace — `scripts/mock-marketplace/` exists but isn't
  wired for this) is materially larger scope than "add real render-path
  e2e coverage for FAQ," which is what this gap asked for. `manifest_verifier
  .go`'s own test coverage is the right place for install-pathway
  regressions, not this page-rendering suite. Not fixed here; flagging for
  anyone reading this file who might otherwise assume install-path
  correctness is covered by `faq.spec.ts`.
- *`pluginVersion` is a bare constant with no automated link back to
  `ut-plugin-faq`'s manifest* — real risk (the next manifest bump won't
  update this fixture automatically) but the same class of drift the
  existing `data/plugins/` vendor copy already has, with no established
  cross-repo sync mechanism in this codebase to hook into. Left as a known
  gap rather than building new sync tooling under this task's scope.
- *`e2e/seed_faq/main.go` duplicates `scripts/e2e_seed/main.go`'s
  `fatalf`/transactional-exec-closure pattern* (reuse angle, two
  independent mentions). Real duplication, but it's the third occurrence of
  an already-established repo convention for one-off seed binaries
  (`scripts/e2e_seed`, `scripts/smoke_quickstart`,
  `scripts/smoke-marketplace` all do this independently already); extracting
  a shared helper touches three unrelated files/repos-worth of existing
  scripts for a cosmetic win, out of proportion to this task.

## An operational note, not a code finding
Mid-review, a background review agent ran routine git commands (`git pull
--ff-only origin main`, a `git stash`) in this same working tree while
uncommitted changes were present — briefly stashed then correctly restored
the diff, no data lost, but worth recording: parallel review agents sharing
a working tree with in-progress uncommitted work is a real hazard.
Uncommitted work should hit a commit sooner when spawning multiple
Bash-capable agents against the same tree, or those agents should run in an
isolated worktree.

## Verification
`go build ./...`, `go test ./...` (full suite, including the new
`TestPluginPage_RTLBundleSetsDirAttribute`), `scripts/ci/guard-data-access.sh`,
`scripts/ci/guard-i18n.sh` — all green. `cd e2e && npx playwright test` — all
12 specs pass (4 new FAQ specs + the 8 pre-existing ones), re-run clean
multiple times after the locale-overlay fix to rule out the flakiness seen
mid-review (traced to concurrent `go build`/`go test`/Playwright runs from a
parallel review agent competing for the same port/CPU, not a real
regression — confirmed by isolated re-runs).
