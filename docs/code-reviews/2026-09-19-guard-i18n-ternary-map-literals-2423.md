# Review: guard-i18n.sh check 5 — ternary and .map() literal coverage (universaltill/ut-docs#2423)

**Card:** `guard-i18n.sh` check 5's `jsassign_re` only matched a hardcoded
prose literal appearing directly as the RHS of `.textContent =` /
`.innerHTML =`. Two real shapes were invisible to it:

1. A ternary whose branch is a raw literal (`.textContent = ok ? "Saved"
   : "Failed"` — the RHS right after `=` is `ok`, not a quote).
2. A literal returned from inside a `.map(...)` callback whose result
   feeds `.innerHTML` (`.innerHTML = list.map(function (c) { return
   "<div>No results</div>"; }).join("")` — the RHS right after `=` is
   `list`, and the literal is usually on a different line entirely,
   inside the callback body).

**Complexity:** medium. Dev at Sonnet (inline), review at Opus (fresh
subagent, isolated worktree).

## What shipped

- `scripts/ci/guard-i18n.sh`: `ternary_assign_re`/`ternary_literal_re`
  catch a literal in either ternary branch on the assignment's own line.
  `map_assign_re`/`map_arrow_literal_re`/`map_return_re`/`map_close_re`
  catch a single-line arrow `.map()` literal directly, and a multi-line
  `function (x) { ... return "..."; }` callback body via a bounded
  (`MAP_SCAN_LINES = 40`) forward scan that stops at the callback's own
  closing brace.
- A new `strip_markup` helper factors out the existing tag-stripping step
  (previously inlined as `tag_re.sub(' ', literal)` at every call site)
  and extends it to treat a **truncated** open tag — no closing `>` at
  all, the shape a `.map()` callback's first concatenated literal segment
  regularly is (`'<button type="button" data-x="' + esc(y) + '">'`) — the
  same way a complete tag already strips cleanly, so its bare attribute
  names don't misread as prose.
- `scripts/ci/guard-i18n_test.sh`: 10 new fixtures (ternary safe/unsafe/
  ignored, map safe/unsafe/ignored/arrow, plus the 3 below from review).

## Independent review (Opus, isolated worktree, fresh context)

**Verdict: PASS-WITH-NITS → both blocking findings fixed before merge.**

1. **N1 — fixed, real regression.** `strip_markup`'s original
   `literal.count('<') > literal.count('>')` heuristic didn't check
   whether the trailing unmatched `<` was actually tag-shaped. A genuine
   `<` in real prose (`"Discount < 5 percent is not allowed"`) got a
   synthetic `>` appended, and `tag_re` then ate everything from that `<`
   to the end of the literal — silently un-flagging a literal that had
   been correctly flagged since ut-docs#205, before this card touched
   anything. Fixed: only append the synthetic close when the trailing
   unmatched `<` is immediately followed by a letter/slash/bang
   (`re.search(r'<[A-Za-z/!][^>]*$', literal)`). Verified: the reviewer's
   exact repro re-flags; the real promotions.html:135 shape (a genuine
   truncated `<button ...>` tag) stays correctly suppressed.
2. **N2 — fixed, real false positive.** `map_close_re` required the
   callback's closing `}` and the `.map(`'s closing `)` on the *same*
   line (`^\s*\}\)`), so a real close style like `}, this).join(...)`,
   `}.bind(this))...`, or the `)` on its own following line never
   matched — the bounded lookahead then ran the full 40 lines past the
   callback and could flag an unrelated function's own `return "...";`
   with a confusing message pointing at code nowhere near
   `.innerHTML`/`.textContent`. Fixed: `map_close_re = re.compile(r'^\s*\}')`
   — a strict superset of the original, so it can only stop the scan
   *earlier*, never later; verified it can't introduce a new miss (every
   existing true-positive fixture still flags) while all three spill
   repros the reviewer found now correctly stop at the real close.
3. **N3 — fixed, coverage gap.** The original `InnerHTMLMarkupOnly`
   fixture only exercised `strip_markup` on a *complete* tag, so the
   partial-tag-concat branch — the actual reason the helper exists — was
   pinned by nothing except incidentally by the real tree containing
   promotions.html. Added `MapPartialTagConcat` (the real shape,
   verbatim-equivalent to promotions.html/settings.html/setup.html/
   tills.html's own pattern) and `LessThanProse` (N1's regression,
   `expect_fail`) as dedicated fixtures, plus `MapCloseThisArg` (N2's
   regression, `expect_pass`) — 3 new cases beyond the original 7.
4. **Confirmed NOT a problem, ternary false positives.** The reviewer
   specifically hunted this (ternary time-string concatenation,
   `toLocaleDateString` with an object-literal `:`, a URL query string,
   `?.` optional chaining, inline `style="color:red"`, `.split("?")`) and
   could not break `ternary_literal_re`/`ternary_assign_re` — the
   `[?:]` immediately-followed-by-a-quote requirement plus the
   single-statement `[^;\n]*` bound is tight enough. Across the real tree,
   9 ternary + 3 `.map()` statements match; zero false hits, all verified
   safe for real reasons (translation lookups, `T(...)` calls,
   escaped-data concatenation), not by coincidence.
5. **Deferred, recall gaps, not fixed:** a ternary split across multiple
   lines (prettier's own long-ternary formatting), a second ternary
   assignment later on the same line (`ternary_assign_re.search` is
   first-match, `jsassign_re` uses `finditer`), `.innerHTML =` and
   `.map(` on different lines, an arrow returning a concatenation rather
   than a bare literal, a `(c = {}) =>` default-param arrow, template
   literals (backticks — pre-existing across the whole guard, not new).
   Consistent with this file's own stated convention (near-zero false
   positives over exhaustive recall); not worth the complexity for a
   line-based heuristic, same tradeoff every check in this file already
   makes.

## Verified beyond automated tests

- Independent TDD re-verification (by the reviewer, not taken on trust):
  reverted only `guard-i18n.sh` to its pre-fix state, kept the new tests,
  confirmed exactly 3 failures (`TernaryProse`, `MapProse`,
  `MapArrowProse`) — not a tautology — then restored and confirmed green.
- Full gate: `gofmt -l .` (clean), `shellcheck scripts/ci/guard-i18n.sh
  scripts/ci/guard-i18n_test.sh` (0 issues, shellcheck 0.9.0 — matches
  `guard-shellcheck-version.sh`'s CI pin), `go build ./...` (n/a — no Go
  files touched, confirmed via `git diff --stat`).
- All 6 `guard-i18n*` test suites green (no regression on the 5 sibling
  checks sharing this file): `guard-i18n_test.sh` (19/19),
  `guard-i18n_verbcheck_test.sh`, `guard-i18n_keycall_test.sh`,
  `guard-i18n_title_test.sh`, `guard-i18n_dupkey_test.sh`,
  `guard-i18n_toast_test.sh`.
- `guard-i18n.sh` on the real, unmodified tree: clean (0 issues) both
  before and after the N1/N2 fixes.
- Scope check: `git diff --stat` shows only `scripts/ci/guard-i18n.sh`
  and `scripts/ci/guard-i18n_test.sh` touched — no `internal/`, `web/ui/`,
  or `web/locales/` changes, so none of the product-code CLAUDE.md rules
  (repository pattern, money type, kiosk isolation) apply to this diff.

**Safe to merge.**

## Deferred / follow-up

- The five recall gaps listed under "Confirmed NOT a problem" item 5
  above are documented, not filed as separate cards — they're the same
  class of accepted heuristic gap every check in this file already
  carries (e.g. check 3's single-word-status gap), not new debt this
  card introduced.
