# Review: release notes v0.35.1

Date: 2026-10-28 · Docs-only change (web/release-notes/{en,de,ar,fa,tr}/v0.35.1.md)

## Scope check

Notes list only what `git log --first-parent v0.35.0..origin/main` contains:
join-flow fixes (#2183), support-partner help (#2199), low-disk floor cap
(#2198), camera overlay vs status bar (#2196), Windows startup-failure page
(#2195), per-signer known-offline (#2193), sales push cursor (#2185), Local
Plus plan (#2178), order-tracking help wording (#2188).

Left out on purpose: SDK `LocationList` (#2186, developer-only),
`set_category_item_order` (#2187, my. slices B and C not shipped yet), DST
test fix (#2197, tests only). The sales-copy PRs (#2169 to #2179 M-series)
are still open and are not mentioned.

## Checks

- Plain New/Improved/Fixed, no PR or card numbers; guard-release-notes.sh ok.
- Translations done by the cycle model against the v0.35.0 glossary
  (till = Kasse / kasa / صندوق); structure (headings, bullet count) identical
  across the five locales.
