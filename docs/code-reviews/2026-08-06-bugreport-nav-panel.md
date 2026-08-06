# Review — 🐞 bug-report button + non-modal capture panel

Ticket: universaltill/ut-docs#346
Date: 2026-08-06
Branch: `feat/346-bugreport-nav-panel` → `main`
Reviewer model: Opus (deliberately different from the model that wrote the diff)

## What shipped

The bug-report capture UI (typed note + voice note + screen recording) moved
out of the standalone `/report-issue` page and into a floating, manager-gated,
**non-modal** panel that every staff page carries:

- `web/ui/partials/bugreport_panel.html` — the capture panel itself, rendered
  by `base.html` as a sibling of `<main>` on every page. Closed by default;
  `openBugReportPanel` stamps it open server-side.
- `web/ui/partials/bugreport_chip.html` + `GET /ui/bugreport-chip` — the 🐞
  toggle in the shared nav, loaded by an htmx placeholder (the same trick
  `/ui/sync-chip` and `/ui/session-chip` already use, because `nav.html` has
  no per-request data). The route is the gate: **empty 200** unless
  `isManagerOrAuthOff(r)`, so a cashier's nav has no 🐞 at all.
- `web/ui/pages/report_issue.html` shrank from 213 lines to 13: the route
  stays (it is the `/menu` tile's target and the manual's declared route) and
  now lands with the shared panel already expanded instead of carrying a
  second copy of the capture markup and its ~180 lines of JS.
- `bugreport_panel.html` added to **every** template-parsing call site that can
  execute `base` — `httpx.NewRenderer`, `httpx.Render`, `ui.NewRenderer`
  (`internal/ui/buttons.go`) and catalog's `RenderWith` file list — because
  `base.html` now references it unconditionally.
- Manual topic `web/help/{en,fa,tr,ar}/bug-reporting.md` (`routes:
  [/report-issue]`), 5 new locale keys in all four locale files, `.bugreport-*`
  CSS, and two new Playwright specs (one per suite).
- Drive-by: the stale doc comment in `internal/cloudsync/issue_reports.go`
  claiming the cloud upload endpoint doesn't exist yet.

