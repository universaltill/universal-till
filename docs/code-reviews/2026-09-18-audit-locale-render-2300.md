# Code review — de-locale render audit for admin pages (ut-docs#2300)

- **Date:** 2026-09-18
- **Ticket:** ut-docs#2300 (`complexity:medium`)
- **Branch:** `feature/ut-docs-2300-audit-locale-render`
- **Reviewer:** independent pass, fresh-context Opus subagent, isolated
  git worktree (per this card's `complexity:medium` routing — a stronger,
  different model than the Sonnet dev subagent that wrote the diff, never
  saw its reasoning).
- **Verdict: needs changes → fixed and re-verified. Safe to merge.** The
  independent review found one **high-severity** real bug (the test
  overlay was silently wiped mid-run by a code path this harness's own
  `/users` check walks), one **high-severity** CI-workflow bug (the pack
  checkout step would fail on every run), and one **medium** CI-guard
  regression (a stale `docs-shots` surface hash). All three fixed and
  re-verified live, in this repo's own checkout, not just in the review's
  worktree.

## What shipped

A repeatable e2e check (`e2e/tests-i18n-audit/audit-locale-render.spec.ts`,
`playwright.locale-audit.config.ts`, `scripts/ci/audit-locale-render.sh`,
`.github/workflows/locale-render-audit.yml`) that renders every
admin/help-topic page route (reusing `tests-docs/lib.js`'s
CI-guaranteed-complete `routedTopics()`) in German and fails on any
rendered, visible line of text that exactly matches a
`web/locales/en.json` value and isn't on the reviewed
`e2e/tests-i18n-audit/allowlist.json` exceptions list. This replaces the
manual crawl used to investigate ut-docs#2296, and catches both a pack
key that's missing/behind (`T()` falls back to English) and a hardcoded
English literal with no i18n key at all — the exact class of bug
ut-docs#2297 just fixed, which a pure JSON key/value diff (the existing,
narrower `scripts/audit-nav-i18n-parity.sh`) cannot see.

German is a plugin-shipped locale, not core (`web/locales/` only ships
en/ar/fa/tr) — `internal/pages/init.go` gained a test-only
`UT_TEST_I18N_OVERLAY_DIR` hook so the harness can exercise the real
`ut-plugin-language-de` pack's `de.json` without a full Ed25519-signed
WASM plugin install.

## What the independent review found

### 1. HIGH — the test overlay was silently wiped mid-run, producing false failures

The diff as first committed (`eba2139`) installed the overlay with a
one-shot `config.I18n.SetOverlays` call right after `pm.SetLocalizer(i18n)`.
`SetOverlays` atomically **replaces** every overlay in one call
(`internal/config/i18n.go`'s own doc comment), and `plugins.Manager`
re-publishes its own overlays from scratch on every `Reload` —
which `Deps.ReloadPlugins` fires not only on plugin lifecycle events but
on two plain, non-plugin paths this exact harness walks: the boot-time
builtin-layout reconciliation in `init.go`, and the setup wizard's/
Settings' shop-type save (`setup_page.go`, `settings_page.go`) — the
second of which the spec's own `ensureOperator()` drives before auditing
`/users`.

**Reproduced live** (not theorized): booted a till with
`UT_TEST_I18N_OVERLAY_DIR` set, confirmed `/settings?lang=de` rendered
German, then `POST /api/settings/shop-type` → the same page came back
with the de overlay gone entirely. Every page audited after that point in
a real run would then match `en.json` values and the audit would fail
with a flood of false positives — the harness would have been unreliable
from its very first real CI run.

**Fix:** `internal/pages/i18n_test_overlay.go` now wraps `plugins.Localizer`
(`testI18nOverlayLocalizer`) instead of calling `SetOverlays` once: it
merges the test directory's overlays into *every* set the plugin manager
publishes, so a reload can never drop them. Test overlay wins over a
plugin's own overlay on a key collision (this is what the run is
deliberately exercising); base locale files and shop overrides still win
over both — `config.I18n.T`'s own layering is untouched. Re-verified live
with a rebuilt binary: German survives the same shop-type `POST`.

