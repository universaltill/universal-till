# Code review — catalog `.pos-notice` migration (ut-docs#238)

Branch `fix/238-catalog-pos-notice-migration`, reviewed at WIP commit
`ca37927`. Independent review by an Opus subagent (`complexity:medium`,
built by Sonnet), run in its own detached git worktree so its
revert/re-run steps could never touch the shared checkout.

Verdict: **safe to merge after the four fixes below**, all applied on
this branch.

## What shipped

catalog.html's five ad-hoc `aria-live` message spots (`#export-msg`,
`#autofill-msg`, `#item-form-msg`, `#labels-msg`, `#keypad-log`) move onto
the documented `.pos-notice` component
(`docs/sale-screen-notifications.md`, ut-docs#213):

- new `httpx.RenderNotice` for handlers whose whole htmx response IS the
  message (`POST /api/catalog/export-save`, `POST /api/print/labels`);
- a page-local `renderNotice()` in catalog.html for the spots its own JS
  fills;
- `scheduleToastDismiss` in `web/public/app.js` generalized from the single
  hardcoded `#toast-message` id to every `.pos-notice` in the document,
  with per-element `dataset.dismissed` state instead of one module-level
  flag;
- three new locale keys (`catalog.item_form.saving`/`.saved`,
  `catalog.keypad.failed_generic`) replacing hardcoded `'⏳ saving…'` /
  `'✓ saved'` / `'failed'` literals, present in all four locale files.

## TDD re-verification (done personally, in the worktree)

Each implementation change was reverted, the specific test re-run, then
restored. All failures were real assertion failures, never compile errors.

| Reverted | Test | Result |
| --- | --- | --- |
| `internal/httpx/notice.go` → pre-#238 ad-hoc `<span>` markup | `TestRenderNotice_*` (7) | 4 FAIL on assertions (`expected pos-notice error class, got: <span class="error">…`), 3 pass — see note | 
| `internal/pages/import_page.go` @ main | `TestExportSave_*` (2) | 2 FAIL on assertions (`got: <span class="error">…` / `<span>✓ Saved to <code>…`) |
| `internal/pages/print_api.go` @ main | `TestPostPrintLabels_*Notice*` (3) | 3 FAIL on assertions (`got: <span class="muted">✗ Pick an item first…`) |
| `internal/httpx/notice.go` escaping (new, added in review) | `TestRenderNotice_TranslatedMessageIsHTMLEscaped` | FAIL (`translated message written as live markup: …<img src=x onerror=…`) |

All restored → green.

Note: three of the seven `notice_test.go` cases
(`MessageIsLocaleBound`, `AppendsExtraContentAfterMessage`,
`WithoutTranslatorFallsBackToKey`) also pass against the reverted stub —
they pin semantics of a helper that did not exist before, so there was no
prior behaviour for them to fail against. Accepted, not a defect.
`MessageIsLocaleBound` additionally carries a `t.Skip` escape if two
translations ever coincide; acceptable for what it guards.

## Findings

### 1. `renderNotice()` built markup with `String.replace` — escaping bypass (fixed)

The client-side helper assembled a template string and substituted four
literal placeholders:

```js
html.replace('LEVEL', level).replace('ROLE', role)
    .replace('TEXT', escapeHtml(text)).replace('LABEL', escapeHtml(NOTICE_MSG.dismiss));
```

Two independent defects, both reproduced:

- **`$` substitution patterns defeat `escapeHtml` outright.**
  `String.prototype.replace` interprets `` $` ``, `$'`, `$&`, `$$` in the
  *replacement* string. A `text` of `` $` `` expands to the portion of the
  string before the match — i.e. the **unescaped** `<div class="pos-notice
  error" role="alert"><span class="notice-text">` markup built so far —
  which then goes through `el.innerHTML`. `escapeHtml` never sees it,
  because the expansion happens after escaping.
- **Placeholder collision.** `.replace(str, …)` hits the *first*
  occurrence, and `TEXT` is substituted before `LABEL`. A `text`
  containing the literal word `LABEL` steals the aria-label's
  substitution, leaving `aria-label="LABEL"` (untranslated) and a mangled
  message.

Every `text` argument is reachable by untrusted input: the keypad `code`
(scanner/operator typed), `itemLabel` from `row.dataset.name`,
`err.message` (raw response body), `ev.detail.xhr.responseText`,
`r.body.error` and `p.source` from the barcode-lookup service's JSON.

**Fix:** build the notice with `document.createElement` + `textContent`
and drop `escapeHtml` entirely — text that never touches the HTML parser
cannot produce markup, and there are no placeholders left to collide.

### 2. `httpx.RenderNotice` wrote the translated message unescaped (fixed)

The helper escaped the dismiss `aria-label` but not the message. Every
other place a translation reaches a page is `{{ T "key" }}` inside an
`html/template`, which escapes automatically; this helper writes straight
to the `ResponseWriter`, so it must do it itself. It is not theoretical:
`en.json` alone has 16 values carrying `&`/`<`/`>`/`"` (e.g.
`help.title` = "Help & Support"), and a locale can be supplied by a
third-party language-pack plugin (ADR-0009), not just this repo.

**Fix:** `template.HTMLEscapeString` on the message; `extra` stays raw
(it is markup by contract — `<code>/path</code>`, `(3)`) with that
contract now spelled out in the doc comment. Two new tests
(`…MessageIsHTMLEscaped`, `…DismissAriaLabelIsHTMLEscaped`) drive a
hostile locale through `config.NewI18nFS`; red-green verified.

### 3. `#export-msg` was a `<span>` receiving a `<div>` (fixed)

`RenderNotice` swaps flow content (`<div class="pos-notice">`) into
`hx-target="#export-msg"`, which was a `<span>` — invalid nesting
(`<span>`'s content model is phrasing content). Changed to a `<div>`; it
is a flex item in the page-head toolbar either way, so the rendered
result is unchanged (confirmed: `catalog.png` does not change).

### 4. `docs/sale-screen-notifications.md` went stale (fixed)

The doc's "Out of scope" section said admin-page `aria-live` spans are
"tracked in ut-docs#238 — don't grow this pattern into those surfaces
without taking that card". This branch **is** #238. Per CLAUDE.md a
behaviour change updates the affected doc in the same session. Updated:
the generalized `scheduleToastDismiss` contract, a third slot for
`httpx.RenderNotice` (including the escaped-message / raw-`extra` rule),
the build-it-with-DOM-calls rule for page-local JS notices, what is still
unmigrated (self-order `.toast`, other admin pages, catalog's own
`#image-msg`, `#split-tender-status`, `#ai-identify-status`), and the two
known-but-unfixed bugs below.

## The two "pre-existing bug" claims — both CONFIRMED pre-existing

- **ut-docs#916** (labels error notice never appears). `git show
  main:internal/pages/print_api.go` shows the identical
  `fail := func(status int, key string) { w.WriteHeader(status); … }`
  with the same 404/400/502 statuses. This diff changed only the body
  markup inside `fail`, never a status code. htmx does not swap a non-2xx
  response, and app.js's `htmx:beforeSwap` 400-override is path-scoped to
  `/api/pos/`, so `/api/print/labels` is unaffected. Reproduced live: the
  request returns 404 and `#labels-msg` stays empty. **Not a regression.**
- **ut-docs#917** (new-item "Saved" notice wiped). `git show
  main:web/ui/pages/catalog.html` has the identical ordering —
  `msg.textContent = '✓ saved'; if (!editing) clearForm();` at line 250,
  and `clearForm()` clearing `msg` at line 127. Only the mechanism of
  clearing changed (`textContent`/`innerHTML`, equivalent). Reproduced
  live: after creating a new item, `#item-form-msg` holds no notice.
  **Not a regression.**

Both are now recorded in `docs/sale-screen-notifications.md` so the next
person touching this surface sees them.

## Checks that came back clean

- **i18n.** Three new keys, present and sensibly translated in all four
  locale files (`en`/`fa`/`ar`/`tr`); `fa`'s "اتصال ناموفق بود" and `ar`'s
  "فشل الربط" reuse the same verb the existing keypad keys already use, so
  the terminology is consistent rather than a fresh coinage. No new
  hardcoded prose in the inline `<script>` — all six strings go through
  the `NOTICE_MSG` lookup, the pattern CLAUDE.md prescribes. The
  pre-existing `#image-msg` literals keep their `// i18n:ignore`
  (ut-docs#205) and were correctly left alone.
- **Scope.** Nothing outside catalog.html, the two shared handlers, the
  generalized `app.js` dismiss logic, the `RenderNotice` helper, its
  tests, and the locale files. The `import_ask_menu_manager_gate_test.go`
  edit is a required consequence (it asserted on `class="error"`).
- **UX guidelines.** Reuses the existing component and `:root` tokens; no
  new CSS at all; in-flow, no modal blocker; `.pos-notice` styling already
  uses logical properties (`margin-block-end`, flex `gap`), so RTL is
  unaffected — and `textContent` insertion cannot disturb bidi the way an
  escaping pass might.
- **Manual.** `web/help/en/catalog.md` documents catalog workflows and
  never describes the message strings or their styling, and no PNG
  changed for `catalog`. Nothing under `web/help/` went stale.
- **`app.js` generalization.** The only other `.pos-notice` elements in
  the tree (`#pos-alert` and the fiscal-override banner in `index.html`,
  `#toast-message` in `basket.html`) all carry `error` or the
  `#toast-message` id, so widening the query changes nothing for them.
  The dismiss handler was already delegated on `document`, so notices
  inserted by page JS get a working ✕ with no rebinding.

## Non-blocking observations (not changed)

- The five containers keep their own `aria-live="polite"` while now
  nesting a `role="alert"`/`role="status"` child. Nested live regions can
  mean a double announcement, or the ancestor's politeness winning over
  the notice's `alert`. Behaviour is unchanged from before this card
  (the wrapper attribute predates it) and getting it right needs a real
  screen-reader pass, so it is left as-is rather than guessed at.
- `renderNotice(log, 'success', code + ' → ' + itemLabel)` concatenates a
  numeric code, an arrow and a name — bidi-awkward in fa/ar. Same shape as
  the pre-#238 string; worth a proper localized template if that surface
  is revisited.
- `.notice-dismiss` sets `min-height: 0`, which opts it out of the fluid
  touch-target scaling the UX guidelines call for. Pre-existing from
  ut-docs#213, unrelated to this card.

## Gate after the fixes

`gofmt -l .` clean · `go build ./...` clean · `go test ./...` fully green ·
all 25 `scripts/ci/` guards in `.github/workflows/ci.yml`'s `build` job
pass, including `guard-i18n.sh`, `guard-docs-shots.sh` (and its two
self-tests + the cross-check), `guard-help-topics.sh`,
`guard-compliance-claims.sh` and `guard-data-access.sh`.

`make docs-shots` was re-run in full (a pre-installed Chromium was
available, ut-docs#622): `catalog.png` did **not** change — the fixes are
JS-only plus one empty element's tag name — so only `surface_sha256`
moved. The incidental `alerts`/`designer` PNG drift from the browser
build was discarded, same call as the prior docs-shots reviews.

Live re-verification (Playwright against a real till, temporary specs
removed after the run): hostile keypad input containing `` $` ``, `$&`,
`$'`, `LABEL` and an `<img onerror>` payload renders as literal text in
exactly one notice with a correctly translated `aria-label` and no script
execution; the error notice persists past 3s and its ✕ removes it;
`#export-msg`'s server-rendered success notice swaps in with
`role="status"`, its `<code>` path, and auto-expires; the item form shows
the localized Saving→Saved notices.
