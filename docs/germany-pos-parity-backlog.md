# Germany POS parity backlog — competitive analysis (Nima's system, 2026-07-28)

Source: photos + video of a working German café's current setup, shared by
Farshid's friend Nima (`/Users/farshid/repos/unitill/nima/`, not committed —
personal photos of someone else's hardware/software). Software is a branded
POS called **"speedy"** running on **iMin Falcon 1** hardware (Android POS
device, model I22T01, 4GB+32GB), paired with a **PAX A920** payment terminal
running a separate app ("SECpos EVO", provided by POS-Cardservice — a German
payment terminal integrator) and an **NT-1200** handheld barcode scanner.

**Update 2026-07-28 (a):** Farshid transcribed/translated the video by hand
(self-hosted Whisper's "base" model couldn't handle the Persian audio
reliably). That content is now incorporated below — see the new VAT
switching section, and the updated Gutschein/FiBu detail.

**Update 2026-07-28 (b):** Nima also mentioned SumUp's till app is "really
modern with lots more features" but he didn't switch because SumUp's
support isn't good. Farshid asked me to install and test the SumUp app
directly — I can't: it needs a real phone/tablet plus a verified SumUp
merchant account (KYC, business/bank details), neither of which I have or
can create, and there's no functional online demo (their "free online
demo" link is a sales lead-capture form, confirmed by fetching it — three
steps of business details, ends in "our team might call you," no product
access). Researched from SumUp's own public help docs and feature pages
instead — see the new section below. Labeled clearly where a claim is
sourced from documentation vs. actually observed running.

## 📋 SumUp — researched, not hands-on tested

**POS Lite hardware**: $499 bundle, 13" full-HD splashproof touchscreen +
SumUp Solo card reader, no monthly subscription fee (standard card
processing fees still apply per transaction).

**Confirmed: SumUp has the exact same eat-in/takeaway VAT feature Nima is
asking for**, and the mechanism is documented precisely enough to use as a
reference:
- Back office (web dashboard): Settings → Global Settings → "Enable
  Eat-in/Takeaway Product option" → Save — turns the feature on, scoped to
  products (implying not every product needs to participate, e.g. bottled
  goods might always be one rate).
- On the till: cog icon (top-right) → Settings → General Settings →
  "Enable Eat-in/Takeaway" → "Enable Prompt" — makes the till ask at each
  sale whether it's eat-in or takeaway, and it applies the correct VAT
  rate automatically based on the answer.
- This is good validation that Universal Till's planned approach (an
  `OrderType`-driven tax rate override, see above) is the right shape —
  SumUp solves it exactly the same way, not some more clever mechanism I'm
  missing.

**Other features confirmed via SumUp's own marketing/help pages** (not
independently verified running):
- Inventory: product catalog with variations/pricing, stock alerts,
  multi-location inventory tracking
- Staff: role assignment, access permissions, performance tracking
  (higher tiers add shift scheduling — not in POS Lite specifically)
- Reporting: real-time sales/performance dashboards, tax reporting insights
- Tipping: preset tip amounts/percentages, prompted on the Solo reader
  (matches what we already confirmed independently via their developer
  API docs earlier)
- Refunds: built into the core flow
- Multi-location: manage sales/inventory/staff across stores from one
  dashboard
- Higher-tier plans (not POS Lite) add: loyalty programs, automated
  marketing, appointment booking, SMS/email marketing

