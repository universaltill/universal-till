# Code review — bug-report panel persists across pages (ut-docs#2342)

**Date:** 2026-09-17 · **Lane:** local · **Branch:** `fix/2342-bugreport-panel-persists`
**Card:** ut-docs#2342 (p2, `source:user`, `complexity:medium`) — product owner,
2026-09-16: "when I click the bug report button, and press the recording
button, if I change the page, it closes the bug report window. It should stay
there and record all the moves, or I should be able to go to different pages
and add multiple screenshots and then send or cancel and close."

## What shipped

BA finding first: the card's premise was already half-overtaken. Since
v0.18.0 (ut-docs#2224, ADR-0098) the panel is rendered *outside* `#ut-page`,
so a boosted rail navigation keeps the DOM, a running `MediaRecorder` and the
screenshots alive — the owner saw the bug on v0.16.0. What still destroyed the
draft was every **full document load**: a form POST/redirect, a language or
theme change, a self-update, JS `location.assign`, and the panel's own
`/my-reports` link. Those are what this change covers.

- `web/public/bugreport-draft.js` (new): the draft store. `sessionStorage`
  for the small meta `{id, open, note, pos}` (tab-scoped on purpose — a
  draft dies with the till's browser session); IndexedDB `ut-bugreport` /
  `blobs` for screenshots, finished recordings and in-progress recording
  chunks, keyed `<draftId>:<kind>[:<seq>]`. Stale-id blobs are purged at
  boot. Every call is best-effort (no IndexedDB → pre-#2342 behaviour).
- `web/ui/partials/bugreport_panel.html`: recorders run with
  `start(1000)` timeslices and persist every chunk as it lands, so a full
  load mid-recording loses ≤1 s; the next document reassembles orphan
  chunks, previews them and says the page reloaded
  (`issuereport.recording_kept_after_reload`). New **Discard** button
  (confirm → drop note/shots/recordings → close; waits for the async
  recorder stop and never re-persists its output). ✕ hides but keeps the
  draft; **Send** success clears the draft and keeps the panel open. Chip
  `aria-expanded` resynced on `htmx:afterSettle`. 12-screenshot cap.
  Open/closed state replaces the old `ut-bugreport-dismissed` key.
- `web/ui/layouts/base.html`: loads the store (`defer`); the panel boots
  from `UT.ready`.
- i18n: `issuereport.discard`, `discard_confirm`, `recording_across_pages`,
  `recording_kept_after_reload`, `screenshot_limit` in en/ar/fa/tr;
  `issuereport.nav_ends_recording` retired (its text was now false).
  de/es packs: follow-up PRs by this lane after core merges.
- Help: `web/help/*/bug-reporting.md` (5 locales) — steps 4 and the ✕
  paragraph rewritten; `make docs-shots` regenerated on the merged tree.
- `web/public/app.css`: Discard button in the head bar.
- e2e: `e2e/tests/bugreport-panel-persists-2342.spec.ts` — 9 tests.

## TDD evidence

Spec written first; against the pre-fix tree **4 of 6 failed** (the four
full-load cases; the two boosted-navigation cases pass on the ADR-0098
baseline and stay as regression guards). After the fix 6/6. The three specs
added for the review's findings 2/3/4 were run against the pre-review
snapshot (`17b1f20b`): **3 failed**, then 3 passed with the fixes. The
independent reviewer repeated the revert→run→restore on its own worktree
with the same result (4 failed / 2 passed → 6 passed).

## Independent review (Fable, isolated worktree, model ≠ author)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | blocker | `guard-docs-shots.sh` red — help topics changed without regenerated shots | **Fixed**: `make docs-shots` on the merged tree (124 shots + manifest) |
| 2 | should-fix | Panel opened by `/report-issue` was treated as dismissed on the first store write (`ensure()` defaults `open:false`) → hidden on the next full load though ✕ was never pressed | **Fixed**: server-open boot path records `open:true`; spec added |
| 3 | should-fix | Restore preferred an older *finished* recording over a newer *interrupted* one, and the blob setter then deleted the newer chunks — silent loss | **Fixed**: recorder start deletes the finished blob; restore prefers chunks; spec added |
| 4 | should-fix | Restored dragged position had no ut-docs#395 resize guard → strandable off-screen after a viewport shrink | **Fixed**: `armResizeGuard()` at boot when restoring `pos`; reclamp on the server-open path; spec added |
| 5 | should-fix | Object URLs never revoked except on thumb ✕ — a session-long document (ADR-0098) pins every recording/PNG | **Fixed**: previews revoke on overwrite and on reset; thumbs revoke on reset |
| 6 | nit | 12 shots + 60 s video can exceed the 32 MiB POST cap → generic save error | Accepted: draft is kept, no data loss; noted for a follow-up if it bites |
| 7 | nit | `keys(kind)` prefix could match `screen-chunk` from `screen` | **Fixed**: prefix terminated with `:` |
| 8 | nit | `moveTo` wrote sessionStorage on every pointermove | **Fixed**: persisted once per drag end / reclamp |
| 9 | nit | restore re-`put` the finished blob it had just read | **Fixed**: `persist` flag on the blob setters |
| 10 | nit | Discard/Send inside the first ms after open can race `restoreDraft` | Accepted (sub-frame window; `discardPending` covers the recorder side) |
| 11 | nit | `window.confirm` dependency for Discard | Accepted: existing precedent (`plugins_store.html`, `tables.html`); verified on the tablet's Chrome |
| 12 | nit | "12" hardcoded in strings and JS separately | Accepted |
| 13 | nit | Two tabs on one till: the second tab's `purgeStale` drops the first's blobs | Accepted; kiosk is single-tab — documented in the store's header |

Reviewer confirmed: script ordering (`defer` store → `UT.ready`) correct;
Discard-while-recording handled; store calls ordered on one `dbPromise`;
chunk count bounded by the 60 s/120 s timers; APIs safe on WebKitGTK 2.52 and
2017+ Android WebView; logical-only CSS; no modal; no network dependency;
no secret-shaped or client-name literal; retired key has no remaining
reference; existing `bugreport-panel.spec.ts` byte-identical and still
green; help prose true once finding 2 was fixed. No Go change → the
MkdirAll / `paths.Data` classes do not apply.

## Verified beyond the automated tests

- **Real device — TECLAST tablet, Chrome 153 / Android 10**, driven over
  CDP against a Mac-served build: draft (note) survived the `/my-reports`
  full load with the panel open; survived a boosted nav to `/menu` with the
  chip `aria-expanded=true`; Discard closed it; a reload stayed closed and
  empty. Touch itself (drag) was not re-verified on hardware this cycle —
  the drag handler is unchanged apart from persisting the end position.
- Screenshots looked at: 1024×600 en/fa/tr (LTR + RTL, Discard fits in all
  four core locales; the Turkish note wraps cleanly), and 360×740 — which
  surfaced a **pre-existing** phone-width defect (the panel opens under the
  three-row top bar; CSS untouched here) → filed **ut-docs#2364** (p3).
- Not verified: the Pi 5 (WebKitGTK) and the Android *app* WebView (its
  `captureScreenshot` bridge is the exact code path the tests fake).

## Gate

`gofmt` clean · `go build ./...` · `go test ./internal/pages/` ·
`guard-i18n`, `guard-docs-shots`, `guard-help-topics`, `guard-help-drift`,
`guard-compliance-claims`, `guard-emoji-font`, `guard-htmx-loaded`,
`guard-e2e-fixtures-import`, `guard-autofill-suppression` all ✓ ·
Playwright: `persistent-shell-2224` + `page-transitions-2223` +
`bugreport-panel` + `bugreport-panel-persists-2342` → **44 passed** on the
merged tree. (One earlier run showed 3 `ERR_CONNECTION_REFUSED` failures —
a port collision with a background run, the known shared-ports trap; the
clean rerun is the result.)

## Verdict

Safe to merge. Deferred: #6 (client-side size pre-check), #10, #12, #13 as
noted; ut-docs#2364 for the 360px overlap.
