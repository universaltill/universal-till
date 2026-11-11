# Review — icon auto-suggest in the shop's languages (ut-docs#2654)

Date: 2026-11-11 · Author: Sonnet (Dev) · Reviewer: Opus 5.5 (independent) · Tester: separate agent

## What shipped

- **`catimport.IconMatcher`** (`internal/catimport/placeholder_lang.go`): built once
  from the shop's languages and an exact-locale label lookup; each language's
  `catalog.builtin_icon.<key>` labels are split on `/` and `,`, normalised once and
  sorted multi-word/longest first. An item name (then its category) is matched per
  language: multi-word labels as adjacent whole words, single-word labels exactly or
  as a ≤3-rune suffix inflection of a ≥4-rune label ("Kahvesi", "Biere"), plus an
  Arabic `ال` article strip. The English `keywordIcons` table is unchanged;
  `PlaceholderIcon`/`PlaceholderIconPath` are now the nil (English-only) matcher.
- **Normalisation** (`placeholder_norm.go`): language-neutral folding — case, Turkish
  İ/I/ı, ß, NFD mark stripping (accents, Arabic harakat), tatweel, Arabic/Persian
  letter variants (ي/ی/ى, ك/ک, ة/ه, hamza seats). ZWNJ and punctuation split words.
- **i18n:** `config.I18n.Lookup` / `httpx.TExact` — exact locale (or its base
  language) through every layer incl. shop overrides, no fallback, no key echo.
- **Shop languages:** `httpx.ShopLanguagesFor` = default language first, then the
  effective staff-language list; `common.ShopIconMatcher` reads `store.staff_locales`
  once (a read error logs and falls back to the default language).
- **Wiring:** import builds one matcher per request for all rows' placeholder
  thumbnails; the item editor's `icon-state` suggestion uses the same matcher.
- Help: `web/help/en/catalog.md` (item-image suggestion and import placeholder prose).
- No new `en.json` keys; no SQL; no migration.

## Findings

| # | Sev | Finding | Disposition |
|---|---|---|---|
| D1 | Low | `NewIconMatcher` dropped `en` from the language list and always ran the English table last, so an English-default shop with a Turkish staff language matched "Pasta" as cake rather than bowl, which contradicts "shop default language first" (BA §2). Reported by the Tester. | **Fixed:** the matcher now holds ordered passes, and an `en`/`en-*` entry is the English-table pass at its own position. An English-default shop checks English first. A tr-default shop with en staff checks tr, then English. A list without `en` still ends with English. New `placeholder_lang_order_test.go` (9 cases) fails on the pre-fix code (3 red: en+tr, en-GB+tr, en twice) and passes after. `placeholder_test.go` was not edited. Help prose now says "your default language first". |
| R1 | Info | Locale dedupe in `NewIconMatcher` was case-sensitive (`seen[loc]`). | Fixed in the same edit (lower-cased key). |
| R2 | Info | Import passes `d.Settings` (`*settings.Store`) into an interface, so a nil store would panic rather than reach the nil check. The same handler already calls `d.Settings.Get` without a guard at `import_page.go:603`, so this adds no new hazard. | Accepted. |
| R3 | Info | The item editor builds a matcher on each `icon-state` request: about 100 RLock'd lookups plus label normalisation. That is microseconds, on a manager-only screen. Import builds it once per request, and labels are normalised once per matcher, not per lookup. Normalising the input is one NFD/NFC pass per name. | Accepted. |
| R4 | Info | NFD strips every `Mn` mark, Indic vowel signs included. It does this to labels and input alike, so matching stays consistent, but in those scripts two different words could fold to the same form. No Indic locale ships today. | Accepted; revisit if an Indic pack lands. |
| R5 | Info | Core neutrality: the only language literal is `en`, which stands for core's own English table (ADR-0160). The fold table is generic Unicode/script folding and contains no word lists. `guard-core-neutral.sh` passes. | Accepted. |
| D2 | Known | ar "كعك"/"كيك" give generic. The built-in label is "كعكة", and the suffix-only rule cannot reach these forms. | Deferred → synonym-keys card (below). |

