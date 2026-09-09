# Rename the quick-sale-buttons feature from "Designer" to "Quick Buttons" (ut-docs#1848)

**PR:** universaltill/universal-till#940 (merged) · companions:
`ut-plugin-language-de`#198, `ut-plugin-language-es`#199 (both merged)
**Card:** universaltill/ut-docs#1848 — German pilot merchant feedback: the
top menu read as untranslated English (`nav.designer`, `nav.journal`
shipped byte-identical to English in the `de` pack despite being
key-complete), and separately "Designer" was judged the wrong English word
for a grid of quick-sale buttons.

## What shipped

- `nav.designer` / `designer.title` renamed **"Designer"/"Till Designer" →
  "Quick Buttons"** in English, with a real translation in every shipped
  locale (ar/de/es/fa/tr), consistent across the nav menu, the `/designer`
  page's own H1 and browser-tab title (`internal/pages/designer_page.go`),
  the `till-designer` manual topic (en/ar/de/fa/tr), the `catalog.md`
  cross-reference to the feature, and the "no products yet" empty-state hint.
- `nav.journal` / `journal.title` fixed in the German pack (same
  identical-to-English bug) → **"Kassenjournal"**, propagated to every
  German manual topic that names the screen (`reports.md`, `sell.md`,
  `order-status.md`, `self-order.md`).
- Full top-level nav audited for the same bug class: `nav.github` and
  `nav.plugins` are also identical to English in German — both reviewed
  and kept as deliberate loanwords (brand name; common German POS/tech
  usage), not a miss.
- New `scripts/audit-nav-i18n-parity.sh`: an advisory (non-CI) report that
  flags a locale's `nav.*`/screen-title value when it's byte-identical to
  English, with a reviewed loanword allowlist — satisfies the card's AC5
  ("reviewable output, not a hard gate").
- `ut-plugin-language-de`: pruned 3 stale `i18n-baseline/de.same-as-en.txt`
  allowlist entries that had wrongly called `nav.designer`/`nav.journal`/
  `journal.title` deliberate loanwords, and bumped `manifest.json` to
  1.1.32 so the fix actually tags and publishes.
- `ut-plugin-language-es`: `nav.designer`/`designer.title` updated to match
  the renamed English concept ("Botones rápidos" — the Spanish pack's
  values were already real translations, not the identical-to-English bug),
  manifest bumped to 1.1.23.

**Non-goals, left alone:** `designer.receipt.title` ("Receipt designer" —
a genuinely different feature per the issue itself), and the site-wide
pattern of every page's browser-tab `<title>` being a hardcoded English
literal (touched only `designer_page.go`'s own, to keep it consistent with
its renamed feature — the systemic gap is real but belongs to its own card).

## Independent review (Opus subagent, per this card's complexity:medium routing)

First pass returned **NOT SAFE TO MERGE** with two blockers and one medium
finding, all fixed before merge:

- **B1 (blocker):** `guard-docs-shots.sh`'s surface hash was stale —
  `internal/pages/designer_page.go` (a file that registers the screenshotted
  `/designer` route) was edited *after* the first `make docs-shots` run.
  Fixed: re-ran `make docs-shots`, verified the guard green.
- **B2 (blocker, the most valuable finding):** neither language-pack PR
  bumped `manifest.json`. `auto-tag-release.yml` only tags (and
  `release.yml` only publishes) when the version has no matching tag yet —
  without the bump, both PRs would have merged green and **shipped nothing
  to any German or Spanish till**, the exact bug this card exists to fix,
  silently. Fixed: bumped both packs' versions; confirmed post-merge that
  `v1.1.32` (de) and `v1.1.23` (es) tags were actually created by
  `auto-tag-release.yml` pointing at the merge commits.
- **M1 (medium):** `catalog.md`'s step 7 (in all 5 locales) still told the
  merchant to use "Till Designer"/"Kassen-Designer"/etc. — the manual's own
  cross-reference to the renamed feature. Fixed in all 5 locales.
