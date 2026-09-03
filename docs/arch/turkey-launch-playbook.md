# Türkiye launch playbook — step by step, in plain words

> For the owner and whoever runs the Turkey pilot. Written 2026-09-02 from
> the research in `turkey-fiscal-compliance.md` (this folder) and
> `ut-docs/reference/turkey-compliance.md`. **Not legal advice**: steps
> marked 🧑‍⚖️ need a Turkish accountant (mali müşavir) or lawyer to confirm.
> Each step says *who* does it, *what* to do, and *why*.

## The words you will hear (glossary)

| Word | What it is | Why it matters to us |
|---|---|---|
| **GİB** (Gelir İdaresi Başkanlığı) | Turkey's tax authority, part of the Ministry of Treasury and Finance. Runs the cash-register rules, e-invoicing and the approved-device lists. Web: gib.gov.tr, ynokc.gib.gov.tr, ebelge.gib.gov.tr. | Every rule below comes from GİB. We never talk to GİB directly; the device maker does. |
| **ÖKC** (Ödeme Kaydedici Cihaz) | "Payment recording device" = a cash register the tax office trusts. | A shop must have one. Our software is not one. |
| **YN ÖKC** (Yeni Nesil ÖKC) | The current generation: internet-connected, tamper-proof, signs each receipt, sends daily totals to GİB. Old ones were switched off on 10 Jan 2025. | The shop needs a YN ÖKC. We drive it. |
| **Yazarkasa POS** | Everyday name for a YN ÖKC that also has a bank card reader built in. Most devices are this. | The card payment happens *on this device*, not in our app. |
| **TSM** (Trusted Service Manager / "ÖKC TSM Merkezi") | The device maker's secure cloud that every YN ÖKC is connected to. It relays receipts and daily reports to GİB. | Explains why the maker, not GİB, is our gatekeeper. |
| **Mali fiş** | The legal receipt the device prints, with its fiscal signature. | Our till may show and format the sale; the mali fiş must come from the device. |
| **Bilgi fişi** | An "information slip" the device prints when the sale is documented some other way (an invoice) or has no money movement. | Our plugin must handle this path too. |
| **Z raporu** | The device's daily closing report, sent automatically to GİB every day the shop sells. | Our end-of-day should show it and reconcile with our own totals. |
| **GMP-3** | GİB's published protocol for connecting "external software" (us) to a YN ÖKC over the shop's LAN. Version 5.0, 2018. | The wire we build against, plus each maker's REST API. |
| **Entegratör** | A software company that a device maker has registered as allowed to talk to its devices. | Universal Till must become one, with each maker we support. |
| **e-Fatura / e-Arşiv** | Electronic invoices. e-Fatura between registered businesses, e-Arşiv to consumers and everyone else. Since May 2026 an approved YN ÖKC can issue them itself. | We do not build invoicing; we pick a device that can. |
| **e-Adisyon** | Electronic table bill for restaurants and cafés with table service. GİB pulls businesses in with three months' notice. | Later concern for the café/restaurant vertical. |
| **KDV** | Turkish VAT: 1 %, 10 %, 20 %. | Already configurable per item in the till. |
| **Mali müşavir** | Certified accountant. Every Turkish business has one. | Your first phone call for the tax questions below. |
| **Basit usul** | "Simplified method" taxpayer class for very small traders. Lighter paperwork until 2027. | Decides whether the pilot shop needs the device on day one. |
| **KVKK** | Turkey's data protection law (like GDPR). **VERBİS** is its registry of data controllers. | Where the shop's data is hosted, and whether we register. |
| **Law 805** | Requires Turkish businesses to keep documents in Turkish. | Our Turkish UI and receipts already cover this. |

## The picture in one sentence

The shop buys a YN ÖKC from a maker such as Beko, Pavo or Hugin. Our till
runs on a tablet, PC or the device itself, sends each basket to the device,
the device takes the money and prints the legal receipt, and the maker's
cloud reports to GİB. We are the screen and the brain; the device is the
legal record.

## Steps for you (business and legal)

### Step 1 — Pick the pilot shop and ask its accountant two questions 🧑‍⚖️
- **Who:** you, with the shop owner and their mali müşavir.
- **Ask:** (a) "Is the shop *gerçek usul* or *basit usul*?" (b) "Which KDV
  rate applies to what they sell?"
