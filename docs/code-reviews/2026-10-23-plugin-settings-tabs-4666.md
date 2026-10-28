# Code review — plugin settings form v2, S1 tabs (ut-docs#4666)

## What shipped

ADR-0121 amendment "plugin settings form v2", section S1, on the plugin
settings page (`/plugins/{id}/settings`):

- A manifest with `settings_tabs` is drawn as tabs on one form with one
  Save. Settings without a `tab` (and stored keys the manifest doesn't
  declare) go to a core "General" tab drawn first; declared tabs follow
  in manifest order; a tab with no settings is not drawn.
- Markup: each tab is a `<details>` (all open with JS off). With JS, 600px
  and up builds a `role=tablist` strip (Left/Right/Home/End, mirrored in
  RTL, 44px targets, scrolls sideways) and shows the active panel; below
  600px the `<details>` stay an accordion with the active one open.
- A tab's `show_when` hides the whole tab; its fields are neither
  validated nor written. One helper (`settingVisibility`,
  `plugin_settings_fields.go`) answers hidden-ness for both render and
  save.
- Chained `show_when` (deferred from #4157's review): a controlling
  setting that is itself hidden — by its own `show_when` or by its tab —
  counts as its stored value, server-side and client-side
  (`data-show-when-stored`).
- After a refused save, each tab with errors shows "⚠ N" with an
  accessible label and the first such tab is opened; focus moves to the
  first bad field.
- A manifest without `settings_tabs` renders byte-identically (golden
  test, captured before the template change).
- 3 new keys (`plugins.settings.tab.{general,errors,list}`) in en/ar/fa/tr
  + meta; help topic `plugins.md` in en/de/ar/fa/tr.

Built by Sonnet (Dev subagent); reviewed by an independent Opus 5.5
subagent in a separate worktree.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | Run-once `window.__utPs*` flags: `#ut-page` is `hx-boost`ed and `base.html` drops a page's window/document/body listeners when it's swapped away, but the flag survives — on a second boosted visit to any plugin's settings, tabs never initialise (and, pre-existing from S2, show_when and the 422 swap hook were dead too). | Fixed: all three flags removed (the shell already de-duplicates listeners); the one listener the shell doesn't track (`matchMedia`) replaces the previous visit's. Driven: three boosted visits each get one strip; 422 swap + focus work on the third. |
| 2 | minor | Render-side chain rule untested (deleting `sv.Hidden = vis.fieldHidden(...)` passed every test). | Fixed: `TestPluginSettingsTabs_ChainedShowWhenRerenderMatchesSave`; confirmed it fails with that line removed. |
| 3 | minor | No test for a controller hidden by its *tab*. | Fixed: `TestPluginSettingsTabs_ControllerHiddenByItsTabUsesStoredValue`; confirmed it fails with the tab check removed from `skipped`. |
| 4 | minor | `show_when` cycles (A↔B, or a tab controlled by one of its own settings) pass manifest validation; server is bounded but order-dependent, and such a tab can never come back. | Accepted here (no plugin declares one); follow-up card to refuse them at install. |
| 5 | minor | A hidden typed takeaway-overrides editor is now skipped on save (was validated regardless). | Accepted: intended — same rule as every other hidden field. |
| 6 | nit | `tab.errors` uses "%d error(s)" rather than an ICU plural; Latin digits in the badge on ar/fa. | Accepted: matches the existing `%d` keys (ICU move is #4480). |
| 7 | nit | Tab panels not focusable; CSS comment claimed one-open accordion. | Fixed: `tabindex=0` on panels in strip mode; comment corrected. |

TDD re-verified by the reviewer: the 5 behaviour tests fail on the
pre-change code for their stated reasons; the golden passes on the old
code (so it really pins "unchanged"); replacing `vis.skipped` with the old
save check fails HiddenTab and Chained; removing the stored fallback in
`effective()` fails Chained.

## Verified beyond automated tests (Tester)

Driven run of the real binary (e2e seed + the FAQ plugin given a tabbed
manifest: General, a connection tab with help, a fiskaly-only TSE tab, an
empty tab, an advanced tab):

- 1024×600 tr and en: strip with 44px tabs, no sideways page scroll,
  long German tab labels scroll in the strip; ArrowRight moves and
  selects; choosing fiskaly reveals the TSE tab live; empty tab not drawn.
- Refused save (blank required URL, out-of-range number, required TSS id):
  422, strip rebuilt once, "⚠ 1" on each failing tab, the first opened,
  focus on its bad field. Screenshot looked at.
- 360px fa (RTL): accordion, General open, others collapsed, hidden TSE
  tab absent; tapping a summary opens it. Screenshot looked at.
- JS off: every tab expanded one after another. Screenshot looked at.
- Not checked: real touch hardware and the kiosk OSK, dark theme, the
  rotation (matchMedia) path on a device.

## Gate

`gofmt` clean, `go build ./...`, `go vet`, `golangci-lint run ./...` 0
issues, `go test ./...`, every guard in `ci.yml`'s build job.

**Verdict:** safe to merge.

## Deferred

- Refuse `show_when` cycles at install (finding 4) — Backlog card.
- `ut-plugin-language-{de,es,pt}`: the 3 new keys, after core merges.
