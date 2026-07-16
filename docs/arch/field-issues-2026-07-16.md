# Field issues batch — Farshid 2026-07-16 (mac app testing)

Reported after v0.2.7 auto-update + a plugin install both worked. Grouped by
root cause. ✅ = fixed this session, 🔧 = quick next, 📋 = backlog (bigger).

## 🔴 Root cause A: the desktop WebView shell is too minimal
`cmd/unitill-desktop` uses `webview_go`, a thin WKWebView wrapper with NO
support for: clipboard, camera permission, file `<input type=file>` dialogs,
`window.open` popups, or file downloads. This one gap explains SIX reports:

- **Copy/paste not working** — WKWebView has no edit menu / clipboard by default.
- **AI camera "unavailable"** — the webview grants no camera permission.
- **Import not working** — the file picker (`<input type=file>`) doesn't open.
- **Receipt-designer logo upload not opening** — same file picker / popup block.
- **"Popups not opening"** — `window.open` is a no-op in the shell.
- **Export catalog (CSV) not working** — WKWebView won't download an attachment
  (same class as the backup-download bug already fixed with save-to-Downloads).

**Interim workaround:** the till is a normal web server — open it in Safari/
Chrome for admin tasks (import/export, logo upload, camera) and everything
works. (Needs a stable/known port — see below.)

**Real fix (📋 big):** upgrade the desktop shell to a capable WebView — either a
native WKWebView wrapper implementing WKUIDelegate (file upload + camera),
WKDownloadDelegate (downloads), and NSPasteboard (clipboard), OR move to Wails
(Go-native, provides file dialogs/clipboard/downloads). This is the highest-
leverage fix — resolves all six at once. Ties to desktop-app.md.

- 🔧 Meanwhile: (a) make the desktop shell prefer a STABLE port (try 8080 first,
  then fall back) so "open in browser" is easy; (b) server-side save-to-Downloads
  for CSV export (like the backup fix) so export works even in the webview.

## 🔴 Root cause B: printing assumes a thermal (ESC/POS) printer
- **Test print on an HP prints each line separately / raw.** The till emits
  ESC/POS bytes; a regular (HP/inkjet/laser) printer renders them as garbage/one-
  line-each. Need printer-TYPE-aware output: thermal → ESC/POS; regular →
  a formatted PDF/plain page (CUPS). Add a printer "type" to printer settings
  and branch the renderer. 📋 (device-plugin-suite ties in for driver plugins.)

## Catalog / data
- **Variants + per-variant barcodes** ("how to add multiple variants/barcodes,
  can each variant have a barcode — not working"). Investigate the catalog
  item-edit UI: variant rows, a barcode field per variant, and scan→variant
  resolution. 📋 (likely a real UI/data gap, needs its own pass.)
- **Import not working** — partly webview file picker (A), partly verify the
  catimport CSV flow end-to-end. 🔧 after A.

## Payments (Stripe) — see the detailed answer below
- **Shared/marketplace-level plugin settings** ("payment secret key should be
  for the market and shared with all tills; change once → push to all"). Today
  plugin settings are per-till (local DB). Need STORE-scoped settings that sync
  to every till (LAN sync of a settings subset, or fetched from the marketplace
  per store). 📋 architectural — ties to the claim/store-ownership work.
- **Stripe button does nothing / which device / emulator** — see below.

## UI
- ✅ **Home (Till) button next to Menu** — added (🧾 Till → sale screen).
- ✅ **Username is now a button** (👤 name).
- **Uninstall not working** — backend exists (POST /api/plugins/{id}/uninstall
  → plugins.UninstallPlugin). Investigate the UI button / webview fetch / an
  error path. 🔧
- **Keyboard layout + dynamic keys settings** — a hardware/peripheral plugin
  with a settings page (on-screen keyboard layout, programmable keys). 📋 new
  plugin, ties to device-plugin-suite.md.

## Stripe — how it actually works (answer)
- The plugin needs your **SECRET key** (`sk_test_…` for testing, `sk_live_…`
  for real) in the `stripe_secret_key` setting — NOT the **publishable** key
  (`pk_…`). A publishable key can't create charges server-side, so the button
  fails silently. That is almost certainly why "the button does nothing."
- **As built it's TEST-MODE**: it creates a confirmed Stripe PaymentIntent with
  a test card token (no real card read). Amounts ending `.13` decline; others
  approve. Great for a demo; not yet a real card-present flow.
- **Real payments need a device or an online flow:**
  - Card-present → a **Stripe Terminal reader** (BBPOS WisePOS E / Verifone) via
    Stripe Terminal, integrated as a device plugin.
  - Online → **Stripe Checkout / Elements** (customer enters the card; uses the
    publishable key in a web page).
- **Emulator:** yes — Stripe Terminal has a **simulated reader** in test mode,
  plus test cards, so we can build/demo the Terminal flow with NO hardware.
  📋 build a Stripe Terminal (simulated-reader) path + wire the payment button
  end-to-end into the tender flow.
