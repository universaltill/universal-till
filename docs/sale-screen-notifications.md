# Sale-screen notifications — the one pattern (ut-docs#213)

The sale screen has exactly ONE way to tell the operator something:
the **`.pos-notice`** component. Do not invent another (before this
pattern there were five competing ones — a fixed center-screen toast, a
hand-built `#toast-error` string, a dead Tailwind partial, ad-hoc
`aria-live` spans, and static banners).

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

## Two slots, one surface

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

A 400 response whose body is a rendered basket fragment (contains
`id="basket"`) is swapped in and NOT treated as an htmx error — the
specific server-rendered notice wins over the generic alert
(`htmx:beforeSwap` handler in `app.js`).

## Out of scope

Self-order screens still use the legacy `.toast` overlay CSS (kept in
`app.css` under "Toasts (legacy overlay)"); admin pages still have
per-feature `aria-live` spans; and two sale-screen widgets keep their own
scoped inline status lines for now (`#split-tender-status`,
`#ai-identify-status`). All of that is tracked in ut-docs#238 — don't
grow this pattern into those surfaces without taking that card.
