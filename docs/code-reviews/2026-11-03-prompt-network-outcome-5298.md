# Review: a prompt's lost connection says the action may or may not have run (ut-docs#5298)

Branch `fix/5298-prompt-network-outcome`. Follow-up to ut-docs#5291. Phases ran
as separate agents: BA, Architect + UX (Opus), Dev (Sonnet), Tester, independent
reviewer (Opus), Dev fix round (Sonnet); the orchestrator re-verified the fixes.
Card resized easy → medium at BA (three paths, seven callers, a new key).

## What shipped

- New key `common.error.outcome_unknown` ("The connection dropped before the
  till replied, so this may or may not have been done. Check before you try
  again."), `en.meta.json` entry (`review: standard`), en/ar/fa/tr; reaches JS as
  `data-msg-outcome-unknown` on `web/ui/partials/pos_alert.html`.
- `web/public/app.js`:
  - `htmx:sendError` from a confirm/PIN prompt form (`data-after-request~="elevation-done"`)
    shows the outcome text in `#pos-alert` as a **sticky** message: an
    unrelated successful poll's self-heal and a later generic network alert
    (htmx or sale-grid) don't clear or replace it; ✕, a server error or an
    explicit new message do. When shown, it is scrolled into view
    (`block: 'nearest'`, no smooth scroll).
  - `utPostWithElevation` keeps its signature; its promise now settles when the
    whole flow ends. A network failure on the first send or on the PIN retry
    rejects with `e.utNetworkError = true` and calls neither `onDone` nor
    `onCancel` (the retry failure used to look like a Cancel). 401 resolves
    before redirecting. `window.utElevationErrorText(e)` maps a tagged error to
    the outcome text.
  - `postButtons` (tile reorder, idempotent) keeps the generic network alert
    and grid refetch; its inner promise now settles on every path, so the
    sell-screen-watch hold is released (a first-send network failure used to be
    an unhandled rejection).
- Callers: 6 in `web/ui/pages/settings.html`, 1 in
  `web/ui/partials/reports_tab_eod.html` show `utElevationErrorText(e)` instead
  of the raw "TypeError: Failed to fetch".
- No replay: no retry button, the PIN dialog is never re-opened, nothing is re-sent.
- Help: one sentence in step 4 of `web/help/{en,de,tr,ar,fa}/elevation.md`;
  `docs/sale-screen-notifications.md` §2 documents the sticky outcome alert.
- Tests: `internal/httpx/pos_alert_test.go` (new attribute); e2e
  `prompt-network-outcome-5298.spec.ts` (new), `prompt-inflight-fetch-5291`
  (retry/first-send abort → tagged rejection, no onCancel; onDone throw
  untagged), `prompt-inflight-cancel-5288` (text, survives a successful
  request, survives a generic network failure, ✕ hides, in viewport),
  `prompt-inflight-pin-5288`.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| T-B1 | Blocker (branch) | Tester's temporary screenshot spec `zz-tester-shots-5298.spec.ts` got swept into the first push (it changes `sync.primary_url`) | Removed in the fix commit |
| T-D1 | High | Outcome alert off-screen after a prompt on a scrolled Settings section (1024x600, 360x740) — `#pos-alert` sits above `<main>` | Fixed: `scrollIntoView({block:'nearest'})`; e2e `toBeInViewport()` (failed before, passes now) |
| R-M1 | Medium | Self-heal check didn't await `htmx.ajax` — possible false pass | Fixed |
| R-M2 | Medium | No test that a later generic network failure leaves the outcome text | Fixed (htmx path test); sale-grid path covered by code guard only |
| R-L1 | Low | Sale-grid `showAlert('', 'network')` replaced the sticky outcome text | Fixed |
| R-L4 | Low | `postButtons` inner promise never settled on 401 | Fixed (`.then(resolve, reject)`) |
| R-L2 | Low | While the outcome text shows, a new failed action gets no fresh announcement | Accepted (Architect): the text already says to check |
| R-L3 | Low | A background poll's 5xx replaces the outcome text | By design — never stuck |
| R-L5 | Low | Two concurrent `utPostWithElevation` calls can leave the first unsettled | Pre-existing, unchanged |
| R-N1 | Nit | ar used "النظام" for the till | Fixed → "الصندوق" |
| — | Note | An `onDone` that throws on the retry leg now reaches the caller's `.catch` (it was swallowed) | Accepted, intended |

## Verification

- False-pass check (Tester): with the new `app.js` stashed, the 6 new/changed
  tests in the four specs fail; restored, all 14 pass. Fix round: 17 pass
  (incl. `htmx-senderror` regressions); the D1 viewport assertion failed first.
- `go test ./...` (103 packages ok; `internal/plugins` 796 s alone),
  `golangci-lint` 0 issues, gofmt clean; guards i18n, i18n-meta, help-topics,
  help-drift, help-terms, help-breadcrumbs, no-showmodal, rtl-logical-css,
  compliance-claims pass.
- Driven run (Tester, Playwright + Chromium): data-reset first POST aborted →
  message in the card's own slot, en and fa, 1024x600 and 360x740 — legible,
  no overlap, no horizontal scroll, RTL mirrored; `#pos-alert` outcome text in
  fa with ✕ on the start side. Not checked: dark theme, de/tr wording on screen.
- Pack follow-ups: `i18n/5298-outcome-unknown` in ut-plugin-language-de/es/pt,
  `check-key-drift.sh` clean against this branch's `en.json`.

Pre-flight: web/help topic updated (5 locales; no screenshot change — transient state); docs/sale-screen-notifications.md updated; reference/ n/a (no documented contract changed); README n/a; en.json key → en.meta.json + every web/locales/*.json + de/es/pt pack PRs; review record this file; follow-ups → ut-docs#5335 (prompt request timeout).
