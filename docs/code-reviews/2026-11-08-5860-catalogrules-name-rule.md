# Review — catalogrules.CheckSingleLineName: the shared single-line name rule (ut-docs#5860, part 1)

**Date:** 2026-11-08 · **Branch:** `feat/5860-catalogrules-name-rule`
**Author model:** Sonnet (dev subagent) · **Tester:** Tester A (mutation run, see ut-docs scratch `tester-a.md`) · **Reviewer:** Fable (independent, author Sonnet)

Pre-flight: web/help n/a (no till UI change; the only behaviour change is admin screens and catsync now also refusing U+E0100–E01EF, same `common.error.name_invalid_characters` message) · reference/ updated (ut-docs `reference/coding-standards.md` §4, branch `docs/5860-name-rule`) · README n/a (`catimport/README.md` updated instead) · en.json/i18n-meta n/a (no keys) · mirrors: ut-cloud `TestNameRuleMirrorsPOS` + `check-catimport-pin.sh` (tag `catimport/v0.5.0` after merge, on the merge commit) · ADR n/a (follows ADR-0164 §1 / ADR-0162 §3) · review record: this file · follow-ups: ut-docs#5871 (till directive/pull paths, part 3).

## Scope
- `catimport/catalogrules/name.go` (new): `CheckSingleLineName` and `ErrNameInvalidCharacters` moved verbatim from `internal/data/singleline_name.go`, plus one new refusal: the supplementary variation selectors U+E0100–E01EF (Mn, invisible, no IVS rendering on receipts or the till's fonts). U+FE00–FE0F, Mongolian FVS and the CGJ stay allowed.
- `catimport/catalogrules/namecases/namecases.go` (new, importless): the shared case table (`Cases()` returns a fresh slice) both repos' tests range over — 10 accepted and 23 refused inputs including every bidi embedding/override, ZWSP, BOM, soft hyphen, word joiner, unbalanced/unclosed isolates, PDI-before-LRI, tag characters and invalid UTF-8.
- `internal/data/singleline_name.go`: a one-statement delegate and an alias of the sentinel, so `errors.Is(err, data.ErrNameInvalidCharacters)` and every #5858 caller (and in-flight #5861/#5862) keep working.
- Tests: `catalogrules/name_test.go` (table, blank, fresh-slice), `internal/data` `TestCheckSingleLineName_DelegatesToCatalogrules`.

## Findings
1. **Predicate semantics (checked, no finding).** `unicode.IsControl` covers Cc; U+2028/2029 (Zl/Zp) explicit; every Cf refused except ZWNJ/ZWJ/LRM/RLM/ALM and balanced U+2066–2069 (depth counter with underflow check); E0100–E01EF range inclusive at both ends; `utf8.ValidString` after `TrimSpace`. `TrimSpace` strips `unicode.IsSpace` (incl. NEL, NBSP, U+2028/2029 at the edges) *before* the check, so a trailing line separator is trimmed, not refused — the #5858 semantics, unchanged, and the returned value is clean either way. A leading BOM/ZWSP is not whitespace and is refused, as designed.
2. **Raw variation selector in `namecases.go` — low, fixed.** Tester A had every bidi/invisible literal escaped, but the `fe0f emoji` case still carried a raw U+FE0F inside `"Heart ❤️"`. Escaped as `❤️` so the file holds no invisible code points at all (Trojan-Source hygiene, consistent with the rest of the table). Re-scanned every changed Go file in both repos for U+200B–200F, U+2028–202E, U+2060–2069, U+FEFF, U+00AD, U+061C and U+E0000–E01EF: none.
3. **`catalogrules.go` package doc — low, fixed.** The amended sentence broke mid-line ("It is\n// stdlib-only") and still claimed `internal/catalogtypes` re-exports "these", which is not true of the name rule (callers use `data.`). Rewrapped and made precise.
4. **Non-test `namecases` package in a shipped module — accepted.** Go test packages cannot be imported across modules; the package is importless (`go list -deps` std-only), so it adds nothing to ut-cloud's dependency surface.
5. **Part 3 absence — accepted by design.** Till directive/pull paths (T1–T11, R8 legacy tolerance, flipping the three `...Unchecked` tests) are ut-docs#5871, blocked on ut-cloud being deployed, so the invariant "no pulled row refused that my. accepted" never inverts.

## Verification
- Reviewer re-verified the TDD claims on the cloud side (ut-cloud record); on this side the catimport tests fail to build without `name.go` (undefined `CheckSingleLineName`), and Tester A's 19-mutant run showed each clause killed by the table once LRM/ALM/balanced-LRI/PDI-before-LRI were moved into `namecases`.
- Gates: `gofmt -l .` clean; `go build ./...`; `go test` internal/data (43s), catsync, pos, pages/catalog; `cd catimport && go vet ./... && go test ./...`; `golangci-lint run` 0 issues (catimport and internal/data); `go list -deps ./catalogrules/...` std-only; guard-catimport-module, guard-data-access, guard-fixed-dates, guard-core-neutral, guard-i18n, guard-mail-addresses pass. `check-catimport-pin.sh v0.4.0` passes only because the tree is uncommitted; it fails once committed, which is the expected signal to tag `catimport/v0.5.0` on the merge commit.

## Verdict
Approve. Two low findings fixed in review; no behaviour change beyond the designed E0100–E01EF tightening.
