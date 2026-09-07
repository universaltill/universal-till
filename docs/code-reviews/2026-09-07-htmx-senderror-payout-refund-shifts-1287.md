# htmx:sendError handling for record-payout, refund and shift forms (ut-docs#1287)

## What shipped

`refund.html`'s refund form, `shifts.html`'s open/close/adjustment forms,
and `reports_tab_tips.html`'s record-payout form each already handled
`htmx:responseError` (a non-2xx HTTP response) but not `htmx:sendError` —
the event htmx fires instead when a request never gets a response at all
(e.g. this back-office/cashier device dropping off the shop LAN mid-tap).
Since this product is offline-first, a LAN drop mid-request is a routine,
expected event, not a corner case, and without a handler the button was a
silent dead tap: no server fragment to swap in, and nothing client-side
said so either.

Each of the three files gets a new `htmx:sendError` listener, added next
to its own existing `htmx:responseError` handler and mirroring that
handler's own shape (not a literal copy of one single pattern — each
file's existing convention differs slightly):

- `reports_tab_tips.html` — form-scoped listener on `#tips-record-form`,
  sets `#tips-result` to `'✗ ' + T.serverError` (adds the page-local
  `var T = { serverError: "{{ T "designer.error.server" }}" }` lookup
  object, the pattern `CLAUDE.md` calls out for inline `<script>` strings).
- `refund.html` — `document.body`-scoped listener (matching its existing
  `responseError` handler's scope), sets `#refund-msg` to
  `'✗ ' + "{{ T "designer.error.server" }}"`.
- `shifts.html` — `document.body`-scoped listener guarded by
  `ev.detail.target.id === 'shift-result'` (identical guard shape to its
  existing `responseError` handler, which all three open/close/adjustment
  forms share via one target), sets the target's `innerHTML` to a styled
  `<div class='error'>…</div>` fragment mirroring the server's own
  `respondShiftError`/`respondCloseError`/`respondAdjustmentError` shape.

No new i18n key: all three reuse the existing shared key
`designer.error.server` (`web/locales/en.json`), the same key
`web/ui/partials/buttons_admin.html`'s own already-shipped `htmx:sendError`
handler (ut-docs#1697) uses — that handler is the precedent this change
follows.

## Tests (TDD)

New Playwright e2e test: `e2e/tests/htmx-senderror-1287.spec.ts`, covering
the record-payout form (`reports_tab_tips.html`) — the cheapest of the
three to set up (no prior sale/shift-open state needed). It drives a real
Chromium session, aborts the `POST /api/reports/worker-allocations`
request (`page.route(...).abort()`, a genuine `net::ERR_FAILED`), and
asserts `#tips-result` shows the localized fallback text rather than
staying empty.

Confirmed to fail pre-fix and pass post-fix (verified twice — once by Dev,
independently re-verified by Reviewer in an isolated worktree): reverting
`reports_tab_tips.html`'s change alone reproduces the exact "dead button"
symptom (`#tips-result` stays empty, `expect(...).not.toBeEmpty()` times
out); restoring the fix passes again.

htmx.min.js's own internal error logger calls
`console.error("htmx:afterRequest")` and `console.error("htmx:sendError")`
as a side effect of *any* network-level request failure — confirmed by
reading the vendored source (`fe()`'s trigger wrapper stamps the event
name itself into `detail.error`, which htmx's internal logger then
prints). This is pre-existing htmx behavior, not a bug introduced here;
the test's `watchConsole` exemption regex
(`/^Failed to load resource:.*ERR_FAILED|^htmx:(afterRequest|sendError)$/`)
is fully anchored so it only swallows those two exact strings, not any
unrelated real console error.

## Review

Independently reviewed by a fresh-context Sonnet subagent (`complexity:easy`
per the model-routing rule), worktree-isolated. **Verdict: safe to merge,
no blocking findings.**

Verified beyond automated tests:
- `ev.detail.target` is populated identically for `htmx:sendError` as for
  `htmx:responseError` — traced through `web/public/vendor/htmx.min.js`:
  both events fire from the same request-config object, built once per
  request with the resolved `hx-target` element before the XHR starts. So
  `shifts.html`'s mirrored `target.id === 'shift-result'` guard is sound,
  not an unverified assumption carried over from the `responseError`
  sibling.
- `go test ./internal/pages/...` passing confirms the Go `html/template`
  syntax of the new `{{ T "designer.error.server" }}` calls is valid in
  all three files (`refund_page_test.go` and `shifts_page_test.go` render
  `refund.html`/`shifts.html` directly; a broken template would fail
  there).
- Full gate: `gofmt -l .` (clean), `go build ./...`, `go vet ./...`,
  `go test ./internal/pages/...` — all green. Guards:
  `guard-i18n.sh`, `guard-compliance-claims.sh`, `guard-htmx-loaded.sh`,
  `guard-e2e-fixtures-import.sh` — all pass.
- No new i18n key, no locale drift, no hardcoded user-facing string, no
  RTL/layout change (diff touches no CSS/positioning), no real client/shop
  name anywhere in the diff.
- `web/help/` update: judged not required — this is an error-handling fix
  on already-documented forms/pages (no new page, route or step), and the
  only user-visible change is that a rare "LAN dropped mid-tap" failure
  now shows the same generic error message these forms' existing
  `responseError` handlers already show for other failures, rather than
  staying silent. Matches the visibility level of the undocumented
  `buttons_admin.html` precedent this change mirrors.

**One non-blocking observation, deliberately not fixed here:** only the
tips form has e2e coverage; `refund.html`'s and `shifts.html`'s new
handlers are exercised by the passing Go template-render tests (proving
the template syntax) but not by a browser-level test, so a typo in the
inline JS itself (not the Go template wrapper) wouldn't be caught by CI.
Accepted as in-scope for this card per its own acceptance criteria
("regression test or driven-run evidence... for at least one of the three
forms") and because all three handlers are the same few lines added in
the same change, following the identical, already-proven pattern.
Follow-up: ut-docs#1707 (extend e2e coverage to refund.html/shifts.html's
`sendError` handlers).

## Safe-to-merge verdict

**Yes.** No blocking findings from independent review; full gate green;
TDD claim independently re-verified end to end.
