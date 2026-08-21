# Code review: guard-i18n.sh doesn't catch hardcoded ToastMessage literals (ut-docs#237)

**Date:** 2026-08-21
**Card:** universaltill/ut-docs#237
**Author (Dev):** Sonnet, inline (this cycle's Scrum Master)
**Reviewer:** Opus, independent fresh-context subagent, worktree-isolated

## What shipped

- New check 6 in `scripts/ci/guard-i18n.sh`: flags a hardcoded prose
  string literal assigned straight to a field named `ToastMessage` —
  `pos.Basket.ToastMessage`, the sale screen's single notification field
  (ut-docs#213) — in either of Go's two idiomatic assignment syntaxes:
  the dotted-field form (`b.ToastMessage = "..."`) and a composite
  literal's key form (`ToastMessage: "..."`). Also catches a literal
  `fmt.Sprintf` format string assigned the same way. Reuses the file's
  existing `prose_re` two-adjacent-word heuristic and the established
  `i18n:ignore` same-line escape hatch. This is invisible to check 3
  (which only scans literals passed directly to `w.Write`/`fmt.Fprint*`
  as an HTTP response body) — a struct-field assignment is a different
  shape entirely, and it's exactly the class of bug ut-docs#213 fixed
  five real cases of, with nothing stopping a sixth.
- New `scripts/ci/guard-i18n_toast_test.sh` — 7 regression cases (dotted
  literal reject, `i18n:ignore` exempt, literal-Sprintf reject, httpx.T
  call accepted, composite-literal-key reject, plain-identifier accepted,
  real-tree sanity pass), mirroring `guard-i18n_test.sh`'s plant/
  expect_fail/expect_pass pattern — but for Go fixtures under
  `internal/pages/` instead of HTML fixtures under `web/ui/`. Every
  fixture is written to compile standalone (no external imports;
  `httpx.T` is stubbed locally) so `go build`/`go test`/gopls don't see
  spurious breakage while a fixture is transiently planted.
- Wired into `.github/workflows/ci.yml` immediately after the existing
  `guard-i18n_test.sh` step.

## What the independent review found

Spawned as an Opus subagent in an isolated git worktree, branched from a
`WIP: pre-review snapshot` commit, briefed with the full diff scope and
told to actually run things, not just read.

**Verdict on the first pass: safe to merge, with one medium finding
worth fixing before shipping.** All fixed this cycle.

**1 medium issue, fixed:**
- The regex only matched the dotted-assignment form (`\.ToastMessage\s*=`),
  so Go's other idiomatic way to set the same field — a composite literal
  key (`Basket{ToastMessage: "..."}`) — evaded it entirely. Reviewer
  planted a probe fixture using that exact syntax and confirmed the
  pre-fix guard exited 0 (missed it). Not the same class as check 3's
  documented recall-for-precision gaps (backtick strings, indirection
  through a variable) — this is the identical direct-literal bug in
  different syntax, and the codebase already builds `Basket` via
  composite literal (`internal/pos/service.go`). Fixed: widened the regex
  from `\.ToastMessage\s*=` to `\bToastMessage\s*[:=]\s*`, re-verified
  zero new false positives across the real tree and against every
  existing safe pattern (httpx.T calls, Sprintf-wrapping-httpx.T, plain
  identifiers, the struct's own field declaration). Added a matching
  `expect_fail` fixture (`ToastCompositeLiteral`) to the regression test.

**3 low-nit issues, all fixed:**
- The check-6 header comment cited `pos_api.go:846` as an example of the
  "plain identifier" pass-through shape; it's actually an `httpx.T(...)`
  call (`b.ToastMessage = httpx.T(locale, msgKey)`) whose argument happens
  to be an identifier — a different example than intended. Moved the
  citation to the httpx.T example and left only the genuinely-identifier
  `pos_api.go:657` on the plain-identifier list.
- The test file's `ToastHttpxT` fixture comment claimed "this is what
  every current ToastMessage assignment in the codebase already does" —
  actually 4 of 11 live assignments are plain identifiers threaded from a
  caller, a different fixture case entirely. Reworded to "what the
  httpx.T-based assignments do."
- The `ToastHttpxT` fixture called the real `httpx.T` with no import,
  so `go build ./internal/pages/...` failed with `undefined: httpx`
  while that one fixture was transiently planted (harmless in CI, since
  guard steps run sequentially in one checkout, but a real local-dev
  papercut for anyone running `go build`/gopls alongside the test).
  Fixed: replaced with a package-local stub function
  (`zzGuardTestHttpxTStub`) so every fixture — verified individually,
  one at a time — now compiles standalone.

**Explicitly accepted gaps (verified to already match check 3's own
documented recall/precision tradeoff, not new debt):**
- Backtick raw strings assigned directly, an intermediate `msg := "..."`
  variable later assigned to `ToastMessage`, a multi-line `fmt.Sprintf(`
  call, and `"..." + variable` string concatenation all evade the
  heuristic — same shape of gap the file's header comment already
  documents for check 3, and the header text for check 6 accurately says
  "only... on the same line" / "only a literal double-quoted string."
- A comment or a raw-string literal containing ToastMessage-shaped prose
  can still trip the guard (no comment-stripping) — verified check 3
  behaves identically on the same shape of probe, so this is
  precedent-consistent, not new inconsistency, and the same-line
  `i18n:ignore` hatch covers a real false positive if one ever occurs.
- `corepos/basket.go` declares a second, unrelated `Basket.ToastMessage`
  outside the `internal/**` glob both check 3 and check 6 scan. It has no
  importers today (dead code) so nothing currently escapes, but if that
  package is ever wired up, neither check sees it — same glob scope as
  check 3 already has, not a regression introduced here. Left as a known
  gap, not actioned — no Backlog card filed since the field is provably
  unreachable code today and re-checking on every future grooming pass
  of an already-known, already-documented gap isn't worth a standing
  card; whoever wires up `corepos` will need to touch the guard's glob
  scope anyway as part of that (much larger) change.

## Independently re-verified (by the reviewer, then again by Dev after
the fixes — both revert-then-restore rounds shown for the record)

- **Reviewer's round** (pre-fix code): reverted check 6 entirely, re-ran
  `guard-i18n_toast_test.sh` — both `expect_fail` cases (dotted literal,
  Sprintf literal) genuinely failed with `expected guard to reject ...
  but it passed`; all `expect_pass` cases still passed, proving the test
  discriminates rather than failing wholesale. Restored, all cases green
  again.
- **Post-fix round** (after the composite-literal widening + comment/
  fixture fixes above): reverted `scripts/ci/guard-i18n.sh` back to its
  pre-diff (`36a52a1`) content, re-ran the (now 7-case) test — all three
  `expect_fail` cases (dotted literal, Sprintf literal, and the newly
  added composite-literal key) genuinely failed the same way; the four
  `expect_pass` cases were unaffected. Restored the fix; all 7 cases
  passed again, working tree clean.
- Confirmed via `git diff --name-only` that the diff touches exactly
  three files (`scripts/ci/guard-i18n.sh`, `scripts/ci/
  guard-i18n_toast_test.sh`, `.github/workflows/ci.yml`) — no product
  code (`internal/`, `web/`, `cmd/`) changed, so the UX-guidelines
  checklist and user-manual-topic requirement don't apply to this diff.
- Full gate re-run clean after the fixes, not just once: `go build
  ./...`, `go vet ./...`, `bash scripts/ci/guard-data-access.sh`,
  `bash scripts/ci/guard-kiosk-engine.sh`,
  `bash scripts/ci/guard-plugin-menu-read.sh`,
  `bash scripts/ci/guard-i18n.sh`, `bash scripts/ci/guard-i18n_test.sh`
  (unrelated check-5 suite, still green — no regression),
  `bash scripts/ci/guard-i18n_toast_test.sh`,
  `bash scripts/ci/guard-compliance-claims.sh`,
  `bash scripts/ci/guard-help-topics.sh` — all pass.
- `go test ./...` (full suite, all packages) run clean before the
  review fixes; the fixes touched only the guard script and its test
  harness (shell/regex-scanning, no Go product code), so the full suite
  was not re-run after — the narrower `go build ./internal/pages/...`
  compile check for each individual fixture, plus the full guard-gate
  re-run above, is the correct-scoped verification for a shell-only diff.

## No help-manual prose update needed

No product-facing route, page, or behavior changed — this is a CI-guard
and its own regression test only. `guard-help-topics.sh` passes clean.

## No client/shop-name or secret-shaped literal

Checked explicitly — none in this diff. Fixture strings are generic
("Applied your discount now", "Item %s not found").

## Safe-to-merge verdict

**Yes**, after the medium fix (composite-literal coverage) and the three
low-nit fixes (comment accuracy ×2, fixture compile-ability) were
applied and the full gate re-verified green.
