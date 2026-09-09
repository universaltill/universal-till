# Code review: fiscal-device confirm/unpair — translated, full-layout error responses (ut-docs#1814)

**Branch:** `fix/1814-fiscal-device-i18n-http-error`
**Reviewer:** independent fresh-context review (Sonnet), no visibility into Dev's reasoning.

## What shipped

`POST /api/fiscal-device/confirm` and `POST /api/fiscal-device/unpair` in
`internal/pages/fiscal_device_page.go` had 4 raw `http.Error(...)` call
sites — two per handler:

- 403 `"owner (admin) required"` — the `canPerform(d, r, "fiscal_tse_override")`
  gate (admin/super_admin only; confirmed against
  `internal/db/fiscal_permission_test.go`, which asserts this permission is
  seeded to exactly `{admin, super_admin}` and explicitly NOT `manager`,
  per ADR-0048 Decision 3).
- 404 `"not found"` — the `fiscalDeviceMarketActive` gate (deliberately
  generic, per the handler's own comments: the endpoint genuinely doesn't
  exist for a non-TR till; the 403 above already tells an unauthorized
  caller the route exists, so nothing is concealed by the 404's status
  code either way).

These 4 sites bypassed the base HTML layout (rail/nav/"Back to sale") that
every other error path on this page already went through, and were never
translated regardless of till locale — unlike this same pair of handlers'
existing 500 path, which already used
`httpx.RenderError(w, r, http.StatusInternalServerError, "fiscaldevice.error.server", err)`.
On a pinned Android kiosk a bare `http.Error` body is a dead end: no
browser chrome, no way back (ut-docs#1455's original finding for this
page's other error paths).

The fix migrates all 4 sites to `httpx.RenderError`, adding two locale
keys — `fiscaldevice.error.owner_required` and `fiscaldevice.error.not_found`
— to all 4 shipped locale files (`en`, `ar`, `fa`, `tr`). No change to
status codes, gate order, or the underlying permission/market logic —
text and rendering only. `TestFiscalDevicePage_EveryErrorRendersFullLayout`
(the established regression-test home for this exact defect class, per
ut-docs#1663/#1786) gained 4 new subtests: confirm/unpair ×
owner_required/not_found, each asserting both the full-layout wrapper and
the translated message text.

## Independent verification performed

Beyond reading the diff:

1. **TDD claim re-verified by hand**, in this isolated worktree (never on
   the shared/orchestrator checkout): reverted just the two lines in
   `internal/pages/fiscal_device_page.go` back to the raw `http.Error`
   calls (test file left untouched), rebuilt, and re-ran the 4 new
   subtests. All 4 failed with real assertion errors — not a compile
   error:
   ```
   fiscal_device_page_test.go:484: error response has no nav rail (bare body, dead end on a pinned kiosk):
       owner (admin) required
   fiscal_device_page_test.go:513: error response has no nav rail (bare body, dead end on a pinned kiosk):
       not found
   ```
   Restored the fix; all 4 (and the rest of the existing subtests in that
   `Test...` function) passed again. The TDD claim holds.
2. **Full gate run**, this worktree:
   - `gofmt -l .` — no output.
   - `go build ./...` — clean.
   - `go vet ./...` — clean.
   - `golangci-lint run ./internal/pages/...` — 0 issues.
   - `go test ./internal/pages/...` — all green (182s).
   - `go test ./...` (full suite) — all green (see below).
   - `bash scripts/ci/guard-i18n.sh` — pass (1528 template keys resolve;
     all locales match `en.json`; no hardcoded Go-side strings).
   - `bash scripts/ci/guard-page-http-error.sh` — pass: confirms this
     file now has zero bare `http.Error`/`LocalizedError`/
     `LogAndLocalizedError` calls in a page-route handler. This is a
     meaningful independent check, not just "the diff looks right" —
     the guard scans the actual post-fix file.
   - `bash scripts/ci/guard-data-access.sh` — pass (no SQL added, N/A
     but confirmed clean anyway).
   - `bash scripts/ci/guard-compliance-claims.sh` — pass.
3. **Read `httpx.RenderError`'s implementation directly** to check the
   specific risk flagged in the review brief — does it behave
   differently for a 404/403 vs. the already-used 500 path, or
   undermine the 404's "generic, not concealment" intent? It does not:
   `err` may be `nil` (both new call sites pass `nil`, same as an
   existing pattern elsewhere in this file for the no-`Settings`-store
   500 case); a `nil` err with status < 500 logs at `Info` level
   server-side only (`[page-error] METHOD PATH STATUS`, no body/message
   in the log line beyond the status), never reaches the response body,
   and every status (200s through 500s) goes through the exact same
   template/layout path — there's no branch that would make a 404 look
   or behave differently from any other status through this function.
   The response body only ever contains the resolved locale string via
   `T(locale, msgKey)`; nothing added here differentiates this 404 from
   a "real" 404 to a caller.
4. **Locale key parity and translation quality**: `guard-i18n.sh`
   confirms structural parity; read the actual ar/fa/tr strings and
   cross-checked them against this file's own established vocabulary
   for the same concept — `pos.toast.fiscal_tse_failing` /
   `refund.error.fiscal_tse_failing` already render "owner (admin)" as
   ar "المالك (المدير العام)", fa "مالک (ادمین)", tr "Mağaza sahibi
   (yönetici)". The new `owner_required` strings use exactly that same
   phrasing (not copy-pasted English, not machine-garbage, and
   consistent with the house translation already in the file) —
   confirmed by grep, not just read-through.
5. **Recurring bug classes**: no file writes in this diff at all
   (`grep -n "MkdirAll\|WriteFile\|os.Create\|paths\."` on the changed
   file returns nothing) — both classes this pipeline keeps finding are
   N/A here.
6. **Scope discipline**: `git show --stat` on the WIP commit lists
   exactly 6 files — the 2 handlers (1 file), the test file, and the 4
   locale files. No drive-by changes.
7. **Manual/help topic**: `web/help/{en,ar,fa,tr,de}/fiscal-device.md`
   exist and are claimed correctly (`guard-help-topics.sh` passes); none
   of them describe the owner-required or not-found error text (grepped
   for "owner"/"admin"/"not found"/"error" in the `en` topic — zero
   hits), so there is no manual prose that goes stale here. This is an
   error-page text/rendering fix on an existing admin-only permission
   gate and an existing generic 404, not a new page, workflow, or
   operator-visible feature — no help-topic update needed. Judgment
   verified, not just trusted.
8. **Tried to break it**: checked whether the not-found message text
   itself might be confusing given it's genuinely context-free ("Not
   found" / "Bulunamadı" / etc.) — but this matches the pre-existing
   English text exactly (no wording regression) and the code comments
   make the terseness a deliberate choice already reviewed under
   ut-docs#1750, not something this card's scope is to improve. Checked
   for any nil-pointer/ordering issue the diff's presence might expose
   differently in the two handlers — none: gate order (`requireManager`
   → `canPerform` → `fiscalDeviceMarketActive` → `d.Settings` nil-check
   → `Settings.Set`) is completely unchanged, only the rendering call at
   two existing branches changed.

## Full test suite

`go test ./...` run to completion in this worktree: **all packages pass**,
no failures, no build errors introduced by this change.

## Findings

No blocking findings. One **explicitly deferred, out-of-scope** item, and
one **cross-repo note** surfaced during verification:

- **Deferred (already known non-goal, confirmed still true and untouched
  by this diff):** the identical raw `"owner (admin) required"` /
  `http.Error` pattern still exists at:
  - `internal/pages/fiscal_api.go`'s `respondFiscalError` — but this is
    an `/api/*` JSON-envelope route (`{ "data": null, "error": … }`),
    not a page route; per `httpx.RenderError`'s own doc comment, API/htmx
    routes are supposed to keep using `LocalizedError`/
    `LogAndLocalizedError`, not `RenderError`, so this isn't even the
    same fix shape — separate follow-up, not this card's scope.
  - `internal/pages/fiscal_country_change.go:105`
  - `internal/pages/settings_page.go:2382` and `:2387`
  All three confirmed still present, unmodified, via direct grep on this
  worktree. Not fixed here — per the task brief, no Backlog card created
  from this session (orchestrator's job, has GitHub API access this
  worktree doesn't).
- **Cross-repo note for whoever merges this PR** (`reviewer` SKILL's
  lang-pack-drift check): the two new keys
  (`fiscaldevice.error.owner_required`, `fiscaldevice.error.not_found`)
  are **not yet present** in either `ut-plugin-language-de`'s
  `locales/de.json` or `ut-plugin-language-es`'s `locales/es.json` on
  their own `main` branches (checked directly — 0 matches in both).
  `lang-pack-drift` is advisory-only on this PR (it only touches
  `en.json` plus the in-repo `ar`/`fa`/`tr` locales, not the external
  packs), so this diff's own CI will stay green — but per
  `universal-till/CLAUDE.md`, `lang-pack-drift` is **blocking on push to
  `main`**, so merging this as-is will turn `main` red until follow-up
  PRs land the same 2 keys in both pack repos. Not this card's scope to
  fix (separate repos), but worth landing those two small follow-ups
  before or immediately after this merges rather than being surprised by
  a red `main`.

## Verdict

**Safe to merge.** The change is exactly what it claims to be: a
text/rendering-only migration of 4 call sites to the page's own
established `httpx.RenderError` pattern, with correct, house-consistent
translations, a genuine (independently re-verified) regression test, and
a clean full gate. No behavior, security-gate, or status-code change.
The one real consequence to flag is the language-pack drift noted above,
which is an operational/merge-sequencing note, not a defect in this
diff.
