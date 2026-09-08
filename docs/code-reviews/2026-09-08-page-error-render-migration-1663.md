# Migrate remaining page-route 500-error sites to httpx.RenderError (ut-docs#1663)

- **Date**: 2026-09-08
- **Branch / PR**: `fix/1663-migrate-page-error-render`
- **Card**: ut-docs#1663 (`p2`, `complexity:medium`, split from #1458)
- **Related**: ut-docs#1455 (`httpx.RenderError` itself, and the incident it fixed), ut-docs#1662 (the earlier manager-gate 403 cluster split from the same parent card), ut-docs#1786 (follow-up: `fiscal_device_page.go`'s 6 sites, deliberately deferred — see below)
- **Reviewer**: independent pass, Opus, fresh context, own isolated worktree, all gates re-run locally

## What shipped

`ut-docs#1455` established that a page-route handler must never answer with a raw `http.Error(...)` or a bare `common.LogAndLocalizedError(...)` — on a pinned Android kiosk this replaces the entire WebView document with plain/localized text and no navigation at all (no rail, no "Back to sale"), because the kiosk hides Android's own nav bar. `httpx.RenderError(w, r, status, msgKey, err)` is the fix: it renders the full `base.html` layout (rail + back-to-sale) with the translated message, and logs the real error server-side without ever echoing it to the client.

`#1458` tracked the remaining sites across the codebase; `#1662` already migrated the 12-site manager-gate 403 cluster. This card (`#1663`) is what was left: **30 of the ~36 remaining marked sites**, across 16 files —

`internal/pages/{catalog/handlers.go, promotions_page.go, country_settings_page.go, journal_page.go, sync_quarantine_page.go, tax_codes_page.go, refund_page.go, audit_page.go, kitchen_stations_page.go, plugin_settings_page.go, registers_page.go, sync_api.go, invoice_page.go, users_page.go, fiscal_register_page.go, locations_page.go}`

Each site converts a same-shape `if err != nil { <old call>; return }` in place, keeping the exact same status code and passing the same `err`:
- Sites that already called `common.LogAndLocalizedError(w, r, status, "<existing key>", "<component>", err)` keep the **same locale key**, just through `httpx.RenderError`.
- Sites that called raw `http.Error(w, "<english string>", status)` now use the existing shared `common.error.server` key ("Something went wrong. Please try again.") rather than minting a new per-file key — none of these files had an existing generic "could not load" key, and reusing the shared one avoids new-key churn across all four locales plus the external `ut-plugin-language-{de,es}` packs (the same pattern `plugins_page.go`/`permission_settings_page.go`/`my_reports_page.go` already use).

One pre-existing test needed updating for the response-shape change: `audit_page_test.go`'s `TestAuditPage_RepoErrorNeverLeaksRawErrorToBody` asserted the **entire trimmed body** equalled the translated string (the old bare-`LogAndLocalizedError` shape). Migrating to `httpx.RenderError` means the message is now a substring of a full HTML page, so the assertion changed to `strings.Contains` — the same pattern every other already-migrated page's tests already use (`plugin_settings_page_test.go`, `sync_api_test.go`, etc.). The no-raw-driver-error-leak half of the assertion is unchanged and, if anything, stricter now that it scans a larger body.

**Deliberately deferred, not an oversight:** `internal/pages/fiscal_device_page.go`'s 6 sites. That file had two commits land on it the day before this card was picked up (Turkey ÖKC/fiscal-device work, `universal-till#750` and its follow-ups, still actively being worked by a different pipeline lane at pick-up time) — touching it risked a merge collision with unrelated in-flight work for zero benefit this cycle. Follow-up card: **ut-docs#1786**. `#1458`'s "zero markers" acceptance criterion is therefore **not** met by this PR — this PR does not close `#1458`, only `#1663`'s own scoped slice of it.

## Independent review findings

**Verdict: SAFE TO MERGE.** No correctness, security, or behavior defects found. Two nit-level findings (stale doc comments in test files, pre-existing text that no longer matched which call path a test exercised) — both fixed in this same PR.

