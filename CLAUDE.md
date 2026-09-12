# universal-till — rules for working in this repo

The offline-first **POS host** (Go, SQLite, HTMX). Full standards: `docs` repo →
`reference/coding-standards.md`. The non-negotiables, some **mechanically enforced**
(CI will fail):

## Data access — repository pattern (enforced by scripts/ci/guard-data-access.sh)
- **Within `internal/`, raw SQL lives only in `internal/data` (repositories)
  and `internal/db` (migrations).** No SQL query text anywhere else under
  `internal/` — CI fails on it. (Scope matches the guard script: one-off
  `main.go` seed/smoke tooling under `scripts/` and `e2e/` — e.g.
  `scripts/e2e_seed`, `e2e/seed_faq` — is test-support, not domain code, and
  isn't a repository method candidate.)
- Threading `*sql.DB` / `*sql.Tx` through the domain layer is fine; writing the
  query outside the data layer is not. Add a `PluginRepo`/`POSRepo`/etc. method
  instead.
- Migrations under `internal/db/migrations/` are **append-only** after the
  first paying shop goes live on this schema (`001_init.sql` may still be
  edited freely before that, across as many pre-revenue releases as needed —
  ADR-0074; superseded "append-only after the first release").

## Offline-first (non-negotiable)
- **Checkout must never be blocked by the network.** A full sale completes offline.
- Surface offline/sync/install state with status chips/banners, never modal
  blockers in the kiosk flow.
- **Status, lock and exit-to-OS must always be reachable — on every surface,
  admin ones included.** The one exception is self-order kiosk mode, where
  they must be deliberately *unreachable* (customer containment); the rule
  resumes the moment an admin takes the device out of that mode. The axis is
  the device's mode, never admin-vs-sale. A full-screen dialog is still fine,
  but it may not cover the nav rail without carrying a compact persistent
  affordance for those three. Decided on ut-docs#1999; full text, and the
  ut-docs#1513 corollary that the way out of kiosk mode cannot live in the
  web UI it is hiding, in `ut-docs/reference/coding-standards.md` §10.

## Self-order kiosk isolation (enforced by scripts/ci/guard-kiosk-engine.sh)
- The self-order kiosk's basket (`common.Deps.KioskEngine`) is a separate
  instance from the cashier's (`common.Deps.Engine`) — ADR-0020, ut-docs#449.
  `/self-order` and `/api/self-order/*` are auth-exempt (reachable by any
  anonymous LAN client), so a handler under those routes that touches
  `Engine` reads or mutates the cashier's live sale. CI fails if any file
  registering a `/self-order` or `/api/self-order/*` route (method-prefixed
  or bare-path) references the `Engine` field as code, regardless of the
  `*common.Deps` receiver variable's name (comments are exempt; a reviewed
  exception needs an inline `// kiosk-engine-guard:allow <reason>` comment).

## Money
- Monetary amounts use the **`internal/money.Money`** type (integer minor units).
  It's a distinct type, so the compiler blocks mixing money with quantities/rates.
  Convert to/from raw `int64` only at DB / external-DTO boundaries via
  `money.FromMinor(x)` / `m.Minor()`. Basis-point rates stay `int64` (not money).

## API, formats, i18n
- Responses `{ "data": …, "error": null }`; JSON **snake_case**; dates ISO-8601.
  (`money.Money` marshals as the same integer, so the wire format is unchanged.)
- **No hardcoded user-facing strings** — every visible string in a template
  goes through `{{ T "some.key" }}`, and the key must be added to **every**
  file under `web/locales/` (en.json is the base; all locales must match its
  key set). Enforced by `scripts/ci/guard-i18n.sh` — CI fails on a missing
  key or a locale that drifts from en.json. Go-side menu labels are locale
  keys too (`nav.*`), rendered through `T` in the nav template.
