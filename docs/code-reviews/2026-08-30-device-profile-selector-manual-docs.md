# Code review — document the device-profile selector in the manual (ut-docs#1302)

- **Date:** 2026-08-30
- **Branch:** `docs/1302-device-profile-selector-manual`
- **Reviewer:** independent reviewer (fresh-context Sonnet subagent, different
  instance from the implementer — `complexity:easy` per the model-routing
  table, where "different model" relaxes to "different instance")
- **Verdict: SAFE TO MERGE.** No blocking findings.

## What shipped

Follow-up from ut-docs#1259 (review finding NB-4, PR universal-till#643).
The device-profile selector (`POST /api/settings/display-mode` —
Register / Back office / Self-order kiosk, `web/ui/pages/settings.html`'s
`<select name="mode">`) was completely undocumented in
`web/help/{en,fa,ar,tr}/display.md` — a pre-existing gap that ut-docs#1259
made more pressing by adding new user-visible behavior to that exact
selector (switching to self-order now signs that screen's session out).

Added a new numbered step (item 9, all four locales — old items 9–15
renumbered to 10–16) covering:

- what each of the three profiles does (Register = default sell screen,
  Back office = manager dashboard, Self-order kiosk = the locked-down,
  customer-facing ordering screen — linked to `web/help/en/self-order.md`),
- that it's a per-till setting (never syncs over the LAN), so one shop can
  mix all three profiles across its tills at once,
- that switching to Self-order kiosk signs out the acting browser
  immediately (mirroring Log out) — Register/Back office never do,
  and why (the self-order screen is auth-exempt by design),
- that changing it needs a manager PIN, same as the other Display
  settings on that page.

`web/help/img/manifest.json` was regenerated (`make docs-shots`, scoped to
the `display` topic across all 4 locales via
`--grep "screenshot: display"`) since the topic markdown's content hash
moved. No PNG changed — the rendered screen itself (`settings.html`) was
untouched by this doc-only change, so the capture is pixel-identical to
what's already committed; this is the documented "manifest-only" legitimate
outcome (`e2e/tests-docs/docs-shots.spec.ts`'s own comments, ut-docs#930).

## Independent review (fresh-context Sonnet, worktree-isolated)

Checked the new English content against the actual source
(`internal/pages/settings_page.go`'s `POST /api/settings/display-mode`
handler, `web/ui/pages/settings.html`'s select block,
`web/locales/en.json`'s `settings.display.mode*` keys) — every claim in
the doc matches what the code does, and nothing the code does is left
undocumented. Verified numbering integrity (clean 1→16 in all four
locales, no gaps/duplicates), fa/ar/tr structural translation quality
(same paragraph position, bold markup on the three option names, the
`[Self-order](/help/self-order)` link present and correctly pointed), no
real client/shop name, and confirmed `self-order.md` needs no change for
this card's scope. Re-ran `guard-help-topics.sh`, `guard-i18n.sh`,
`guard-compliance-claims.sh`, `guard-docs-shots.sh` (all pass), `gofmt -l .`
(empty), and `go build ./...` (succeeds) independently in an isolated
worktree.

**One trivial, non-blocking observation:** the doc's prose calls the Back
office destination "the manager dashboard" while the on-screen select
option's parenthetical label is "(manager station)"
(`settings.display.mode_backoffice` in `en.json`). Both are accurate —
"manager dashboard" is `backoffice_page.go`'s own doc-comment wording — so
this is two independently-correct descriptions, not a defect. Not fixed;
not worth a follow-up card.

## Verification

| Check | Result |
|---|---|
| `guard-help-topics.sh` | pass |
| `guard-i18n.sh` | pass |
| `guard-compliance-claims.sh` | pass |
| `guard-docs-shots.sh` | pass (23 routed topics × 4 locales fresh) |
| `gofmt -l .` | empty |
| `go build ./...` / `go vet ./...` | pass / pass |
| Full guard suite (`guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-webkit-version.sh`,
  `guard-kiosk-launch-flags.sh`, `guard-android-status-address.sh`,
  `guard-android-i18n.sh`, `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `check-brand-assets.sh`,
  `guard-makefile-version.sh`, `guard-migration-version-collision.sh`,
  `guard-osk-loaded.sh`) | all pass |

No Go source touched, no tests applicable — this is a documentation-only
change.

Closes universaltill/ut-docs#1302.
