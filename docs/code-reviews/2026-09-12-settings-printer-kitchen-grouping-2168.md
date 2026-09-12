# 2026-09-12 — Settings → Printer: kitchen printer sat under the receipt-policy control (ut-docs#2168)

## What shipped

`web/ui/pages/settings.html`'s printer card rendered the kitchen-printer
address field directly above the receipt-policy control (`receiptPolicy`
select + the DE locked-note), with no visual boundary between them. A
real product-owner report on the pilot tablet (v0.14.13, DE locale) read
the working receipt-policy control as belonging to the kitchen printer —
i.e. as having been silently removed.

Fix: moved the kitchen-printer address field into its own
`<fieldset>/<legend>` group (mirroring the existing
`web/ui/pages/plugin_settings.html` precedent — plain inline style, no
new CSS class), positioned *after* every receipt-printer control (mode,
charset, address, device, discovery, drawer pin, system hint, receipt
policy — all still under the card's existing `<h2>` "Receipt printer").
The DE receipt-policy lock (ut-docs#1908, a separate open legal question)
is untouched — this card is explicitly not about that lock.

New i18n key `settings.printer.kitchen_group.title` added to every
locale in this repo (en/ar/fa/tr), value copied from each locale's own
existing `settings.printer.kitchen_addr` translation (same phrase). The
kitchen field's own inner label was changed from `kitchen_addr`
("Kitchen printer") to the existing `settings.printer.address` ("Printer
address") so the legend directly above it doesn't repeat the same phrase
verbatim (review finding — see below).

New regression test `TestSettingsPage_KitchenPrinterIsGroupedSeparatelyFromReceiptPolicy`
(`internal/pages/settings_page_test.go`) asserts that, after
`name="receiptPolicy"`, there is a `<fieldset>...</fieldset>` containing
both `name="kitchenAddr"` and the literal `<legend>Kitchen printer</legend>`
tag.

`make docs-shots` regenerated (the surface hash changed) — all 124
screenshots (31 topics × 4 locales) refreshed; `guard-docs-shots` green
at the new hash.

**Follow-through to the external language packs** (core key is brand
new, so core merges first per this repo's own CLAUDE.md/reviewer-skill
ordering rule): `ut-plugin-language-de` (`locales/de.json` +
`settings.printer.kitchen_group.title: "Küchendrucker"`, manifest version
1.1.65→1.1.66) and `ut-plugin-language-es` (`locales/es.json` +
`"Impresora de cocina"`, manifest version 1.1.56→1.1.57) — each PR opened
and merged in the same cycle, immediately after this PR merges, so the
`lang-pack-drift` red window on `main` stays as short as possible.

## Independent review

Reviewed by a fresh Opus subagent (complexity:medium → Sonnet builds,
Opus reviews), in an isolated git worktree, with its own revert-then-
restore TDD re-verification.

**Findings, and disposition:**

1. **Blocking (sequencing, not code):** the reviewer found both language
   pack repos' edits sitting correctly on local disk but not yet pushed/
   PR'd anywhere, and confirmed `scripts/ci/check-lang-pack-drift.sh`
   fails against each pack's live `main` (as expected for a brand-new
   key). Per this repo's own CLAUDE.md ordering rule ("the key is brand
   new... merge core first... the same lane... owns landing the pack
   follow-up(s) in the same cycle"), the correct order is core-first,
   pack PRs immediately after — not pack-first, which the pack's own
   `check-key-drift.sh` would reject as an orphan key. No diff change
   needed; this determines merge *order*, executed as: merge this PR,
   then push+open+merge both pack PRs before ending the cycle.
2. **Non-blocking, fixed:** the original assertion
   `strings.Contains(kitchenSection, "Kitchen printer")` was a false
   pass — `kitchen_addr`'s own former label text was the identical
   phrase, so the test still passed with the reviewer's `<legend>` line
   deleted entirely, meaning the new key had zero real coverage. Fixed to
   assert the literal `<legend>Kitchen printer</legend>` tag. Re-verified
   personally: reverting just this one line makes the test fail again for
   the right reason; restoring it passes.
3. **Non-blocking, fixed:** the same phrase ("Kitchen printer") rendered
   twice in a row (legend, then the field's own label) — reads oddly and
   a screen reader would announce it twice. Fixed by reusing the existing
   `settings.printer.address` key ("Printer address") for the field label
   instead, which needed no new key and disambiguates via the legend
   exactly the way `plugin_settings.html`'s own precedent does.
4. **Cosmetic, fixed:** a `{{ T "settings.printer.title" }}` template
   action inside the new HTML comment was dead (elided by `html/template`
   inside a comment, verified empirically) — reworded to plain prose.
5. **Verified clean, no change needed:** DE receipt-policy lock
   completely untouched (neither hunk touches those lines; Go-side
   `receiptPolicyLockedForCountry`/`print_api.go` unchanged;
   `TestSettingsPage_ReceiptPolicyControl` +
   `TestSettingsPage_GermanyLockSurvivesFormReplay` both pass); the
   reorder doesn't break `kitchenAddr` posting or the "Use for kitchen"
   discovery button (`getElementById` only, no DOM-position dependency,
   `id="printer-kitchen-addr-input"` preserved); no real client/shop name
   anywhere in the diff; i18n keys correctly sorted, no duplicates.
6. **Help manual (`web/help/en/printing.md`):** conclusion is no text
   update needed. The manual describes *what each control does*, never
   DOM layout/adjacency, and its step order already didn't match either
   the old or new DOM order. The screenshots (the part of the manual rule
   that actually goes stale on a layout change) were regenerated.
7. **UX (no Playwright available in the review environment, judged
   structurally):** the only new style is `margin-block-start:1rem`
   (logical, matches `plugin_settings.html`'s own `margin-block-end`); no
   new CSS class, so this gets identical browser-default fieldset/legend
   rendering to that precedent; no `left`/`right` anywhere in the diff;
   `dir="ltr"` preserved on the input; legend text is short in every
   locale (longest is es "Impresora de cocina", ~20 chars) so no overflow
   risk at the 1024×600 kiosk floor; no new modal blocker (a `<fieldset>`
   is inline flow content, not an overlay).

**Deferred, not fixed (out of scope for this card):** nothing — findings
2–4 were cheap enough to fold into this same diff rather than file
follow-ups.

## Verified (beyond automated tests)

- `gofmt -l .` — clean.
- `go build ./...`, `go vet ./...` — clean.
- `golangci-lint run ./internal/pages/...` (and, independently by the
  reviewer, project-wide) — 0 issues.
- `go test ./...` (whole repo, both by dev and independently by the
  reviewer) — all green.
- `bash scripts/ci/guard-i18n.sh` — all locales match en.json's key set,
  no duplicates.
- `bash scripts/ci/guard-compliance-claims.sh`,
  `guard-docs-shots.sh`, `guard-help-topics.sh`, `guard-help-drift.sh`
  (5 pre-existing baseline lines only, ut-docs#1973, unrelated),
  `guard-htmx-loaded.sh`, `guard-autofill-suppression.sh`,
  `guard-page-http-error.sh`, `guard-emoji-font.sh` — all green.
- Language pack repos: `scripts/validate.sh` and (pointed at this
  branch's local `en.json` via `UT_CORE_EN_JSON`) `scripts/check-key-drift.sh`
  both clean for `ut-plugin-language-de` and `ut-plugin-language-es`.

**Verdict: safe to merge**, no blocking code findings. Merge sequencing
(core, then the two pack PRs, in this same cycle) is the one thing that
had to happen in a specific order, and is handled as part of this
card's close-out.
