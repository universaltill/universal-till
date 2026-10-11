# ut-docs#2832 slice 1: plugin settings dropdowns, conditional fields, labels

**Repos:** universal-till (this record), ut-cloud (signer mirror), ut-plugin-integration-ai 2.4.0 (consumer), ut-docs (architecture docs), ut-plugin-language-{de,es,pt} (3 new keys).
**Author:** Opus 5.5 (lane:sdk). **Reviewer:** Fable (independent, read-only, ran the affected tests).

## What shipped
- Manifest `settings[]` gains `label`, `help` (i18n keys), `type: "choice"` + `options[{value,label}]`, and `show_when{key,in}`. These are validated in both install paths (`ParseManifest`, `VerifyManifest`) by `validateSettingForm`: 1–32 unique options, a default must be an option, no options on a non-choice, and `show_when` must name another declared setting that is neither declared nor heuristically a secret, with no comma in its values. Choice is also refused on a credential-named key.
- Settings page: fields are ordered as the manifest declares them, with the translated label (raw key as the title) and help. A choice renders as a `<select>`:
  - an unset value shows a selected "not set" entry, so the browser never preselects option 1;
  - an older free-text value stays selectable as itself (`data-unknown`) and hides nothing;
  - POST refuses a value that is neither an option nor the trimmed stored value (400, localized) before any write.
- `show_when` is applied server-side (`hidden`) and live in JS. A saved secret shows a "✓ Saved" tag and a "Remove saved key" checkbox (`clear_setting_<key>`, owner 2026-10-10); a blank box still keeps the key.
- Locale keys `plugins.settings.invalid_choice`, `.secret_saved`, `.secret_remove` are added in en/tr/ar/fa, with pack PRs for de/es/pt. Help topic `plugins.md` is updated in all 5 languages.

## Why `type: "choice"` and not "options on a plain setting"
A concurrent lane:sdk run (which yielded to this one) proposed no new `type`, so that older tills would ignore the unknown fields. That premise fails for marketplace installs: the POS verifies the signature over a re-marshal of its own `Manifest` struct (same as ut-cloud `canonicalBytes`). An older till drops the unknown fields and fails verification either way. An explicit type plus `min_pos_version` gives a clear "requires POS ≥ X" refusal instead of a signature error.

## Review findings (Fable) and outcome
1. MAJOR: an empty stored choice rendered option 1 as selected, so saving a sibling setting would write option 1. **Fixed**: a selected "not set" entry, with a test.
2. MAJOR (process): the plugin edits sat on `main`. A concurrent run's yield had switched the checkout. **Fixed**: moved to `feat/2832-choice-settings` before committing.
3. MAJOR: the plugin's `min_pos_version` was still 1.0.0. **Fixed at release**: it is set to the first core release carrying this change before the plugin merges, and `validate.sh` asserts it.
4. MINOR: `show_when`/`choice` on a heuristic-secret key. **Fixed** in the validator, with tests.
5. MINOR: merge order. **Followed**: universal-till first, ut-cloud right after (mirror guard reads universal-till `main`).
6. MINOR: a legacy controlling value hid fields the plugin still uses. **Fixed** server-side and in JS, with a test.
7. MINOR: an untrimmed stored value made re-saving fail. **Fixed**, with a test.
8. MINOR: the manifest is parsed twice per request. **Accepted**: admin page only, a small file; threading it through `secretSettingCheck` would widen the diff.
9. NIT: a Typed (takeaway) row inside the wrapper gets a double gap, and label/help are ignored there. **Accepted**: cosmetic, and no plugin declares label on a typed key.
10. NIT: global vs per-till row in the `show_when` map. **Fixed**: the global row wins.
11. NIT: test gaps. **Fixed** by the tests above.

## Verified beyond unit tests
- TDD re-verify: with the POST choice check removed, `TestPluginSettingsAPI_POST_ChoiceValidated` fails ("OpenAI": code 200, want 400). Restored, it passes.
- A real driven run (built binary, throwaway data dir, the real AI plugin 2.4.0 manifest + locales, Playwright):
  - switching the provider shows/hides the address and key live (self_hosted/compat/claude/openai);
  - the key saves and shows ✓ Saved, and is never in the page;
  - Remove saved key clears it;
  - a legacy `OpenAI` value shows both fields, and typing hides nothing.
- Screenshots looked at: en 1024×600 and 360 wide, de 1024 (plugin labels in German; core strings fall back to English because no de pack was installed on the throwaway till), fa 1024 (RTL correct). Fixed after looking: the Saved tag stretched full width, and fields without help lost their spacing. Not checked: the kiosk `body.kiosk` mode and real touch hardware. The page is a native `<select>`, but there was no tablet run this cycle.
- Gates: full `go test ./...` rc=0 (before the review fixes); after the fixes, `internal/plugins` (full) and `internal/pages` (full) pass, plus guard-i18n, help-drift, help-topics, compliance, competitor, core-neutral, data-access, autofill, emoji-font, no-showmodal and kiosk-engine. `guard-deadcode-baseline` fails locally on `internal/logging/file.go`, which this branch doesn't touch (pre-existing, local toolchain). Local golangci-lint is too old for go1.27; CI runs it.

## Verdict
Safe to merge after CI, in order: universal-till → ut-cloud → language packs → core release → plugin 2.4.0 with `min_pos_version` set.
