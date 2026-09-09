# 2026-09-09 — Sale entry point: cart icon + action label (ut-docs#1896)

## What shipped

The nav rail's route back to selling (`nav.till`) used a receipt-document
glyph and the place-name label "Till". Comparing the pilot tablet's SumUp
Business app against ours, the product owner noted SumUp leads with a
shopping-trolley icon meaning "sell" — a place-name label/icon told the
operator where they'd land, not what tapping it does. This is the small,
cheap half of the naming problem ut-docs#1829 already fixed the wording
of (removing the redundant "Home"/"Start" Menu tile) — this card is about
what the one remaining entry point's icon and label should be.

- `web/ui/partials/nav.html`: the till rail item's icon changes from
  `{{ icon "receipt" }}` to `{{ icon "shopping-cart" }}` — reusing the
  glyph ut-docs#1845 already vendored and that the self-order kiosk's
  checkout button already uses (`index.html`). No new icon path added.
- `web/ui/pages/menu.html` and `error_page.html`: the "← Back to sale"
  buttons get the same cart glyph (`.btn-ico`, a pre-existing class),
  replacing the plain "←" text arrow — every route back to selling now
  reads consistently.
- `web/locales/{en,ar,fa,tr}.json`: only the `nav.till` key's **value**
  changes, from a place name to an action word — en "Till"→"Sell", ar
  "الصندوق"→"بيع", fa "صندوق"→"فروش", tr "Kasa"→"Satış". No keys
  added or removed, so `lang-pack-drift` is not implicated by this diff
  the way a new key would be.
- `internal/httpx/icons.go`: no new glyph; doc comments on both
  `"receipt"` and `"shopping-cart"` updated to record the swap and the
  reasoning.
- `e2e/tests/nav-rail-svg-icons-1423.spec.ts`: the two assertions that
  pinned the till rail item's icon as `"receipt"` now expect
  `"shopping-cart"`. The parity/box-size assertion set was updated to
  match. `/fiscal-device`'s own unrelated `"receipt"` usage
  (`menu_page.go`) is untouched.
- `web/help/img/**` + `manifest.json`: `make docs-shots` regenerated all
  108 shots (27 topics × 4 locales) — the nav rail renders on every
  authenticated page, so the shared app-surface hash `guard-docs-shots.sh`
  checks changed.

## Independent review

Fresh-context Sonnet subagent (complexity:easy → same-tier review, per
`scrum-master`'s model-routing table), isolated worktree, no access to
the implementation reasoning. Actually ran (not just read): `gofmt -l .`,
`go build ./...`, `go vet ./...`, `golangci-lint run` (0 issues),
`go test ./internal/httpx/... ./internal/pages/...` (green, including
`TestBaseMenu_NoRedundantHomeTile` and
`TestMenuPage_EveryDrawnTileIconNameResolves`), `guard-i18n.sh`,
`guard-docs-shots.sh`, `guard-help-topics.sh`, `guard-kiosk-engine.sh`,
`guard-compliance-claims.sh` — all clean. e2e Playwright run was not
executable in that worktree (no pre-installed Chromium resolved there);
the updated spec was reviewed statically instead and was separately run
for real from the main checkout (see below).

**Verdict: safe to merge.**

**One non-blocking finding, fixed:** `internal/pages/menu_page.go`'s
`iconSVGFor["/"]` was left at `"receipt"` — dead today (`baseMenu` has no
`"/"` entry per ut-docs#1829/`TestBaseMenu_NoRedundantHomeTile`, so this
key is never looked up), but stale and inconsistent with the rest of the
diff's "every entry point" claim. Fixed to `"shopping-cart"` with a
comment explaining why the key is unreachable but kept, and `make
docs-shots` re-run (only `web/help/img/en/sell.png` changed at the byte
level — a 2-byte PNG re-encode with no visible pixel difference,
confirmed by re-viewing the image; `/` renders nothing from this map in
production).

The reviewer also confirmed: i18n word choices are real, sensible words
for "sell" in each language (not transliterations), short enough not to
overflow the rail's hidden/phone-width label; no new CSS anywhere in the
diff (`.nav-toggle`/`.btn-ico` are pre-existing, reused classes, so RTL
logical-property behaviour is unaffected); commit author is the pipeline
owner's real GitHub-linked identity with Claude only in a
`Co-Authored-By:` trailer; no file-write handlers in this diff, so the
`os.MkdirAll`/`paths.Data` recurring-bug classes don't apply.

## Verified beyond automated tests

- `go test ./...` (full suite, main checkout): green.
- `golangci-lint run ./...`: 0 issues.
- All `build`-job CI guards touching this surface (`guard-i18n.sh`,
  `guard-docs-shots.sh`, `guard-help-topics.sh`, `guard-data-access.sh`,
  `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
  `guard-page-http-error.sh`, `guard-compliance-claims.sh`,
  `guard-emoji-font.sh`, `guard-htmx-loaded.sh`): all pass.
- `e2e/tests/nav-rail-svg-icons-1423.spec.ts` run for real against a
  live server (pre-installed Chromium at
  `/opt/pw-browsers/chromium_headless_shell-1194`): all 3 tests pass,
  including the phone-width (360px) test that now asserts
  `svg[data-icon="shopping-cart"]` is visible with its label.
- **Visual check (attestation, not just automated assertions):**
  regenerated screenshots read directly for `/menu` and `/` (sell
  screen) in **en** and **ar** (RTL), plus `/menu` in **fa** (RTL) —
  cart icon + action label render cleanly in all three, no
  overlap/clipping/wrapping, RTL mirrors correctly (nav rail stays on
  the visual right, icon trails the Arabic/Farsi label correctly, same
  as the unmodified rail items around it). Turkish (`tr`) was checked
  via the regenerated screenshot set's presence/guard pass but not
  individually eyeballed by a human-equivalent read — low risk given
  the label is a single short Latin-alphabet word ("Satış") in a slot
  that already fits longer neighbours.
- Not checked, and explicitly out of reach for a cold cloud cycle: a
  real physical tablet (the card's own AC 4/"10.1\" pilot tablet, de
  locale") — `de` is not one of this repo's 4 shipped core locales (it's
  the external `ut-plugin-language-de` pack); real-hardware verification
  needs a local/interactive session with the device, per the same
  reasoning `ut-docs#1281`/`ut-docs#1720` already established for this
  class of check. Residual risk is low: this reuses the already-vendored,
  already-shipped SVG icon mechanism ut-docs#1423 built specifically so
  no icon can render inconsistently across devices/fonts (the exact risk
  real-hardware checks exist to catch for a *new* glyph) — this diff
  introduces no new glyph, only repoints an existing rail item at one
  already in production use (the kiosk checkout button).

## Deferred / follow-up

- **External pack drift (not CI-enforced, so easy to miss):** the
  `ut-plugin-language-{de,es}` packs' own translations of `nav.till`
  still say the equivalent of "Till", not "Sell" — `lang-pack-drift`
  only checks key *presence*, not translation freshness of an existing
  key's value, so this will not go red anywhere. Filed as a new Backlog
  card so a session with access to those repos can update the two
  translations to match the new semantic.
- Real-tablet verification (see above) — filed as a new Backlog card,
  same pattern as ut-docs#1281/#1720.

## Safe-to-merge verdict

Yes. `merge_method: "merge"` (never squash/rebase, per this skill's own
note on commit re-attribution).
