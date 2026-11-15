# Code review — refuse show_when cycles (ut-docs#4885)

Date: 2026-10-23 · Lane: `lane:cloud-41` · Built by Sonnet, reviewed by Opus 5.5 (fresh context).

## What shipped

- `internal/plugins/settings_v2.go`: `validateShowWhenAcyclic` refuses a
  visibility cycle in a manifest's settings. A node is a setting key. There
  is an edge S→K when S's `show_when` names K, or when S's `tab` has a
  `show_when` naming K. A setting inside the tab it controls is therefore a
  K→K loop. The check runs at the end of `validateSettingsV2` (top level,
  with tabs) and of `validateListSetting` (record fields, no tabs). Both
  install paths go through it (`ParseManifest`, `VerifyManifest`). The
  error names the path, e.g. `manifest settings: show_when cycle a → b → a`.
- `internal/plugins/settings_v2_test.go`: `TestSettingsV2_ShowWhenCycles`,
  10 cases run on both install paths. Refused: a 2-cycle, a 3-cycle, a tab
  controlled by its own setting, a cycle through a tab, a record-field
  cycle, and a cycle through a key repeated in another scope. Accepted: a
  chain, a tab controlled from another tab, a tab controlled from no tab,
  and a record-field chain.
- Mirrored on upload in ut-cloud `pkg/manifest` (`showWhenCycle`), and the
  rule is documented in ut-docs `reference/plugin-manifest.md`.

## Findings

1. **Fixed (low).** A key repeated in another scope overwrote the first
   entry's edges (`edges[s.Key] = out`), so a cycle through the first entry
   was missed. `validateSettingKeys` allows the same key in different
   scopes. Now the edges append, and a test case covers it in both repos.
2. **Accepted (cosmetic, ut-cloud only).** Upload reports a self-reference
   `show_when` twice: once from `checkCondition` and once as a cycle. Upload
   lists every issue, so a second line costs nothing, and skipping n==k
   would also drop the legitimate tab self-loop.
3. **Accepted.** No shipped or bundled plugin declares `show_when`
   (`plugins/layout-salon`, `plugins/tax-tr`). The v2 fixture manifests
   still pass.

The reviewer confirmed that the graph covers everything that controls
visibility on the page. Only settings and tabs carry `show_when`;
`depends_on` only re-asks options, and setup steps have no condition. The
page JS (`web/ui/pages/plugin_settings.html`) reads a hidden controller's
value, so a real cycle could hide both fields for good.

## Verified beyond the automated tests

- TDD re-verified in a separate worktree: with main's `settings_v2.go` the
  five cycle cases fail, and with the fix they pass. The same holds in
  ut-cloud. The repeated-key case failed before its fix in both repos.
- No UI, i18n or help change: this is manifest validation only.

## Verdict

Safe to merge.
