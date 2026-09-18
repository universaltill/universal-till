# Migrate remaining hardcoded inline-JS strings (ut-docs#453)

**Card:** ut-docs#453 — repo-wide follow-up to ut-docs#205 (guard-i18n.sh
check 5: hardcoded prose assigned to `.textContent`/`.innerHTML` inside
inline `<script>` blocks).

## What shipped

- `scripts/ci/guard-i18n.sh` check 5's glob widened from `web/ui/**/*.html`
  only, to also scan `web/public/**/*.js` (excluding `web/public/vendor/`,
  third-party code) — closing the gap the card's own header comment had
  flagged since #205.
- All 9 remaining `i18n:ignore`-tagged violations migrated to the
  established `var T = {...}` template-populated JS lookup pattern
  (precedent: `web/ui/partials/bugreport_panel.html`,
  `web/ui/pages/settings.html`'s already-migrated handlers):
  - `web/ui/layouts/base.html`'s self-update status-bar button (6 strings:
    confirm/downloading/uptodate/failed/restarting/installed-reload).
  - `web/ui/pages/catalog.html`'s item-image upload handler (4 strings:
    an error-prefix, pick-item-first, choose-image, uploaded).
  - `web/ui/pages/settings.html`'s customer-search handler ("No matches.").
- Two additional un-tagged hardcoded strings found in the same functions
  being migrated (invisible to the guard's regex — a ternary expression in
  base.html, and a string built via `.map()` in settings.html for a
  hardcoded "Erase" button label) were fixed as drive-bys, since leaving
  them while migrating their siblings in the same handler would be a
  half-finished migration. The general regex blind spot itself (any other
  live instance elsewhere in the repo) is filed as a separate follow-up,
  ut-docs#2423 — deliberately out of scope here.
- 14 new locale keys added to all 4 `web/locales/*.json` files (13 from
  the original migration + 1 more, `catalog.image_uploading`, added
  during review triage for a third un-tagged string at
  `catalog.html:1409` that the first review round found) — ar/fa/tr
  translated directly by this cycle's model, not machine-translated or
  baseline-parked.
- The two external language packs (`ut-plugin-language-de` PR #291,
  `ut-plugin-language-es` PR #291) got the same 14 keys translated in the
  same cycle, since these are brand-new core keys and the pack side can't
  land first (its own key-drift guard treats a translated key with no
  matching core key as an orphan). `main` will show `lang-pack-drift` red
  between this PR's merge and those two merging — expected, bounded, and
  owned by this same cycle per `scrum-master/SKILL.md`'s "Work that has no
  card is not covered" rule.
- `web/help/img/manifest.json`'s `surface_sha256` refreshed via
  `scripts/ci/update-docs-shots-surface-hash.sh` (no rendered pixel
  changes — every migrated string is written by JS only after a
  click/submit, never present in an initial page render; confirmed by
  reverting the three `web/ui/**` edits and observing the guard pass
  clean again).
- `CLAUDE.md`'s now-stale "Known gap: the guard only scans
  web/ui/**/*.html" note updated to reflect the widened coverage.

## Independent review (Opus, complexity:medium tier)

First round found 2 blockers and 3 real-but-minor issues; none were
disputed, all were fixed in this same cycle before merge:

- **Blocker — `guard-docs-shots.sh` red** (stale surface hash after the
  three `web/ui/**` edits). Fixed: ran
  `update-docs-shots-surface-hash.sh`, justified the no-pixel-change
  escape hatch (see above), re-verified the guard passes.
- **Blocker — `lang-pack-drift` would break `main`** (12 brand-new core
  keys, no pack follow-up). Fixed: opened and translated the two pack
  PRs named above; per the brand-new-key case this repo owns, `main`
  merges first and goes briefly red until those two land, same cycle.
- **Real-but-minor — `catalog.html:1409`'s `'⏳ uploading…'`** left
  un-migrated in the same handler (a single-word literal, an accepted
  heuristic gap in the guard's own prose regex). Fixed: added
  `catalog.image_uploading`, migrated, translated in all locales
  including the two packs.
- **Real-but-minor — the `'✗ '` error-prefix key used inconsistently**
  (present at `catalog.html:1400` but a second identical literal at
  `:1434` left inline). Fixed: both sites now use
  `imageT.uploadErrorPrefix`.
- **Real-but-minor — `CLAUDE.md`'s stale known-gap note.** Fixed (see
  above).

Independently re-verified personally (not taken on the reviewer's word):
- TDD claim for the guard widening — reverted `guard-i18n.sh`'s change,
  re-ran `guard-i18n_test.sh`, confirmed the `web/public/*.js` case fails
  with the exact expected message, restored, confirmed all 9 cases pass
  again.
- `web/public/app.js`'s previously-flagged strings were already migrated
  by an earlier, unrelated change (sourced via `card.dataset` from
  server-rendered `data-*` attributes) — read the file directly to
  confirm before trusting the claim.
- All 9 original `i18n:ignore` markers cleared — `grep -rn "i18n:ignore"
  web/ui/` returns zero hits repo-wide, not just in the three touched
  files.
- English locale values are byte-identical to the literals they replace
  (including em dashes, ellipses, and the ✗/✓ glyphs) — no user-visible
  change for English users; ar/fa/tr users newly see real translations
  instead of an always-English fallback.
- JS syntax validated (`node --check` on the extracted inline `<script>`
  bodies of all three touched files, Go template actions stripped first).
- Locale JSON validity + exact key-parity (ar/fa/tr == en's key set)
  confirmed for all 4 files after every edit round.

## Verified beyond automated tests

- Real driven run (`e2e/run-till.sh`'s boot pattern: throwaway DB,
  `UT_AUTH=off`, built binary run from inside the temp data dir) +
  Playwright, screenshotted and DOM-asserted:
  - Settings page customer-search "No matches." in en/fa/ar — correct
    text, correct RTL layout, no overlap/truncation (screenshots taken,
    not just asserted).
  - Catalog page image-upload validation messages in en/fa — exact text
    match via `textContent`, zero `pageerror`/console-error events.
  - Self-update status-bar button's armed ("Confirm update to v<ver>")
    state in en/fa — exact text match including the version-number
    concatenation, zero JS errors.
- `go build ./...`, `go vet ./...`, `gofmt -l .` — clean.
- `go test ./internal/pages/...` (the package rendering all three touched
  templates) — all packages pass.
- `bash scripts/ci/guard-i18n.sh`, `guard-i18n_test.sh`,
  `guard-data-access.sh`, `guard-compliance-claims.sh`,
  `guard-docs-shots.sh`, `guard-help-topics.sh`, `guard-help-drift.sh` —
  all pass.

## Explicitly deferred / out of scope

- ut-docs#2423 — widen `guard-i18n.sh`'s regex to catch a ternary-
  conditional or `.map()`-built hardcoded literal in general (this PR
  fixed the two specific live instances it found by hand; the general
  regex gap is a separate, larger effort).
- `catalog.image_upload_error_prefix`'s value (`"✗ "`) is a glyph, not
  translatable text, and is identical across all locales by design —
  allowlisted in both language packs via their own `same-as-en`
  mechanism rather than treated as an untranslated key.

## Safe-to-merge verdict

Yes, after the fixes above. No behavioural change for English users; a
real, verified improvement for ar/fa/tr users (previously-always-English
strings now show real translations). `main` will show `lang-pack-drift`
red for a short, bounded window until the two pack PRs land — same cycle,
same lane, tracked above.
