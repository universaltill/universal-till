# Changelog

Release notes for the till. Versions come from the release tag
(`RELEASING.md`); entries under **Unreleased** ship in the next release.

## Unreleased

### Added

- **Till roles: additional or satellite (ut-docs#2781).** A manager picks a
  joined till's role on the main till before it is created — on the
  pairing-code card, or next to Approve for a request found on the network —
  and can change it later on the Tills page (`POST /api/sync/tills/{id}/role`,
  audited as `till_role_changed`). The role is stored in `tills.role`
  (migration 067), reaches the till with its next sync, and is reported in
  its link hello. Until ut-docs#1154 a satellite is the counter-pay
  self-order kiosk only (ADR-0020, ADR-0086): register and back office are
  refused, and a satellite found on either is switched back to the kiosk
  (audited as `display_mode_forced`). Choosing the kiosk on an additional
  joined till asks "Make this a satellite?" first.
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

- **Opening another order no longer loses an "add by hand" counter order
  (ut-docs#3586).** A pay-at-the-counter order with no items the till could
  price (only lines listed to add by hand) counted as an empty basket. If the
  cashier then opened another order, that order was not held first: it was
  replaced, with no held entry and no sale. Tapping **Hold** on it also showed
  the "basket is empty" error. Both now treat it as a real order: opening
  another order holds it first under its own name, and **Hold** parks it.
- **Marketplace installs check plugin settings like the importer does
  (ut-docs#3514).** A marketplace-installed plugin whose manifest has a
  malformed setting-bound permission (`net:@setting:`/`tcp:@setting:`, or
  one naming a setting it doesn't declare) or an unknown setting type (a
  typo of `secret`, which would have stored that value unsealed) is now
  refused at install with the same message the importer gives, instead of
  installing and silently granting nothing. Installed plugins are not
  re-checked.

- **A Linux till that can't update itself now says so (ut-docs#2733).** With
  automatic updates on (the default) and an update waiting, a till whose
  install folder it can't write — a .deb install whose ownership broke —
  skipped the nightly update silently, forever, while Settings still said
  "Update automatically". The status-bar update chip now says the till
  can't install the update by itself and links to Help → Software updates
  (reinstall the latest .deb); Settings → Software update → Check now shows
  the same. A chip, never a dialog; additional tills keep their own
  follow chip.

- **A queued tax plugin no longer retries forever once one is already on the
  till (ut-docs#3511).** A fiscal plugin queued during setup, before a tax
  plugin for the country arrived by file or with a restored backup, kept
  being retried in the background and kept its Settings reminder up. The
  retry now sees the local plugin, drops the queued one without touching the
  catalog, and never installs a second fiscal plugin; if that local check
  itself fails, the entry simply stays queued for the next attempt.

- **A 0% shop default tax rate is charged 0% (ut-docs#3392).** A shop whose
  default tax rate is set to 0% (the US and "Other" presets, or any shop
  that chose 0%) was charged 20% on every item with no tax code of its own,
  because the sale engine treated a 0% default as "not configured".
  **Behaviour change:** after this release those items are charged the
  shop's 0% default. Shops with a non-zero default rate are unaffected, and
  so are items on a tax code. Completed sales are unchanged.
- **A completed sale can no longer be reopened (ut-docs#3368).**
  `POST /api/pos/sale/status` used to accept any target status for any
  sale, so a completed sale could be set back to `open`/`parked` and drop
  out of every report and Z total. A completed sale can now only be
  `voided`; `refunded` (counted by no report — real refunds are return
  sales) and `completed` (only tender completes a sale) are refused as
  targets; `voided` is final. A refused change answers 409 with nothing
  changed and no audit row. Voiding through this API now needs the
  `refund` permission, or a manager's PIN in `override_pin` (the audit row
  then names the approver and the refused operator). No till screen calls
  this route.
- **A 0% tax code is charged 0% (ut-docs#3250).** An item on a tax code
  whose rate is 0% (zero-rated food, an exempt line) was charged the shop's
  default rate, because the sale engine treated a 0% rate the same as "no
  tax code". Now only an item with no tax code, or one whose tax code no
  longer exists, falls back to the default rate. **Behaviour change:** a
  till with items on a 0% tax code was in practice charging the default VAT
  on them; after this release those items are charged 0%, which is what the
  tax code says. A sale parked before the update is re-rated at 0% when it
  is resumed. Completed sales are unchanged.