- Lower-severity findings also fixed: Turkish "Hızlı Butonlar" didn't match
  the product's existing "düğme" vocabulary → renamed to "Hızlı Düğmeler"
  (nav, screen title, manual, with correct vowel-harmony suffixes); German
  `designer.search_title` said "Schnelltasten" next to `designer.title`'s
  own "Schnellwahltasten" → aligned; a stale dev-facing HTML comment in
  `buttons.html` updated; the German manual's other "Journal" mentions
  (`reports.md`, `sell.md`, `order-status.md`, `self-order.md`) brought in
  line with the `nav.journal`/`journal.title` fix.
- Confirmed clean by the reviewer and independently re-verified: no
  `designer.receipt.*` touched, no secret-shaped literal, no real
  client/shop name, all 6 changed locale JSONs valid with no
  cross-locale value swaps, all `till-designer.md` front matter parses,
  screenshots show the correct rendered text (`en`/`ar` visually confirmed).

## What was verified beyond the independent review

- `gofmt -l .` clean, `go build ./...`, full `go test ./...` (all packages
  green), `golangci-lint run ./...` (0 issues) — re-run after every one of
  six `main`-merge conflict-resolution rounds this PR needed (see below),
  not just once at the start.
- `scripts/ci/guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-compliance-claims.sh`, `guard-docs-shots.sh` all green on every
  round; `scripts/audit-nav-i18n-parity.sh` confirmed the German pack has
  no remaining unreviewed identical-to-English `nav.*` value.
- `ut-plugin-language-de`/`-es`: `scripts/validate.sh` green on both.
  `scripts/check-key-drift.sh` failures on both were investigated and
  confirmed pre-existing and unrelated to this diff (orphan
  `pos.toast.voucher_scan_*`/`tender.pay_voucher` keys from `ut-docs#1833`'s
  pack-side translations landing ahead of core's matching PR, and
  separately missing `import.xlsx_*`/`elevation.summary.allow_negative_*`/
  `settings.stock_tracking.*` keys from two other in-flight cards,
  `ut-docs#1837`/`#1843`) — verified via a clean worktree on each pack's
  own unmodified `main`, and documented with the exact commits that
  introduced them, in standing-down comments on both PRs rather than
  silently absorbing unrelated scope. (Both gaps were independently
  resolved by other lanes' own follow-up work within the hour, per this
  pipeline's "the lane that merges a core change owns the implied pack
  follow-up" rule — see `ut-docs#1861`, filed then closed as
  already-addressed.)
- `main` moved six times under this PR while it was in flight (five other
  cards' PRs merging concurrently: #939 xlsx import, #938 voucher scan,
  #936 stock tracking, #942 menu Home-tile removal, #941 scan-collect
  routing) — each required a real merge (not rebase, per this repo's
  "never rewrite history" convention), full re-validation, and a
  `make docs-shots` regeneration; one merge (`web/help/de/order-status.md`
  against #941) needed a manual three-way resolution to keep both sides'
  content (the new scan-collect step plus this PR's Kassenjournal rename).
  All confirmed with a fresh `go build`/`go test`/lint/guard pass each time.
- Merged with `merge_method: "merge"` (never squash/rebase), per this
  repo's real-email-leak mitigation.

## Safe-to-merge verdict

**Merged.** Both companion pack PRs merged and confirmed tagged/publishing.
`main`'s `ci`/`android-ci` green on the merge commit
(`67f621c526107019646faecbf74321da2df35782`); `lang-pack-drift` failed on
the same commit, confirmed pre-existing (see above) and tracked separately.

## Explicitly deferred

- The site-wide hardcoded-English browser-tab-`<title>` pattern (L1 in the
  independent review) — real, ~30 other handlers share it, but out of this
  card's scope; not filed as a new card by this session (low-severity,
  cosmetic-only, no user-facing regression).