### 2. HIGH — the CI workflow's pack checkout step would fail on every run

`.github/workflows/locale-render-audit.yml` used
`actions/checkout@v4` with `path: ../ut-plugin-language-de`.
`actions/checkout` rejects any `path:` resolving outside
`$GITHUB_WORKSPACE` — the job would have failed on its very first real
step, on every run, for a reason unrelated to any actual i18n finding.

**Fix:** check the pack out *inside* the workspace
(`path: ut-plugin-language-de`) and pass its location to the script via a
new `UT_DE_PACK_DIR` env var — the same in-workspace-checkout convention
`ci.yml`'s `adr-taxonomy-guard` job already uses for its own `ut-docs`
checkout. `scripts/ci/audit-locale-render.sh` now resolves
`${UT_DE_PACK_DIR:-<repo>/../ut-plugin-language-de}`, still exporting an
absolute path to Playwright (`run-till.sh` `cd`s into a temp data dir
before `exec`ing the binary, so a relative path would have broken there
too). Verified all three branches locally: dev-machine skip (exit 0),
`UT_LOCALE_AUDIT_STRICT=1` with no pack (exit 1), and a relative
`UT_DE_PACK_DIR` resolving to the real sibling pack.

### 3. MEDIUM — stale `docs-shots` surface hash

`internal/pages/i18n_test_overlay.go` is a route-less, non-test `.go`
file under `internal/pages`, and `init.go` also changed — both are kept
in `guard-docs-shots.sh`'s surface fileset (`tests-docs/lib.js`'s own
documented rule: a file can still feed a screenshotted page's template
data without registering a route itself). `web/help/img/manifest.json`
wasn't refreshed, so `guard-docs-shots.sh` failed (verified against a
pristine `git archive` of `eba2139`: exit 1).

