# Code review — icon search/auto-suggest ignore the other invisible characters (ut-docs#6057)

Date: 2026-11-12 · Lane: `lane:cloud-41` · Branch: `fix/6057-fold-default-ignorables`
Author: Sonnet (Dev) · Reviewer: Opus 5.5, fresh context, scratch copy ·
Tester: independent agent.

## What shipped

- `internal/catimport/placeholder_norm.go`: `normalizeWord` (and with it
  `FoldForSearch`) drops an explicit `ignorables` list: U+00AD soft hyphen,
  U+200B ZWSP, U+200C ZWNJ, U+200D ZWJ, U+200E LRM, U+200F RLM, U+061C ALM,
  U+2060 word joiner, U+2066–2069 bidi isolates and U+FEFF. All of them except
  ZWNJ/ZWJ are Cf, which the old Mn drop missed. The list is explicit, not
  `\p{Cf}`, because Go's and Node's Unicode tables can differ and break parity.
- **Split form.** `invisibleSeparators` (ZWSP, ZWNJ, ZWJ, LRM, RLM, ALM, the
  isolates) become spaces before `normalizedWords` folds, so they split words
  as they did before this change. Soft hyphen, word joiner and FEFF only join.
  `hasZWNJ` is renamed `hasWordJoiner` and checks the separator set, so the
  matcher also tries the joined view whenever the two forms can differ.
- `web/public/icon-picker-filter.js` `fold`: the same 13 code points, with a
  keep-in-sync comment. JS has no split form; its query split stays `\s+`.
- Tests:
  - Ten parity rows appended to `testdata/search_fold_parity.json`, with
    existing rows unchanged.
  - Go cases: `TestNormalizeWord` (one per character), ZWSP splits, bidi marks
    split but join in the joined view, joiners never split.
  - `TestIconMatcher_Ignorables`: Kaf­fee, Milch‎Kaffee, isolate-wrapped words,
    mid-word fa قه‎وه.
  - The picker keywords, plus node `wordMatches` cases.

Does this occur in real data? No shipped icon label contains any of these.
LRM/RLM and the isolates occur in fa/ar UI strings. `CheckSingleLineName`
allows the bidi marks in saved item names. Import thumbnails and the picker
query take pasted text unchecked. So yes, they can reach both folds.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| M1 | Medium | The first version made every new character a joiner in the split form. The bidi marks, soft hyphen, WJ and FEFF used to split words (leftover Cf is not a letter), so "Milch‎Kaffee" and "⁨Milch⁩⁨Kaffee⁩" dropped from coffee to generic. A regression for bidi marks, which sit where RTL and LTR runs meet. | **Fixed.** The bidi marks and isolates are separators in the split form; the matcher tries split and joined views, so "Kaf‎fee" and "Milch‎Kaffee" both match. Soft hyphen/WJ/FEFF stay joiners: a hyphenation point, a "no break" mark and a stray BOM never separate words. This changes the BA's AC 2 ("only ZWSP splits") on purpose. New `TestNormalizedWords_BidiMarksSplit`, seen red with the old split set. |
| L1 | Low | Comments contradicted each other (ZWSP called a "joiner"; `match()` still said only ZWNJ). | **Fixed.** |
| L2 | Low | Some matcher cases already passed on `main`. | **Fixed.** A mid-word bidi case was added; it fails without the fold change. |
| I1 | Info | A test comment named only the soft hyphen. | **Fixed.** |

## Verification

- **TDD:** with the fix reverted, 6 Go tests and 11 node tests fail (Dev,
  Reviewer and Tester each saw this). Partial mutations were each caught:
  dropping the isolates from the JS class (2 node tests), and dropping ZWSP from
  the spacer (2 Go tests). For M1 I re-checked myself: with the separator set
  shrunk back to ZWSP/ZWNJ/ZWJ, `TestIconMatcher_Ignorables` and
  `TestNormalizedWords_BidiMarksSplit` fail; restored, they pass.
- **Parity:** the Tester folded every BMP character as "a<c>b" through both Go
  and JS (63,488): 0 differences, old or new. Old vs new Go differ on exactly
  the 11 newly dropped characters. The Reviewer's 50,000-string differential
  fuzz also found 0 mismatches.
- **Gate:** gofmt, go build, go vet, golangci-lint 0 issues,
  `go test ./internal/catimport/... -race`, and node fold test 58/58. Guards
  passed: i18n, core-neutral, fixed-dates, data-access, rtl-logical-css. In full
  `go test ./...` only `internal/plugins` hits the default 10-minute timeout on
  this loaded box (as in #6052); it passes alone and doesn't import catimport.
- **Driven run (Tester):** a real till (seed_demo, en+fa shop) in Chrome. In
  the item editor's icon search, Coffee stays visible and Beer hidden for
  "cof<SHY>fee", "<LRM>coffee", "cof<ZWSP>fee", "<BOM>coffee", قهوه, "<LRM>قهوه"
  and "<RLI>قهوه<PDI><RLM>"; "coffeex" hides it. No page errors. Screenshots
  were checked.
- Tree incident: during review a scratch-copy command briefly reset
  `icon-picker-filter.js` in the real checkout. The Tester restored it from
  backup, and the final diff was re-checked to hold the 6057 class.

Pre-flight: web/help topic — n/a (search matches more spellings; no steps,
wording or screen changed); `reference/` — n/a (no documented behaviour lists
the fold's characters); README — n/a; en.json/en.meta — n/a (no keys); ADR —
n/a (extends the #6052 pattern); plan/price, platform claim, plugin
taxonomy/host function, my. UI — n/a; `catimport/VERSION` — n/a
(`internal/catimport` is not the nested module); ut-plugin-language-* packs not
scanned (not in this session; the fold handles them either way); the English
keyword path (`matchKeyword`/`tokenize`) doesn't use this fold, so "Cof­fee"
still misses the English coffee keyword — follow-up → ut-docs#6073; review record
— this file.

## Verdict

Approve. All findings are fixed.
