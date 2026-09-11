# Manual content: payments & tenders (ut-docs#333)

**Date:** 2026-09-11
**Card:** universaltill/ut-docs#333 — "Manual content: payments & tenders"
**Complexity:** medium

## What the card asked for

Write the manual topics for payment-method configuration (default tender,
provider fees), card/payment-plugin setup, split tender/partial payment,
and refunding to the original tender, to the standard set by ut-docs#324.

## What was actually found (BA pass before writing)

`web/help/en/payments.md` (topic `payments`, no `routes:` — same
route-less pattern as the sibling `vouchers.md` topic, since payments are
reached through the sell screen rather than a standalone page) already
existed and already covered quick-pay, splitting a payment across methods,
giving change on a cash sale, and gift vouchers, in real depth.

The real, bounded gap was narrower than the card's own surface list
suggested:

- **Configuring payment methods** (default/preferred tender + per-provider
  fees) — genuinely undocumented as its own topic; only a single inline
  mention existed ("set provider fees in Settings → Payments").
- **Refunding to a tender** — already covered, in real depth, in
  `web/help/en/sell.md` step 6 (Journal → sale history → Refund, refund
  method choice, proportional service-charge refund). Duplicating it in
  `payments.md` would be a maintenance smell (two places that can drift);
  a cross-link is the right shape.
- **Card and payment plugins ("what each needs")** — Stripe/SumUp/QR pay
  are external `ut-plugin-payment-*` plugins with no code in this repo, so
  their own specific setup requirements are out of scope here; the generic
  install-and-key flow was already mostly covered and only needed a small
  expansion (naming that a demo plugin exists for trying the panel without
  a real provider, and the correct install path).

## What shipped

- `web/help/en/payments.md`: new **Configuring payment methods** section
  (the `Settings` → Payments card: preferred method, provider fees, the
  manager/admin elevation gate, and the fee range-check rejection), a new
  **Refunding a payment** section cross-linking `sell.md`'s existing refund
  walkthrough instead of duplicating it, and a small expansion of the
  existing plugin-install step. Keywords front-matter extended for search.
- `scripts/ci/i18n-baseline/help-drift-baseline.json`: reviewed-drift
  entries added/updated for `payments` (ar, de, fa, tr) — `de` had no prior
  entry (it matched English exactly before this change); ar/fa/tr's prior
  entries (tracking ut-docs#1962) are superseded since the structural
  change is now this card's, not that pre-existing gap's — same convention
  #329 used for `till-designer`. Per the card's own "English only on this
  card" instruction; translation deferred to ut-docs#341.

## Verified against the running app

- Booted a throwaway till (`e2e/run-till.sh`, port 8091, demo catalog
  seeded) and curled `/settings` directly: confirmed the exact live text
  of the Payments card (`Preferred method` dropdown listing Cash/Card/Gift
  Card/Voucher, `Provider fees` help text, per-method `% +` / fixed rows,
  Apply/Save buttons) against what was written.
- Read `internal/pages/settings_page.go` (`payments-default` and
  `payments-fee` handlers, the `checkOrElevate(..., "settings", ...)`
  elevation gate, the `pct<0 || fixedMaj<0 || pct>100` range check) and
  `web/ui/pages/settings.html`'s `#settings-payments` card before writing.
- Read `internal/pages/refund_page.go` (the `methods := []string{"cash"}`
  + originally-used-methods construction) and `internal/pages/index_page.go`
  (the pay-grid/quick-pay filter that excludes `type="voucher"` methods)
  and `internal/db/migrations/001_init.sql` (`gift` row seeded with
  `type='voucher'`) to ground the preferred-method and refund-method claims
  in the actual code rather than assumption.
- `bash scripts/ci/guard-help-topics.sh`, `guard-help-drift.sh`,
  `guard-docs-shots.sh`, `guard-compliance-claims.sh`: all green. `gofmt
  -l .` empty, `go build ./...`/`go vet ./...` clean, `go test
  ./internal/pages/... -run 'TestEveryTopicResolves|
  TestManualIsTranslatedInEveryShippedLocale|
  TestRouteRegistryResolvesKnownPages'` green (no Go source touched; run
  as a sanity check since these tests read the help-topic tree).

## Independent review

A fresh-context Opus subagent (complexity:medium → Opus review, per
`scrum-master`'s model-routing table) reviewed the diff independently,
re-reading the referenced Go/template source rather than trusting the
prose. Found 2 must-fix inaccuracies and 3 further precision issues in the
first draft, all fixed before this record was written:

1. **Fabricated specific**: "a built-in demo payment method" — nothing in
   this repo seeds a built-in demo tender; a demo payment plugin exists
   only as an external `ut-plugin-payment-*` plugin (seen in test
   fixtures), out of scope per the card. Reworded to "a demo payment
   plugin is also available from the store."
2. **Wrong navigation path**: "Settings → Plugins" — Plugins is a
   top-level nav item (`/plugins`, `Order: 700, InNav: true`), not a
   Settings sub-page; the store is at Plugins → Store, matching
   `plugins.md`'s own wording. Fixed.
3. The "preferred method" claim was unconditionally true in the first
   draft; verified against `index_page.go` that voucher-type methods
   (Gift Card, Voucher — both seeded with `type='voucher'`) are excluded
   from the quick-pay grid regardless of being set preferred. Reworded to
   state that exception explicitly.
4. "Leaving it at '—' falls back to cash" overstated the actual behavior
   (`index_page.go`'s reorder only fires when a preference is set; empty
   leaves the existing `sort_order` list, cash first only because it's
   seeded `sort_order=1`). Reworded to describe the real mechanism.
5. The refund-method sentence read as self-contradicting once checked
   against `refund_page.go`: cash is *always* offered (seeded
   unconditionally), not only when actually used. Reworded so the claim
   matches the code exactly.

A "what can go wrong" note (the `✗ range` rejection) was also added to the
new Configuring section to match every other section's structure in this
file, and the "cash is always there; a plugin adds more" framing was
corrected — Card, Gift Card and Voucher are seeded out of the box, not
added by a plugin.

Everything else — the fee model (percent → basis points, fixed → minor
units, currency-decimals-aware), the elevation gate, the Apply/Save button
labels, the `.payMethods` data-availability gate on the card, the refund
entry point, and both cross-links (`/help/elevation`, `/help/sell`'s
service-charge-proportional refund text) — was confirmed correct against
source.

## Scope / non-goals

- Not documenting specific payment-plugin providers' (Stripe/SumUp/QR pay)
  own setup requirements — they live in their own `ut-plugin-payment-*`
  repos, out of scope here.
- Not duplicating the refund walkthrough already in `sell.md` — cross-linked
  instead.
- Not translating the new English content into ar/de/fa/tr — deferred to
  ut-docs#341 via reviewed baseline entries, same convention as #329/#330.
- No Go/HTML/CSS change — content-only.

## Verdict

Safe to merge. All four guard scripts green; independent review findings
fixed and re-verified against source. Closes universaltill/ut-docs#333.