**Correction after checking SumUp's German-locale docs specifically**
(Farshid's prompt to check per-country help pages, not just en-GB): SumUp
**does** support German TSE/KassenSichV compliance — I'd missed this by
only checking the English and generic German homepage, not the
`/de-DE/pos/` article path. Real detail, from `help.sumup.com/de-DE` and
`sumup.com/de-de/kassensicherungsverordnung`:
- TSE is **not built-in by default** — activated separately via
  dashboard ("TSE-Konfiguration" tab), a few clicks, automatic once
  business info (incl. VAT ID) is complete.
- **Two options**: "Cloud TSE" (software-only, activated per till) or a
  TSE-integrated hardware printer (module valid 30 months, then
  replacement). Cloud TSE looks like a paid annual subscription (10-year
  mandatory data retention mentioned).
- **Powered by fiskaly** — a real, well-known third-party Cloud-TSE
  provider. Their own compliance-check app description explicitly cites
  both KassenSichV *and* DSFinV-K, so the underlying data is DSFinV-K
  export-ready — I could not confirm whether SumUp's own POS UI has a
  one-click DSFinV-K export button the way Nima's "speedy" does
  (`Kassenprüfung DSFinV-K` in its Export menu), only that the TSE layer
  produces DSFinV-K-compliant data.

**One real architectural implication for us**: "Cloud TSE" being
*cloud*-based sits in tension with offline-first — if a till can't reach
the TSE service, can it still legally sign a transaction? SumUp's
hardware-TSE-printer alternative avoids that (the security module is
local). Worth deciding deliberately for `ut-plugin-tax-de` rather than
defaulting to whichever's easier to integrate — see ADR-0025.

**Still genuinely unverified**: DATEV/FiBu one-click export, Gutschein-
style stored-value voucher cards. Possible Nima chose "speedy" over
SumUp for these two specifically rather than fiscal compliance broadly,
now that TSE turns out to not be the gap I first assumed.

Sources: [SumUp POS Lite](https://www.sumup.com/en-us/pos/pos-lite/),
[SumUp eat-in/takeaway setup](https://help.sumup.com/en-GB/articles/58dOVySPiAaE7JAeYKpvTv-set-up-my-eat-in-or-takeaway-option),
[SumUp order types](https://help.sumup.com/en-US/articles/1kZVZnQfoNjRMvDcXeEZWT-order-types),
[SumUp tax rate customization](https://help.sumup.com/en-US/articles/3iutaQUy0CIU1JGR8OUVQn-customize-tax-rates)

## 🔴 VAT rate must switch on dine-in vs. takeaway (new, from the video)

Nima, walking through a sale: ordering a coffee, the till asks "eat in the
shop or take away" — pressing "ToGo" isn't just a label, **it changes the
tax rate**: **19% VAT for eat-in, 7% VAT (Germany's reduced rate) for
takeaway**, specifically for drinks. He was explicit that **cakes are 7%
regardless of dine-in or takeaway** — so this isn't a single till-wide
toggle, it's a per-item-category rule where some categories are pinned to
one rate and others (drinks) switch based on consumption context. He
called this out as something the till "must definitely have."

This is real German tax law (§12 UStG's reduced rate for food/drink not
consumed on-site), not a "speedy"-specific quirk — any till operating in
Germany needs this to issue correct receipts.

**Checked against the current codebase — this is a well-scoped gap, not a
rebuild:**
- Per-line `TaxRateBasisPoints` already exists (`internal/pos/sales.go`) —
  the sale/line model already supports a tax rate per line item.
- An `OrderType` field already exists (`internal/print/kitchen.go`:
  "dine-in / takeaway / delivery / phone") — but it's **only used for
  kitchen ticket printing**, not wired into tax calculation anywhere.
- **The actual gap**: nothing connects `OrderType` to `TaxRateBasisPoints`.
  Needs: (a) a per-item-category "reduced-rate-eligible" flag or a
  dine-in-rate/takeaway-rate pair instead of a single tax rate, (b) the
  sale flow re-deriving each eligible line's tax rate when `OrderType`
  changes (including switching an already-added line if the customer
  changes their mind mid-order, which the video shows happening).

## Tip flow, confirmed from the video

Matches what Farshid already said: on the current PAX/SECpos setup, the
cashier manually asks "do you want to tip?", keys the tip into the till
**before** pressing "pay by card" (which is what actually sends the total
to the PAX terminal) — the terminal has no way to feed a tip back on its
own. This confirms the SumUp appeal is specifically removing that manual
step, not a difference in what tip data ends up in the till.

## 🔴 Critical / legal blocker for Germany

**German fiscal compliance (KassenSichV) — Universal Till has none of this
today** (verified: zero matches for TSE/DSFinV-K/DATEV/GoBD anywhere in
`universal-till`). This isn't a competitive nice-to-have, it's a legal
requirement for operating a cash register commercially in Germany since
2020:

- **TSE (Technical Security Equipment)** — every transaction must be signed
  by a certified security module (hardware dongle or cloud TSE service).
  Nima's system has this (implied by the DSFinV-K export existing at all —
  it can't be generated without a TSE signing every sale).
- **DSFinV-K export** — confirmed directly in the screenshots: Export menu
  → "**Kassenprüfung DSFinV-K**". This is the standardized digital
  interface tax auditors require for a `Kassennachschau` (cash register
  audit). No DSFinV-K capability = the till cannot legally be audited =
  effectively cannot be used commercially in Germany.
- **GoBD-compliant immutable audit log** — Universal Till already has an
  audit trail page (`docs/code-reviews/2026-07-24-audit-trail-page.md`) —
  worth checking whether it meets GoBD's specific immutability/completeness
  requirements or just logs changes generally.

**This blocks the German prospect** (`prospect-german-shop-migration`
memory) from being a real production migration, not just a demo, until
solved. Needs its own research spike: cloud TSE providers (e.g. fiskaly,
epson TSE) usually expose a REST API — likely a new
`ut-plugin-fiscal-de` (or core feature, given it's legally mandatory rather
than optional) that (a) calls a TSE provider to sign each transaction, (b)
generates DSFinV-K export on demand, matching Nima's system's per-date-range
export UX.

## 🟠 Tips: SumUp reader → till auto-sync

Nima's specific complaint: his current PAX A920 terminal (running the
separate "SECpos EVO" app) lets the customer pick a tip on the card
terminal, but the till software has **no idea** — staff have to manually
key the tip amount into "speedy" afterward. He says SumUp doesn't have this
problem (tip selected on the SumUp reader flows back into the till
automatically) and prefers it for that reason alone.

Checked SumUp's actual behavior before writing this: confirmed via SumUp's
own docs — the **SumUp Solo** reader prompts the customer for a tip
(fixed amount or %) at checkout, and SumUp's **Cloud API** (Transactions
endpoint) returns the tip as part of the transaction result, so it's a real
API field, not a walled-garden POS-Lite-only feature.

**Gap, precisely scoped:**
- `ut-plugin-payment-sumup` already exists, already drives a paired SumUp
  reader via the Cloud API for the authorize-before-tender flow (see its
  README) — but **has zero mention of tips anywhere in `src/main.go`**.
- **Universal Till's core domain model has zero tip concept at all** —
  confirmed, no `tip` field anywhere in `internal/`. This is bigger than a
  plugin change: it needs a `tip_amount` on the sale/payment record, a
  receipt line, and probably a reporting/export line (tips are often
  handled separately for German payroll/tax purposes — same "ask an
  accountant" pattern as the DSFinV-K item above, worth confirming rather
  than assuming).
- Once the core model has a tip field, `ut-plugin-payment-sumup` reads it
  back from the reader's transaction result and sets it — same shape as
  the existing authorize-then-poll loop it already has for payment status.

## 🟡 Feature gaps found in "speedy" (from the screenshots)

Grouped by what Universal Till already has vs. doesn't, so this isn't a
flat wishlist — check each against the current codebase before assuming
it's missing; I did that for the two flagged above but not exhaustively
for the rest below (time-boxed to what the photos actually showed).

**Sales screen:**
- Color-coded, nested category grid for the menu (categories: Espresso
  Spezialitäten, Kaltgetränke, Frühstück/Bagels, Kuchen, Zubehör Verkauf,
  etc. — cafés need deep category trees, not a flat product list)
- Per-item modifier/variant picker (the `<Neu>` marker next to prices —
  looked like "tap to configure a variant" e.g. milk type, size)
- A dedicated "ToGo" toggle modifier (dine-in/takeaway — see the VAT
  section above, this is where it actually matters)
- **Named open tabs** ("Offene Belege"/open receipts, tabs like "Haaft 1")
  — multiple concurrent open orders held by name, not just one active
  basket
- **German bottle/cup deposit systems**: "Pfandrückgabe" (bottle deposit
  return) and specifically **Recup** (a named German reusable-cup deposit
  scheme) as first-class menu buttons — worth checking if any existing
  plugin covers deposit/Pfand handling at all

**Back office / settings:**
- **Pricing rules engine** ("Preisfindungsregeln") — separate from flat
  per-item pricing; likely time-based (happy hour) or quantity-based
- **Multi-location inventory** ("Lagerorte" — storage locations, plural)
- **Customer groups + customer accounts** with **customer credit/balance**
  ("Kundenguthaben") — loyalty-adjacent, check against any existing
  loyalty plugin
- **Voucher ledger** ("Gutscheinbuch") — confirmed exact UX from the video:
  Nima makes his own barcoded Gutschein cards, sells one for e.g. 30 EUR,
  the customer's balance decrements each visit as they scan it and order,
  until it hits zero. So: a stored-value card tied to a barcode, balance
  tracked server-side, redeemable in partial amounts across many visits —
  not a one-time discount code.
- **Loyalty stamp card** — Nima mentioned this as something *other* cafés
  do ("every 10th coffee free"), not confirmed as part of his own system.
  Noted as a possible nice-to-have, not a confirmed requirement.
- **Staff time tracking** ("Arbeitszeiterfassung") — clock in/out,
  separate from the user-account/role system
- **In-app support ticket** ("Supportanfrage") — files a support request
  from inside the till UI itself

**Export/Import (the big one Nima mentioned — "lots of export import"):**
Every category below is independently exportable, date-ranged, via
Email/Save-to-file/Share/Excel:
- Full database backup (one-click, not just per-table)
- Per-category config export/import (items, categories, discounts,
  pricing rules, customers, users, printers — round-trippable, not
  export-only)
- **Endabrechnungen** (end-of-period settlement/Z-report) in three
  granularities: summary, per-transaction, per-item
- **Endabrechnungen FiBu Buchungsstapel** — a DATEV-style accounting
  booking-batch export. **Confirmed directly from the video** — Nima
  called this "one of the most important things it must have": at the end
  of each month he presses one button, it exports everything the till did,
  and sends it to his tax/accounting person automatically. DATEV is the
  standard German accounting/bookkeeping software format, "Buchungsstapel"
  (booking batch) is DATEV's own import-file term. **This is his single
  highest-priority item**, by his own framing, not just something I
  inferred from the menu.
- Change/audit log export
- Stock level export
- Time-tracking export

**Nima said he can pull a full backup of his real data** if useful for
testing import — worth taking him up on that once there's an import path
to actually test against (currently there isn't one beyond the plugin's
own scope).

## Confirmed NOT gaps (checked against real code, not assumed)

To keep this list honest rather than inflating scope — these three things
from the video/photos are already implemented in Universal Till:
- **Cash drawer auto-open** — `internal/print/escpos.go`'s `KickDrawer`
  sends the standard ESC/POS drawer-kick command already.
- **Change calculation on cash tender** — `internal/pages/pos_api.go`'s
  `/api/pos/tender` endpoint already returns a `Change` amount.
- **Receipt/invoice numbering** — extensive existing support
  (`internal/data/invoice_repo.go`, `internal/pages/invoice_page.go`, and
  more) — Nima saying "we should have numbers too" was him not knowing
  this already exists, not a real gap.

## What I could NOT do myself

- **Could not "download and test SumUp"** — SumUp POS Lite is a real
  mobile/tablet app tied to a live merchant account; that needs a phone/
  tablet and someone to actually create a test SumUp merchant account,
  neither of which I have access to. I *did* verify SumUp's tip-sync
  behavior and Cloud API shape via their public developer docs (see the
  tips section above) rather than guessing.
- **Did not start implementing any of this.** This is a genuinely large
  set of features spanning legal compliance, core domain model changes,
  and several plugins — worth prioritizing together (the 🔴 fiscal item
  is the one I'd argue is non-negotiable for Germany specifically; the
  rest is a real product-scope conversation, not a solo engineering call).
