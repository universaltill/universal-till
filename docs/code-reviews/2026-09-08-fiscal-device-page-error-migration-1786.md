# ut-docs#1786 — fiscal_device_page.go's last 6 page-error:allow sites migrated to httpx.RenderError

Date: 2026-09-08
Branch: `fix/1786-fiscal-device-page-error-migration`
Review: one independent subagent, fresh context, Sonnet (`complexity:easy`).

## The gap

`internal/pages/fiscal_device_page.go` (the Turkey ÖKC fiscal-device status
page) had 6 error-response call sites still using `common.LocalizedError`/
`common.LogAndLocalizedError` instead of `httpx.RenderError`, each marked
`// page-error:allow ... tracked in ut-docs#1458`. A GET-page handler that
answers with the old helpers replaces the WebView's entire document with a
bare, rail-less body — on a pinned Android kiosk there is no browser chrome
and no Android nav bar either, so that is a dead end with no way back.
`#1663` migrated the other ~30 sites `#1458` tracked but deliberately
deferred this file (it was under fast-moving concurrent Turkey ÖKC
development at the time) — this card is that last slice.

## The fix

Swapped all 6 sites, same status codes and message keys, no logic change:

- Line 88 (the shared `requireManager` gate every handler goes through
  first): `common.LocalizedError(..., 403, "common.error.manager_or_admin_required")`
  → `httpx.RenderError(..., 403, "common.error.manager_or_admin_required", nil)`.
- Lines 116, 199, 239 (`common.LogAndLocalizedError(..., 500,
  "fiscaldevice.error.server", "fiscal_device", err)`) → `httpx.RenderError(...,
  500, "fiscaldevice.error.server", err)`.
- Lines 195, 235 (`common.LocalizedError(..., 500, "fiscaldevice.error.server")`,
  the `d.Settings == nil` guards, no `err` to pass) → `httpx.RenderError(...,
  500, "fiscaldevice.error.server", nil)`.

All 6 `page-error:allow ut-docs#1458` markers removed. Confirmed
`grep -rn "page-error:allow.*1458" --include="*.go" .` now returns nothing
anywhere in the repo — this closes #1458 for good, as the card's own
acceptance criteria requires (`#1663` explicitly did not close it).

No i18n/locale-key changes needed — every key already existed and is
reused as-is, matching the card's own acceptance criteria.

## Tests (TDD)

None of these 6 sites had direct test coverage before (that's precisely
why they were still flagged `page-error:allow` rather than caught by an
existing assertion). Added
`TestFiscalDevicePage_EveryErrorRendersFullLayout` with one subtest per
site, each asserting both the status code and a full-layout body
(`class="nav"` present) rather than a bare one:

- **non-manager GET** — site 1 (line 88).
- **GET with a failing repo read** — site 2 (line 116): closes the test DB
  before a manager GET so `posRepo.LatestFiscalDeviceReceipt` fails.
- **confirm/unpair with no Settings store** — sites 3 & 5 (lines 195, 235):
  sets `d.Settings = nil` directly, hitting the guard in production code
  (not a mock).
- **confirm/unpair with a failing Settings.Set** — sites 4 & 6 (lines 199,
  239): `DROP TABLE settings` before the request, leaving the
  `plugins`/`plugin_catalog` tables the market-active gate reads intact so
  the request reaches the `Settings.Set` call instead of 404ing earlier.

Verified TDD twice, independently: I ran the new test against the
pre-fix code first (all 6 subtests failed with the expected bare-body
symptom) before applying the fix, and the reviewer separately reverted
just the handler file post-fix and reproduced the same 6 failures, then
restored and confirmed all 6 pass again.

## Independent review

Sonnet fresh-context subagent, isolated worktree. **Verdict: SAFE TO
MERGE — no findings that block merge.**

Verified: `gofmt -l` clean, `go build ./...` clean, `go vet
./internal/pages/...` clean, `golangci-lint run ./internal/pages/...` 0
issues, `go test ./internal/pages/... -run TestFiscalDevicePage -v` all
green, `bash scripts/ci/guard-page-http-error.sh` OK, zero remaining
`page-error:allow ut-docs#1458` markers, no `.html`/`.css`/`web/help/`
files touched (correctly not needed — same rendered template, Go-only
change), no client/shop names or secret-shaped literals in test data.

Read `internal/httpx/render_error.go` and `internal/pages/common/errors.go`
side by side against all 6 call sites and confirmed behavioral parity:
status codes and message keys preserved exactly; `RenderError`'s own
`err`-aware logging (`[page-error] METHOD PATH STATUS: err`, feeding
`logging.Recent()` for 5xx) covers what `LogAndLocalizedError`'s
`"fiscal_device"` log-tag logging did, under a different prefix — nothing
else in the repo greps for that literal tag, so it's a cosmetic
log-format change only. One INFO-level note: the two former plain
`common.LocalizedError` sites (`d.Settings == nil`, no `err` argument)
previously logged nothing server-side at all; `RenderError` now logs them
at Error level too, since status ≥ 500. Called out as a strict
improvement in the direction #1455 (the original "not logged anywhere"
finding) already wanted — not a regression.

Independently re-verified the TDD claim (see above) and scrutinized the
DB-close/`DROP TABLE` test tricks for fragility — found clean: `Close()` is
idempotent so the later `t.Cleanup` double-close is harmless, dropping only
`settings` leaves the market-active gate's own tables intact so the request
reaches the intended call site rather than 404ing early (confirmed via the
log lines), and `d.Settings = nil` exercises the real production guard
rather than a mock. `t.Setenv("UT_AUTH", "off")` matches this package's
established pattern elsewhere.

One out-of-scope observation, not a defect in this diff: the same file's
`POST /api/fiscal-device/{confirm,unpair}` handlers still use 4 bare
`http.Error` calls (never marked `page-error:allow`, since
`guard-page-http-error.sh` only classifies GET page routes, not `/api/*`).
Correctly out of scope for this card — filed as ut-docs#1814 for a
separate i18n/consistency pass.

## Verified beyond automated tests

- `go build ./...` (whole-repo compile, not just the touched package).
- Confirmed no leftover `common.LocalizedError`/`LogAndLocalizedError`
  call or `page-error:allow` comment anywhere in
  `internal/pages/fiscal_device_page.go`, and no such marker referencing
  `#1458` anywhere else in the repo.
- `internal/pages` full package suite (`go test ./internal/pages/...`,
  matching this repo's own CI step for this package) green, not just the
  new/touched tests.

## Deferred / explicitly out of scope

- ut-docs#1814 (the 4 bare `http.Error` calls on the `/api/*` endpoints in
  this same file) — separate concern, separate card.
