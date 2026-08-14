# Subscription, pricing, and monetization plan (2026-07-28)

Addresses: admin-grantable free/discounted subscriptions, time-limited
campaigns, plugin gating after a trial lapses, no-card-required free
periods, which payment processor to bill merchants with, competitive
pricing research, accounting-software integrations, and letting
third-party POS systems pay for Universal Till Cloud API access.

**What this is, and isn't**: pricing numbers and processor choice are
real recommendations, grounded in research below, but they're business
calls for Farshid to confirm — not something I can unilaterally decide.
The *mechanisms* (how admin-granted subscriptions work, how gating
works) are closer to real architecture decisions and are flagged below
as needing their own ADR once the business shape is confirmed.

## 1. Admin-grantable subscriptions (free forever, time-limited, campaigns)

**Recommendation: build this on Stripe Billing's native coupon/trial
primitives, don't invent a custom discount engine.** Checked Stripe's
actual capabilities before recommending, not assumed:

- **Coupons/promotion codes** are created at the merchant (that's us)
  level, scoped to a subscription/invoice/specific item, with a
  percentage-off or fixed-amount-off, and a duration: **a single
  invoice, N billing cycles, or forever**. This directly covers "free
  forever for specific users" (100%-off, forever duration) and "1
  month free" campaigns (100%-off, 1 cycle duration) with zero custom
  billing logic — Stripe already tracks and enforces the expiry.
- An admin-facing "grant subscription" action in the merchant portal
  would just be: create a Stripe coupon (or reuse a campaign-wide one)
  and apply it to that merchant's subscription. Simple to build on top
  of, not a reason to avoid Stripe Billing.
- **No card required for the free period**: confirmed this works —
  a subscription can be created without a payment method attached at
  all while a 100%-off coupon keeps the invoice total at zero; Stripe
  only requires a card once an actual non-zero charge is about to
  happen (i.e., when the free period ends). This matches "for the free
  time I don't need credit card" exactly, without a custom-built
  workaround.

## 2. Plugins stop working after a free/paid period lapses

This needs to fit inside what's already decided (ADR-0013's
`access: registered` + entitlement model, and the revised ADR-0027 —
Universal Till *can* have paid official plugins, but nothing already
free can be quietly taken away). For a plugin that **is** paid
(official or vendor) and tied to an entitlement:

- **Recommend: soft-disable, not a hard kill-switch.** When an
  entitlement lapses, the till should stop the plugin from functioning
  (matches "should stop working" — that intent is respected), but via
  the mechanism the plugin architecture already has: capability grants
  are revocable (ADR-0001 — "a module gets nothing until the manifest
  grants it"), so an expired entitlement simply means the till stops
  granting that plugin its capabilities going forward. The **already-
  installed binary stays on disk** (nothing is deleted, no forced
  network call to "kill" it, works even if the till is offline when
  the entitlement lapses locally per its last-known-good check) — it
  just can't do anything without its grants.
- **Not mid-transaction.** A plugin actively mid-use (e.g., a payment
  plugin mid-authorization) shouldn't be yanked out from under a sale
  in progress — check the entitlement at the *start* of a plugin
  invocation, not abruptly during one. Small UX detail, real
  consequence if skipped (a half-completed card payment is a genuinely
  bad failure mode).
- **This is architecture, not just business policy** — worth a real
  ADR once confirmed (entitlement-check timing, offline grace period
  length, what the till shows the merchant when a plugin goes dark).
  Not written here since it depends on the pricing/tier shape below
  being confirmed first.

## 3. Which payment processor bills merchants for subscriptions

**Recommendation: Stripe**, for our own SaaS billing specifically —
distinct from ADR-0016's payment orchestration (that's about *merchants'
customers* paying at the till; this is about *us* charging *merchants*
a monthly fee, a completely different flow with different requirements).
Reasoning:
- Stripe Billing's coupon/trial primitives directly implement section 1
  above with no custom engine.
