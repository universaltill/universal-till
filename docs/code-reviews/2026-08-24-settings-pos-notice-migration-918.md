# 2026-08-24 — settings.html: migrate ad-hoc client-JS status text to `.pos-notice` (ut-docs#918)

## What shipped

`ut-docs#918`'s own estimate ("~30 `aria-live` spans" in `settings.html`)
was a raw `grep aria-live` count that didn't distinguish what each span
actually does. Scoped precisely during BA/Architect, it splits three ways:

1. **9 spans genuinely receive ad-hoc client-JS `msg.textContent =
   '✓/✗/⏳ ' + text`**: `window-mode-msg`, `launch-on-startup-msg`,
   `exit-to-os-msg`, `data-reset-msg`, `archives-msg`, `cust-msg`,
   `cat-msg`, `export-msg`, `retention-export-msg`. **This card's actual
   scope.**
2. **~11 spans are pure elevation-dialog-retry `hx-target`s**
   (`retention-msg`, `printer-settings-msg`, `till-name-msg`, etc.) —
   confirmed via grep that nothing ever writes ad-hoc text into them
   client-side. Nothing to migrate.
3. **The rest are server-rendered via raw `fmt.Fprintf`** in
   `backup_api.go`/`update_api.go`/`settings_page.go` (enrol flow) — split
   out to a new card, ut-docs#956 (a different risk surface: Go handler
   changes, not client JS).

### The fix

- `web/public/app.js` — new shared `renderNotice(el, level, text)`,
  building `.pos-notice` markup via `document.createElement` (never
  innerHTML — `text` is routinely not ours to trust). Same DOM-construction
  approach `catalog.html`'s local copy (ut-docs#238) already uses; extracted
  because settings' 9 sites would have made a 6th copy-paste not worth it.
  `catalog.html` itself is untouched (out of scope — no reason to touch
  already-shipped behaviour).
- `web/ui/layouts/base.html` — `data-notice-dismiss="{{ T "notice.dismiss"
  }}"` on `<body>`, same convention as the existing `data-currency-*`/
  `data-conn-online`/`data-conn-offline` attributes, so the new shared
  helper (a static, non-templated asset — it can't call `{{ T }}` itself)
  can read a translated dismiss-button aria-label.
- `web/ui/pages/settings.html` — the 9 sites now call `renderNotice(el,
  level, text)` for their terminal success/error states, replacing the
  ad-hoc glyph-prefixed textContent assignment. `T`-lookup objects (already
  present at every site) are unchanged — this is a rendering fix, not an
  i18n fix; every string here was already routed through `httpx.T`/`{{ T
  }}` before this change (`universal-till/CLAUDE.md` cites this file's
  `data-reset-btn`/`export-run-btn` handlers as the T-lookup exemplar).
- `e2e/tests/settings-pos-notice-918.spec.ts` — new Playwright coverage.

## Independent review (Opus, worktree-isolated, different model from the
Sonnet session that wrote the diff)

Verdict on the first pass: **not safe to merge as-is** — one CI-blocking
finding, two should-fix findings, two minor/docs findings. Evidence
gathered independently (full `go test ./...`, all 16 build-job guards, a
revert-then-restore proving the new e2e spec is real coverage — not
vacuously passing — and a live browser probe of the DOM). All fixed in
this same round (none were blocker-class — money/tax/data-loss/security —
so a second full review round wasn't earned; fixes were re-verified
directly against the same gate):

1. **BLOCKING — `guard-docs-shots.sh` fails.** Proven causal (passes when
   the 3 source files are reverted to the pre-diff commit, fails at HEAD):
   `web/help/en/display.md` (routes: `[/settings]`) owns screenshots this
   diff's `web/ui/**`/`web/public/**` changes invalidate the cached hash
   for. **Fix:** ran `make docs-shots` (via the pre-installed-Chromium
   fallback, ut-docs#622) and committed the regenerated
   `web/help/img/manifest.json` + the one screenshot that actually changed
   pixels (`web/help/img/en/invoices.png` — a pre-existing, documented
   flake in the harness itself, ut-docs#930, unrelated to this diff;
   `display.png` itself is byte-identical across all 4 locales, since the
   9 message spans are empty on a fresh page load regardless of
   `<span>`/`<div>` — nothing to see visually).
2. **Should-fix — in-progress indicators vanished mid-operation.** 8 of
   the 9 sites' *progress* states (data-reset "Clearing…", archive
   restore/purge, catalog-cleanup "removing…", customer/catalog search
   "…", both export "…") were routed through `renderNotice(el, 'info',
   ...)`, and `.pos-notice` with level `info`/`success` auto-expires after
   ~2.5s (`scheduleToastDismiss`) — the pre-migration `msg.textContent =
   '⏳ ' + T.x` had no such expiry, so a slower-than-2.5s operation used to
   show nothing while its button was still disabled. **Fix:** reverted
   those 8 in-flight/progress writes to plain `msg.textContent = text`
   (no `.pos-notice` wrapper, no auto-expire) — only the *terminal*
   success/error state (already covered) goes through `renderNotice`.
   Added a 3rd e2e test (`customer search progress indicator...`) that
   stalls the response 2.8s and asserts the progress text survives past
   the 2.5s auto-expire window and never became a `.pos-notice`.
