# Code review — {data,error} envelope for 7 more JSON API surfaces

Ticket: universaltill/ut-docs#387
Date: 2026-08-08
Branch: `fix/api-envelope-7-handlers-387`

## What the change does

`universal-till/CLAUDE.md` mandates every JSON API response be
`{ "data": …, "error": null }` on success / `{ "data": null, "error": "…" }`
on error. Seven handler surfaces were still responding bare
(`{"success":…,"message":…}`-shaped, or no envelope at all), found during a
grep sweep after ut-docs#378 fixed the same gap in `inventory_api.go`/
`shifts_api.go`. This change brings them into line, following the
established `writeJSON`/`respondError`/`respondSuccess` pattern already in
`inventory_api.go`:

- `pos_api.go` — `POST /api/pos/tender`'s JSON success branch.
- `marketplace_v1_stub.go` — `writeJSONResponse` call sites (5 routes under
  `/v1/install/*`, `/v1/telemetry/plugins`).
- `plugin_api.go` — `writeInstallResponse` + 6 handlers (enable/disable,
  uninstall, update, rollback, check-updates, import-from-file).
  `handleExportPlugin` (binary stream) correctly untouched.
- `plugins_store_page.go` — the `respond` closure backing
  `/api/plugins/store/*`.
- `update_api.go` — `POST /api/update/apply`'s response helpers.
- `data_api.go` — the shared `respond` closure plus two GET handlers
  (`/api/data/customers`, `/api/data/obsolete-items`) that previously
  bypassed it entirely.
- `plugin_page.go` — `/api/plugins/entries/{plugin}/{key}/action`'s success
  body.

Every JS consumer of a changed response shape was updated in the same
change: `web/ui/layouts/base.html`, `web/ui/pages/settings.html`,
`web/ui/pages/plugins_store.html`, `web/ui/pages/plugins.html`,
`web/ui/partials/plugin_install_modal.html` — each now reads `res.data.*`
on success and `res.error` on failure instead of the old bare fields.
Existing fields (`message_key`, `event_id`, `already_current`, etc.) all
still exist, just relocated under `data`.