Verified independently, not just re-stated from the dev pass:
- **Conversion correctness**: all 30 sites keep their original status code, pass the live `err` (never `nil` where a variable was in scope, no swallowed error, no dropped/duplicated `return`).
- **Locale keys**: every reused key exists in all four shipped locales (`en`, `ar`, `fa`, `tr`); `common.error.server` is the right generic fit — the reviewer independently checked every touched file's own locale namespace for a better-fitting existing key and found none (all existing keys in those namespaces are form-validation strings, not "failed to load" ones).
- **Route context**: every converted site is genuinely a page-route handler, not an API/htmx-fragment endpoint that would now wrongly receive a full HTML document — checked directly (`GET /audit`, `/catalog`, `GET /tills`, `/journal/{receipt}`, `GET /sync-quarantine`, `GET /catalog/tax-codes`, `GET /refund/{receipt}`, `GET /invoices`, `GET /invoice/{display_no}`, `GET /plugins/{id}/settings`) and for the six closure-indirected sites (`renderPage`/`renderLocations`/`renderRegisters`/`renderPromotions`/`renderFiscalRegister`/`renderUsers`, each with exactly one caller) by tracing each closure back to its one registered `GET` route. Cross-checked against every `hx-get` target in `web/ui` — no converted route is used as an htmx-fragment target.
- **Imports**: `go build ./...` is definitive that no file lost its last `common.` usage.
- **Observability**: no regression — both the old and new call paths log at Error level for 5xx into the same `logging.Recent()` ring (`ADR-0018`'s Problems view); the 17 ex-`http.Error` sites actually *gain* server-side logging they never had before.
- **No other stale exact-body test**: grepped every `internal/pages/**/*_test.go` for an exact-body assertion tied to any of the converted keys; all neighbouring tests already used `strings.Contains`, confirmed empirically by a green `go test ./...` across the whole repo.
- **No secrets, no client/shop names** in the diff.
- **Scope discipline**: exactly the 16 source files + `audit_page_test.go`, nothing else.

Nits fixed: three doc comments in `audit_page_test.go`, `sync_api_test.go`, and `plugin_settings_page_test.go` still said a test's call path went through `common.LogAndLocalizedError`/"like catalog/handlers.go's sites" when that path had just moved to `httpx.RenderError` (or, for `sync_api_test.go`/`plugin_settings_page_test.go`, covered a *mix* of converted and not-yet-converted sites under one heading) — reworded to say which is which.

## Verified beyond automated tests

- `gofmt -l .` clean.
- `go build ./...`, `go vet ./...` clean.
- `golangci-lint run ./...` — 0 issues.
- `go test ./...` (whole repo) — every package green, no FAIL.
- `bash scripts/ci/guard-page-http-error.sh` — OK, and `grep -rn "page-error:allow.*1458"` confirms only `fiscal_device_page.go`'s 6 markers remain (the deliberate deferral above) plus the four unrelated, already-legitimate exceptions (`setup_page.go`'s pre-enrollment wizard, `order_tracking.go`'s anonymous customer surface ×2, `settings_page.go`'s `/api/` routes ×2) — untouched, correctly so.
- `bash scripts/ci/guard-i18n.sh`, `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-help-topics.sh`, `bash scripts/ci/guard-compliance-claims.sh` — all clean.
- Independent reviewer re-ran the specific fixed test (`TestAuditPage_RepoErrorNeverLeaksRawErrorToBody`) in isolation and confirmed the pass, then read it to confirm both halves of the assertion still hold meaning.

## Explicitly deferred / not closed by this PR

- `fiscal_device_page.go`'s 6 remaining `page-error:allow ut-docs#1458` sites — tracked as **ut-docs#1786**.
- `#1458` itself stays open until #1786 lands; this PR closes `#1663` only.

## Safe-to-merge

Yes. Independent review found no blockers; full local gate (build/vet/lint/test/all relevant guards) green after the two nit fixes.
