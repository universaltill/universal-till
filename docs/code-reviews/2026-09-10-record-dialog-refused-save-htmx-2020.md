# 2026-09-10 — Record dialog: a refused save keeps the dialog open (ut-docs#2020)

## What shipped

`ut-docs#2010`'s app-wide list/edit standard (the full-screen `<dialog>` on
`/categories`) refused every mutation the same way it answered success: a
303 redirect to `/categories?err=...`. That closed the dialog and threw
away whatever the operator had typed — even when the thing that failed was
unrelated to the field they were editing (the exact repro: rename a
category, then tap deactivate; the deactivate refuses, and the rename,
never itself the problem, vanished with it).

The dialog's forms (`categories.html`'s `record_dialog_fields`/
`record_dialog_destructive` slots) now post via `hx-boost`, targeting the
dialog's own `aria-live="polite"` message region
(`"#category-dialog-msg"`, inlined in `record_dialog.html`) with
`hx-swap="innerHTML"`:

- **Refused**: `internal/pages/categories_page.go`'s
  `renderCategoryDialogError` answers a non-2xx status with just the
  translated message text (`RenderPartial` on the new
  `record_dialog_msg.html`, no wrapper). `app.js`'s existing
  `htmx:beforeSwap` handler force-swaps a non-empty `text/html` non-2xx
  body and marks it not-an-error, so the page-wide banner never also
  fires. The dialog and its form are never mentioned in the response, so
  neither is touched.
- **Succeeded**: `HX-Redirect` (a real navigation), never a bare 3xx — a
  boosted form's own fetch/XHR layer would otherwise follow a bare
  redirect itself and land the whole next page inside the message `<div>`.
