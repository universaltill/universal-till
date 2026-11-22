# Changelog

Release notes for the till. Versions come from the release tag
(`RELEASING.md`); entries under **Unreleased** ship in the next release.

## Unreleased

### Added

- **A till folder copied onto other hardware moves its numbers ahead
  (ut-docs#6907).** A main till whose whole data folder (secrets included)
  is copied to other hardware (a folder or SD card copy) now notices the new
  machine at its first start and moves receipt and Z numbers ahead once, as
  after a restore; the original keeps counting. A whole-disk clone to another
  PC keeps the machine id and is not noticed, and the official Docker image
  has no machine id, so it is not checked. A copied joined till shows
  **Join the main till again**. Only a hashed machine id is stored. Set
  `UT_HOST_ID` to your own id, different on every machine and set on the
  original before copying (a pin copied with the folder hides the copy), or
  `off` to skip the check.

- **Every till registers itself and reports its changes (ut-docs#6559,
  ADR-0200).** Once setup is finished, the till registers itself with the
  Universal Till cloud in the background (never during a sale, retried with
  capped backoff while offline) and refreshes its record on every start. It
  also reports which software version or plugin changed and when (a small
  local queue, 200 events at most, sent with the next check-in; there is no
  shutdown signal). The setup wizard's last screen and Settings → Privacy
  tell the owner exactly what is sent, show whether the till is registered
  and when the cloud last answered, and have a **Send usage facts** switch
  (the control moved from "Till registration → Cloud record" so the notice's
  "Settings > Privacy, switch off Send usage facts" is literally true; the
  check-in now also sends the build's release channel, which the notice
  always listed). The "Register this till" opt-in, the Register
  now button and the terms box in the wizard are gone. A source build, a
  demo, a test till (`UT_NEVER_REGISTER=1`, set by every CI and e2e
  launcher) and a shop in a refused country never register.

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
- **Install channel for store builds (ut-docs#6388, ADR-0194).** Each build knows whether it is a sideload, Google Play, App Store or direct build; on Play and App Store there is no plan purchase surface (the routes answer 404), no self-update check and no marketplace paid badge, and an unknown channel fails closed to the App Store rules.
- **Claim code as text on store builds (ut-docs#6401).** On Google Play and App Store builds the claim card shows the code as plain text with instructions for another device, with no link or QR; help and release notes drop the plan-purchase text there, and `guard-store-strings.sh` keeps checkout URLs, prices and buy wording out of them.
- **About screen with legal links (ut-docs#6402).** A new About screen, open to every signed-in role on every channel, links to the privacy notice, the terms, a generated open-source licences page (CI fails when it is stale) and account deletion.
- **Store-build review fixes (ut-docs#6388, #6401, #6402).** On Google Play and App Store builds the Support partner topic shows no link or QR, the Software update topic no longer explains sideloading and is not linked from Settings, and the setup wizard's update routes answer 404. The About screen's privacy link opens the app privacy notice, the licences open inside the till with a back link, and web addresses are shown as text on a kiosk. The licences page now also lists the Go runtime and the front-end code shipped with the till. `guard-store-strings.sh` also catches schemeless checkout addresses, prices, "Upgrade now" style wording, templates, scripts and translations; a Play Android build must carry `-PinstallChannel=play`.

### Fixed

- **Lock, status and exit-to-OS stay reachable from the full-screen
  Inventory and catalogue dialogs (ut-docs#6125).** The item editor, Import,
  Tax codes and the stock dialog now sit beside the nav rail and above the
  status bar, and carry a compact Lock and exit-to-OS in their title line at
  every width. Import and Tax codes scroll as one box with their head pinned.

- **Card refunds no longer stop working after about 1,000 card sales
  (ut-docs#6438).** A card plugin's approved answer may now carry
  `transaction_ref` (the provider's charge id). The till stores it on the
  sale's payment row and hands it back to the plugin on the refund as
  `original_payments`, so a refund finds the original payment from the sale
  itself: on any till, after a plugin reinstall, with no per-sale plugin
  storage (which the host caps at 1,024 keys). Needs Stripe / SumUp plugin
  1.4.0 or newer for the full effect; plugins never auto-update, so update
  each till by hand. A card sale the till never linked must be refunded in
  the provider's dashboard. The settle event also gains
  `authorize_request_id` and `transaction_ref`. The SDK fake host now enforces
  the 1,024-key cap like the real host.
- **Import and Tax codes dialogs scroll on touch screens (ut-docs#6553).**
  On the tablet and the phone the Import catalog and Tax codes dialogs could
  not be scrolled, so the lower part (the import steps, the New tax code
  form) was out of reach, and their content sat against the screen edge. The
  dialogs now scroll, have normal margins, keep the Catalog back button in
  view while you scroll, and the last field stays above the on-screen
  keyboard.

- **Promoting a till counts its own late sales once (ut-docs#5777, #5807).**
  When an additional till is promoted to main till, its own sales that the
  old main till recorded only after its last day close are now counted in
  the promoted till's next Z report, exactly once. The till learns when the
  main till recorded each of its sales from the sales copy, both the regular
  feed and a full re-sync. Upgrade the main till first: a till that re-syncs
  its copy from a main till older than this release, or copies from one
  older than the release that introduced the sales copy's arrival times,
  keeps no recording time for those sales, and after a promote they still
  appear in no Z. A gift-voucher sold in such a sale still shows only in the
  old main till's voucher totals.
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
