# Per-country device & payment plugin suite (BACKLOG)

> Status: **backlog**, not built. Captured 2026-07-16 (Farshid). Follows the
> plugin-first rule: fiscal/payment hardware is country-specific → each is a
> plugin, core stays neutral. Builds on the payment-plugin seam (authorize-
> before-tender, ADR-0001 processes for hardware) and ADR-0014 connectors.

## The ask

A library of **device integration plugins**, per country:
- **Turkey — ÖKC fiscal devices:** an integration/device plugin for **each
  certified YN ÖKC** (New-Generation Cash Register) vendor. The plugin drives
  the certified device, which issues the legal fiscal receipt + reports to GİB
  (see turkey-fiscal-compliance.md). One plugin per ÖKC model/vendor.
- **Payment terminals, per country/acquirer:**
  - **UK** — e.g. Dojo, Worldpay, SumUp, Stripe Terminal, Square.
  - **UAE** — Network International (N-Genius), Magnati, Mercury, tap-to-pay.
  - **China** — Alipay / WeChat Pay (QR + native), UnionPay.
  - **Iran** — Shaparak / local PSPs and card readers (سداد/به‌پرداخت/… ) — note
    the sovereign/offline + cloudless-region constraints.
  - Generic — SumUp/Stripe/Adyen where they operate.

## How it fits our architecture

- **Payment plugins** already have the seam: `payment` type + the blocking
  `payment.<key>.authorize` event (authorize BEFORE the sale completes) proven by
  the demo terminal and qrpay. Each real terminal = a plugin implementing that
  seam against the provider's SDK/API, config (merchant id, terminal id, keys)
  in plugin settings. Hardware terminals run as **process plugins** (ADR-0001),
  cloud/QR ones as WASM.
- **ÖKC fiscal** = a `device` (+ integration) plugin: the sale is handed to the
  certified device which prints the fiscal receipt and does GİB reporting; our
  receipt/print path defers to it where the law requires.
- **Reuse** the connector patterns from ADR-0014 (settings, net:* host, offline
  behaviour) and the payment authorize seam. Core adds seams/host functions only.
- **Monetization** (ADR-0013): these are premium/registered-tier plugins — the
  per-market value capture; enterprise/regulated markets pay.

## Reality / dependencies

- Each provider needs its **SDK/API + credentials + (often) certification** and,
  for Turkey ÖKC, a **certified hardware partner**. So this is a per-integration
  business+engineering effort, not one sprint — but the plugin platform makes
  them independent, incremental, and shippable one at a time.
- Sequence by market priority (Turkey ÖKC + UAE payments first, per current
  leads; Iran/China per the sovereign-market roadmap).

## Related

turkey-fiscal-compliance.md · enterprise-department-stores.md (UAE payments) ·
ADR-0014 (connectors) · ADR-0013 (paid tier) · [[plugin-first]].