3. **Should-fix — `guard-i18n.sh`'s inline-JS check (ut-docs#205) was
   blind to `renderNotice(el, level, 'literal')`**, only matching
   `.textContent`/`.innerHTML` assignments directly. Demonstrated live:
   stripping the `i18n:ignore` from `settings.html`'s two pre-existing
   hardcoded-English exceptions (lines 666/668, tracked debt from before
   this card) passed the guard as `renderNotice(...)` but failed it as
   `.textContent = ...` — i.e. this migration would have silently retired
   that guard's enforcement on those two lines. **Fix:** extended
   `scripts/ci/guard-i18n.sh` with a second regex matching `renderNotice`'s
   3rd (text) argument, feeding the same prose heuristic + tag-stripping +
   `i18n:ignore` escape hatch. Re-verified: stripping the ignore comment
   now fails the guard again on both syntaxes; with the comments restored,
   it passes.
4. **Minor — invalid `<div class="pos-notice">` inside `<span
   aria-live>`.** All 9 sites were `<span>` (a `.pos-notice` is
   `display:flex`, a block-level box, inside an inline element); the
   already-shipped `catalog.html` precedent uses `<div>`. Browser-forgiving
   (no reported visual break), but semantically wrong, and `#cust-msg`
   specifically was also missing the `min-height:1.2rem` its 8 siblings
   have (a layout-shift risk once populated). **Fix:** changed all 9 to
   `<div>` (safe — every one sits either directly under a `.set-row`/other
   flex container, whose children ignore their own outer `display` value
   under flex layout, or as a block-level sibling of an already-block `<p>`
   /`<label>`) and added the missing `min-height` to `#cust-msg`. Re-ran
   the full settings e2e suite (12/12 green) plus the existing
   `settings-fee-row-251.spec.ts` horizontal-overflow assertions, which
   would have caught a layout regression across the page.
5. **Docs — `docs/sale-screen-notifications.md` (the named pattern doc)
   was stale** in two places: its "Out of scope" list still said "Admin
   pages other than catalog still have per-feature `aria-live` spans"
   (now true only of the ~14 pages tracked at ut-docs#919, not
   settings.html), and its body didn't mention the new shared `app.js`
   helper (only catalog's now-superseded local copy). **Fix:** added a 4th
   "Client-JS-built" slot describing the shared helper and the
   `data-notice-dismiss` convention, and updated the "Out of scope" bullet
   to name settings.html's actual remaining gaps (ut-docs#956, ut-docs#919)
   instead of the stale blanket statement.

**User manual — explicitly checked, no update needed.** `grep -rn
"✓|✗|⏳" web/help/` returns only `sell.md` (TSE nav chip, unrelated).
`display.md` describes the Settings → Data → Clear-transactions flow but
never the glyph prefix or the message area's visual shape — this is a
same-information, better-semantics-only change with no manual prose made
stale by it. The screenshot regeneration (finding 1) is the manual-adjacent
artifact this change does owe, and it's covered above.

## Verified beyond automated tests

- Ran the actual `e2e` Playwright suite against a real Chromium browser
  (not just Go unit tests) for `settings-fee-row-251.spec.ts`,
  `settings-osk.spec.ts`, and the new `settings-pos-notice-918.spec.ts` —
  12/12 green, both before and after every fix round.
- Revert-then-restore proved the new e2e spec is real coverage: reverting
  the 3 source files to the pre-migration commit made the new tests fail
  (`.pos-notice` never appears); restoring made them pass again.
- Live-drove `guard-i18n.sh`'s new regex against both a `renderNotice(...)`
  and a `.textContent = ...` hardcoded literal (with the `i18n:ignore`
  comment stripped) to confirm it actually catches the class of mistake
  it's meant to catch, not just that it doesn't false-positive on the
  real diff.
- Full local gate: `gofmt -l .` (clean), `go build ./...` (clean), `go
  test ./...` (all 41 packages, zero failures), and all 16
  `build`-job-listed CI guards (`guard-data-access`, `guard-kiosk-engine`,
  `guard-plugin-menu-read`, `guard-i18n`, `guard-compliance-claims`,
  `guard-docs-shots`, `guard-help-topics`, `guard-webkit-version`,
  `guard-kiosk-launch-flags`, `guard-android-status-address`,
  `guard-android-i18n`, `guard-emoji-font`, `guard-htmx-loaded`,
  `guard-autofill-suppression`, `check-brand-assets`,
  `guard-makefile-version`) — all pass.

## Safe-to-merge verdict

Yes, after the fixes above. No blocker-class (money/tax/data-loss/
security) findings at any point — this is a UI-rendering/accessibility
migration with no server-side behaviour change (diff is `web/ui/**` +
`web/public/app.js` + `e2e/**` + one CI guard script; zero `.go` file
changes).

## Explicitly deferred (new Backlog cards)

- **ut-docs#956** — the server-rendered ad-hoc `fmt.Fprintf` fragments in
  `backup_api.go`/`update_api.go`/`settings_page.go`'s enrol flow.
- **ut-docs#919** (pre-existing) — the ~14 other admin pages' `aria-live`
  spans.
- **ut-docs#920** (pre-existing) — self-order's legacy `.toast` overlay.
- Not filed as a new card (small, noted here for visibility only):
  `catalog.html`'s local `renderNotice` copy could later be consolidated
  onto the shared `app.js` one now that it exists — deliberately left
  alone this round to avoid touching already-shipped, unrelated behaviour
  in a migration-scoped diff.
