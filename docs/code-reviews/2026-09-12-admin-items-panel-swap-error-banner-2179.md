# Code review: admin/items in-panel swap failures show a visible banner (ut-docs#2179)

**Card:** ut-docs#2179 (split out of ut-docs#2162/#2116's independent review)
**Repo:** universal-till
**Complexity:** medium (Sonnet build, Opus review)
**PR:** fix/2179-admin-items-panel-swap-error-banner

## What shipped

`/admin` and `/items` are two-pane master-detail shells: a tree/rail row
does an in-panel htmx swap into `#admin-panel`/`#items-panel`. When the
destination handler errors (403/500), `httpx.RenderError` renders a full
`base`-templated HTML error page — it has no htmx-fragment awareness by
design (see that function's own doc comment: API/fragment routes use
`LocalizedError`/`LogAndLocalizedError` instead). Two bugs combined to
make this a completely silent no-op:

1. `web/public/app.js`'s `htmx:beforeSwap` listener (from ut-docs#916)
   force-swapped ANY non-2xx, non-empty `text/html` response into the
   swap target — including this full document. Forcing a whole
   `<html><head>...<body>...` document into a small panel `<div>` as
   innerHTML doesn't render anything sane; it's the exact "does nothing
   at all" the card reports.
2. Even where `htmx:responseError` fires, its target (`#pos-alert`) only
   existed on the sale screen (`index.html`) — `admin.html`/`items.html`
   had no such element.

## Fix

- `web/ui/partials/pos_alert.html` — new shared partial, extracted from
  index.html's own former inline `#pos-alert` markup verbatim (same id,
  classes, i18n keys). Registered in `internal/httpx/httpx.go`'s
  `renderFiles` set and in `internal/ui/buttons.go`'s `NewRenderer` (its
  own, separate hardcoded file list — see "What the independent review
  found" below). Included from `admin.html`, `items.html`, and
  `index.html` (refactored to use the shared partial instead of its own
  copy — zero behavior change).
- `web/public/app.js`'s `htmx:beforeSwap`: added an early return when the
  response looks like a full HTML document (`/^\s*(<!doctype html|<html)/i`
  against `d.serverResponse`) — never a targeted fragment safe to force
  into a panel target, unlike the `.muted` fragments ut-docs#916 targets.
  Falls through to the normal `htmx:responseError` → `showAlert` path.
- No Go handler changes, no new i18n keys (reused `pos.error.server`,
  `pos.error.network`, `notice.dismiss` — the message is already
  generic/safe), no ADR (follows the established #916/#1287/#213
  client-side pattern, not a new cross-cutting decision).

## What the independent review found (Opus, isolated worktree)

Verdict: **safe to merge**, no blocking findings. Six findings, most
severe first — two fixed before commit, four accepted as-is:

1. **Fixed** — `catalog.html`'s item-form-modal Variants-panel refresh
   (three `htmx.ajax()` GET calls: modifier-dialog-close reset,
   deactivate reset, `openCatalogRow`) passed no `source`, so
   `ev.detail.elt` fell back to `document.body` — outside
   `#item-form-modal`. Combined with this PR's OWN new scoping guard on
   `catalog.html`'s generic `htmx:responseError` listener (added to fix
   an unrelated cross-talk bug the reviewer's own testing surfaced during
   development — see below), a real failure on the open dialog's Variants
   fetch would go completely silent instead of painting into
   `#item-form-msg` as it did before. Fixed by adding
   `source: '#item-form'` to all three calls (the save flow already does
   this).
2. **Fixed** — the save flow's own `markFailed` one-shot
   `htmx:responseError` listener was `document.body`-scoped with
   `{once: true}` and no filtering — an unrelated panel-swap failure
   elsewhere on the page (concurrent with an in-flight save) would
   consume the one-shot slot, leaving the real save failure unnoticed and
   the dialog stuck on "Saving…" with no outcome. Fixed: scope the check
   inside the handler and only remove the listener on an actual match,
   so an unrelated event leaves it armed for the real one.
3. Accepted as-is — `#pos-alert` only reaches three pages (index/admin/
   items); standalone `/catalog`, `/inventory` etc. still have no banner
   surface. Not a regression (same as before this PR); filed as a
   follow-up rather than widening this PR's scope (base.html would close
   it for every page at once).
4–6. Nits (e2e wording, a `toBeHidden()` precondition that's also true
   for an absent element, an inaccurate code comment) — addressed or
   accepted, no behavior impact.

The unrelated cross-talk bug finding #1/#2 build on: during development,
the fix above (letting `htmx:responseError` fire for the full-document
case) newly exposed that `catalog.html`'s own generic
`htmx:responseError` listener (painting into `#item-form-msg`) was
**unscoped** — it fired for ANY failed request anywhere on the page,
including the admin/items tree-row click itself, stamping raw unrelated
response text into the item-form dialog's message area even when that
dialog was closed. Fixed by scoping that listener to
`ev.detail.elt.closest('#item-form-modal')` — which is what surfaced the
two further gaps above once the reviewer checked every call site that
relies on it.

