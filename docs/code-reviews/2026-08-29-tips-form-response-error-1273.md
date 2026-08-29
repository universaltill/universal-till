# Code review — /reports tips form silently swallows server errors (ut-docs#1273)

- **Date:** 2026-08-29
- **Branch:** `fix/1273-tips-form-response-error`
- **Reviewer:** independent reviewer (fresh-context Sonnet, this pipeline's
  `complexity:easy` review tier — "different model" relaxes to "different
  instance" per the `reviewer` skill), isolated worktree.
- **Verdict: SAFE TO MERGE.** No blocking findings. Two non-blocking notes,
  both accepted as out-of-scope follow-up rather than fixed here (reasoning
  below).

## What shipped

Found by independent review of ut-docs#1272's fix: the tips-allocation
record-payout form on `/reports`' tips tab
(`web/ui/partials/reports_tab_tips.html`, POSTs to
`/api/reports/worker-allocations`) had no `htmx:responseError` handler.
`http.Error`/`common.LogAndLocalizedError` (`internal/pages/reports_page.go`)
return non-2xx plain-text bodies, and htmx never swaps a non-2xx response
into the target by default — so **any** validation (400) or save (500)
error was completely silent: the "Record payout" button appeared to do
nothing.

The fix:

- Gave the form a stable `id="tips-record-form"`.
- Added a dedicated `#tips-result` element (`.muted` + `aria-live="polite"`,
  the same idiom `#shift-result`/`#refund-msg`/`#eod-range-msg` already use
  elsewhere in this codebase — no new design tokens).
- Bound a `htmx:responseError` listener directly on the form node (not
  `document.body`), writing `ev.detail.xhr.responseText` into `#tips-result`
  via `.textContent` (plain text, matching `refund.html`'s convention —
  this endpoint's errors are plain `http.Error`/`LocalizedError` text, not
  pre-rendered HTML like `shifts.html`'s).

**Why not just retarget the form onto a small result div, `shifts.html`-style?**
This form's `hx-target` is deliberately the shared `#report-tab-panel` (a
full tab re-render on success — see `renderTipsTab` in `reports_page.go`,
which re-renders the tab so the operator immediately sees the updated
totals/table), unlike `shifts.html`'s forms which always target their own
small result div. So the closest actual precedent is
`web/ui/partials/buttons_admin.html`: a big shared success target, with
errors routed to a separate small dedicated element instead. That's the
shape this fix follows.

**Repeated-swap safety.** `reports_tab_tips.html` is itself torn down and
re-inserted into `#report-tab-panel` every time the tab (re-)loads —
exactly the same "partial gets replaced, script tag re-runs" situation
`reports_tab_eod.html` already has. Binding the listener to the form
element itself (rather than `document.body`) means the old node — and its
listener — is discarded with the old subtree on every successful swap,
instead of accumulating duplicate `document.body` listeners across tab
switches.

## Independent review — what was checked

- **Gates, all real output, all green:** `gofmt -l .` (empty), `go build
  ./...`, `go vet ./...`, `go test ./internal/pages/...` (full package,
  including `internal/pages/catalog` and `internal/pages/common`),
  `guard-i18n.sh`, `guard-data-access.sh`, `guard-docs-shots.sh`.
- **TDD claim independently re-verified, not taken on faith:** with only
  `web/ui/partials/reports_tab_tips.html` reverted to `HEAD~1` (test file
  untouched), `TestReportsPage_TipsTabRecordFormWiresResponseErrorHandler`
  fails for real (`expected the record-payout form to carry a stable id for
  the error handler to bind to, got: ...` — the pre-fix template). Restoring
  the file returns it to green. The test is a real wiring pin (asserts the
  rendered form id, result-element id, and the `htmx:responseError` string
  are present) — it doesn't execute the JS itself, an honest scope
  limitation, not a false claim.
- **htmx event-model correctness**, checked against the codebase's own
  prior art rather than assumed: `base.html` sets no
  `allowScriptTags:false` in `htmx.config`, so htmx's default inline-script
  execution on swap applies (same mechanism `reports_tab_eod.html`/
  `buttons_admin.html` already rely on). `htmx:responseError` fires with
  `detail.elt` as the triggering element — since `hx-post` lives directly
  on this `<form>`, binding on the form node itself is correct, no
  bubbling-order subtlety.
- **i18n:** raw English validation strings (`"date must be YYYY-MM-DD"`,
  `"amount must be a positive integer (minor units)"`, etc.) becoming
  visible was checked against `refund_page.go`'s identical
  raw-`http.Error`-for-4xx convention (`"sale not found"`, `"manager PIN
  required"`, `"select at least one item to refund"`, all surfaced verbatim
  by `refund.html`'s own handler) — consistent with existing convention,
  not a new gap this fix introduced. The 500 path was already localized
  (`LogAndLocalizedError` / `reports.tips.error.save`). `guard-i18n.sh`
  agrees.
- **UX:** no new hardcoded colors/spacing, no `left`/`right` literals, no
  new modal — reuses the existing `.muted`/`aria-live` idiom outright.
- **Screenshots:** `make docs-shots` regenerated (required — this diff
  touches `web/ui/**`, and `guard-docs-shots.sh` hashes the whole surface).
  `manifest.json`'s `surface_sha256` changed as expected; the 4 touched PNGs
  (`en/sell`, `fa/sell`, `ar/translations`, `fa/translations`) differ by a
  handful of bytes each — normal PNG-encoder jitter from re-running the
  suite, confirmed visually identical, not a content change. No manual-topic
  prose needed updating (no new visible feature — an existing silent failure
  became visible, no new screen/flow).
- No real client/shop name or secret-shaped literal in the diff. No
  file-write/`os.MkdirAll`/`paths.Data` concern (UI-only diff).

## Findings — both non-blocking, neither fixed here

1. `msg.textContent = '✗ ' + (ev.detail.xhr.responseText || '')` falls back
   to an empty string, so a genuinely empty response body would show just
   "✗ " — matches `shifts.html`'s existing `|| ''` convention (not
   `refund.html`'s `|| 'error'`), so not a regression, just an
   already-inconsistent pair of precedents. Not worth resolving inside this
   ticket's scope.
2. The fix wires `htmx:responseError` (non-2xx) but not `htmx:sendError`
   (a network-level drop with no response at all — a real scenario on this
   offline-first LAN product). `buttons_admin.html` already handles both.
   Genuinely out of this ticket's scope (which was specifically about the
   missing `responseError` handler) and shared by `refund.html`/
   `shifts.html` too, not something this diff introduced — filed as
   **ut-docs#1287** to fix all three in one pass rather than partially here.

## Manual verification beyond automated tests

Drove the real running app (`e2e/run-till.sh`, `UT_AUTH=off`) with a headless
Chromium (`/opt/pw-browsers`), through the actual `/reports` → tips tab →
zero-amount submission flow:

- **en, light theme, 1024×600:** error now renders as `✗ amount must be a
  positive integer (minor units)` under the form, correctly positioned,
  nothing overlapping/cut off.
- **fa, RTL:** same error text (untranslated, matching existing convention
  — see i18n note above) renders correctly right-to-left with no layout
  breakage.
- **dark theme (`data-theme="dark"`):** same rendering; not independently
  re-verified beyond reuse of the proven `.muted` utility class shared with
  `#shift-result`/`#refund-msg`/`#eod-range-msg`, all already dark-theme-safe.
- **Happy path unaffected:** a valid ¥5.00/£5.00 submission still
  re-renders the whole tab with the updated total in place (`£5.00` visible
  post-submit) — confirms the fix didn't regress the existing full-tab
  refresh behavior.

Local server killed after verification; no leftover process.