Manual screenshots (`web/help/img/**`, `manifest.json`) were regenerated
twice (once after the initial diff, again after the review fix below) —
`internal/pages/**.go` is part of `guard-docs-shots.sh`'s hashed surface,
so any change there requires a fresh `make docs-shots` run regardless of
whether a screen's actual rendering changed. This sandbox has no network
access to download Playwright's browser (`ut-docs#364`); used the
pre-installed Chromium at `/opt/pw-browsers/chromium` via a temporary
`launchOptions.executablePath` in `e2e/playwright.docs.config.ts`
(reverted after, no diff left in that file — same approach as PR #246).

## Review

Independent review by an Opus subagent, fresh context, isolated git
worktree (`isolation: "worktree"`) — different model from the Sonnet
implementer. Verdict: **PASS WITH FIXES NEEDED** — one substantive finding,
fixed below; everything else confirmed clean.

### Finding — fixed: false-pass test on `update_api.go`

`TestUpdateApplyResponseHelpers_EnvelopeShape` declared its own **local
copy** of the `respond`/`respondCurrent` closures and asserted against the
copy, never calling the real handler code. The reviewer proved this by
reverting `update_api.go`'s actual response helpers to the old bare shape
and re-running the test: it stayed green (`PASS`), and the entire
`internal/pages` suite stayed green too — zero regression coverage on this
endpoint's envelope change.

**Fix**: extracted `respond`/`respondCurrent` out of the handler closure
into package-level functions `respondUpdateApply`/
`respondUpdateApplyCurrent`, and rewrote the test to call those directly
(plus added a case for `respondUpdateApplyCurrent`/`already_current`, which
the old copied-closure test never exercised at all). Re-verified personally
via the same revert-then-restore method the reviewer used: reverted
`respondUpdateApply` to the bare shape, confirmed
`TestUpdateApplyResponseHelpers_EnvelopeShape` now fails for real
(`expected data.message populated and error:null, got {Data:{Message:}
Error:<nil>}`), then restored and confirmed it passes again.

### Everything else the reviewer checked (confirmed clean, no changes needed)

- Envelope correctness on all 7 files, both success and error paths — no
  field silently dropped (`message_key`, `event_id`, `already_current`,
  `from_version`/`to_version`, `version`, `warnings`, `updates`/`count`,
  `plugin_id` all present under `data`).
- JS consumers: every changed shape's consumer updated and reads the new
  fields correctly; two consumers found outside the original file list
  (`web/public/app.js`'s tender call — takes the untouched HTML branch;
  `plugin_buttons.html`'s HTMX post — response body ignored,
  `hx-swap="none"`) — both confirmed unaffected, not silently missed.
- Full gate (build/vet/gofmt/test/all three guard scripts) — clean,
  reproduced independently in the reviewer's own isolated worktree.
- `guard-docs-shots.sh` — independently reproduced (with the same
  `executablePath` workaround), manifest byte-identical to what was
  committed.
- Revert-then-restore TDD re-verification on 3 tests (not just the 1
  above): `plugin_page_test.go` and `plugins_store_api_test.go`'s new
  assertions both genuinely fail when their handler's fix is reverted, then
  pass again when restored.
- Recurring bug classes (missing `os.MkdirAll`, cwd-relative path instead
  of `paths.Data(...)`) — checked, don't apply; this diff writes no files.
- No real client/shop name, no literal credentials.
- Manual (`web/help/**/*.md`) never documents wire format — nothing to
  update there.
- Scope — no drive-by changes; every route in ut-docs#387's list is
  covered; non-goals (`handleExportPlugin`, `/healthz`) respected.

### Minor, non-blocking (not fixed, noted for awareness)

- `writeInstallResponse`'s error path drops the human-readable `message`
  string when a `messageKey` is set — fine for the browser (JS maps the
  key to a localized string), slightly poorer for a hypothetical
  non-browser API client. Pre-existing shape choice, arguably an
  improvement (one canonical `error` field), not a regression.

## Verification

- `go build ./...`, `go vet ./...` — clean.
- `gofmt -l .` — only pre-existing, untouched files flagged
  (`external_api_test.go`, `import_page_test.go`,
  `internal/plugins/marketplace/client.go`,
  `internal/thirdparty/webview_go/webview.go`) — none touched by this diff.
- `go test ./...` (full suite) — all packages `ok`.
- `guard-data-access.sh`, `guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-docs-shots.sh` — all clean.
- TDD re-verification personally repeated on the post-review fix (see
  above): reverted, confirmed real failure, restored, confirmed pass.

## Post-merge CI finding — fixed: `tests/e2e` consumer missed by both dev and review

CI's `e2e` job (`tests/e2e/`, a *different* Playwright suite from `web/ui/**`'s
inline JS and from `guard-docs-shots.sh`'s harness) failed on
`tests/e2e/tests/plugin_install_flow.spec.ts` — two specs asserted
`expect(body).toMatchObject({ plugin_id: …, … })` directly against the raw
response of `POST /v1/install/intents` and `GET /v1/install/status`, which
this PR moved under `data`. Neither the dev implementation brief nor the
independent Opus review's consumer sweep covered `tests/e2e/` (both scoped
the JS-consumer check to `web/ui/**`) — a real scope gap in this PR's own
process, not just the diff.

**Fix**: updated both assertions to check `body.error` is `null` and
`body.data` matches the same shape as before. Verified locally against a
CI-equivalent run (seeded via `scripts/e2e_seed`, `UT_AUTH=off
UT_DEV_MODE=true`, matching `.github/workflows/ci.yml`'s `e2e` job exactly)
— all 5 specs in the file pass, and the full `tests/e2e` suite (21
non-skipped specs; the 4 `docs_hub`/`plugin_lifecycle_docs` specs skip
without a `DOCS_READ_TOKEN`, same as CI without the secret) passes clean.

Grepped `tests/e2e/tests/` for every other route this PR touches
(`api/pos/tender`, `v1/install`, `v1/telemetry`, `api/plugins/store`,
`api/update/apply`, `api/data/*`, `api/plugins/entries`,
`install-from-marketplace`, etc.) — `plugin_install_flow.spec.ts` is the
only file referencing any of them, so no further gap.

## Deferred (not in scope, noted for a future backlog card)

- `/api/pos/tender`'s JSON-mode **error** paths (invalid JSON, insufficient
  stock, payment declined) still respond via `http.Error` (plain text), not
  JSON, even under `Accept: application/json` — pre-existing inconsistency,
  only the success shape was in this ticket's scope.
- The remaining `http.Error(...)` plain-text validation/gate responses
  (403/400/404 checks) across these files were never JSON to begin with —
  correctly untouched.
