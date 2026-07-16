# Sales-channel integrations — delivery, marketplaces, e-commerce, local payments (BACKLOG)

> Status: **backlog**, per-country. Captured 2026-07-16 (Farshid). Plugin-first:
> every one of these is an `integration` (or `payment`) plugin on the connector
> pattern (ADR-0014: settings for keys, `net:*`, offline queue) — the core stays
> neutral. Build against each provider's **sandbox** first (they need free dev
> accounts / API keys, like Stripe).

## Integration shapes (reused across all providers)

- **Delivery / food aggregators** — *inbound orders*: receive an order (webhook
  or poll), create it in the till (held/kitchen order), print a kitchen ticket,
  push status back (accepted/ready). Overlaps restaurant-phone-orders.md + KDS.
- **Marketplaces / e-commerce** — *two-way listing sync*: publish catalog +
  price + stock out; receive orders in and fulfil. Reuses the outbound
  (sale/stock events) + inbound (catalog import) seams.
- **Local payment providers** — `payment` plugins on the authorize seam (like
  the Stripe plugin already built).

## United Kingdom 🇬🇧

- **Delivery / food:** Deliveroo, Uber Eats, Just Eat.
- **Marketplaces / sell items:** Shopify, Amazon (Selling Partner API), eBay,
  Etsy, **Vinted** (second-hand), Depop, Meta/Facebook Marketplace & Shops.
- **Payments / BNPL:** Klarna, Clearpay; card terminals in device-plugin-suite.md.

## Turkey 🇹🇷

- **Delivery / food:** **Yemeksepeti**, Getir, Trendyol Yemek, Migros Yemek.
- **Marketplaces / e-commerce:** **Hepsiburada**, **Trendyol**, **N11**,
  Çiçeksepeti, Pazarama, PttAVM, Amazon.com.tr.
- **Payments:** Turkish bank VPOS + gateways (Garanti, Ziraat, İşbank/NestPay,
  iyzico, PayTR) — device-plugin-suite.md; fiscal ÖKC — turkey-fiscal-compliance.md.

## UAE 🇦🇪

- **Payments (priority — esp. what Ansar uses):** Network International
  (N-Genius), Magnati, Mercury, Telr, PayTabs, Ziina, Amazon Payment Services
  (PayFort); tap-to-pay + Apple/Google Pay. *(Confirm Ansar's acquirer when the
  relationship allows — likely Network International or their bank's VPOS.)*
- **Delivery:** Talabat, Deliveroo UAE, Careem, Noon Food.
- **Marketplaces / e-commerce:** Noon, Amazon.ae.

## Notes

- Each provider = one reusable plugin, per-install config; prioritise by the
  live lead (UAE payments for Ansar; Turkey delivery + e-commerce + fiscal for
  the Turkey launch; UK marketplaces for the home market).
- Many share machinery: a **generic marketplace-order connector** and a
  **generic listing-sync connector** can be the base that provider plugins
  specialise (like the webhook connector is the base for ERP connectors).
- Related: plugin-integration-roadmap.md (build queue), device-plugin-suite.md
  (payments/fiscal devices), restaurant-phone-orders.md (orders + kitchen),
  [[plugin-first]].
