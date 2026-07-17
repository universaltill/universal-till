# Code review — plugin trust badges + untrusted-install consent (till side) (2026-07-17)

Branch `feat/trust-badges`. Farshid's ask: visible trust tiers on plugins +
an install-time "do you trust this publisher?" alert for unverified ones.

## Tiers (trustTierOf)
- **official** — first-party `com.universaltill.*` ids: gold 🏠 badge
  ("golden house"), matching the marketplace's first-party signing prefixes.
- **verified** — marketplace trust_tier verified/trusted: green ✔ badge.
- **unverified** — everything else: amber ⚠ badge.

## Consent
Download/Install on an UNVERIFIED card first shows a localized confirm
naming the publisher ("This plugin is from an unverified publisher: Acme.
Do you trust…?") — native dialog in the desktop shell, browser confirm
elsewhere; cancel aborts before anything touches the till. Card carries
`data-tier`/`data-vendor`; the prompt string is i18n (×4) injected as
`utTrustPrompt`.

## Tests
Store render test now uses a first-party id + a third-party vendor and
asserts both badges, the consent data attributes and the localized prompt.
Suites + guards green.

## Remaining (same queue item)
Marketplace-side badge styling (storefront/portal/detail) to match; tier
assignment rules for "verified" (ties into vendor registration/paid
developers — pending Farshid's decision on the grants machine user).