No concurrency concerns: there is no shared mutable cache, the matcher is immutable
after construction and is built per request, and `-race` on catimport is clean.
Empty, whitespace-only and punctuation-only names return no match and go to the
category, then generic. A language with no bundle has no label rows, so it is
skipped and English still runs. The two recurring bugs do not occur: no file is
written and no path is built.

## Verification

- Reviewer (in worktree, after the fix): `gofmt -l .` produced no output;
  `go build ./...` passed; `go vet` on the touched packages passed;
  `go test ./internal/catimport/... ./internal/httpx/... ./internal/config/...
  ./internal/pages/common/... ./internal/pages/catalog/...` passed;
  `go test ./internal/pages/ -run '2654|Placeholder|Import'` passed;
  `go test -race ./internal/catimport/` passed; `golangci-lint run ./...` reported 0 issues;
  the i18n, i18n-meta, core-neutral, data-access, help-topics and fixed-dates guards
  all exited 0.
- **TDD re-verified:** with the `import_page.go` wiring reverted to
  `tillimport.PlaceholderIconPath`, `TestImport_PlaceholderMatchesShopLanguage` fails
  ("Çay thumbnail = drink.svg, want tea.svg"). After restoring the wiring it passes.
  The D1 test fails on the pre-fix matcher and passes on the fix.
- Tester: 9 mutants (no shop-language step, ToLower-only normalisation, no article
  strip, exact-only matching, uncapped prefix, no longest-first sort, staff-only
  language list, import or icon-state reverted to English) were each killed by at
  least one test. **Driven run** used the built binary with a fresh data dir per
  scenario, `/api/import` commit plus `icon-state`:
  - tr: Çay→tea, Türk Kahvesi→coffee, Pasta→cake, Kaffee→generic (→drink through
    the "Drinks" category).
  - en: Pasta→bowl.
  - ar: القهوة, قهوة and قَهْوَة→coffee; ماء and الماء→water.
  - fa: چای/چاي→tea, کیک/كيك→cake.
  - de with no pack installed: everything generic or English, and no error.
  - Screenshots of the tr and ar (RTL) catalogs showed the tiles correct with no overlap.

  The Tester's D1 repro (en default + staff "en,tr", Pasta→cake) is the case fixed
  above.

Pre-flight: help topic `web/help/en/catalog.md` updated (docs-shots n/a: no
layout change, only which icon is suggested); `reference/` n/a (no reference page
covers placeholder matching); README n/a; en.json/en.meta n/a (no new keys); ADR
n/a (fits ADR-0160: words come from the i18n layers); plugin/manifest mirrors n/a;
follow-ups deferred → cards below (Scrum Master to file).

## Verdict

**Approve** with the D1 fix applied (uncommitted in the review worktree; squash into
the feature commit).

## Deferred

1. Icon synonym keys `catalog.icon_keywords.<key>` (comma-separated words; English
   mirrors `keywordIcons`, pinned by a test). This covers ar "كعك"/"كيك", de
   "Milchkaffee" and similar compounds.
2. Icon picker search box matching shop-language names (`data-keywords`), not only
   English.
3. German compound / head-noun matching (a suffix with a minimum stem length).
4. Possessive and other non-suffix inflections (ar "قهوتي") are not matched by
   design. Fold them into card 1 or 3 if shops ask for them.

## Post-review CI fix (2026-11-11, routine `:41`)

`ci / desktop-shell` ran `guard-deadcode-baseline.sh`, which flagged `catimport.PlaceholderIcon` and `PlaceholderIconPath` as unreachable: both production callers now use `IconMatcher`. The baseline may not grow in a feature PR (`guard-deadcode-baseline-growth.sh`), so the two wrappers moved to `internal/catimport/placeholder_english_test.go`. Their bodies are unchanged (`(*IconMatcher)(nil)`, the English-only path), so `placeholder_test.go` still pins the English table without edits. Three comments that named the old functions were updated, one of them in `internal/pages/catalog/handlers.go`. That edit is comment-only, so the docs-shots surface hash was refreshed with `update-docs-shots-surface-hash.sh` (trailer `Docs-Shots-Unchanged: true`). Re-ran: gofmt, `go build ./...`, `go test` on catimport and pages/catalog, golangci-lint (0 issues), guard-docs-shots (fresh). The deadcode guard itself needs Go 1.27 and could not run in this container, so CI checks it.
