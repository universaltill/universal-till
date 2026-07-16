# Turkey fiscal compliance — requirements & plan (BACKLOG)

> Status: **backlog / research**, not built. Captured 2026-07-16 (Farshid wants
> to launch in Turkey). Selling a retail POS in Turkey is **gated on fiscal
> certification** — this is not just tax rates, it's a regulated device/reporting
> regime under the Revenue Administration (GİB / Gelir İdaresi Başkanlığı).

## What Turkey requires (beyond VAT)

1. **KDV (VAT).** Standard 20%, reduced 10% and 1%. Our tax engine already
   supports configurable rates + inclusive/exclusive — this part is easy;
   Turkey just needs the right rate set and Turkish receipt labels (we have the
   `tr` locale).

2. **YN ÖKC — Yeni Nesil Ödeme Kaydedici Cihaz (New-Generation Cash Register).**
   The big one. Retail sales to consumers must go through a **GİB-certified
   fiscal cash register** that signs each receipt and reports Z-reports to GİB
   over a TSM (secure service manager). A plain software POS **cannot legally
   issue a retail fiscal receipt on its own**. Two viable routes:
   - **(a) Integrate a certified ÖKC device** — the POS drives a certified
     fiscal printer / payment-recording device (many are combined card+fiscal
     units). Our POS sends the sale; the certified device produces the legal
     fiscal receipt and does the GİB reporting. This fits our **device-plugin**
     model (ADR-0001 processes for hardware) — a "TR ÖKC" device plugin per
     certified vendor.
   - **(b) Become a certified PC-fiscal solution (GMP-3 / EFT-POS software)** —
     heavy certification of our software itself. Not the starting path.

3. **e-Fatura / e-Arşiv Fatura.** Mandatory electronic invoicing above turnover
   thresholds: **e-Fatura** (B2B, registered users) and **e-Arşiv Fatura** (B2C
   / non-registered). Issued through the GİB portal or, in practice, a licensed
   **özel entegratör** (private integrator). This mirrors the UAE e-invoicing
   direction and our ERP-connector pattern: an **integration plugin** that hands
   invoices to an özel entegratör's API.

## How it maps to our architecture (pluggable, per-country)

Fiscalization is country-specific and regulated → it belongs in **plugins**,
not the core, exactly like payments and ERP connectors:
- **TR ÖKC device plugin** (device/hardware plugin) — drives the certified
  cash-register/fiscal-printer hardware; the legal receipt + GİB reporting is
  the device's job.
- **TR e-Fatura/e-Arşiv plugin** (integration plugin) — pushes invoices to an
  özel entegratör (same shape as the ERP connectors, ADR-0014).
- Core stays jurisdiction-neutral (money type, tax engine, invoice numbering);
  the compliant, certified bits are swappable plugins. Same model serves UAE
  (FTA tax invoice + e-invoicing) and any other country.

## Reality check / dependencies

- **Certification requires a local partner.** We realistically integrate a
  certified Turkish ÖKC vendor and/or özel entegratör rather than self-certify.
  That's a business/partnership step before code.
- This is a **launch blocker for Turkey** (can't legally do consumer retail
  without it), so it must be sequenced before a Turkish go-live — even though
  the software POS, `tr` locale, and Turkish receipts already work.
- Pair with the same fiscal-plugin framework for **UAE (FTA)** — one pluggable
  fiscalization seam, per-country implementations.
