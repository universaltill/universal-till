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
- Migrations under `internal/db/migrations/` are **append-only** after the first
  release (`001_init.sql` may still be edited pre-release).

## Offline-first (non-negotiable)
- **Checkout must never be blocked by the network.** A full sale completes offline.
- Surface offline/sync/install state with status chips/banners, never modal
  blockers in the kiosk flow. Status/lock/exit must always be reachable.

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
- Validate all external input (users, plugins, devices).

## Plugins
- Installed plugins are Ed25519-verified before they run
  (`internal/plugins/manifest_verifier.go`). Never run an unverified plugin.

## Before committing
- `go build ./... && go test ./...` and `bash scripts/ci/guard-data-access.sh`
  and `bash scripts/ci/guard-kiosk-engine.sh`.
- Feature branch; code review recorded in `docs/code-reviews/<date>-<topic>.md`;
  then merge to `main`. No secrets in logs or committed files.

## Decisions & documentation (docs repo → adr/, ADR-0007)
- **ADRs are binding.** Before implementing, check `docs/adr/` — do not
  contradict an accepted ADR; changing course requires a superseding ADR first.
- **Document-first:** significant/architectural choices get an ADR *before*
  code; non-trivial features start from a short spec in the docs repo.
- Key standing decisions: plugin runtime = in-process **WASM (wazero)**,
  processes only for hardware plugins (ADR-0001); 20-type plugin taxonomy is
  fixed (ADR-0002); offline-first, assets vendored (ADR-0003); server-rendered
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
- **`README.md` is kept up to date every time it goes stale** — any change
  that affects what the README claims (features, setup steps, badges,
  version floors, structure) gets a README edit in the same session, not a
  separate follow-up.
