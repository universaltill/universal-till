# Review: single-line names typed on the till refuse control, bidi and invisible characters (ut-docs#5858)

Date: 2026-11-08. Lane: `lane:cloud-54`. Complexity: medium.
Built by Sonnet (Dev subagent); reviewed by an independent Opus 5.5 subagent; tested by a separate Tester subagent.

## What shipped
- `internal/data/singleline_name.go`: `CheckSingleLineName` + `ErrNameInvalidCharacters`. Trims; refuses
  `unicode.IsControl`, U+2028/2029, every Cf except ZWNJ/ZWJ, LRM/RLM, ALM and the isolates U+2066–2069;
  isolates must balance; emoji tag characters refused. Blank returns ("", nil) so callers keep their own blank rule.
- Applied on the admin-only entry points: items/variants (`internal/pos/catalog_ops.go`), categories (page +
  menu designer, via checked admin variants), modifier groups/options, option sets (rename, value replace —
  only new or changed values are checked), tax codes, registers.
- Paths shared with my. directives, cloud pulls, file import and enrolment stay unchecked on purpose (so the
  till never refuses what my. accepted), pinned by regression tests; follow-ups #5860 (directive/pull + my.
  parity, land together), #5861 (customer import), #5862 (till/device names).
- catsync item and category names use the same predicate (newly accept LRM/RLM/ALM/balanced isolates; refuse
  U+2028/2029); external ids/SKUs keep `cleanText`.
- One new key `common.error.name_invalid_characters` (en.json + en.meta.json); every handler answers a
  translated 400 through the screen's own render path.
- ut-docs `reference/coding-standards.md` §4 bullet (branch `docs/5858-name-control-chars`).

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| F1 | medium | `ReplaceOptionSetValues` checked every value, so one legacy/my.-stored bad value blocked edits to the rest of the set | fixed: only new/changed values checked; `TestSingleLineName_ReplaceValuesIgnoresUnchangedBadValue` (re-verified by orchestrator: red without fix, green with) |
| F2 | medium | modifier and option-set errors answered text/plain, which the page's HTMX swap discards — the operator saw only the generic banner; tests passed for the wrong reason | fixed: rendered through `renderModifierMutationResult` / `renderOptionSetsListStatus`; tests send HX headers and assert the HTML fragment |
| F3 | low | raw invisible/bidi characters in source and tests | fixed: `\uXXXX` escapes |
| F4 | low | a row whose stored name already has a bad character can't be saved until the name is retyped | accepted: the name is in the field being edited |
| F5 | low | supplementary variation selectors U+E0100–E01EF pass | deferred → #5860 (comment) |
| F6 | low | catsync feeds may bring visually identical category names differing by an RTL mark | accepted |
| F7 | nit | docs bullet should cite #5860 | fixed |
| F8 | nit | variant update with U+202E / RLM round-trip; more F2 handler coverage | fixed |

Tester (D1 = F1, D2 = F3): tests shown non-false-pass by reverting three paths and weakening the predicate
five ways. Driven run of the real till (demo seed, Chromium): category with U+202E → 400 and the translated
message in the dialog; tax code form shows the translated message; names with U+200F stored byte-for-byte;
item and register via curl → translated 400. Status, lock and exit stayed reachable. English, light theme,
1280x800 only. No sale/basket/payment file touched (offline checkout unaffected).

## Gate
gofmt clean; `go build ./...`; `go vet ./internal/...`; `go test -p 1` on data, pos, catsync, pages/...;
golangci-lint 0 issues; guard-i18n, guard-i18n-meta, guard-data-access, guard-core-neutral, guard-fixed-dates
green. A `-race` run on this 4-core box timed out in unrelated packages under load; CI runs the race suite.

Pre-flight: till user-visible behaviour → `web/help/` n/a (no help topic lists naming rules; checked catalog,
categories, option-sets, tax-codes, registers topics), `reference/` updated (coding-standards §4),
README n/a; new `en.json` key → `en.meta.json` updated (English only, ADR-0160); my./admin UI n/a (#5860);
follow-ups filed #5860–#5862.

Verdict: approve after fixes — fixes applied and verified.
