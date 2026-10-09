# Review: a failed plugin view action keeps the operator's input (ut-docs#3879)

## What shipped
- **htmx action failure** (unreadable post, `ui.action.ask` error/timeout,
  invalid answer, refused job): `servePluginView` answers the failure status
  (502 / 400 / 429) with `HX-Reswap: none` and only an out-of-band notice
  into the action's alert slot. The view and the form stay untouched, file
  and password inputs included; submitting again is the retry. On a plugin
  page the slot is `#plugin-view-alert` (new, above `#plugin-view`); in a
  content slot panel (ut-docs#3872) it is that panel's own
  `<panel id>-alert` (`slot.html`). Every successful htmx answer and every
  htmx poll answer clears the same slot out of band.
- app.js scrolls a filled alert slot into view (`htmx:oobAfterSwap`,
  `block: 'nearest'`). Without this, the notice sat off-screen above a long
  form whose Save button was at the bottom (found in the driven run).
- **No-JS action failure**: core asks `ui.view.ask` again (empty params,
  same timeout) and renders the notice above that view. The failed form is
  refilled from the raw post by `pluginview.View.Refill`: text, number and
  money get the raw text, select gets a known option, toggle is ticked only
  when posted `"true"`, secret and file fields never. When the re-ask fails,
  the old body notice is shown. Status stays 200, as before.
- New key `plugin.view.action_failed` in en/ar/fa/tr. Pack PRs for
  ut-plugin-language-{de,es,pt} follow the core merge. The plugins help
  topic in all 5 languages has been updated, and the docs-shots manifest
  regenerated (no PNG changed). ut-docs `reference/plugin-views.md` has a
  new "When an action fails" section.

## Review
Independent review by Fable; the Opus 5.5 author did not review its own
work.
- **Blocker (fixed):** the branch predated #3872, which merged mid-cycle.
  Once merged, a failed action in a slot panel targeted
  `#plugin-view-alert`, which slot host pages do not have. htmx dropped the
  OOB fragment, so the failure was silent. Two #3872 tests also failed.
  - Fix: rebased onto main; `pluginViewAlert.ID` is now set; every panel
    renders its own alert slot; the app.js listener matches on the
    `.plugin-view-alert` class.
  - `TestPluginSlot_FailedActionKeepsPanelHeading_3872` and
    `TestPluginSlot_ActionAnswersIntoPanel_3872` are updated to the new
    contract. `RendersPanelsInStableOrder` asserts each panel's alert slot.
  - TDD: reverting `slot.html` makes the order test fail ("has no alert
    slot"). Reverting the Go alert id makes the action and failed-action
    tests fail.
- **Should-fix (fixed):** nested live regions. The inner notice dropped
  `role="status"`; the wrapper's `aria-live="polite"` is the live region.
- **Nit (fixed):** an unread no-JS post now passes no action to the refill
  (previously harmless: posted was nil).
- **Nit (accepted):** the no-JS worst case is two timeouts (about 10 s) when
  the plugin is slow. This is a rare path; documented.
- **Checked clean:** refilled values are escaped by html/template, and
  nothing is marked safe. `Submission.Posted` (it holds secrets) is never
  logged, sent to the plugin or rendered. Status codes are unchanged. GET
  and poll behaviour is unchanged. Upload release on a busy job is correct.
  The ar/fa/tr strings read naturally.
- **TDD claims** re-verified by the reviewer against the merge-base, per
  layer (template-only and Go-only reverts each fail the new tests; restore
  passes).

## Verified beyond unit tests
- Driven in Chromium against the real `htmx.min.js` 1.9.12 and `app.js`,
  serving the handler's real output.
  - A failed post kept "typed by operator" and the password field. The
    notice appeared in view at 1024×600 and 360×800. Retry re-posted the
    same values. The success answer cleared the notice. No `#pos-alert`
    banner and no page errors.
- Screenshots looked at: 1024×600 (kiosk) and 360×800 (phone), light
  theme, English.
- Not looked at: dark theme and RTL. The notice reuses the existing
  `.plugin-view-notice.warn` and the only new CSS is `margin-block-end`.
  Slot panels were not driven in a browser; that path is covered by
  handler tests only.
- Gate: gofmt, build, vet, `go test ./...`, every `ci.yml` build guard.
  Two guards are red only locally, for toolchain reasons: there is no
  shellcheck binary, and deadcode was built with go1.26 against a go1.27
  module. CI runs both.

## Verdict
Safe to merge. Merge core first, then the three language-pack PRs in the
same cycle: `lang-pack-drift` stays red on main until they land.

## Rebase onto main 5a86482, then 9522b01 (2026-10-09, lane:cloud-41 PR sweep)
- `web/help/img/manifest.json` conflicted: took main's, re-applied only
  this branch's four `plugins` topic hashes (checked against
  `sha256sum web/help/*/plugins.md`), then
  `update-docs-shots-surface-hash.sh`; docs-shots guard green.
- Semantic conflict with #3963 (merged meanwhile): its two new slot tests
  expected the old "unavailable" body for a refused action. A permission
  refusal is a failed action, so under this change it answers 502 +
  `HX-Reswap: none` and fills the page's or the panel's own alert slot.
  The tests now assert that (new `assertSlotActionFailed` for panel
  targets); the "plugin not asked" assertions are unchanged. No production
  code changed in the rebase.
