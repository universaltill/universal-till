# Review: Mac-instance test batch, one combined review (2026-11-28)

Scope: PR #2789 (`fix/7190-mac-test-ui-batch`: ut-docs#7190-#7197, #7199,
#7201) plus the further fixes in `wt-gate`: #7193 behaviour (Dismiss keeps
the Undo record), #7198, #7199/#7200 (legal text and the `copy_held` state),
#7202, #7203, #7204 (wording and the Main role; not a duplicate listing),
#7205 (legacy-excluded by ADR-0190), shop-type preview height, backup card
state cleanup, docs-shots regeneration under `web/help/img`. Diff base:
`163537a7e` (merge-base with `origin/main`).

Reviewer: orchestrated here; code review by a **Fable** subagent
(`reviewer/SKILL.md`: Sonnet-built, touching data/security -> Fable) and a
separate Fable counsel pass for the legal text (record:
`2026-11-28-mac-bugs-7200-legal.md`). The `reviewer` and `legal` skills are
not in the Skill tool's list; their files in ut-docs were read directly.

## Findings

| # | Sev. | Finding | Disposition |
|---|---|---|---|
| 1 | Important | Cloud backups card hid working functions: "Copy to the cloud now" when blocked, "Restore from the cloud" when blocked and not held, and the "first copy" line for Off/undecided. ADR-0199 D17: uploads run in every state; a saved-key restore works. | **Fixed (TDD).** Card shows the state line in every waiting state, Upload now needs only the plan entitlement, Restore link always. `TestOffsite_CardButtonsAndFirstCopyLineFollowTheState` table corrected first; failed on 5 rows, passes. |
| 2 | Important | Manual said "Dismiss (no more Undo) ... ends the Undo", contradicting #7193 (Dismiss only hides the strip). | **Fixed.** `web/help/en/display.md`. |
| 3 | Important (counsel F1) | Undecided-state ask said Off keeps the key "only on this till" though Off never deletes a held copy (true after every restore on an On shop). | **Fixed.** `ask_off_text`, `choose_off`, `blocked`, `state_not_in_plan` redrafted; meta descriptions updated. Details in the legal record. |
| 4 | Medium (counsel F2) | Provisional `copy_held` set only for On -> Off, not undecided -> Off while the cloud cannot be asked. | **Fixed (TDD).** `TestOffsite_ChoosingOffFromUndecidedUnconfirmedMarksHeld` failed (`copy_held = ""`), passes after the handler sets it for any Off that was not already Off. |
| 5 | Nit | `/catalog` browser title stayed "Catalog" while the h1 followed the shop-type relabel. | **Fixed (TDD).** Title assertion added to `TestCatalogPage_HeadingFollowsShopTypeRelabel_7203`. |
| 6 | Nit | Dismiss is a read-modify-write without compare-and-set. Impact bounded: expiry is unchanged, the undo endpoint re-checks the current type. | Skipped, accepted. |
| 7 | Nit | After Dismiss the Undo button is not visible on the already-rendered Shop type card until reload; the card reuses the banner string. | Deferred to a card (small UX: `HX-Trigger` panel refresh and a card-specific string). |
| 8 | Nit | `copy_held` (and its siblings `key_in_cloud`, `escrow_blocked`, `link_copy`) survives a move of a till to another shop; `enroll.OnStoreLeft` only gets a store id and deletes the key file. For a short window the card shows the old shop's held state until the next `GetKey`. Pre-existing for the siblings. | Deferred to a card (needs a DB handle in the hook). |
| 9 | Nit | `app.css` scroll shadow uses a literal `rgba(0,0,0,.18)`; check the dark theme. | Deferred to the UX card. |
| 10 | Medium | Counsel F4-F8 (pending state for On, `anyWrapped` for older key generations, `held` read outside the plan gate, `privacy` split, blocked line in the ask state). | Deferred to cards; F5 first. See the legal record. |

## Security points (checked by Fable, re-read by me)

