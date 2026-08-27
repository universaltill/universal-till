# 2026-08-27 — TSE provisioning banner: fix message clipping (ut-docs#1112)

## Summary

Fixed the Settings page's `tseProvisioning` banner silently clipping a
long message at the card edge instead of wrapping.

## Background

Found by Tester while verifying ut-docs#1026 (the `missingFiscalSigner`
banner) — pre-existing, not introduced by that card, out of scope there.
`web/ui/pages/settings.html`'s `tseProvisioning` block rendered its
message inside a `.chip`. `.chip` is `white-space: nowrap`, and the
parent `.settings-grid .card` is `overflow: hidden` — so a long message
(e.g. "TSE setup could not be started: an active subscription is required
for a managed TSE") was silently clipped at the card edge, not wrapped or
ellipsized visibly. Reproduced in both LTR (en) and RTL (fa).

`#1026` hit the identical bug for its own new `missingFiscalSigner`
banner and fixed it there by swapping `.chip` for `.notice-block-warn` (+
`.row-warn-icon`) — this codebase's existing wrapping warning-block
component (`internal/pages/import_page.go`'s currency-confirmation
prompt uses the same pattern). This card applies the same fix to the
sibling `tseProvisioning` block.

## Change

- `web/ui/pages/settings.html`: the `tseProvisioning` block's message
  span swapped from `.chip` to `<p class="notice-block-warn">` +
  `<span class="row-warn-icon">`, matching `missingFiscalSigner`'s
  pattern exactly. The block now wraps in a `.tse-provisioning-block`
  div (replacing `.chip-row`) so the dismiss button's
  `hx-target="closest .tse-provisioning-block"` still resolves to the
  whole message+button unit for the outerHTML swap on dismiss.
- `internal/pages/setup_tse_test.go`: `TestSettingsShowsTSEProvisioningChipAndDismiss`
  extended with an assertion that the message renders inside
  `.notice-block-warn` — TDD-confirmed (fails pre-fix with an explicit
  regression message, passes post-fix).
- `web/help/img/manifest.json`: regenerated via `make docs-shots` (the
  settings page's own screenshot is unchanged — `tseProvisioning` isn't
  shown in the default/seeded state used for docs screenshots — but the
  surface hash moved since `settings.html` changed). One unrelated
  `invoices.png` PNG-encoder-noise diff (6 bytes, no content change) was
  reverted, same precedent as `#1026`'s own review.

No Go logic changed — markup/CSS-class only, per the card's stated scope.

## Verification

- `gofmt -l .`, `go build ./...`, `go vet ./...` — clean.
- `go test ./...` — full suite green.
- All 18 CI-blocking guard scripts — all pass (including `guard-i18n.sh`
  and `guard-docs-shots.sh`, the two most relevant to this diff).
- **TDD**: reverted just the `settings.html` fix, confirmed the new test
  assertion fails with `"TSE provisioning message not wrapped in
  .notice-block-warn — regressed to the clipping .chip layout
  (ut-docs#1112)"`; restored the fix, confirmed it passes.
- **Real driven run**: started the app, seeded a `kickoff_rejected` +
  `subscription_inactive` TSE provisioning state directly in SQLite,
  logged in through the setup wizard, and screenshotted `/settings` at
  1024px width in both en/LTR and fa/RTL. `scrollWidth === clientWidth`
  (450 === 450) in both locales — no horizontal overflow/clipping.
  Visually: the message wraps cleanly across two lines in both
  locales; RTL correctly mirrors the icon+text to the right with the
  dismiss button below. Server killed after.
- No manual/help-topic update needed — this is a CSS-class fix to an
  existing banner already covered generically by the Settings page's
  help topic, not new user-facing behavior or a new page/route.

## Independent review (different-model pass)

Fresh-context Sonnet subagent, worktree-isolated (no prior involvement,
own git worktree so its TDD revert/restore never touched the shared
checkout). Independently:

- Re-ran the exact TDD revert→fail→restore→pass sequence itself and
  confirmed the same output.
- Re-ran the full gate itself (`gofmt`, `go build`, `go vet`, full
  `go test ./...`, `guard-i18n.sh`, `guard-docs-shots.sh`,
  `guard-help-topics.sh`).
- Confirmed `.notice-block-warn`/`.row-warn-icon` predate this change
  (no new CSS invented) and use only logical properties (no
  `left`/`right`), reading the actual CSS source.
- Confirmed the `.chip`/`overflow:hidden` clipping claim directly against
  the CSS source (`app.css:288`, `:1533`).
- Confirmed `hx-target="closest .tse-provisioning-block"` correctly
  resolves given the new wrapper structure.
- Checked for the two recurring bug classes (missing `os.MkdirAll`,
  cwd-relative path vs `paths.Data(...)`) — confirmed inapplicable, zero
  Go logic in the diff.
- Confirmed no client/shop name or hardcoded secret anywhere in the diff.

**Verdict: safe to merge as-is.** No findings, blocking or otherwise.

## Outcome

Merged via PR (see issue for link).
