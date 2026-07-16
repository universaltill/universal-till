# Plugin & integration build-queue

Operationalizes the **plugin-first** rule: everything vertical- or country-
specific ships as a plugin on a core seam. This is the single map of the plugin
ecosystem — what exists, what's planned, and what unblocks each — so work can be
picked up in dependency order. Updated 2026-07-16.

## Core seams that plugins ride (built)

| Seam | Status | Used by |
| --- | --- | --- |
| `sale.completed` event (rich payload) | ✅ | ERP/webhook connectors |
| `stock.adjusted` event | ✅ | inventory sync connectors |
| `payment.<key>.authorize` (blocking, pre-tender) | ✅ | payment terminals |
| `settings_get` + `net:*` host functions | ✅ | any configurable connector |
| Plugin storage (offline queue) | ✅ | connectors' retry queues |
| Catalog import service (`catimport`) | ✅ (+stock/dept) | inbound ERP import |
| Device/process runtime (ADR-0001) | ✅ | hardware: ÖKC, terminals |
| Menu/page/report/theme/language types | ✅ | UI + content plugins |

## Plugins that exist

| Plugin | Type | Status |
| --- | --- | --- |
| Webhook / ERP connector (reference) | integration | ✅ built, e2e-proven |
| AI Assistant (self-hosted Ollama) | integration | ✅ |
| Demo card terminal | payment | ✅ |
| QR Pay | payment | ✅ |
| Themes (midnight, buttons-left, screen-top) | theme | ✅ |
| Languages (de, es) | language | ✅ |
| FAQ | page | ✅ |

## Build queue (planned) — in dependency order

Each is a clone/extension of an existing seam; core does not change.

1. **SAP connector** (integration) — clone the webhook connector, override the
   `postSale` transform to SAP IDoc/BAPI/OData. *Blocked on: Ansar IT endpoints.*
2. **MS Dynamics 365 / LS Central connector** (integration) — same, Business
   Central OData. *Blocked on: Ansar IT endpoints.*
3. **Inbound ERP sync connector** (integration + scheduler) — pull catalog/
   price/stock via ERP OData → core import service (docs/arch/erp-inbound-
   import.md). *Blocked on: endpoints; and a scheduled-pull seam.*
4. **TR ÖKC fiscal** (device) — drive a certified New-Generation Cash Register;
   the device issues the legal receipt + GİB reporting (turkey-fiscal-
   compliance.md). *Blocked on: certified Turkish hardware partner.*
5. **TR e-Fatura / e-Arşiv** (integration) — push invoices to an özel entegratör.
   *Blocked on: integrator contract.*
6. **UAE FTA e-invoicing** (integration) — when the mandate's integrator/format
   is set. Core invoice already does TRN + "Tax Invoice" + VAT breakdown.
7. **Payment terminals** (payment/device), per market — UK (Dojo/Worldpay/SumUp/
   Stripe), UAE (Network/Magnati/Mercury), China (Alipay/WeChat/UnionPay), Iran
   (Shaparak/local). Ride the authorize seam. *Blocked on: per-provider SDK +
   credentials + certification.* (device-plugin-suite.md)
8. **Restaurant / hospitality** (integration + device) — phone-order voice→
   translate→kitchen (restaurant-phone-orders.md), KDS. *Needs: hospitality
   order model + kitchen print (✅ ticket render done) + self-hosted STT.*
9. **Product-search network publisher** (integration) — store publishes catalog+
   stock to the "find it near me" index (product-search-network.md). *Blocked on:
   the cloud search service + multi-store tier.*

## What's needed from OUTSIDE (the real unblockers)

- **Ansar IT** — SAP + Business Central API endpoints/specs → connectors 1–3.
- **Certified partners** — Turkish ÖKC vendor, e-Fatura/UAE integrators → 4–6.
- **Payment providers** — SDKs + merchant credentials + certification → 7.
- **Cloud/multi-store hosting** — the search network + head-office consolidation.
- **Apple Developer account** — notarize the mac desktop app (not a plugin, but
  the last polish on the webview app).

Everything on OUR side (seams, the reference connector, import, print, events)
is done. The queue above is now "clone a template + wire the provider," gated
mostly on third-party access, not our platform.