- **A failure the server never got to answer meaningfully at all**
  (network unreachable, or a plain-text 403/500 `beforeSwap` doesn't
  force-swap) — `record-dialog.js`'s new `dialogFailureFallback`, wired to
  the global `htmx:sendError`/`htmx:responseError` events, writes a
  generic translated message (`common.error.network`/`common.error.server`,
  from the dialog's own `data-error-*` attributes) into the same region.

Also: the message region is genuinely empty by default and collapses via
CSS `:empty` (no space reserved, matching ut-docs#2000's head-height
lesson); `record-dialog.js`'s `open()` clears it on every open so a stale
refusal never appears to describe a new one; the destructive form's
confirm moved from a custom `data-record-confirm` + `document`-level
listener to htmx's native `hx-confirm`.

New/changed non-code artifacts: `web/locales/{en,ar,fa,tr}.json` gain one
key (`common.error.network`); `ut-docs/reference/list-and-dialog-pattern.md`
(created — see below) and a pointer from `ut-docs/reference/ux-guidelines.md`;
`web/help/img/**` regenerated (`make docs-shots`).

## A gap found and closed in the same change: the reference doc never existed

`ut-docs#2010`'s own close-out comment claimed the binding pattern doc had
been written to `ut-docs/reference/list-and-dialog-pattern.md` — it had
not, in either repo's history, despite five separate files in this repo
referencing it by that exact path. This card writes it for real, since
its own AC6 needs a real "skeleton" to restore the message region into and
every future ut-docs#2012 screen is told to read it.

## What the independent review found

Two Opus rounds (`complexity:medium` → Opus per `MODEL-ROUTING.md`), both
isolated worktrees, both actually building/testing/running the real e2e
suite against a live server rather than reading the diff.

**Round 1 — verdict NOT SAFE TO MERGE, one blocker:**

- **AC5 (offline-first, ADR-0003) not met.** Every failure that wasn't the
  deliberate 400 fragment was completely silent: `#pos-alert` (`app.js`'s
  only fallback banner) exists solely on the sale screen, nowhere on this
  pattern. Driven-browser evidence: network abort, a plain-text 403, and a
  plain-text 500 all produced zero visible feedback, dialog just sitting
  there. Fixed with `dialogFailureFallback` as described above.
- Should-fix: the reference doc's "Where errors surface" section
  overclaimed a page-banner path the code didn't actually take; the
  `hx-swap="outerHTML"` swap likely broke `aria-live` announcement (a live
  region generally must stay the same DOM node across a content change);
  no regression test pinned the *other* real bug this card also fixed
  (dismissing `hx-confirm` no longer stopped the request once boosting
  landed — htmx's own submit listener, bound to the form itself, always
  ran first).
- Nits: a dead `--danger` hex fallback in CSS, a couple of stale/dangling
  comments, two doc typos.

Also independently confirmed as genuinely fixed (not just claimed) during
round 1, by execution: **htmx's `hx-boost` caches a form's `action`/
`method` ONCE, at DOM-process time** (verified against the actual
vendored `htmx.min.js` 1.9.12 source) — invisible to `record-dialog.js`'s
per-row `action` mutation without the `reboost()` helper (`htmx.process(el)`)
added alongside it; and **a `document`-level confirm-then-`preventDefault()`
guard cannot stop a boosted submit**, since htmx's own listener sits on the
form (the event's target) and fires first regardless of registration
order — replaced with htmx's native `hx-confirm`. Both reproduced live
(wrong endpoint / request sent despite dismiss) before the fix, and
disproven-reproducible after.

**Fixes applied**: `dialogFailureFallback` (AC5); `record_dialog.html`
inlines the message `<div>` directly (no longer re-rendered via
`{{ template "record_dialog_msg.html" }}`) and every form's `hx-swap`
moved to `innerHTML`, so the live-region node is never replaced;
`record_dialog_msg.html` simplified to just the message text (dropped
from `httpx.renderFiles` — nothing references it by name anymore); two
new e2e tests (`(h)` dismiss-sends-nothing, `(i)` network-failure); the
reference doc's error-surface section rewritten to match; CSS/comment
nits fixed.

**Round 2 (scoped to the round-1 fix, not a full re-review) — verdict NOT
SAFE TO MERGE, one blocker, different from round 1:**

- **`guard-docs-shots.sh` failed** — CI-blocking, and this diff changes
  `web/ui/**`/`web/public/**`/`internal/pages/**.go` without having run
  `make docs-shots`. Fixed: ran it (112 screenshots, 28 topics × 4
  locales, all passed); guard now green.
- Should-fix: `dialogFailureFallback`'s global listeners fired for ANY
  htmx failure on the page, not just the dialog's own submission —
  reproduced live by aborting only `base.html`'s unrelated 30s
  `#pairing-notice` poll while a dialog sat open with nothing submitted;
  30 seconds later the dialog claimed "something went wrong." Fixed by
  checking `ev.detail.elt` is actually inside the open dialog before
  writing anything. Also: the pattern doc's create-mode `reboost()` advice
  was incomplete (`reboost()` alone re-processes whatever `action` is
  already there — a screen needs to clear/reset it first); doc corrected.
- Nits: one more stale "plain-form" comment in `categories_page.go`
  (missed by round 1's fix nine lines above it) — fixed; a CSS indentation
  nit and a comment-wording imprecision — left as genuinely cosmetic.

Everything else in round 2 came back independently confirmed correct: the
AC5 fix itself (all three failure shapes produce the right message, no
double-messaging with the specific-refusal path), the `outerHTML`→
`innerHTML` redesign is complete with no leftover references anywhere, the
locale-key change is exactly one new key across all four files, and 18/18
e2e tests including a `--repeat-each=3` run of the three new/changed ones
with no flake.

## What was verified beyond automated tests

- **Both htmx traps reproduced live** before their fixes and disproven
  live after, against a real running server driven by real Chromium — not
  inferred from reading `htmx.min.js`'s source alone, though that source
  (the actual shipped `web/public/vendor/htmx.min.js`, 1.9.12) was read to
  understand *why* in both cases (`lt()`'s one-time `action`/`method`
  capture; `ht()`'s submit listener bound to the form itself with no
  `defaultPrevented` check; `he()`'s own fresh, in-line `hx-confirm` read).
- **The `htmx:beforeSwap`/`isError` interaction that keeps the specific
  and generic error paths from double-firing** was traced through the
  vendored source and confirmed by execution: the deliberate 400
  fragment's specific message survives untouched precisely because
  `app.js` sets `isError = false` before `htmx:responseError` would
  otherwise fire the generic fallback for the same response.
- **The misattribution bug** (round 2's should-fix) was found by
  literally waiting out `base.html`'s 30-second background poll against a
  live server with an open dialog and nothing submitted — not something a
  diff read or a synchronous test would surface.
- `go test ./internal/pages/... ./internal/httpx/...`: all pass, including
  two new Go tests (`TestCategoriesPage_DeactivateBlockedRendersInDialogMessageForHtmxRequest`,
  `TestCategoriesPage_HtmxSuccessAnswersWithHXRedirectNotBareRedirect`) and
  the reverted `parsePartialWithPage` test helper (simplified back once
  `record_dialog.html` no longer references a second file by name).
- `gofmt -l .`, `go vet ./...`, `golangci-lint run ./...` (0 issues).
- CI guards run directly: `guard-data-access.sh`, `guard-i18n.sh` (1606
  keys, all locales in sync), `guard-compliance-claims.sh`,
  `guard-page-http-error.sh`, `guard-htmx-loaded.sh`, `guard-docs-shots.sh`,
  `guard-help-topics.sh`, `guard-help-drift.sh` — all pass (help-drift's
  own output lists only pre-existing, already-baselined drift unrelated to
  this change).
- e2e: `categories-record-dialog-2010.spec.ts` — 18/18 passed on the final
  run, and (independently, in round 2's own isolated worktree) a
  `--repeat-each=3` pass of the three touched/new tests with no flake.
- No SQL, no DB/migration change, no file I/O (so no `os.MkdirAll`/
  `paths.Data` exposure — this pipeline's two recurring bug classes), no
  money, no real client/shop name, no secret-shaped literal anywhere in
  the diff.

## Deferred, not fixed here

- **`web/help/en/categories.md`** is not wrong (its existing bullets never
  claimed WHERE a refusal's message appears), but doesn't yet mention that
  a refused save now keeps the dialog open with what was typed. Round 2
  flagged this as a nit, not a blocker; deferred rather than translating a
  new bullet into `ar`/`fa`/`tr` help topics for a purely-cosmetic manual
  improvement in this already-large change.
- **Self-healing the generic fallback message** (clearing it if a
  subsequent request from the same dialog succeeds, the way `app.js`'s own
  `#pos-alert` clears itself on the sale screen) was not added — `open()`
  already clears it on the next open, and round 2 judged the gap a
  should-fix, not a blocker, since AC5's actual failure mode (silence) is
  fixed either way.
- **ut-docs#2012** (rolling this pattern out to the other nine screens) is
  its own card, unaffected by this one beyond the reference doc it now
  points at.

## Verdict

**SAFE TO MERGE.**