- **Why:** (a) decides whether the ÖKC rule bites immediately (ut-docs#1213).
  (b) sets the default rate in the till (ut-docs#1211).
- **Takes:** one phone call.

### Step 2 — Look at the device the shop already has
- **Who:** you or the shop owner.
- **Do:** read the brand and model off the device (Beko 300 TR / X30 TR,
  Pavo N86, Hugin Tiger or S1, Ingenico Move/5000F, inPOS M530, …). Note
  which bank issued it.
- **Why:** we integrate with the maker the pilot shop already uses. That
  is the first maker we support.
- **Takes:** two minutes.

### Step 3 — Register Universal Till as an integrator with that maker
- **Who:** you (needs a Turkish phone number and, usually, a company name;
  see Step 6).
- **Do:** contact the maker's integration desk. Known entry points:
  - Token / Beko: developer.tokeninc.com (TokenX Connect), payment devices
    solution centre 0850 250 07 67.
  - Pavo: pavo.com.tr, ask for the "satış uygulaması entegrasyonu" REST API
    key process.
  - Hugin: developer.hugin.com.tr (PC Link REST API).
  - Ingenico / Worldline: ikasa.com.tr, "GMP3 hizmet bedeli" and integrator
    listing.
- **Ask for:** integrator registration, the GMP-3 and REST documentation,
  a **test device or sandbox**, the annual per-device activation fee, and
  whether they admit a third-party Android app on their Android models.
- **Why:** without this nothing else can be built. This is the block on
  ut-docs#1280.
- **Cost signal:** a device is ₺6,000–₺10,000; activation fees are per
  device per year and set by the maker (third-party list prices seen
  around ₺4,500/yr for one brand). The shop pays the device and the fee,
  we never do (ADR-0047).
- **Takes:** days to weeks, depending on the maker.

### Step 4 — Get one test device to whoever will do the engineering
- **Who:** you.
- **Do:** buy or borrow the pilot shop's model and put it on the same LAN
  as a development laptop. Ask the maker to enable integration on it.
- **Why:** the plugin cannot be finished against documents alone; every
  maker's real behaviour differs.

### Step 5 — Check the chosen model is on GİB's e-belge list
- **Who:** you or the mali müşavir.
- **Do:** look up the model on ynokc.gib.gov.tr (approved devices) and, when
  GİB publishes it, the VUK 593 e-belge-capable list on ebelge.gib.gov.tr.
- **Why:** if the device can issue e-Arşiv itself, the shop needs no
  separate invoicing system and neither do we.

### Step 6 — Decide how Turkish shops will pay us 🧑‍⚖️
- **Who:** you and Farshid, with an accountant.
- **Options:** (a) a Turkish partner or reseller who invoices shops in
  Turkey and pays us; (b) a Turkish company (Ltd. Şti.) of our own; (c)
  invoice from abroad (works legally as reverse-charge VAT, but Turkish
  shops want a Turkish e-Fatura and card payments to a foreign company are
  friction).
- **Why:** the free tier needs none of this; any paid tier does.
- **Recommendation:** start with (a) for the pilot; revisit (b) once there
  are paying shops.

### Step 7 — Decide where Turkish shops' data lives 🧑‍⚖️
- **Who:** you, with a lawyer who knows KVKK.
- **Options:** keep TR data on the shop's own hardware and, for cloud
  features, host in Turkey; or sign KVKK standard contractual clauses for
  our EU hosting.
- **Why:** since Sep 2024 ongoing transfers abroad need either an adequacy
  decision (none exist) or those clauses. Fines are large. (ut-docs#1210.)
- **Also:** if we become a data controller for Turkish people's data, a
  Turkish representative and VERBİS registration may be required.

### Step 8 — Keep an eye on one pending rule
- **What:** GİB's draft "Physical and Virtual Payment Systems" communiqué
  (July and November 2025 drafts). It tightens bank-POS-without-ÖKC and
  gives four months after publication.
- **Do:** ask the mali müşavir to tell you when it is published.

### Step 9 — Run the pilot in two phases
1. **Shadow mode** (can start as soon as the till is installed): the till
   runs beside the shop's existing device; the device stays the legal
   record. No fiscal blocker. KVKK still applies, so Step 7 first.
2. **System of record** (after the plugin is done): the till drives the
   device. The till already refuses to complete a sale in Turkey if no
   device is configured, so this cannot happen by accident.

## Steps for engineering (what we build, in order)

These are ours. **Status 2026-09-03:** E2 and E3 are built and tested
(`plugins/tax-tr`, `scripts/okc-sim`, `/fiscal-device`, the tender path
and both receipt renderers); E4 exists as a working plugin with the bridge
driver complete and every maker driver as a fail-closed scaffold. What
remains needs Step 3–4 above (a maker's integrator pack and a device on a
LAN) plus the ADR in E1.

### E1 — Write the ADR for the Turkish device seam (ut-docs, document-first) — *open*
Germany's `fiscal.sign.ask` fires *after* payment and lets the sale
proceed unsigned if the signer is down (ADR-0044). Turkey is different:
the device *takes the payment and prints the receipt*, so it must be
called *at* tender and the sale cannot proceed without it. The code now
does exactly this: the ÖKC plugin implements the existing blocking
`payment.<key>.authorize` seam (payment-provider contract) as a "pay on
device" tender, answers with a `fiscal_device` evidence block core
persists (`internal/fiscal/device.go`, `fiscal_device_receipts`), and the
first receipt flips `fiscal.tse_configured`. No new extension point was
added — it reuses one ADR-0041 already migrated — but the additive
authorize payload (`currency, total, tax_inclusive, lines[]`) and the
evidence block are contract changes that need recording in ut-docs
(`reference/payment-provider-contract.md`) and an ADR registering the
"device market" reading of `fiscal.tse_configured`.

### E2 — Build an ÖKC simulator — *done*
`scripts/okc-sim` (library in `plugins/tax-tr/okc/sim`): a stateful
stand-in device on TCP speaking the bridge protocol — receipt counter, Z
counter, idempotency on the till's event id, and switchable failure modes
(decline all, silent, slow). `go run ./scripts/okc-sim` and point the
plugin at it. Used by the driver tests and by the runtime test that runs
the real `.wasm` through the till's WASM host.

### E3 — Core: "pay on device" tender and record fields — *done*
- The authorize payload now carries the basket (`lines[]` with VAT rates,
  `currency`, `total`, discounts) so a device can print; a declined,
  silent or unreachable device refuses the tender and keeps the basket
  (the existing decline toast, no modal).
- `fiscal_device_receipts` stores receipt number, kind, serial, Z number
  and issue time per sale and per refund (`data.FiscalDeviceReceipt`);
  both receipt renderers print them.
- `/fiscal-device` (manager, Türkiye + plugin active): plugin state and
  device address, confirmed/not, last receipt, today's count, confirm and
  unpair with audit. Help topic `fiscal-device` in `en`, `tr`, `fa`, `ar`.
- Still open: showing the device's Z status inside end-of-day (today the
  page shows the last Z number the device reported).

### E4 — `ut-plugin-tax-tr` against the first maker — *plugin built, maker drivers pending*
`plugins/tax-tr` (WASM, `tcp:*`): sale, refund and settle legs, settings
for driver/host/port/timeouts, the **bridge** driver complete and tested
end to end through the till's runtime. `gmp3`, `hugin-pclink`,
`pavo-rest` and `token-x` are scaffolds that refuse every tender with a
clear log line until filled in from the maker's integrator pack against a
test device (Steps 3–4). Moves to its own repo when first published.

### E5 — Second maker, then Android-on-device
Add the pilot region's second most common maker. If a maker admits
third-party apps, build the Android till for their device so the shop
needs one box only (ADR-0023).

## What is already done (no action)
- Turkish UI and manual, key-for-key with English.
- Turkey defaults in setup (TRY, 20 % VAT included in price, tr-TR).
- The till refuses to complete a Turkish sale as system of record without a
  configured signer (universal-till#621).
- Service, table and cover charges are blocked for Turkish shops
  (30 Jan 2026 rule).
- Staff percentage-share ledger ("yüzde usulü").

## Your next three actions
1. Call the pilot shop's accountant (Step 1) and read the device model off
   the counter (Step 2).
2. Contact that maker's integration desk and ask for the developer pack
   and a test device (Steps 3–4).
3. Tell engineering which maker it is; E1–E3 start meanwhile.