- **This includes inline `<script>` blocks**, not just template markup: a
  status message set via `.textContent`/`.innerHTML` in a page's own JS is
  just as user-facing as anything in the HTML around it. Route it through a
  small, page-local, template-populated lookup object —
  `var T = { key: "{{ T "some.key" }}" }` — the pattern already used in
  `web/ui/partials/bugreport_panel.html` and `web/ui/pages/settings.html`'s
  `data-reset-btn`/`export-run-btn` handlers. `guard-i18n.sh` flags a
  hardcoded prose literal here too (ut-docs#205); a reviewed pre-existing
  exception (not yet migrated) gets a same-line `// i18n:ignore` comment,
  same escape hatch the Go-side check already uses. Known gap: the guard
  only scans `web/ui/**/*.html` — shipped JS under `web/public/` isn't
  covered yet.
- **RTL:** the document `dir` is derived from the locale (`httpx.IsRTL`);
  style with **logical** CSS properties (`margin-inline-start`, `text-align:
  start/end`, `padding-inline-*`) — never `left`/`right` — so RTL locales
  (fa, ar, he, …) lay out correctly with no extra CSS.
- Adding a key to `web/locales/en.json` also needs a follow-up in the
  external `ut-plugin-language-{de,es}` packs. `lang-pack-drift` CI
  (`.github/workflows/lang-pack-drift.yml`) checks this: **blocking** on
  `push` to `main`, **advisory-only** on a PR that touches `en.json`
  (shows a `::warning::` on the Files-changed tab naming exactly which pack
  repo(s) need a follow-up PR — ut-docs#1857 — plus the exact missing
  key(s) in the Actions job summary, never blocks merge) so the gap
  surfaces to the author before `main` goes red, not after. The PR check
  is `paths:`-scoped, so it doesn't run at all on a PR that never touched
  `en.json` — its absence from that PR's check list is normal, not stuck.
  `scripts/ci/check-lang-pack-drift.test.sh` self-tests the guard itself
  (fixture pack repos over a throwaway local HTTP server) and runs before
  it in CI, same pattern as each pack repo's own `check-key-drift.test.sh`.
  Each fixture's `check-key-drift.sh` comes from a local sibling checkout
  if one is on disk, else a live fetch from that pack's own `main` (so the
  test still runs on a bare CI checkout of just this repo) — see that
  script's own header if you need the exact resolution order.
  Never add it to branch protection's required checks for the same reason
  (most PRs would show no check run, which reads as "waiting forever").
- Validate all external input (users, plugins, devices).

## Compliance wording (Germany pilot)
- **No compliance-certification outcome claims** (ADR-0040) — "GoBD-compliant",
  "audit-proof"/"revisionssicher", "certified by the Finanzamt", or asserting
  we file the merchant's §146a Abs. 4 AO notification are all forbidden; the
  full product-owner-approved allow/forbid list is on ut-docs#667. Describe
  what the software *does* (a factual capability), never promise a legal
  *outcome* — that depends on how the shop operates, not on us.
  Enforced by `scripts/ci/guard-compliance-claims.sh` across
  `web/locales/*.json`, `web/help/**`, and `web/ui/**` — same
  `compliance-claim:allow` escape-hatch convention as `guard-i18n.sh`'s
  `i18n:ignore`, for a reviewed exception (e.g. a doc explaining in prose why
  a term is avoided).

## Plugins
- Installed plugins are Ed25519-verified before they run
  (`internal/plugins/manifest_verifier.go`). Never run an unverified plugin.

