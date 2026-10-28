# Review: receipt designer tracking QR label names the Shop plan (ut-docs#5107)

**Shipped.** `designer.receipt.show_tracking_qr` said "Order tracking QR
(paid plans)". Since #5067 Local Plus is paid, but order tracking is Shop
and up (`CapOrderTracking: PlanShop`), so the label promised the QR to
Local Plus shops. Value-only change (same key, no placeholders, no meta
change — the key sits in the key-meta baseline):

- `web/locales/{en,tr,fa,ar}.json` → "Order tracking QR (Shop plan and up)"
  and translations (plan names kept in English, as in `subscription.plan.*`).
- The bold quote of the label in `web/help/{en,de,tr,fa,ar}/designer.md` and
  `customer-order-tracking.md` (de quotes the de pack's new value).
- `web/help/img/{en,fa,ar,tr}/designer.png` + `manifest.json` regenerated
  (`docs-shots` run for the two topics only; the order-tracking PNGs did not
  change — the label is not on that page).
- README feature line: "paid plans" → "Shop plan and up".
- Language packs de/es/pt carry the new translation (pack PRs first, per
  reviewer step 4 for an existing key).

Historical release notes (v0.33.0) and older review records keep the old
wording on purpose.

**Review.** Authored by Opus; reviewed by a Sonnet subagent (the
different-model rule). No findings. It byte-checked all ten help quotes
against the locale values (ZWNJ/diacritics in fa/ar), checked each
translation against the locale's existing plan-tier wording, and flagged
README:67 as loose — fixed here.

**Verified beyond automated tests.** Looked at the regenerated designer
screenshots in en, tr and fa (1024×600): the label fits on its row next to
the "Available once order tracking moves to our cloud." note, RTL
parentheses render correctly. German (longest locale) got shorter. Not
checked at 360px (layout unchanged; the label is 6 characters longer in en).

**Gates.** `go build ./...`; `go test ./...`; all 55 guards in `ci.yml`
pass except two local-tooling failures unrelated to the change
(`guard-deadcode-baseline.sh`: container's deadcode built with go1.26 vs
go1.27 module; `guard-shellcheck-version.sh`: no shellcheck installed) —
CI runs both.

**Verdict.** Safe to merge after the three pack PRs.