- Already integrated in the codebase (`ut-plugin-payment-stripe`) —
  reuses existing familiarity, though that plugin serves a different
  purpose (merchant's own card-present sales) and isn't the same
  integration as billing merchants for their subscription.
- Industry-standard for SaaS subscription billing; well-documented,
  handles proration/dunning/failed-payment retry logic that would
  otherwise need building by hand.

## 4. Pricing tiers — researched, not guessed

Checked real competitor pricing before recommending numbers:
- **Square, Toast, Loyverse**: free core software, monetize via card
  processing fees (2.49–2.6%) instead of a subscription.
- **Lightspeed**: $109–$339/mo flat SaaS fee (annual billing ~15–18%
  cheaper) for a fuller feature set — charges for the *base* software,
  unlike the free-core group above.

Universal Till's core is **already publicly committed as free forever**
(README, ADR-0013) — that rules out the Lightspeed shape (can't charge
for base software after promising it's free) and puts us in the
Square/Toast/Loyverse shape: free core, paid cloud/premium layer on
top. Given that, and matching the user's own framing ("the cheap one is
only using marketplace plugins I think"):

| Tier | What it unlocks | Suggested price |
|---|---|---|
| Free (Local) | Core POS, unlimited devices via LAN sync, all free plugins | £0 |
| Cloud Starter | Marketplace access to `registered`-tier plugins, basic cloud backup | ~£9–15/mo — priced to feel like an impulse add-on, matching Loyverse's budget-add-on positioning, not Lightspeed's base-fee positioning |
| Cloud Pro | + multi-till/multi-store cloud sync, advanced analytics | ~£29–39/mo — still well under Lightspeed's entry tier, since our core isn't also being paid for here |
| Enterprise | Multi-location, dedicated support, custom | Custom quote, matches every competitor's own pattern |

**This fills in the README's existing `$??/mo` placeholders** — those
have been sitting unfilled in the pricing table already. Numbers above
are a starting recommendation, not final; real placement should also
account for actual infra cost per merchant (cloud sync storage,
support load) which isn't modeled here.

## 5. Accounting software integrations

Adds **QuickFile** (UK-focused, relevant given UK is a launch market —
`multi-cloud-sovereign`/payment-orchestration notes) alongside the
already-roadmapped QuickBooks and Xero. Same shape as the DATEV/FiBu
export gap found in `germany-pos-parity-backlog.md` — each is really an
export-format plugin (`export` canonical type, ADR-0002), not a payment
or fiscal mechanism. Worth building these as a small family of similar
plugins once the pattern is proven on one (probably Xero or QuickBooks
first — larger addressable market than QuickFile specifically).

## 6. Letting other POS systems pay for Universal Till Cloud API access

This is a **real, distinct business line** — not a small feature. The
ask: a competitor's till (SumUp, etc.) pays to use *our* cloud
infrastructure — inventory management, multi-location/shop management,
machine sync, and (later) publishing inventory for search in a
consumer-facing app/web app we build — at a price **at or above** our
own till subscription price (your framing, and it's the right instinct:
otherwise it undercuts our own paying customers).

**Flagging this as needing its own dedicated design, not deciding it
here**, because it's genuinely large:
- It means `ut-cloud` becomes a **headless multi-tenant platform**
  usable by non-Universal-Till POS software, not just our own till —
  a different API surface (stable, versioned, documented for external
  integrators) than what exists today (built for our own till's needs).
- Real open questions: what exactly syncs (just inventory/catalog, or
  sales data too — which has bigger privacy/competitive-sensitivity
  implications since a competitor's sales data would touch our
  infrastructure)? Per-transaction or per-seat pricing? Does a
  competitor's shop show up in the same consumer search results as our
  own merchants', and if so, does that dilute or strengthen the
  platform (more shops = more useful search, but also promoting a
  competitor's till)?
- **Recommend**: park this behind the core subscription/pricing work
  above landing first (it depends on knowing our own tier pricing to
  set a floor), then a proper ADR once you've had more time to think
  through the competitive dynamics — this isn't a "build it this week"
  item, it's a "this could be a real second product" item.

## Summary of what's ready to build now vs. needs your call first

**Ready to build** (mechanism is clear, just needs implementation):
admin-grantable Stripe coupons for free/discounted subscriptions,
no-card-required trial flow, QuickFile/Xero/QuickBooks export plugins.

**Needs your confirmation before building**: the actual price numbers
in section 4, and whether Stripe is the right call for merchant billing
specifically (vs. e.g. Paddle/Chargebee, which exist specifically to
handle SaaS billing + tax/VAT compliance across countries as a
merchant-of-record — worth a look given the multi-country launch plan,
flagging as an alternative worth 30 minutes of comparison before
committing to Stripe outright).

**Needs a dedicated follow-up, not decided here**: the plugin-gating
ADR (section 2), and the third-party-POS-cloud-API product (section 6).
