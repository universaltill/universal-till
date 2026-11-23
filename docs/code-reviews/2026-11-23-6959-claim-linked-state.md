# Review: Till registration shows a linked state for a claimed shop (ut-docs#6959)

Date: 2026-11-23. Combined Tester + Reviewer (not the Dev).

## Change
- `settings_page.go` passes `storeClaimed` (`storeHasOwner`: the cloud's
  `claimed` flag, else the older plan/status inference; error = false).
- `settings.html`: claimed shop shows "This store is linked to your Universal
  Till ID." (+ My stores link when link-outs are allowed) instead of the Claim
  button; the Pair form folds into `<details id="pair-details">` under
  "Reconnect this till to a shop". Unclaimed/unknown keeps the Claim button
  (#867: the elevated `claim-code` POST is untouched) and the Pair form open.
- Keys `settings.enrol.linked`, `linked_manage`, `reconnect_title` (+ meta),
  help topic `claim`, Go test, two e2e specs.

## Findings (fixed)
0. **`TestPersistentShell_NonPageLinksOptOutOfBoost` failed** (full suite; Dev ran targeted tests only): the new My stores link lacked `hx-boost="false"`. Added.
1. **Unclaimed Pair block painted a stray "Details" label.** The Dev used
   `<details open>` without a `<summary>` for the unclaimed case, so Chromium
   drew its own untranslated English "Details" disclosure above "Pair with a
   shop" (seen in the 360px screenshot). Now `<details>` only when claimed;
   unclaimed is the original plain `<div>`. The Go test now fails on a
   `<details>` in the unclaimed case (it used to accept it).
2. **Summary had no disclosure marker.** `btn-touch` on `<summary>` made it
   `display:flex`, removing the triangle; the collapsed row read as plain
   text. Replaced with an inline 44px, bold, pointer summary that keeps the
   native marker and keyboard toggle.
3. **Duplicate sentence.** `reconnect_note` repeated the end of `pair_help_2`
   word for word inside the opened section. Removed the paragraph and its key.

## Verified, no change needed
- #867: the Claim button's POST path is unchanged; hiding it when the store is
  known claimed removes no path (a revoked store flips back on next render).
- Existing e2e edits only add `claimed: true` to the fake cloud, one new test,
  and an `openReconnect` helper that opens the details when closed; no
  assertion was loosened. Specs that touch the pair form on an unclaimed till
  (`settings-pair-with-shop-3523`, `confirm-dialog-escape-focus-5275`) pass.
- RTL: no physical CSS added; fa screenshots at 1024x600 and 360 mirror
  correctly. a11y: status line uses icon + words, `role="status"`, summary
  keyboard-toggles with Enter and Space, native focus ring.
- Known limit: when the till has no `claimed` signal it falls back to plan
  inference, so a till on a paid plan counts as claimed (existing helper
  behaviour, wording only).

Pre-flight: review record: this file · help: `web/help/en/claim.md` updated (one step) · en.json + en.meta.json: English only, 3 keys (ADR-0160) · docs-shots: no shot captures the Till registration card, so no PNG changes; `web/help/img/manifest.json` re-stamped with `node e2e/tests-docs/write-manifest.js` (the tool; `make docs-shots` itself fails on macOS, ut-docs#4407) and `guard-docs-shots.sh` passes · ADR: none changed · reference/README: n/a · follow-ups: none.