## Agent worktree hygiene
- `.claude/worktrees/` (real registered git worktrees created by
  `Agent(isolation: "worktree")` / `EnterWorktree` with a `name`) is
  gitignored, not repo content — nothing removes them automatically when a
  run ends, so they accumulate (233 MB / 4 stale worktrees found
  2026-09-04, poisoning repo-wide greps with duplicate hits at old
  commits — ut-docs#1567). Run `make prune-worktrees`
  (`scripts/prune-stale-worktrees.sh`) periodically or whenever a
  repo-wide search feels off; it only ever removes a worktree that is
  fully merged into `origin/main`, clean, and past the retention window
  (default 3 days) — anything still holding unmerged or uncommitted work
  is left alone and reported.

## Before committing
- `gofmt -l .` (no output), `go build ./...`, `go test ./...`,
  `golangci-lint run ./...` (0 issues — `.golangci.yml` enables `unused`;
  `cmd/unitill-desktop` is excluded there until a `-tags=desktop` pass with
  real GTK/WebKit headers lands, ut-docs#1581), `shellcheck scripts/ci/*.sh`
  (0 issues — ut-docs#1943; a pre-existing warning is either fixed or gets
  a targeted `# shellcheck disable=SCxxxx` directive with a reason on the
  preceding comment line (a shellcheck directive comment must carry only
  `key=value` pairs — trailing prose on the same line fails to parse,
  SC1072/SC1073), never a blanket suppression), and every
  CI-blocking guard in `.github/workflows/ci.yml`'s `build` job — currently:
  `guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
  `guard-page-http-error.sh`,
  `guard-i18n.sh`, `guard-compliance-claims.sh`, `guard-docs-shots.sh`,
  `guard-help-topics.sh`, `guard-help-drift.sh`, `guard-webkit-version.sh`,
  `guard-kiosk-launch-flags.sh`, `guard-android-status-address.sh`,
  `guard-android-i18n.sh`, `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `guard-e2e-fixtures-import.sh`,
  `check-brand-assets.sh`, `guard-makefile-version.sh`, and
  `guard-shellcheck-version.sh` (all under `scripts/ci/`). This list drifts as
  guards are added — check the workflow file's `build` job for the
  authoritative, current one rather than trusting this snapshot.
  `guard-shellcheck-version.sh` (ut-docs#1955) is the shellcheck-version
  equivalent of the `golangci-lint-action`'s `version: v2.5.0` pin just
  above — CI's `shellcheck` comes preinstalled on `ubuntu-latest` rather
  than through a pinnable action, so this guard instead fails loudly the
  moment the installed `shellcheck --version` drifts from its own hardcoded
  baseline, so a runner-image bump can't silently change which findings
  `scripts/ci/*.sh` gets flagged for.
- **`android/**` or `mobile/**` changes also gate on
  `.github/workflows/android-ci.yml`** (ut-docs#1658, filter widened to
  include `mobile/**` by ut-docs#1721's review — `mobile` is the
  gomobile-bind package itself, and a change there is exactly what this
  workflow exists to verify, not just a Kotlin-side edit) — a separate
  workflow (not a step in `ci.yml`'s `build` job): `./gradlew assembleDebug`
  against `android/`, proving the Kotlin actually compiles against the
  generated Go `.aar`. It always runs (a top-level `paths:` trigger filter,
  GitHub's usual convention — see `lang-pack-drift.yml` — was measured
  unreliable for this workflow: it silently skipped real
  `android/**`-touching pushes on the same PR during this card's own
  testing), but skips its SDK/NDK/gomobile setup cost internally via a real
  `git diff` when the change doesn't touch `android/**` or `mobile/**`.
  Before this, only `release.yml`'s `android-app` job compiled it, so a
  Kotlin compile error under `android/` could merge to `main` clean and
  only surface at release time or on a real device. The residual gap this
  left (ut-docs#1721 review, filed as ut-docs#1735) — `gobind` reports a
  type it silently can't bind as a comment in its generated output and
  still exits 0, which a compile-only gate cannot see — is now closed by
  `scripts/ci/guard-gobind-skip.sh`, which runs `gobind` itself and scans
  its generated output for that skip comment; it runs as two steps in
  `android-ci.yml`'s `compile` job (the guard, then its own regression
  test), right after "Install gomobile/gobind" and before the Gradle
  build, so a silent skip fails fast instead of shipping a phone build
  silently missing a method.
- **A `CanonicalTypes` change (`internal/plugins/manifest_verifier.go`)
  also gates on `adr-taxonomy-guard`** (ut-docs#2134) — a separate,
  standalone job in `.github/workflows/ci.yml` (not a step in the `build`
  job, same reasoning as `android-ci.yml` above: it needs a second repo
  checked out, `ut-docs`, so it stays out of `build`'s own checkout).
  `scripts/ci/guard-adr-plugin-taxonomy.sh` reads `ADR-0002`'s taxonomy
  line for real and fails if it disagrees with `CanonicalTypes` — replacing
  a same-repo test that used to diff `CanonicalTypes` against a second
  hardcoded copy of itself and could never see the ADR drift (which is
  exactly how `language` and `layout` both shipped with their own accepted
  ADRs while ADR-0002's own text kept saying "20 canonical types").
  **A sibling guard, `scripts/ci/guard-adr-taxonomy-drift.sh` (ut-docs#2159),
  runs in the same job and catches the same class of drift in every OTHER
  ADR that restates the taxonomy count in one of a handful of known
  phrasings** — ADR-0002's own text drifting is not the only way this goes
  stale; ADR-0050 did too (ut-docs#2150). It is line-based regex, not a
  markdown parser, and explicitly not exhaustive against a phrasing nobody
  has used yet (the guard's own header names exactly which phrasings it
  matches and its known gaps — widen it there, not here, if a future stale
  mention slips past). A line deliberately narrating a historical count (as
  part of explaining that ADR's own decision, e.g. ADR-0010/ADR-0050) is
  exempt via an inline `<!-- taxonomy-count:historical -->` marker — same
  reviewed-exception convention as `i18n:ignore`/`compliance-claim:allow`.
  **Unlike `manifest-contract-guard`'s direction** (checks out this
  *public* repo from `ut-cloud`, no token needed), this job checks out
  `ut-docs`, which is **private** — it needs a `DOCS_READ_TOKEN` repo
  secret (a PAT with read access to `universaltill/ut-docs`), and skips
  loudly (`::warning::`, non-blocking) rather than failing when that
  secret isn't available, e.g. a fork PR. **A `CanonicalTypes` change
  landing here needs `ut-docs`'s own ADR-0002 PR merged first** (or at
  least merged before this job's run reads `ut-docs`'s default branch) —
  same ordering dependency `manifest-contract-guard` already documents for
  its own direction.
- **A change to the docs-shots harness itself also gates on
  `.github/workflows/docs-shots-determinism.yml`** (ut-docs#2184) — same
  reasoning as `android-ci.yml`/`adr-taxonomy-guard` above: a separate
  workflow, not a step in `ci.yml`'s `build` job, because it is far more
  expensive (it runs the real Playwright `docs-shots` harness TWICE,
  ~5 minutes, to byte-diff every screenshot) than that job's other guards,
  all pure source-hash/lint checks with no browser involved.
  `scripts/ci/guard-docs-shots-determinism.sh` proves the property
  `guard-docs-shots.sh` cannot see (that one hashes source surfaces, never
  PNG bytes): two consecutive runs on an identical tree must produce
  byte-identical PNGs and `manifest.json`. `paths:`-scoped to the harness
  itself (`e2e/playwright.docs.config.ts`, `e2e/tests-docs/**`,
  `e2e/scripts/docs-shots.sh`/`resolve-chromium.sh`, the docs `run-till*.sh`
  servers, `e2e/package*.json`, `Makefile`) on PRs, plus a weekly Monday
  cron and `workflow_dispatch` as defense-in-depth against a cause that
  isn't a diff to those files (e.g. a Chromium/Playwright version bump
  silently changing what its `--disable-gpu`-et-al. determinism flags
  actually determinize). Never add it to branch protection's required
  checks, same reason as `lang-pack-drift`/`adr-taxonomy-guard` above (most
  PRs get no check run at all for it).
- Feature branch; code review recorded in `docs/code-reviews/<date>-<topic>.md`;
  then merge to `main`. No secrets in logs or committed files.

## Decisions & documentation (docs repo → adr/, ADR-0007)
- **ADRs are binding.** Before implementing, check `docs/adr/` — do not
  contradict an accepted ADR; changing course requires a superseding ADR first.
- **Document-first:** significant/architectural choices get an ADR *before*
  code; non-trivial features start from a short spec in the docs repo.
- Key standing decisions: plugin runtime = in-process **WASM (wazero)**,
  processes only for hardware plugins (ADR-0001); 22-type plugin taxonomy is
  fixed (ADR-0002, amended by ADR-0010 and ADR-0088 Decision B); offline-first,
  assets vendored (ADR-0003); server-rendered
  HTMX UI, no SPA (ADR-0008).
- Behaviour changes update the affected doc (`docs/reference/`, guides,
  `architecture/plugin-architecture.md`) in the same session.
- **The user manual ships with the feature, not after it.** Anything a
  shop owner sees or does that a change adds, removes or alters gets its
  topic under `web/help/` updated in the *same branch* — the prose, the
  steps, and a regenerated screenshot (`make docs-shots`) where the
  screen itself changed. A new page needs a manual topic declaring its
  `routes:` and a `?` link. `scripts/ci/guard-help-topics.sh` enforces the
  manual's own internal consistency (no two topics claim the same route,
  every topic's front matter parses, no locale is missing topics `en`
  has) **and page-route coverage**: every user-facing GET page route
  registered under `internal/pages/**` must be claimed by some topic's
  `routes:` — exactly or via a `{param}` pattern, matched with the same
  segment-wise matcher the runtime "?" resolves through. Non-page
  namespaces (`/api/`, `/ui/` fragments, static assets, …) are denylisted
  by prefix in `scripts/ci/checkhelptopics/routecoverage.go`, each with its
  reason. (The coverage check was the follow-up tracked as ut-docs#365,
  closed by ut-docs#326.) A section INSIDE an already-claimed page gets an
  explicit `{{ helpLink "topic-id" }}` hint (see settings.html), not a
  competing `routes:` claim.
  Standing instruction from the product owner, 2026-08-06 (ut-docs#324) —
  the manual is only worth having if it is never behind the product.
  `guard-help-topics.sh` only checks a translated topic *exists* — it can't
  see a translation that fell behind English's actual content.
  `scripts/ci/guard-help-drift.sh` (ut-docs#1962) closes that gap: it fails
  when a translated topic's structure (heading/step/bullet counts) no
  longer matches English, unless the mismatch is recorded in
  `scripts/ci/i18n-baseline/help-drift-baseline.json` (kept outside
  `web/help/` deliberately — that tree is `//go:embed`'d into every shipped
  binary, and this file is CI-only bookkeeping; same
  record-then-burn-down convention as `ut-plugin-language-*`'s own
  `i18n-baseline/` files) — a baseline entry itself fails once the real
  drift no longer matches what it recorded, so a fixed or worsened entry
  can't go unnoticed.
- **`README.md` is kept up to date every time it goes stale** — any change
  that affects what the README claims (features, setup steps, badges,
  version floors, structure) gets a README edit in the same session, not a
  separate follow-up.