**Fix:** ran the documented no-pixel-change escape hatch
(`scripts/ci/update-docs-shots-surface-hash.sh`, ut-docs#2102) — this
change cannot alter a rendered pixel with the env var unset (it's an
opt-in test-only hook). `manifest.json` now differs by exactly one field
(`surface_sha256`).

## Verified clean by the independent review (no changes needed)

- **`readTestI18nOverlays`/wrapper correctness**: genuinely collects all
  `*.json` into one map before merging — no per-file overlay call, no
  locale silently dropped.
- **No missing `os.MkdirAll`** — nothing in this diff writes to disk
  (reads only).
- **No cwd-relative path bug** — `en.json`/`allowlist.json` resolve from
  `__dirname`; the shell script resolves from `BASH_SOURCE`; the pack path
  is absolutized before export.
- **Plugin trust chain untouched**: the hook injects inert translation
  *strings* only, never executes plugin code. Traced every
  `template.HTML(` sink in `internal/` (11 call sites) — none takes `T`
  output raw; `html/template`'s auto-escaping applies at every render
  path, inline `<script>` `var T = {...}` blocks included. Does not
  weaken ADR-0006 / `manifest_verifier.go`.
- **No production leak of `UT_TEST_I18N_OVERLAY_DIR`**: repo-wide grep —
  only in the new/changed test-support files; no default, no config/YAML
  wiring; not referenced by `packaging/systemd/unitill-pos.service`.
- **CI workflow genuinely fails for real on a finding** (not a "described
  one thing, does another" mismatch): the run step has no `|| true` and
  no forced-success branch, unlike `lang-pack-drift.yml`'s deliberate
  advisory design — confirmed the comment and the logic agree.
- **Allow-list (11 entries)**: every entry cross-checked against the real
  `ut-plugin-language-de/locales/de.json` and `web/locales/en.json` — all
  are de.json's own deliberate values (loanwords/cognates/the product
  name), not missing-key fallbacks. de.json currently has full key parity
  (2518/2518).
- **Route-source reuse is real**, not reinvented: `require('../tests-docs/lib')`
  + `routedTopics()`, same pattern `docs-shots.spec.ts` already uses.
- **Test quality**: the original `i18n_test_overlay_test.go` suite was
  real, not tautological (a genuine multi-file regression for the
  overlay-replacement semantics); the review rewrote it around the new
  wrapper, kept all five original assertions, and added three: survives a
  simulated plugin reload (the bug above), test overlay wins on a key
  collision, and the caller's map is never mutated.
- **No hardcoded user-facing strings, no `web/ui/**` change** —
  `guard-i18n.sh` clean.
- **No secrets, no real client/shop name** — the workflow uses no
  secrets; `ADMIN_PIN`/`"Demo Shop"` are the existing shared e2e fixtures
  (`e2e/tests/helpers.ts`, `docs-shots.spec.ts`), not new test data.

## Re-verified after folding in the review's fixes (this pass, on this checkout)

- `gofmt -l .` — clean.
- `go build ./...`, `go vet ./...` — clean.
- `go test -count=1 ./internal/pages/...` — pass (155s), all five original
  assertions plus the review's three new ones.
- `shellcheck scripts/ci/*.sh` — clean.
- `bash scripts/ci/guard-i18n.sh` — clean.
- `bash scripts/ci/guard-docs-shots.sh` — **now clean** (was failing
  before the manifest refresh above).
- **Real Playwright run** (`bash scripts/ci/audit-locale-render.sh`)
  against the real `ut-plugin-language-de` sibling clone: **2 passed, 0
  failed** — including the `/users` manager-gated topic, which walks the
  exact setup-wizard shop-type-save path that used to wipe the overlay.
  This is the concrete, end-to-end proof the fix holds, not just a unit
  test in isolation.

## UX / visible-surface check

Not applicable. This is backend/CI-tooling: no `web/ui/**` or
`web/help/**` file is touched, and the one core-code change
(`internal/pages/init.go`) is inert unless a test-only env var is set. No
shop owner or admin sees any new or altered surface.

## Non-goals, explicitly (unchanged from the original design, restated for the record)

- Free-form English-word detection for a hardcoded literal that isn't
  also a real `en.json` value — a real, different, more expensive
  problem; documented as a known limitation in the spec's own header
  comment, not silently claimed as solved.
- Any locale other than German — `es`/other packs are separate future
  cards.
- A topic's secondary routes (`routes[1:]`) — same accepted gap as the
  docs-shots harness itself (ut-docs#900); only `routes[0]` is audited.
- Wiring into `release.yml`'s release gate — separate, already-noted
  future card (see ut-docs#2297's own body).
- Adding `locale-render-audit.yml` to branch-protection required checks —
  a human/settings decision, not a code change; explicitly noted in the
  workflow's own header comment, mirroring the same warning
  `lang-pack-drift.yml`/`adr-taxonomy-guard`/`docs-shots-determinism.yml`
  already carry.

## Deferred findings (not fixed here, noted for a human/future card)

1. **The PR trigger's `paths:` list doesn't cover the surfaces most
   likely to introduce the bug this audit hunts** (`web/ui/**`,
   `internal/pages/**` generally) — only the harness itself,
   `i18n_test_overlay.go`, and `en.json`. A hardcoded-English-literal PR
   elsewhere gets no check run until `push: main` catches it on an
   already-red base. Defensible (a multi-minute real-Chromium run on
   every UI-touching PR is expensive) but is a conscious tradeoff a human
   should confirm, not a silent gap — flagging rather than widening it
   unilaterally.
2. Whether to add this workflow to branch-protection required checks —
   explicitly left to the repo's own admin/settings owner, per the
   standing convention for the other three separate CI workflows.

## Regression risk

None identified beyond the three bugs above, all fixed and independently
re-verified live against the real German pack. The one core-code change
(`internal/pages/init.go`) is a no-op with the env var unset — the
`testI18nOverlayLocalizer` wrapper is never constructed and `pm.SetLocalizer`
receives the same `*config.I18n` it always did.
