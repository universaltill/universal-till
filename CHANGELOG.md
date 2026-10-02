# Changelog

Release notes for the till. Versions come from the release tag
(`RELEASING.md`); entries under **Unreleased** ship in the next release.

## Unreleased

### Added

- **Camera barcode scan on iPhone and iPad (ut-docs#696).** The sale
  screen's camera-scan button now shows on every device. Where the browser
  has no native barcode reader (WebKit, so every iPhone/iPad, and some
  Android WebViews) a decoder bundled with the till reads the code on the
  device; nothing is fetched from the internet and no frame leaves the
  device.
- **UK age-restricted sales support (ut-docs#3340).** A catalog item can be
  flagged "Age restricted (ID check at the till)". The till gates tendering
  a sale on the cashier recording an ID-check outcome (accepted/refused)
  for any unverified restricted line, via a non-modal sheet; the outcome is
  recorded with the sale. The self-order kiosk fails closed (no completion,
  "pay at the counter") on any restricted item, and staff take over via the
  existing kiosk PIN-login path. No jurisdiction-specific age or date is
  hardcoded — only the generic flag and prompt mechanism ship in core.
- **Users and PINs from my. (ut-docs#2810, ADR-0115 §2 amendment).** The
  main till applies the cloud directives `save_user`, `set_user_pin` and
  `deactivate_user` (contract: ut-docs `reference/till-user-directives.md`).
  A PIN arrives only HPKE-sealed (RFC 9180, X25519 / HKDF-SHA256 /
  AES-128-GCM) to a new main-till directive key,
  `<data dir>/secrets/directive-x25519.key`, created at the main till's first
  cloud check-in and reported as `directive_key` on every check-in. Satellite
  tills never create or report one and leave these directives to the main
  till. The main till's config report now also carries `users` (never PIN
  hashes or sessions).
- ut-cloud: `claims.DirectiveMinTillVersion` for `save_user`,
  `set_user_pin` and `deactivate_user` is the release that ships this entry
  (the first release after **v0.28.0**, which was cut just before this
  merged; v0.28.0 does not contain it).

### Fixed

- **A 0% tax code is charged 0% (ut-docs#3250).** An item on a tax code
  whose rate is 0% (zero-rated food, an exempt line) was charged the shop's
  default rate, because the sale engine treated a 0% rate the same as "no
  tax code". Now only an item with no tax code, or one whose tax code no
  longer exists, falls back to the default rate. **Behaviour change:** a
  till with items on a 0% tax code was in practice charging the default VAT
  on them; after this release those items are charged 0%, which is what the
  tax code says. A sale parked before the update is re-rated at 0% when it
  is resumed. Completed sales are unchanged.