- **`copy_held` cannot leak:** it is in `OffsiteBackupKeys` and in
  `RestoreDropKeys` (list test, count 16, passes); per-till by prefix; set
  only after a successful `GetKey`/`PutKey` carrying the key, or provisionally
  on Off (the safe direction, corrected by the next round). A till moved to
  another shop is the pre-existing sibling-key gap (finding 8).
- **Dismiss keeps Undo, no expiry bypass:** `Encode`/`ParseUndo` round-trip
  `Dismissed` and leave `ExpiresAt` untouched, so Dismiss cannot extend it;
  an expired record is pruned; the undo endpoint ignores `Dismissed` and keeps
  its manager and PIN gate; a new change writes a fresh record.
- **Restore wipe list:** `RestoreDropKeys` gains `backup.offsite.copy_held`.
  Side effect (counsel F1): after a restore the choice and the flag are
  undecided/absent until the next round, which the redrafted ask text now
  covers.
- **HTML escaping:** all new output goes through `html/template`; the
  `data-held` attribute is conditional and static; the new icons are static
  map entries; the Undo note text comes from `T` with the type label replaced
  server-side then escaped by the template.
- **Recurring bugs:** no file write or cwd-relative path is added.

## TDD claims re-verified

Done by me inline (revert the production line, run the test, restore, same
turn; the fixes were uncommitted so no WIP commit existed). Each test failed
with the claimed error when reverted and passes restored:

| Fix | Reverted change | Test | Failure seen |
|---|---|---|---|
| #7193 Dismiss | `u.Dismissed = false` | `TestShopTypeDismiss_HidesBannerButKeepsUndo` | "banner still renders after dismiss" |
| #7202 | gate check disabled | `TestMenuLayoutSettings_SkipsHiddenRowsTheViewerCannotSee_7202` | "Locations row must be skipped when its gate is off" |
| #7205 / ADR-0190 | `CategoriesLegacy` -> `Categories` | `TestServiceLedgerShopGetsHardwareTileDescription` | "a legacy service shop's Hardware tile must stay plain" |
| #7205 | the opposite swap | same test | "a template-ledger service shop's Hardware tile must carry its description" |
| #7200 copy_held | `noteHeld` no-op | `TestCopyHeldFollowsTheCloudAndSurvivesAnOffChoice` | `after On: copy_held = "", want 1` |
| #7204 | `Unpaired` forced false | `TestTillsOthers_UnpairedSaysNotConnectedNotUnreachable` | "want not-connected wording and the pair action" |
| Review fixes 1, 4, 5 | test written/updated first | card table (5 rows), `...UnconfirmedMarksHeld`, catalog title | failed before the fix, pass after |

Final run after all edits: `go build ./...`, `go vet` (pages, offsitebackup),
`go test -count=1` for `internal/pages`, `internal/offsitebackup`,
`internal/pages/catalog` pass; db, shoptemplate, uislot, httpx, settingsnav
passed in the earlier run. Two old wording assertions in
`offsite_backup_api_test.go` were updated to the redrafted text.

## Extra verification

`gofmt -l internal` clean; `go build ./...`; `go vet` on the affected
packages; guards: i18n, i18n-meta, compliance-claims, help-topics,
rtl-logical-css, no-showmodal, no-native-confirm, mail-addresses,
competitor-naming, core-neutral pass. ADR-0190: `settingsCategoriesFor` is
the only caller of `settingsnav.Categories`; the legacy salon golden test
passes. #7202 skip fails closed. #7204: the unpaired card appears only when
the cloud list is unavailable and credentials are incomplete. #7205 keeps
legacy shops on the frozen generic tile.

Pre-flight: Till user-visible behaviour: help topic `display.md` updated,
docs-shots regenerated under `web/help/img`; README n/a (no text went stale);
en.json keys: `en.meta.json` updated, English only (ADR-0160); plan/price n/a;
platform claim n/a; plugin type/host function n/a; review record: this file
and the legal record; follow-up work: deferred findings 7-10 become cards.

## Verdict

Approve after the fixes above, subject to owner sign-off on the changed legal
wording (`review: legal` keys) as `legal/SKILL.md` requires. Nothing here is
committed; the orchestrator commits and pushes.
