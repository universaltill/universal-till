# Review: first ICU plural keys + ICU-aware guard-i18n check 8 (ut-docs#4480)

**Date:** 2026-10-29 · **Branch:** `feat/4480-icu-plural-counts` · **Lane:** `lane:cloud-41`
**Author:** Sonnet (Dev subagent) · **Reviewer:** Opus 5.5 (independent subagent, fresh worktree)

## What shipped

- Two count strings move to new MessageFormat keys (ADR-0160 §8: new key,
  old `%d` key kept for the language packs):
  `categories.item_count_plural`, `products.category_button_count_plural`,
  in en/ar/fa/tr with metadata (`plural: count`). Arabic gets all six CLDR
  forms (لا أصناف / صنف واحد / صنفان / ٥ أصناف / ١١ صنفًا / ١٠٠ صنف); fa and tr
  keep the singular noun after a numeral.
- Call sites switched to `{{ TM "…_plural" "count" N }}`: the category tile,
  the overflow sheet and the designer row (`web/ui/partials/buttons.html`)
  and the categories table (`web/ui/pages/categories.html`).
- `guard-i18n.sh` check 8: a key whose English value is ICU is compared by
  argument name + type, not by the verb regex (branch sets differ per
  language; `two {2}` / `one {{count}}` read as template tokens). The locale
  value must parse: balanced braces, apostrophe quoting, CLDR categories or
  `=N`, no duplicate branch, `other` required.
- `internal/msgfmt/locale_icu_test.go`: every shipped translation of an ICU
  key must parse with the till's own parser (TM silently shows English when
  it does not).

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | Guard accepted ICU values `msgfmt` refuses (no `other`, misspelt category, duplicate branch, unsupported types, `'|` quoting), which then render English | **Fixed**: guard mirrors category/duplicate/`other` rules (3 new self-test cases, red first); the authoritative check is the new Go test running `msgfmt.Parse` over every locale (re-verified by planting an `ar` value with no `other`: test failed naming the key, restored). Residual: the guard's quoting/type edge cases are looser than `msgfmt`; the Go test covers them. |
| 2 | Low | de/es/pt packs lack the new keys → English plural until they land | **Fixed in-cycle**: pack PRs in ut-plugin-language-{de,es,pt} after the core merge (reviewer rule 4). |
| 3 | Low | No review record | This file. |
| 4 | Nit | ar `zero` "٠ أصناف" unidiomatic; dual "زرّان" spelled with a shadda the singular lacks | **Fixed**: "لا أصناف" / "لا أزرار", "زران". |
| — | — | e2e specs still asserted `3 item(s)`, `1 button(s)`, `0 button` (found by the orchestrator) | **Fixed**; the three specs run green (11 tests). |

## TDD re-verified (reviewer, own worktree)

- Locales reverted → `TestShippedCountPluralKeys_RenderEveryCategory` fails
  (raw key echoed); buttons.html reverted → `TestButtonsHTTPList_CategoryTabsModeRendersTiles`
  fails ("2 items" missing); guard reverted → 6 verbcheck cases fail. All
  pass restored.

## Verified beyond automated tests

Driven run (`e2e/run-till.sh`, demo catalogue): `/categories` in ar
(1024×600), fa (360px card layout), en; sale screen category tiles in ar and
en. Looked at the screenshots: Arabic shows ٢٧ صنفًا / ١٠ أصناف / صنفان,
locale digits, no clipping or overlap; Persian card rows unaffected. Not
looked at: dark theme, the designer row and overflow sheet visually (covered
by their e2e specs only).

## Gates

`gofmt`, `go build ./...`, `go test ./...`, `guard-i18n.sh`,
`guard-i18n-meta.sh`, `guard-i18n_test.sh`, `guard-i18n_verbcheck_test.sh`,
shellcheck on touched scripts (only the pre-existing info-level SC1091 on
the unchanged `source` line), help/RTL/compliance/naming guards.

## Deferred

- The packs' own `scripts/check-key-drift.sh` still uses the verb regex for
  ICU values (same false-fail for a pack whose branch set has a digit-only
  or argument-only body) — Backlog card ut-docs#5160.

**Verdict:** safe to merge.
