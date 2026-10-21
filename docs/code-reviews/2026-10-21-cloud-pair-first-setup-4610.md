# Review: cloud pair-first setup (ut-docs#4610, ADR-0166)

Branch `feat/4610-cloud-pair-first-setup`. Independent Fable review.

## What shipped

- First-boot wizard: the join step offers "Main till" (pair with the shop
  made on my.universaltill.com; owner's join-as UX). Choosing it hides the
  address field and asks for the pairing code and a till name.
- `POST /api/setup/cloud-pair` (same-origin, Host allow-list, wizard nonce,
  rate-limited) pairs with `first_till:true` and holds the pairing in memory
  only; a "Your shop" step shows the cloud's name, country and currency.
- The final `POST /api/setup` (`cloud_paired=1`) overrides country, currency,
  tax and name from the held pairing, writes the seed, then commits the
  credential and requests a check-in. Lands on `/import?welcome=1&syncing=1`.
- Step order: identity, name, type. The syncing chip is a `?syncing=1` note.

## Findings and fixes

1. A cloud shop named "My Shop" / "Dükkânım" passed `cloudShopView` but was
   refused by the final post, so it could never finish. `cloudShopView` now
   uses `isRefusedStoreName` with the request locale: HasName=false, the
   local name step is asked, and a typed name is kept. Test:
   `TestCloudShopNamedLikeThePlaceholderAsksForALocalName`.
2. A failed `Commit` was only logged and the wizard finished silently. Now
   it lands on `/import?welcome=1&pairfail=1` with a visible note
   (`setup.cloud.pair_incomplete`), no syncing chip. No in-wizard retry:
   setup is already complete (first-boot gate closed) and the code is spent,
   so the note says to make a new code and pair in Settings, Tills. Tests:
   `TestCloudSetupCommitFailureIsSurfaced`,
   `TestImportWelcomeShowsPairingIncompleteNoteNotSyncing`.
3. The "after seed" claim was unproven. `commitCloudPairing` is now a seam;
   the test asserts that at Commit time the gift-voucher row and store name
   exist and no credential is stored yet.
- Nit: the pairing failure was Warn-logged twice; now once, in
  `enroll.PairFirstTill`.

## Verified

Driving the real binary against a stub cloud: the radio hides the address;
a wrong code, `store_has_till` and a good code behave; a reload keeps the
held pairing; the final post lands on `/import?welcome=1&syncing=1`; the DB
holds marketplace.token, store_id, store name, country and currency.
Also: gofmt, build, vet, unit tests (enroll, languard, auth, pages
Setup|CloudPair|Import) and the i18n, i18n-meta, help-topics and help-drift
guards pass.

## Deviations

The step-1 "I already set up my shop online" button was dropped (the join
choice replaces it).

## Verdict

Safe to merge.

## Deferred

- multitill docs screenshots: `make docs-shots` on Linux.
- Language-pack follow-up PRs for the new core keys.
- Suite e2e against a stub pair endpoint.