Out of scope by the card and **not** treated as gaps: screenshot capture
(ut-docs#347), a "My reports" page (ut-docs#348), keeping a recording alive
across navigation (ut-docs#349). The self-order kiosk is structurally
untouched — `self_order_page.go` / `self_order_shop.go` render standalone
documents through `RenderPartial` and never parse `nav.html` or `base.html`;
confirmed by diff (both files unmodified) and by fetching `/self-order`, which
contains no `bugreport` markup at all.

## What the review found

### 1. Blocking (fixed): opening 🐞 showed no way to report anything

Driven, not read. The panel's height is bounded so it clears the sale screen's
basket column and tender panel; measured in Chromium, that band is only
~13rem on a till-sized screen:

| viewport | panel top | tender top | clear band |
|---|---|---|---|
| 1280×720 | 82px | 314px | 232px (13.1rem) |
| 1024×600 | 78px | 301px | 223px (13.1rem) |
| 1280×800 | 82px | 358px | 276px (15.5rem) |
| 1366×768 | 83px | 321px | 238px (13.2rem) |

The first cut spent that band on the panel title plus two paragraphs of prose
(the "recording stops if you navigate" note and the logs note), stacking the
note field, the two recorders and Save below them. Result, measured on the
running app: panel `scrollHeight` 650px inside a 213px box, with **neither the
note textarea nor the Save button inside the visible area** at 1280×720,
1024×600, 1280×800, 1366×768, `/settings`, `/catalog` or `/report-issue`. Save
was outside the visible box even at 1920×1200. An operator pressing 🐞 saw two
paragraphs of explanation and a scrollbar — the card's whole point ("send is
one press") was reachable only by discovering an inner scroll.

Both Playwright suites passed through this: `toBeVisible()` does not mean
"inside the viewport or its scroll container", and `.fill()` auto-scrolls the
element into view before typing.

Fixed without touching the non-overlap guarantee:

- Content reordered — note field and Save first, the two recorders next, the
  explanatory prose last as fine print.
- Note field and Save share one flex row (`.bugreport-send`) instead of
  stacking; stacking them alone cost more than the band has.
- The two recorders share a two-column grid (`.bugreport-capture`), so the
  optional half of the form costs one block of height, not two.
- Panel padding `1rem → .85rem`, head `h2` `1.1rem → 1rem`, textarea
  `rows="3" → "2"`.

Re-measured after the fix: note field and Save are **fully inside the panel's
visible box** at 1280×720, 1024×600, 1280×800, 1366×768, 1920×1200, in `fa`
(RTL) as well as `en`, and on `/settings`, `/catalog` and `/report-issue` —
while `panel.bottom ≤ tender.top` and `panel.left ≥ basket.right` still hold
everywhere (mirrored correctly in RTL).

Locked with new tests in **both** suites, and both were confirmed to be real:
reverting only the content ordering made all three parameterised cases in
`e2e/tests/bugreport-panel.spec.ts` fail with genuine bounding-box assertion
errors, and restoring it made them pass.

### 2. Accepted / deferred: the panel overlaps the basket below 900px wide

Under `@media (max-width: 900px)` the sale screen collapses to a single
stacked column (`basket / tender / products`), and the fixed panel then covers
the top of the basket — measured at 800×600 and 600×800. The tender is still
clear at both. Not fixed here, deliberately: at that breakpoint *any* floating
panel overlays something, and the two candidate fixes (put it in the document
flow, or bottom-anchor it) each reflow or cover something worse — reflowing the
POS grid mid-sale is a worse failure than an overlay the operator can dismiss.
Every viewport the till actually ships on (1024×600 and up) is clean. Worth a
follow-up card if narrow/portrait tills become a target; not a merge blocker.

### 3. Accepted: the panel markup renders for non-managers too

`base.html` includes the panel unconditionally, so a cashier's page carries the
(hidden) capture markup and its inline script even though the 🐞 toggle is
absent. Not a security issue — `POST /api/issue-reports` gates independently
and returns 403 — and the panel holds no data. Gating the markup as well would
mean a second per-request branch in the layout for no security gain.

### 4. Pre-existing, noted only

- `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure` fails in this
  container. Confirmed not caused by this diff: `internal/issuereport` is
  untouched (`git diff --stat` on the package is empty) and the container runs
  as uid 0, so the test's `0o500` directory does not actually block a write.
  Environment gap, not a regression.
- `scripts/ci/check-lang-pack-drift.sh` reports `ut-plugin-language-es` behind
  core. It was already behind (it is missing pre-existing `import.status.*`
  keys); the five new `issuereport.*` keys add to that list. The workflow is
  deliberately not a `pull_request` trigger (see its own header comment,
  ut-docs#299), so it does not block this PR — the pack repo catches up on its
  own cadence.
- `httpx.Render("ui/pages/report_issue.html", …)` still passes a hardcoded
  English `"title": "Report an issue"` into `<title>`. Pre-existing on this
  route and on every other page that does the same; `guard-i18n.sh` does not
  cover `<title>`. Not introduced here, left alone.

## What I verified personally (not taken on trust)

- `go build ./...`, `go vet ./...` — clean, before and after my fix.
- `go test ./... -race` — green except the pre-existing failure above.
- `guard-data-access.sh`, `guard-i18n.sh` (834 keys, all locales match en.json),
  plus `guard-emoji-font.sh`, `check-brand-assets.sh`,
  `guard-kiosk-launch-flags.sh`, `guard-webkit-version.sh` — all green.
- **Manager gating by revert-and-confirm**: commented the
  `isManagerOrAuthOff(r)` check out of `GET /ui/bugreport-chip`, re-ran
  `TestBugReportChip` — it failed with the real button markup in the
  no-session response body; restored the check, green again. The gate is the
  handler, not the test.
- **Every `nav.html` parse site enumerated** (`grep -rn 'partials/nav.html'
  internal/`) and cross-checked against `bugreport_panel.html`.
  `internal/ui/basket.go` is the only one not updated and correctly so: it
  executes the `basket` fragment, never `base`, so the escape analysis never
  reaches the panel. Backed up by fetching every whole-page route on a live
  server — `/ /menu /catalog /inventory /reports /settings /help /report-issue
  /designer /import /plugins /plugins/store /journal /backoffice /shifts /tills
  /receipt-designer /audit /setup /self-order /self-order/shop /ui/buttons` —
  no 500s.
- **Non-modality read line by line**: no backdrop element, no `inert`, no focus
  trap, and **no document-level key handler of any kind** in the new JS (a
  global `keydown` would eat barcode-scanner keystrokes). The only
  document-level listener is a `click` delegate that matches
  `closest('#bugreport-toggle')` and never calls `preventDefault`/
  `stopPropagation`. Proven behaviourally by the spec that completes a full
  cash sale — scan, basket, Cash, receipt — underneath the open panel.
- **RTL**: no literal `left:`/`right:` in the new CSS (only `inset-inline-end`,
  `text-align: start`, `margin/padding-inline`); confirmed live at `?lang=fa`
  — panel flips to the inline start, text right-aligns, Save sits correctly on
  the button's RTL side.
- **Template comments**: no `{{/* … */}}` comments in any new or changed
  template, so the `*/`-inside-a-comment trap from `98deb0b` cannot apply here.
  The comments used are HTML comments.
- **i18n**: all five new keys present in en/fa/tr/ar with real translations
  (read the fa/tr/ar strings — they are genuine, not English copies).
  `issuereport.intro`, which the rewritten `/report-issue` page stopped using,
  is still rendered by `settings.html`, so nothing was orphaned.
- **Manual**: four locales of `bug-reporting.md`, front matter matching
  `catalog.md`'s shape (`id/title/section/order/summary/routes[/keywords]`),
  `order: 360` in the "Connecting & extending" section, consistent across all
  four. fa/tr/ar are real translations. `helpHref` logic untouched
  (`internal/manual` has an empty diff) and verified live: `/` → `/help/sell`,
  `/catalog` → `/help/catalog`, `/report-issue` → `/help/bug-reporting`.
  No screenshots to regenerate — the manual has no images yet and there is no
  `docs-shots` make target in this repo.
- **Recurring bug classes**: this diff writes no files to disk, so the missing
  `os.MkdirAll` class does not apply; `internal/issuereport` (which owns
  `paths.Data(...)` + `MkdirAll`) is untouched, so the cwd-relative-path class
  cannot have been reintroduced.
- **Playwright, both suites, run by me**:
  - `e2e/` (port 8091, `UT_AUTH=off`, plus the auth project on 8092):
    **65/65 passed** — 62 pre-existing plus my 3 new viewport cases. No
    regressions in `sale-screen-213`, `tender-panel-reachable`, `manual` (RTL)
    or `ui-scale-basket`.
  - `tests/e2e/` (port 8080, seeded temp DB outside the repo):
    **16 passed, 4 pre-existing `DOCS_ROOT`-gated skips**. Note for future
    runs: `plugin_install_flow.spec.ts` needs
    `UT_ENABLE_MARKETPLACE_STUB=true` (or dev mode) on the server, otherwise
    its four tests 404 — that is harness configuration, not a code defect.
- No real client or shop name, and no secret-shaped value, anywhere in the new
  files. Temp databases were created outside the working tree and removed.
- `README.md` checked for staleness: its only "report bugs" mention is about
  contributing on GitHub, not the in-app feature, and it does not document
  `/report-issue`. Nothing went stale, so no README edit.

## Verdict

**Safe to merge.** One blocking finding, found by measuring the running app
rather than reading the diff, fixed in this branch and locked with tests in
both Playwright suites that were proven to fail without the fix. The remaining
findings are an accepted narrow-viewport limitation, an accepted cosmetic
duplication of hidden markup, and pre-existing environment/repo conditions
that this change neither caused nor worsened.
