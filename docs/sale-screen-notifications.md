# Operator notifications — the one pattern (ut-docs#213, ut-docs#238)

The sale screen has exactly ONE way to tell the operator something:
the **`.pos-notice`** component. Do not invent another (before this
pattern there were five competing ones — a fixed center-screen toast, a
hand-built `#toast-error` string, a dead Tailwind partial, ad-hoc
`aria-live` spans, and static banners).

ut-docs#238 started extending the same component off the sale screen:
the catalog page's five message spots (`#export-msg`, `#autofill-msg`,
`#item-form-msg`, `#labels-msg`, `#keypad-log`) now render notices too.
It is the pattern for any admin page's message spot from here on — see
"Out of scope" below for what has NOT been migrated yet.

## The component

```html
<div class="pos-notice error|success|info" role="alert|status">
  <span class="notice-text">…</span>
  <button type="button" class="notice-dismiss" aria-label="{{ T "notice.dismiss" }}">✕</button>
</div>
```

- **In-flow, never an overlay** — it must not occlude the pay buttons
  mid-sale (offline-first rule: status surfaces, never modal blockers).
- **`error`**: `role="alert"`, persists until the operator dismisses it.
- **`info` / `success`**: `role="status"`, auto-expire after ~2.5 s
  (`scheduleToastDismiss` in `web/public/app.js`).
- The dismiss button is wired by a delegated listener in `app.js` — it
  survives every `#basket` outerHTML swap, nothing to rebind.
- `scheduleToastDismiss` scans **every `.pos-notice` in the document**
  (ut-docs#238; it used to look up the single `#toast-message` id) and
  tracks each one's "already scheduled" state on that element's own
  dataset. So a notice anywhere on any page auto-expires by the same
  rules, and a page that inserts one itself should call
  `scheduleToastDismiss()` after inserting — otherwise the new notice is
  only picked up on the next `htmx:afterSwap`.

## Three slots, one surface

1. **Server-rendered** (the normal path): set `ToastMessage` +
   `ToastLevel` (`"info"`|`"success"`|`"error"`) on `pos.Basket` and
   render the basket; `web/ui/partials/basket.html` places the notice at
   the top of the basket panel. Every basket mutation swaps `#basket`
   wholesale, so the notice rides along for free. **Messages must go
   through `httpx.T(locale, key)`** — never a Go string literal (the
   i18n guard cannot see `ToastMessage` assignments; ut-docs#237 tracks
   closing that blind spot).
2. **Client-side** (`#pos-alert` in `web/ui/pages/index.html`): filled by
   `app.js` on `htmx:responseError` / `htmx:sendError` with localized
   strings carried on `data-msg-*` attributes (JS stays locale-free).
   Before #213 a failed request surfaced nothing at all.
3. **A response fragment that IS a notice** (ut-docs#238):
   `httpx.RenderNotice(w, locale, level, key, extra...)` writes the markup
   above for a handler whose whole `hx-target="#some-msg"` response is the
   message — catalog's export-save and labels-print handlers. It takes a
   locale KEY, never a Go string literal, and HTML-escapes the translated
   message (nothing else here goes through `html/template`, and a locale
   can come from a third-party language-pack plugin). Its variadic `extra`
   is appended raw as markup (`<code>/path</code>`, `(3)`) — a caller that
   interpolates anything user-controlled there must escape it itself.

A page that builds a notice in its own JS (catalog's `renderNotice`)
builds it with `document.createElement` + `textContent`, never an
innerHTML string: the text is routinely a scanned barcode, an item name,
or a raw `xhr.responseText`.

A 400 response whose body is a rendered basket fragment (contains
`id="basket"`) is swapped in and NOT treated as an htmx error — the
specific server-rendered notice wins over the generic alert
(`htmx:beforeSwap` handler in `app.js`).

## Out of scope

Still on their own pattern, all of it tracked under ut-docs#238 — don't
migrate one without taking that card:

- Self-order screens use the legacy `.toast` overlay CSS (kept in
  `app.css` under "Toasts (legacy overlay)").
- Admin pages other than catalog still have per-feature `aria-live`
  spans, and catalog's own `#image-msg` was left behind with the rest
  (its strings are hardcoded under the ut-docs#205 inline-JS i18n
  follow-up).
- Two sale-screen widgets keep their own scoped inline status lines
  (`#split-tender-status`, `#ai-identify-status`).

Known, filed separately, NOT fixed by the catalog migration:

- **ut-docs#916** — `POST /api/print/labels` answers failures with real
  404/400/502 statuses, and htmx does not swap a non-2xx response by
  default, so `#labels-msg`'s error notice is rendered server-side and
  never reaches the screen. Pre-dates #238.
- **ut-docs#917** — catalog's item form renders its "Saved" notice and
  then, for a new item, calls `clearForm()`, which wipes the same slot
  before the browser paints it. Pre-dates #238.
