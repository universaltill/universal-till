# 2026-09-09 — Android native error bar (ut-docs#1457)

## What shipped

`MainActivity.kt`'s `WebViewClient` gains three overrides:

- `onPageStarted` — hides the native error bar on every new top-level
  navigation (including the bar's own "Back to till" retry).
- `onReceivedHttpError` — shows the bar for a main-frame response with
  status ≥ 400, **unless** the response's MIME type is `text/html`. That
  skip is load-bearing: ut-docs#1455 already guarantees every Go
  page-route handler answers a failure through `httpx.RenderError` (a
  translated, full-layout page with a nav rail and "Back to sale"), so
  showing this native bar on top of that would be a redundant second
  "you're stuck" UI over a page that already tells the operator what
  happened. Only a genuinely non-HTML error body (an unmatched route, or
  a `page-error:allow` bare-`http.Error` escape such as
  `setup_page.go`/`order_tracking.go`) gets the native fallback.
- `onReceivedError` — the transport-level sibling (server not up yet, a
  network blip mid-navigation): no HTTP response at all, so no HTML body
  could exist to render into in the first place.

Both new overrides guard `request?.isForMainFrame != true` so a failed
sub-resource (icon/asset) never pops the bar over an otherwise-working
page — mirrors the existing `shouldOverrideUrlLoading`'s own MAIN FRAME
ONLY comment.

New UI: a plain `LinearLayout` bar (`activity_main.xml`, no new Gradle
dependency — same "plain Android widgets, no Material Components"
pattern the existing `pin_warning` banner already uses) with a message
`TextView` and a borderless "Back to till" `Button`, both bound through
i18n string resources in every shipped Android locale (`values`,
`values-ar`, `values-fa`, `values-tr`). "Back to till" (`backToTill()`)
loads the till's own root explicitly rather than reloading whatever
failed, since a failed main-frame load may leave nothing else loadable.

Manual: `web/help/{en,de,fa,tr,ar}/recovery.md` gains a section
distinguishing this (one page briefly failed, till otherwise fine) from
that topic's existing subject (the whole till failing to start).

## Independent review (Opus, per this card's `complexity:medium` routing)

**Verdict: SAFE TO MERGE.** Did not take the implementation on trust: no
Android SDK/emulator is available in this environment (confirmed —
`ANDROID_HOME` unset, `dl.google.com` blocked at the proxy, same
constraint as ut-docs#1281/#1254), so the reviewer reconstructed the
three new overrides against hand-built stubs of the real
`WebViewClient`/`WebResourceRequest`/`WebResourceResponse`/
`WebResourceError` API surface and compiled them standalone with the
project's own Kotlin 2.0.21 — **exit 0, zero warnings** — then deleted
the `isForMainFrame` guard as a negative control and confirmed it fails
exactly as expected (an unsafe call on a nullable receiver), proving the
guard and the resulting smart cast are both real, not assumed. Also
independently traced the `mimeType`-based skip against both ends of the
codebase: `httpx.RenderError` (`internal/httpx/render_error.go`) executes
`base.html`, which Go correctly sniffs as `text/html`, so the skip fires
for #1455's page; `index_page.go`'s `http.NotFound` and both existing
`page-error:allow` escapes emit `text/plain`, so the bar correctly still
covers those. Also traced `/` on a self-order till (anonymous → redirected
to `/self-order` by `auth.Middleware`; authenticated → the same by
`index_page.go`'s `mode == "self_order"` branch) to confirm "Back to till"
can never expose the cashier screen to a customer.

All applicable guards run and green: `guard-android-i18n.sh` (11 keys × 3
locales, placeholders intact), `guard-help-topics.sh`, `guard-i18n.sh`
(1592 keys), `guard-compliance-claims.sh`.

### Findings acted on before merge

1. **fa translation quality** (`values-fa/strings.xml`) — "این صفحه بار
   نشد" was colloquial; the repo's own `fa.json` uses «بارگذاری» for
   "load" in 20+ existing strings (`fiscaldevice.error.server`,
   `tables.error.load_failed`, …). Changed to «این صفحه بارگذاری نشد.»,
   and the matching quoted UI text in `web/help/fa/recovery.md`.
2. **Latent silent-no-op** (`MainActivity.kt`, the button's click
   listener) — the original draft called `hideErrorBar()`
   unconditionally before `backToTill()`, which is a no-op while
   `allowedHost` is still null. Traced every `loadUrl` call site and
   confirmed this is latent, not live, in the current codebase (no path
   sets `allowedHost` back to null after the first successful report) —
   but fixed anyway: the click listener now only calls `backToTill()`;
   `onPageStarted` is what actually hides the bar, and it only fires when
   a navigation genuinely started. A tap that does nothing no longer also
   erases the only affordance on screen — the same silent-no-op shape
   ut-docs#1647 was filed for.
3. **Manual under-states what the operator sees on the transport-failure
   path** — for `onReceivedError` (connection refused, server not up),
   Chromium commits its own untranslated English error page underneath
   the native bar. Added one clause to all five `recovery.md` locale
   sections: the screen above the bar may briefly show a technical
   message in English regardless of the till's language, and that's
   normal — use the button below it. (The `onReceivedHttpError` path
   needed no such clause: there the till's own body is shown, e.g. Go's
   `404 page not found`.)
4. **ar wording consistency** (`values-ar/strings.xml`,
   `web/help/ar/recovery.md`) — «الرجوع إلى» → «العودة إلى», matching 3/3
   existing occurrences in `ar.json` (`menu.back_to_sale`,
   `auth.exit_to_os.help`, `auth.exit_to_os.summary`).
5. **AppCompat-consistent button style** (`activity_main.xml`) —
   `?android:attr/borderlessButtonStyle` → `?attr/borderlessButtonStyle`,
   matching this AppCompat-themed app's own convention (no behavioural
   difference; both resolve through `Widget.Material.Button`, so the
   button's touch target was already compliant either way).

### Findings noted, not changed

- "Back to till" vs. the page-level `menu.back_to_sale` ("Back to sale")
  for the same destination — defensible as native-chrome-vs-page-chrome
  wording, recorded as a deliberate choice rather than drift.
- No `accessibilityLiveRegion="polite"` on the new bar — the existing
  `pin_warning` banner directly above it has the same gap; not a
  regression this card introduces, and not fixed here to avoid silently
  taking on an unrelated pre-existing gap's scope.
- `web/help/de/recovery.md` quotes the native bar's strings in German,
  but there is no `values-de/` (`KNOWN_LOCALES = {en, fa, tr, ar}`) — a
  German till actually shows the English native strings. Confirmed this
  is established practice already (`web/help/de/display.md` does the
  same for `kiosk_pin_not_engaged`), so left as-is.

## Verified beyond automated tests

No test was added. This module (`android/`) has **zero existing
unit/instrumented tests and zero test dependencies**, and
`android-ci.yml` is deliberately compile-only by explicit documented
design in that workflow's own comments — no CI job would ever execute a
Robolectric/instrumented test even if one were added here, and this
cloud session has no Android SDK/emulator to run one even once. Adding a
test nobody (not CI, not the author) could ever see pass would produce a
file that looks like coverage and rots silently — strictly worse than an
honest gap. This mirrors the accepted precedent from ut-docs#1254/#1281
(ship read-and-cross-checked, verify on real hardware in a follow-up).
The standing infrastructure gap itself is already tracked at
ut-docs#1691 (Android module has no automated test surface) — not
duplicated here.

Instead: the reviewer's standalone-stub compile (above) closes the
specific risk a unit test would most likely have caught (does this
compile, is the null-handling sound); the real remaining unknown is
device-level behavior a Robolectric test wouldn't cover either way
(does Chromium's own error-page commit fire a second `onPageStarted`
that would erase the bar immediately). Filed as ut-docs#1947 with exact
repro steps for a local/interactive session with the physical device.

## Safe to merge

Yes. No blockers found. All should-fix findings applied above; one
follow-up filed (ut-docs#1947) for on-device verification.
