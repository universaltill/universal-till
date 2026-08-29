# Code review: opt-in gated eager store registration (ADR-0071)

- **Card:** universaltill/ut-docs#879
- **PRs:** `universal-till` feat/879-setup-wizard-opt-in-eager-registration (this
  branch); `ut-docs` [#1297](https://github.com/universaltill/ut-docs/pull/1297)
  (ADR-0071)
- **Built by:** Dev subagent (Fable, `complexity:hard`)
- **Reviewed by:** independent Opus subagent (different model from the build, per
  this pipeline's model-routing rule for hard cards)
- **Orchestrator:** autonomous pipeline cycle, lane `cloud-54`

## What shipped

An explicit, not-pre-ticked opt-in question on the setup wizard's last screen:
register this till with the Universal Till cloud marketplace now. On yes,
`POST /api/setup` makes one best-effort, 5-second-bounded `enroll.EnsureRegistered`
call (mirrors `installBasePluginsForSetup`'s existing shape) — never blocking or
failing the wizard's response. On no/absent, no call — ADR-0015's lazy registration
is untouched. The choice persists as `marketplace.auto_register_opt_in` and is
toggleable afterwards from Settings (new `POST /api/settings/auto-register`, same
elevation-gating/audit pattern as the existing `POST /api/enrol/now`); toggling on
re-triggers one best-effort attempt, toggling off never deregisters.

Full rationale and scope: `ut-docs/adr/0071-setup-wizard-opt-in-eager-registration.md`.

## Review findings and disposition

The independent review (fresh-model, adversarial — see full report referenced
below) found two MAJOR issues, both fixed before this commit, plus five
non-blocking notes:

1. **F1 (fixed) — consent copy over-stated what's sent.** The wizard/Settings/help
   copy in all 4 locales said the till sends "shop type/address" — but
   `enroll.register`'s actual payload is device ID, store name, app version, and
   country-derived region only (`internal/enroll/enroll.go`); `ut-cloud`'s
   `merchant_organization` schema has no shop_type/address field to receive them
   either (ADR-0026 decision 3, still unimplemented). Over-disclosure, not a privacy
   leak, but wrong on a consent screen either direction. Corrected in
   `web/locales/*.json`, `web/help/{en,tr,fa,ar}/claim.md`, `web/help/{en,tr,fa,ar}/
   users.md` (the last one completed by the orchestrator after the reviewer's pass,
   mirroring the reviewer's own `claim.md` phrasing per locale), and the ADR itself.
2. **F2 (fixed) — a promised background retry doesn't exist.** The ADR and a code
   comment claimed a till that opts in while offline gets picked up later by
   `enroll.Init`'s background loop. False: that loop only fetches the signing key
   and registers a *device* under an *already-registered* store — it never performs
   the initial store registration. Corrected in the ADR, `setup_page.go`'s doc
   comment, and the help-manual copy (offline → setup still completes, till stays
   unregistered until the plugin store, "Register now", or a later Settings toggle).
3. **F3 (noted, not fixed, out of scope)** — `RegisterNow`'s `attemptMu.Lock()` isn't
   context-aware, so on a black-holing network the wizard's 5s bound can queue behind
   the background loop's own ~15s signing-key fetch (worst case ~20s, not 5s).
   Pre-existing pattern shared with `setup_base_plugins.go`/`setup_tse.go`; belongs in
   `internal/enroll`, not this card. **Filed as universaltill/ut-docs#1298** for a
   future cycle.
4. **F4 (noted)** — 12 screenshots changed; `display.png` (×4 locales) is the real,
   required diff (new opt-in block, verified by eye: unchecked checkbox + help text).
   The rest were flagged as pixel-level render nondeterminism on untouched screens —
   confirmed again on the orchestrator's own re-run of `make docs-shots` after the
   F1/F2 copy fixes, which produced a *different* small set of incidental diffs
   (`invoices.png`, some `sell.png`/`translations.png` files) than the reviewer's
   run — consistent with non-deterministic rendering, not a regression either time.
5. **F5 (noted, pre-existing, untouched)** — a stale comment in
   `sync_plugins_test.go` describing `/v1/stores/register`'s stub as 404-only when it
   actually counts-then-500s. Real, but predates this diff; left as a tidy-up
   opportunity, not blocking.
6. **F6 (noted, design judgement)** — the new wizard hint is the longest string on
   any step; not verified on real touch hardware (no fitting environment here). The
   ADR requires the full what/who/why sentence; UX sign-off, not a defect.
7. **F7 (verified, no action needed)** — the reviewer independently confirmed (via a
   scratch test, since deleted) that the Settings checkbox correctly reflects
   `marketplace.auto_register_opt_in` in both states.
8. **F8 (noted)** — the ADR's Implementation section mentions a wizard screenshot
   that `guard-docs-shots.sh` cannot produce (it shoots by help-topic route, and no
   topic's primary route is the wizard). Documentation drift, not a functional gap.

## Independent verification performed (beyond the subagent review)

- **TDD claim, re-verified twice independently**: once by the orchestrator (revert
  `setup_page.go`/`settings_page.go`/`common/state.go` to the parent commit →
  compile-red with `undefined: common.KeyAutoRegisterOptIn` → restore → clean
  build), once by the reviewer in an isolated worktree with a *stronger* red
  (constant added back, handlers still absent → all 7 tests fail on real assertions,
  not just compilation) plus a mutation test (removing the `if !optIn { return }`
  gate flips the "zero attempts" assertion to a real failure).
- **Global-state / concurrency claim, verified**: `enroll.register` returns before
  any `kv.Set`/`mu.Lock()` write on a non-2xx response, so a refused (500)
  registration in tests leaves zero persisted identity — the `storeRegisterHits()`
  counts genuine HTTP hits (pre-existing seam in `sync_plugins_test.go`), not a flag.
- **Full gate**: `gofmt -l .` empty; `go build ./...`, `go vet ./...` clean;
  `go test ./...` (all 40+ packages) green; `go test ./internal/pages/...
  ./internal/enroll/... -race` green (no data races); all 30 CI-blocking guards in
  `.github/workflows/ci.yml`'s `build` job pass, re-run a second time after the F1/F2
  fixes and the `make docs-shots` regeneration.
- **i18n**: all 4 locale files hold the same key count with the 6 new keys present
  in each; tr/fa/ar are genuine hand-translations (the homelab Ollama endpoint was
  unreachable from this sandbox — flagged for a native/model pass when reachable),
  read coherently, no leftover English.

## Scope discipline confirmed

No SQL added outside `internal/data`/`internal/db` (test file's raw query is
guard-exempt, same as 10+ other `*_test.go` files in this package); no changes to
`ut-cloud`'s `merchant_organization` schema (ADR-0026 decision 3, explicitly out of
scope); no deregistration flow built; `setup_base_plugins.go:149`'s existing
incidental `EnsureRegistered` call untouched; `marketplace.telemetry_opt_in`
(unrelated plugin-telemetry feature) never conflated with the new
`marketplace.auto_register_opt_in` key.

## Verification summary

| Check | Result |
|---|---|
| `gofmt -l .` | empty |
| `go build ./...` / `go vet ./...` | clean |
| `go test ./...` | all packages ok |
| `go test ./internal/pages/... ./internal/enroll/... -race` | ok, no races |
| 30/30 CI guards (`.github/workflows/ci.yml` build job) | all pass |
| TDD red→green | independently re-verified twice (orchestrator + reviewer) |
| i18n key parity, 4 locales | confirmed |
