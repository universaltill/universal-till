# Turkey (Türkiye) — market entry: law, integration route, competitors

> Status: **research pass, 2026-09-02** (product-owner request: "what do we
> need to do to deliver Universal Till to real shops in Turkey, and who do
> we compete with"). Supersedes the 2026-07-16 backlog note that used to
> live in this file. **Not legal or tax advice** — every item marked
> *(confirm)* needs a Turkish mali müşavir / avukat before the pilot moves
> from shadow mode to system of record.
>
> The canonical compliance register is
> `ut-docs/reference/turkey-compliance.md` (research pass #1158, amended
> #1209). This file covers what that one does not: the concrete
> software-to-device integration route, the pending 2025/26 rule changes,
> the competitor landscape, and a go-to-market checklist. Where the two
> overlap, the ut-docs register wins.

## 0. The one-paragraph answer

A software POS **cannot** be the legal point of sale in Turkey on its own.
Retail sales must be documented by a GİB-approved **Yeni Nesil Ödeme
Kaydedici Cihaz (YN ÖKC)** — a TSM-connected device that signs the receipt
and streams daily Z-reports to the tax authority — or by a bank/payment-
institution-run **VUK 507 secure mobile payment + e-document system**. The
lawful shape for Universal Till is therefore the same as Germany's TSE
pattern (ADR-0025 Decision 2): **our till computes the sale, a partner's
certified device issues the fiscal receipt**, over GİB's published
external-software protocol **GMP-3** or the device maker's REST API. Since
**VUK 593 (8 May 2026)** the same device can issue e-Fatura/e-Arşiv, so we
do not need to build e-invoicing either. What we *do* need: a
`ut-plugin-tax-tr` device plugin, an integrator registration with one or
two ÖKC makers (Token/Beko, Pavo, Hugin are the obvious three), a test
device, a KVKK hosting decision, and a Turkish invoicing route for
subscriptions. Core's fail-closed gate for TR already shipped
(`internal/fiscal.RequiresHardGate`, universal-till#621, ut-docs#1208).

## 1. Legal landscape (as of 2026-09-02)

### 1.1 Fiscal device obligation — Law No. 3100 + VUK communiqués

| Rule | What it says | Source class |
|---|---|---|
| Law No. 3100 | VAT taxpayers selling goods/services at retail (first- and second-class merchants: shops, markets, cafés, restaurants, salons, repair shops…) must use an ÖKC; a new business within 30 days of opening. | Statute, corroborated by TÜRMOB/GİB guides |
| VUK 426 (2013) | Introduced YN ÖKC (IP-based, TSM-connected, fiscal-memory device). | Primary |
| VUK 483 / 527 | Integration of external hardware/software with YN ÖKC; EFT-POS (bank card terminal) must be integrated into the ÖKC; narrow exemptions (art. 6) for taxpayers who issue e-belge for every sale. | Primary (text not fetched from this sandbox; ynokc.gib.gov.tr is egress-blocked) |
| VUK 507 (2019) | **GMÖEBYS** — Secure Mobile Payment & Electronic Document Management System: banks, payment institutions or ÖKC makers together with an *özel entegratör* may run a system where an e-Arşiv invoice ("NİHAİ TÜKETİCİ", ≤ ₺500 originally) replaces the ÖKC receipt. Provider-owned; a software vendor can integrate with such a provider (e.g. Paycell POS, Pavo N86 "VUK 507" mode) but cannot *be* one. | Primary + vendor guides |
| VUK 566 (Sep 2024) | Z-report fiscal data goes to GİB via the ÖKC TSM centres; fallback via özel entegratör e-Arşiv systems or Dijital Vergi Dairesi. | İSMMMO circular |
| Old-generation ÖKC sunset | Eski nesil devices closed to use from **10 Jan 2025**; any device with full fiscal memory or >10 years must be replaced with YN ÖKC. | Multiple advisory sources |
| **VUK 593** (RG 33247, **8 May 2026**) | e-belge (e-Fatura, e-Arşiv and other VUK 509 documents named in GİB technical guides) may be issued **directly from a YN ÖKC**, signed with the device's own fiscal certificate. Makers need GİB technical approval (tests by GİB + TÜBİTAK); approved brand/model list to be published on ynokc.gib.gov.tr and ebelge.gib.gov.tr. | Ten YMM/advisory publications, three read directly (ut-docs#1209) |

**External software rule (the one that governs us):** communication between
a sales application (PC, tablet, cloud, order-taking handheld) and a YN
ÖKC must follow GİB's *"ÖKC – Harici Donanım ve Yazılım Haberleşme
Protokolü GMP-3"* (current v5.0, 2 Aug 2018). Two modes exist in the
field:

- **Wired / "kasa modu"** — the till and the ÖKC share a LAN (static IP);
  the till sends the basket, the ÖKC takes payment (card on its own
  EFT-POS, or cash) and prints the *mali fiş*. Our software never prints
  the fiscal receipt.
- **Wireless / "TSM mode" ("çoklu sepet listesi")** — the till pushes
  baskets to the maker's cloud (TSM); the cashier picks the basket on the
  ÖKC screen and completes it there. Works with no LAN pairing.

Both modes are **enabled per device by the ÖKC maker**, who charges an
annual activation ("GMP3 hizmet bedeli" / "entegrasyon bedeli") per device
and lets the merchant pick the integrator software from a list — i.e.
**we must be registered as an "entegratör firma" with each maker**. No
GİB certification of the external software itself was found; the
qualification burden sits on the maker/TSM. *(confirm with the maker at
onboarding.)* Vendor-quoted integration fees seen: ₺4,500/yr for one
SambaPOS→inPOS licence; Beko/Token and Ingenico/Worldline (iKasa) sell the
activation per device per year through their own portals.

**Bilgi fişi:** when a sale is documented by an invoice (e-Arşiv/e-Fatura)
instead of a receipt, or for non-monetary operations (advance, meal card,
invoice collection), the ÖKC must still print an *information slip*. The
plugin has to model that path, not only "print receipt".

**Card payments in store:** since 483/527 the bank EFT-POS is part of the
ÖKC. For the TR in-store flow the tender is therefore **"pay on the
ÖKC"**, not a separate iyzico/Craftgate terminal. iyzico/Craftgate remain
relevant for online/QR/pay-by-link (ut-docs#114).

### 1.2 Pending: draft Communiqué on Physical and Virtual Payment Systems

GİB published a draft on **9 Jul 2025**, updated **17 Nov 2025**; not found
in the Resmî Gazete as of this pass — **watch item**. Draft content:

- Retail sellers must use **YN ÖKC or GMÖEBYS**, with narrow exceptions.
- Bank POS (seyyar, kablolu, soft POS) only after a GİB system pre-check
  and a merchant undertaking; virtual POS restricted to verified
  e-commerce (dealer exception with minimum dealer count, contract
  notification, fixed IP, slip referencing the principal).
- Effective the 4th month after publication; existing devices get 2
  months to comply.

Effect on us: none of it loosens the ÖKC requirement; it tightens
bank-POS-without-ÖKC workarounds, which strengthens the case for the
device-partner route.

### 1.3 Documents and thresholds — 2026 figures

| Item | 2026 value |
|---|---|
| Invoice (fatura) mandatory at or above | **₺12,000** incl. VAT per customer per day (jewellery ₺36,000); below that an ÖKC receipt suffices unless the customer asks for an invoice |
| e-Fatura (B2B) obligation | gross revenue **≥ ₺3M** in 2024 or 2025 (≥ ₺500k for e-commerce, real estate, vehicle dealers; hotels regardless of turnover) → transition during 2026 *(confirm exact date)* |
| e-Arşiv (B2C / non-e-Fatura recipients) | since **1 Jan 2026** no amount threshold for regular taxpayers; every such invoice is e-Arşiv |
| Basit usul / işletme hesabı taxpayers (VUK 589) | paper invoices allowed up to ₺3,000 until 31 Dec 2026; **all** invoices via GİB e-Belge portal from 1 Jan 2027 |
| e-Adisyon (table-service F&B) | GİB may oblige adisyon-issuing businesses already on e-Fatura/e-Arşiv, with ≥ 3 months' notice; self-service excluded |
| QR code on e-Fatura/e-Arşiv | mandatory since Sep 2023 |
| UBL-TR 1.2.1 package | mandatory since 2 Feb 2026 |
| Z-report | daily on every day with sales; auto-transmitted by YN ÖKC via TSM |
| Record retention | 5 years from the year following the record year (VUK md. 253) |
| KDV rates | 1% / 10% / 20% (since 10 Jul 2023); salon/service rate for the pilot vertical *(confirm, ut-docs#1211)* |

### 1.4 Penalties (2026, VUK 588 amounts)

- Consumer found without a receipt/fiş: **₺8,700 per document**, capped at
  ₺87,000 per calendar year (special irregularity, VUK 353).
- Not using a mandated ÖKC: special irregularity tiers under VUK mük. 355,
  revalued yearly; the 2026 first-tier figures are in the VUK 588 table
  *(exact tier for the pilot shop: confirm)*.
- Fiyat Etiketi Yönetmeliği (service/cover charge ban and price-list
  display, amendment RG 33153, **30 Jan 2026**): **₺3,973** per violation
  under Law 6502 art. 77 — already enforced in the till (ut-docs#962).

### 1.5 Data protection, language, invoicing us

- **KVKK**: Law 7499 (1 Sep 2024) — continuous cross-border transfers need
  an adequacy decision (none issued) or KVKK SCCs/BCRs. Foreign controllers
  register in **VERBİS** through a Turkish representative; 2025 fine range
  ₺272,380–₺13,620,402; 16,350 organisations were investigated in Aug 2024.
  Decision needed: TR-region/on-prem hosting vs SCCs (ut-docs#1210). The
  offline-first till keeps the primary record on the shop's hardware,
  which limits exposure to the sync/backup path.
- **Language**: Law No. 805 obliges Turkish enterprises to keep
  transactions, books and documents in Turkish. Receipts and the operator
  UI must be Turkish — `web/locales/tr.json` already mirrors `en.json`
  key-for-key (1,852 keys), and the manual has a `tr/` tree.
- **Selling subscriptions**: a foreign vendor charging Turkish businesses
  is B2B reverse-charge for KDV, but Turkish shops need a Turkish
  **e-Fatura** to expense the cost, and card payments to a foreign MoR
  are friction for esnaf. Realistic routes: a Turkish reseller/partner
  that invoices locally, or a Turkish entity. Stripe (ADR-0058 MoR) does
  not onboard Turkey-based merchants, which matters only if *we* were
  Turkish; it can still charge Turkish cards. DST (7.5%) is irrelevant at
  our scale.

## 2. Market and competitors

### 2.1 Size of the prize (small independents, our segment)

| Segment | Count | Source year |
|---|---|---|
| Bakkal (corner shops) | ~176,000 | 2025 (AA) |
| Markets incl. discount chains | 55,737 (42,782 discount) | end 2024 |
| Food & beverage businesses | >100,000 (≈70,000 restaurants) | TÜİK 2024 |
| Contactless ÖKC devices in the field | ~2,000,000 | 2025 (bthaber) |
| Cards in circulation | >400,000,000 (largest card market in Europe) | 2025 |

### 2.2 The device layer — gatekeepers, not competitors

Every retail sale runs through one of these; we integrate with them.

| Maker (TSM operator) | Position | Devices / price (Jan 2026 street) | Integration surface |
|---|---|---|---|
| **Token Finansal Teknolojiler (Koç) — "Beko"** | ~800k devices, >50% share; TSM processes ~6.5M tx/day | Beko 300 TR, X30 TR (₺9,200), 400 TR, 1000 TR (Android) | GMP-3 wired; **TokenX Connect** cloud API (client-id/secret, QR terminal pairing); Token Store for on-device apps; developer.tokeninc.com |
| **Pavo (ex-Aktif Bank)** | 250k+ devices, ~20% share; "Best Android POS Provider 2025" | Pavo N86 and Android models; VUK 507 mode available | REST integration (API key from Pavo portal, "Satış Uygulamaları" → REST); PavoPay app store |
| **Hugin** | Long-standing brand, strong in Anatolia | Tiger T300 (₺6,100), VX-675, **S1 Android 12** ("third-party app support") | **Hugin PC Link** — HTTPS REST, no DLL; developer.hugin.com.tr; D10 protocol on Android |
| **Ingenico / Worldline (iKasa)** | Global brand, large installed base | Move/5000F, IDE280/IWE280, Axium DX8000 | GMP-3 wired/wireless; annual "GMP3 hizmet bedeli" bought on ikasa.com.tr, integrator chosen from list |
| inPOS, Paygo, Profilo, Verifone, Vera, Ödeal | Smaller shares | inPOS M530 ₺6,500, Paygo SP630 Pro ₺6,500 | GMP-3 via third-party integrators |
| **Banks** (Garanti BBVA, İş, Akbank, Ziraat, QNB) | Distribute the above, often free of charge against 1.5–3% commission, blocked-settlement deals down to 0% | Bank-branded Beko/Ingenico/Pavo units | Same as the maker |
| **Paycell POS (Turkcell)** | VUK 507 Android device, free SIM/data, e-invoice cost covered | Rental model | Provider-owned flow; integration TBD |

Take-away: **Android ÖKCs with app stores (Beko 400/1000 TR, Pavo, Hugin
S1) are the natural home for a Universal Till Android build** (ADR-0023) —
the fiscal unit, the card reader and our UI on one ~₺6–10k device the
merchant already has to buy. Whether makers admit a third-party APK, and on
what terms, is the first question for the partner conversation.

### 2.3 The software layer — direct competitors

| Product | Segment | Price signal | Notes / weakness vs us |
|---|---|---|---|
| **Adisyo** | cafés/restaurants | from **₺458/mo** | cloud SaaS, needs internet; "start here" pick for small cafés in comparison guides |
| **Simpra** | restaurants/hotels | quote-based | enterprise-leaning, bundled QR menu |
| **robotPOS** | restaurants, chains | quote | 60+ integrations incl. every ÖKC maker, e-Adisyon, couriers; Windows-heavy |
| **Menulux** | cafés/restaurants | integrations from ₺88/mo | *advertises a "hybrid" offline-sync architecture as its differentiator* — confirms offline is a selling point |
| **Karekodgarson** | restaurants | quote | 130+ modules, AI forecasting; breadth over simplicity |
| Optimus POS, GoPOS, Onpos, NarPOS, vRest, Makrops, Kardo, Rita | restaurants | ₺300–1,500/mo range | all cloud subscriptions, all sell ÖKC integration as an add-on |
| **SambaPOS** | restaurants | V5 paid licence; ÖKC integration ₺4,500/yr (inPOS) via LiwaSoft | Turkish-origin, open-source roots (V3), the nearest thing to our licence model; Windows/.NET only, no offline-first story beyond a local DB |
| **BenimPOS**, **Bakkal Defteri**, DemirSoft, Dara, AKINSOFT Wolvox, DİA | bakkal/market/retail | first month free / lifetime licences / yearly | desktop-era retail suites with per-maker ÖKC modules |
| Nebim V3, Logo, Mikro, ikas | mid-size retail / e-commerce | enterprise pricing | out of our segment; integration targets, not rivals |
| Square, SumUp, Zettle | — | — | **not available in Turkey** |
| Loyverse, Odoo POS | app-store / partner installs | free / partner | usable as a till app but no ÖKC path → cannot be the legal point of sale |

What nobody in the table offers together: **free core, offline-first,
open source, Turkish UI, and a maker-agnostic fiscal plugin**. Every
incumbent is a paid cloud subscription with ÖKC integration as an extra
line, and the ÖKC maker's activation fee is passed to the merchant in all
cases — exactly the ADR-0047 posture (mandated third-party cost stays with
the merchant; our free tier never carries it).

## 3. What we need to do

### 3.1 Business / legal (owner + Turkish advisors)

1. **Confirm the pilot shop's taxpayer class** (gerçek usul vs basit
   usul) — decides whether the ÖKC requirement bites on day one
   (ut-docs#1213).
2. **Pick device partners and register as integrator** with Token/Beko,
   Pavo and/or Hugin; obtain a test device or sandbox. This is the
   `blocked:env` on ut-docs#1280 — it needs a person in Turkey, not a
   cloud session.
3. **Check the chosen model is on GİB's VUK 593 e-belge list** before
   promising invoice issuance from the device.
4. **KVKK**: choose TR hosting/on-prem for TR tenants or execute SCCs;
   appoint a representative and register in VERBİS if thresholds apply
   (ut-docs#1210).
5. **Invoicing route** for paid tiers: Turkish reseller/partner or entity.
6. **Mali müşavir opinion**: KDV rate for the pilot vertical (ut-docs#1211),
   VUK mük. 355 tier, and whether the shop will be pulled into
   e-Adisyon.
7. **Watch** the Physical and Virtual Payment Systems communiqué for its
   Resmî Gazete date; re-read this file when it lands.

### 3.2 Engineering (this repo + `ut-plugin-tax-tr`)

1. **`ut-plugin-tax-tr` device plugin** (ADR-0001 process plugin for
   hardware, ADR-0025 Decision 2): implement GMP-3 wired mode first (LAN,
   deterministic, offline-friendly), then one maker REST surface (Hugin PC
   Link or Pavo REST) — basket push, payment result read-back, ÖKC receipt
   number / Z-number stored against our sale, *bilgi fişi* path for
   invoice-documented and non-monetary operations, refund/void mapping.
2. **Tender model for TR**: the card is taken on the ÖKC; our payment step
   becomes "send to ÖKC, wait for result". Cash still closes on the ÖKC.
   Fail closed if the device is unreachable (gate already in place).
3. **Day close**: surface the ÖKC Z-report status in our end-of-day and
   reconcile ÖKC receipt numbers with our sale log (a gap in the ut-docs
   register: "day-close/Z-raporu" and "TSM connectivity" were listed as
   not yet researched — now scoped above).
4. **Android build on the ÖKC itself** as a later, high-leverage form
   factor (ADR-0023) once a maker confirms APK admission.
5. **Country defaults**: `country_settings_repo.go` already ships TR
   (TRY, 20%, tax-inclusive, tr-TR); keep the 1%/10% reduced rates
   configurable per item.
6. Already done and should stay: TR fail-closed gate (#621), service
   charge ban and price display (#962), `yüzde usulü` allocation ledger
   (#965/#987/#988), Turkish locale and help tree.

### 3.3 Suggested sequence

Shadow-mode pilot (till runs beside the shop's existing ÖKC; no fiscal
blocker, KVKK still applies) → integrator registration + test device →
plugin against one maker → system-of-record pilot in one shop → second
maker → Android-on-ÖKC build.

## 4. Confidence and gaps

- GİB primary texts (ynokc.gib.gov.tr, gib.gov.tr, resmigazete.gov.tr) and
  most Turkish vendor knowledge bases are egress-blocked from this
  sandbox; findings rest on convergent secondary sources (advisory firms,
  integrator documentation, search snippets). Same caveat as ut-docs#1158
  and #1209.
- Integration fee amounts vary by maker and were seen only as third-party
  list prices; treat them as order-of-magnitude.
- Market-share figures are maker self-reported (Token, Pavo) and dated
  2023–2025.

## Sources (read or corroborated in this pass)

- Law/communiqué summaries: bizimhesap.com (YN ÖKC transition 10 Jan 2025);
  turmob.org.tr circular 26.09.2024/122-1; ismmmo.org.tr VUK 566 note;
  alomaliye.com, vergidegundem.com, kardemymm.com.tr, bakicelik.com (VUK
  593); mysoft.com.tr and ozan.com (VUK 507); muhasebetr.com, pwc.com.tr,
  vergidegundem.com (2026 penalty tables, VUK 588); bizimhesap.com,
  parasut.com, faturaport.com, stb-cpaturkey.com (2026 invoice/e-Arşiv
  limits, VUK 589); birfatura.com, vrest.com.tr (e-Adisyon);
  fintechistanbul.org, mondaq.com, pwc.com.tr (draft payment-systems
  communiqué, Jul/Nov 2025); alomaliye.com, ticaret.gov.tr, kafe360.com
  (Fiyat Etiketi amendment 30 Jan 2026); ynokc.gib.gov.tr GMP-3 v5.0 PDF
  (title only; body blocked).
- Integration mechanics: benimpos.com, demirsoft.com, akinsoft.net
  knowledge bases (Beko/Token, Ingenico, Hugin, Pavo GMP-3/REST);
  developer.hugin.com.tr (PC Link); developer.tokeninc.com (TokenX
  Connect); diaakademi.com (Pavo REST); kb.sambapos.com (Hugin GMP-3);
  forum.liwasoft.com (SambaPOS inPOS licence ₺4,500/yr).
- Market: tokeninc.com blog and horecamailing.com (Token/Beko share);
  pavo.com.tr / PSM (Pavo share); karekod.org, beko.com.tr, akakce.com
  (device prices); aa.com.tr, marketingturkiye.com.tr, musiad.org.tr,
  TÜİK via oemhoreca.com (business counts); bthaber.com (device and card
  counts); kobitime.com, hesap.com (bank POS terms).
- Competitors: adisyo.com, menulux.com, robotpos.com, karekodgarson.com,
  vrest.com.tr, eprompos.com, optimuspos.com, benimpos.com,
  bakkal-defteri.com, sambapos.org, community.squareup.com (Square not in
  Turkey), loyverse.com.
- KVKK / language / VAT: lexology.com, prighter.com, ibanet.org,
  biscotti-cmp.com (KVKK, VERBİS); karabiyiklaw.com, cailliau-colakel.av.tr
  (Law 805); fonoa.com, avalara.com, commenda.io (foreign vendor VAT).
