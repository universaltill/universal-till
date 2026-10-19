# Code review — plugin settings form v2, S2 field kinds (ut-docs#4157)

**Date:** 2026-10-19 · **Lane:** lane:cloud-41 · **Author model:** Opus 5.5 · **Reviewer model:** Fable (independent)

## What shipped

ADR-0121 amendment "plugin settings form v2", section S2, on the plugin
settings page (`/plugins/{id}/settings`):

- New field kinds drawn from the manifest: `bool` (a real checkbox styled
  as a ≥ 44px switch, with a `present_setting_<key>` marker so an unticked
  box saves `false` and an older form without the marker changes nothing),
  `number` (`inputmode="numeric"`, osk.js turns it to `none`), `percent`
  (locale decimal, `%` suffix, stored basis points), `money` (locale
  decimal, shop currency code, stored minor units), `text` with
  `max_len`/`placeholder`; `required` on every kind.
- Stored `value_json` shapes exactly as the ADR table; an old string that
  parses as the new type is rewritten on the next save, one that doesn't
  is shown with `plugins.settings.error.stored_invalid` and kept unless
  the operator changes it.
- Server-side validation of every visible field on every save; one failure
  answers 422 with the form re-rendered (htmx: the form swaps itself,
  `htmx:beforeSwap` lets the 422 through, focus moves to the first bad
  field; no JS: the whole page with status 422, success = 303). Values are
  kept, secrets never echoed, `aria-invalid` + `aria-describedby` per field.
- `PluginRepo.SavePluginSettingsBatch`: seals first, one transaction, one
  commit, `onSettingsChanged` once if anything changed, one
  `plugin_settings_saved` audit row. The takeaway editor's write joins the
  batch; its bad rate is now a 422 field error on its row.
- 9 new `plugins.settings.error.*` keys in en/ar/fa/tr (+ meta), help topic
  `plugins.md` in en/ar/fa/tr/de; language packs de/es/pt follow up.

Split off (filed, `blocked:dep` on #4157): tabs #4666, guidance #4667.

## Review findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | A `required` field hidden by `show_when` blocked Save in the browser (native constraint validation on a hidden control; no request, no message). | **Fixed:** form is `novalidate`; the server validates every visible field and answers with translated errors. Test: `TestPluginSettingsV2_FormIsNoValidate`, assertion in `…_GET_RendersEachKind`. |
| 2 | major | An invalid stored percent/money value prefilled into a `pattern` box blocked saving anything (same class as #4086). | **Fixed** by the same `novalidate`; submitted unchanged it is kept and siblings save. Test: `TestPluginSettingsV2_FormIsNoValidate`. |
| 3 | minor | Percent/money parse accepted values above 2^53 that the read side then flagged invalid. | **Fixed:** same bound at parse → `error.number`. New case in `…_ValidationFailsWith422AndWritesNothing`; verified it fails without the fix. |
| 4 | minor | show_when controller read from a secret's blank box ("keep"); chained show_when evaluates against a stale controller. | **Partly fixed:** a secret controller now reads its stored value. Chained controllers need an unusual manifest; left as is (noted on #4666, which reworks visibility for tabs). |
| 5 | minor | `statusWriter` forced 422 over a template-render 500. | **Fixed:** only a 200 is turned into 422. |
| 6 | nit | Number limits formatted with grouping/locale digits next to "no thousands separators". | **Fixed:** number limits are plain digits; percent/money keep the shop format. |
| 7 | nit | `plugins.settings.invalid_choice` / `invalid_endpoint` now unused. | Accepted: removal churns every language pack; left for a cleanup. |
| 8 | nit | afterSwap focus relies on `detail.elt`. | Accepted: correct in the vendored htmx; harmless if not. |

Reviewer re-verified two TDD claims by breaking the code: per-key hook →
`TestSavePluginSettingsBatch_OneCommitOneBumpChangedKeys` fails ("bumped 4
times"); skipping the validation gate →
`TestPluginSettingsV2_POST_ValidationFailsWith422AndWritesNothing` fails on
every sub-case. Also checked: no SQL outside `internal/data`, no file
writes, no `template.HTML`/`HTMLAttr`, no `showModal`, ADR-0082 fail-closed
POST unchanged, all plugin strings escaped.

## Verified beyond automated tests (Tester)

Driven run of the real binary (seeded till, FAQ plugin given a v2 manifest
with every kind, a hidden required field and an invalid stored number):

- 1024×600 en, 360 fa (RTL), 1024×600 tr: no sideways scroll; switch,
  `%`/currency suffixes, errors under fields sit correctly (suffixes on the
  inline-start side in RTL). Screenshots looked at.
- Invalid submit → 422, form swapped, three fields marked with translated
  messages (Persian digits on fa), summary next to Save, focus on the first
  bad field. Nothing stored.
- Valid submit with the hidden field revealed (mode → b) → 200, stored
  `enabled=false`, `max_items=7`, `discount=1250` (12.5%), `fee=340`
  (3.40), `extra=3`, invalid `old_count="twelve"` untouched.
- Not checked: real touch hardware (emulated viewport only), dark theme,
  the kiosk OSK with these fields.

## Gate

`gofmt` clean, `go build ./...`, `go vet`, `golangci-lint` 0 issues,
`go test ./internal/pages/ ./internal/data/ ./internal/plugins/
./internal/httpx/` ok, guards (i18n, i18n-meta, rtl, no-showmodal,
data-access, help-topics, help-drift, core-neutral, plugin-settings-bump,
page-http-error, docs-shots) ok.

**Verdict:** safe to merge.
