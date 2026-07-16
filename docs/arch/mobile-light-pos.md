# Mobile light POS — Android / iOS (BACKLOG)

> Status: **backlog**, not built. Captured 2026-07-16 (Farshid). The
> merchant-facing "use your own phone/tablet as a till" app — the MICRO tier
> ("no hardware to buy"). Distinct from the *shopper* consumer app
> (consumer-app.md, in the docs repo).

## The idea

A light Universal Till that runs on a merchant's **own Android/iOS phone or
tablet** — for solo traders, market stalls, pop-ups, table-side ordering, and
line-busting on a busy floor. "Download the app, start selling."

## Two shapes (decide during design)

1. **Companion register (webview) — easiest, ships first.** A thin native
   shell (like the mac desktop app: WebView2/WKWebView pattern) that points at
   the shop's **primary till** over the LAN (ADR-0011 sync) — the phone/tablet
   becomes an extra register with no server of its own. Reuses the entire
   server-rendered HTMX UI (already responsive), QR-pairs to the primary, syncs
   offline. Minimal new code.
2. **Standalone light till — later.** The phone itself IS the till (runs the
   data locally) for a merchant who only has a phone. Heavier: needs the Go
   core running on mobile (gomobile / a bundled server) or a re-implemented
   lightweight client. Full offline single-device selling.

## How it maps to us

- The web UI is already mobile-responsive HTMX, so shape (1) is largely a
  native wrapper + LAN pairing — the same webview approach as the shipped mac
  `.app`, retargeted to Android (WebView) and iOS (WKWebView), or via a
  cross-platform shell (Wails/Capacitor/React-Native-webview).
- Ties to the **MICRO hardware tier** (marketed "use your own device") and the
  storefront device tiers (product-storefront memory).
- Payments: the per-country payment plugins (device-plugin-suite.md) + tap-to-
  pay on the phone (SoftPOS) where supported.

## Open questions

- App-store distribution (Apple Developer + Play Console accounts, signing) —
  Farshid is deferring the Apple developer account for now, so shape (1) as an
  internal/sideloaded build first, store distribution later.
- SoftPOS / tap-to-pay-on-phone certification per market.
- Standalone (shape 2) mobile Go runtime feasibility.

## Sequence (when picked up)

1. Companion webview app (Android first — no dev-account gate), QR-paired LAN
   register against the primary till. Reuses the mac desktop shell pattern.
2. Tap-to-pay / payment-plugin integration.
3. Standalone light till (local data) if there's demand.

Related: desktop-app.md (webview pattern), consumer-app.md (the *shopper* app —
different audience), device-plugin-suite.md, product-storefront memory.