## Verified beyond automated tests

- `gofmt -l .` (clean), `go build ./...`, `go vet ./...`, `go test ./...`
  (full suite, zero failures, run three times across the fix iterations),
  `golangci-lint run ./...` (0 issues).
- Guards: `guard-i18n.sh`, `guard-data-access.sh`, `guard-docs-shots.sh`
  (surface-hash-only update via the documented escape hatch — the new
  `#pos-alert` element ships `hidden`, verified via a full, genuine
  `make docs-shots` run: zero PNG bytes changed after reverting an
  unrelated Chromium-version-drift regeneration; the mismatched
  pre-installed Chromium in this sandbox, flagged by the guard's own
  warning, produced diffs on completely untouched pages like `menu.html`
  too, confirming that noise was environmental, not from this diff),
  `guard-help-topics.sh`, `guard-help-drift.sh`, `guard-compliance-claims.sh`,
  `guard-page-http-error.sh`, `guard-htmx-loaded.sh`, `guard-shellcheck-version.sh`
  + `shellcheck scripts/ci/*.sh` (0 issues; no shell files touched).
- **Real TDD**, re-verified independently by the reviewer via revert/
  restore (not just taken on trust): removing the `pos_alert.html` line
  from `internal/ui/buttons.go`'s file list reproduces
  `TestButtonsNewRenderer_WorksFromAnyWorkingDirectory`'s exact
  `"no such template \"pos_alert\""` failure (this fix's own real
  regression, found during Dev by running the FULL `go test ./...`, not
  just the touched package — the initial diff broke this sibling
  package's own separately-maintained template file list). Removing the
  app.js discriminator reproduces both e2e tests failing with `#pos-alert`
  present-but-hidden. Removing either page's `{{ template "pos_alert" }}`
  line fails exactly that page's own test, independently.
- New e2e regression coverage
  (`e2e/tests/htmx-panel-swap-error-banner-2179.spec.ts`): a 500 on
  `/admin`'s `/country-settings` swap and a 403 on `/items`' `/inventory`
  swap, each mocked via `page.route()` (deterministic, same pattern
  `htmx-admin-error-swap-916.spec.ts`/`htmx-senderror-1287.spec.ts`
  already use for this class of client-swap-handling bug) — asserts the
  banner becomes visible with the right generic message, the untouched
  panel keeps its own original heading (not wiped, not replaced with the
  fetched document's own markup), and the banner dismisses.
- Full regression sweep after the F1/F2 fix: `catalog-*` + `items-shell-*`
  specs (83 tests) plus `htmx-admin-error-swap-916`/`htmx-senderror-1287`
  (mirrors the reviewer's own sweep) — all green, including
  `catalog-save-notice-917.spec.ts`'s real (unmocked) 400 duplicate-SKU
  save-failure test, which is what actually proves the item-save error
  path still reaches `#item-form-msg` through the now-scoped listener.
- Real, driven visual check (real headless Chromium, real server, not
  just rendered-HTML assertions): screenshotted the visible banner state
  on `/admin` at 1280×800 desktop, the 1024×600 kiosk floor, and 360px
  phone width — no overlap, clipping or wrapping at any size, status/
  lock/menu bar stays reachable underneath. Screenshotted `/items` in
  `fa` (RTL): dismiss button correctly on the logical-start (rendered
  left) side, Persian message renders correctly, layout unaffected —
  `.pos-notice`/`.notice-dismiss` (`app.css`) use only logical properties
  (`margin-block-end`), no `left`/`right` literals, confirmed by the
  reviewer independently too. This is a REUSED, already-shipped component
  (previously only on the sale screen) — no new CSS, no new touch targets.
- Not verified: on real touch/kiosk hardware (cloud sandbox has none);
  dark theme (no dark theme currently shipped to check against).

## Non-goals (per the card)

- Re-plumbing the auth middleware (401 handling is untouched, already
  covered by ut-docs#2144).
- `#pos-alert` reaching every page (`/catalog`, `/inventory`, etc.
  standalone) — same follow-up the review's finding #3 above flags;
  worth a Backlog card (base.html-level include) rather than widening
  this PR.

## Help manual

No update needed — this is an invisible client-side error-handling fix
(no new screen, route, or step a shop owner is taught); confirmed no
`web/help/` topic describes this surface's error behavior either way.
`guard-help-topics.sh`/`guard-help-drift.sh` both clean.

## Verdict

Safe to merge. Independent Opus review found two real, narrow issues
(both introduced by this PR's own necessary scoping fix, not
pre-existing) — both fixed and re-verified with the full targeted + Go
+ e2e gate green afterward. One accepted follow-up (`#pos-alert` on
every page) filed to the backlog, not built here.
