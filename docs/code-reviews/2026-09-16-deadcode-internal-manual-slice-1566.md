# Code review: `internal/manual` deadcode-baseline slice (ut-docs#1566)

**Date:** 2026-09-16
**Branch:** `fix/1566-deadcode-internal-manual-slice`
**Card:** universaltill/ut-docs#1566 ("Burn down 97 unreachable functions in
universal-till"), this cycle's per-package slice: `internal/manual` (4 of the
70 remaining `scripts/ci/deadcode-baseline.txt` entries — `Library.IDs`,
`Library.Locales`, `Library.MissingTranslations`, `Library.RouteCovered`).
Fifth slice after `internal/httpx` (universal-till#1148), `internal/data`
(universal-till#1149), `internal/plugins` (universal-till#1153) and
`internal/pages` (universal-till#1184).
**Complexity:** hard (card-level).
**Review model:** Opus (independent subagent, fresh context), per
`MODEL-ROUTING.md`'s hard-tier routing. Dev ran on Fable (isolated worktree)
per the same routing.

## What this slice does

All 4 baseline entries were individually investigated (repo-wide caller
search across production code, `scripts/ci/*` tooling, and tests) before
deciding disposition. **None were deleted — all 4 have a real, legitimate
caller the whole-program `deadcode` analysis cannot see.**

`deadcode` only scans the two shipped product binaries under `-tags=desktop`
(actually three roots — see "Found during review" below). It cannot see a
caller in `scripts/ci/*`, which this card's own acceptance criteria treat as
a legitimate root, just not a product binary. Both `internal/manual`
consumers are `go run` mains under `scripts/ci/`:

- **`Library.IDs`** — sole non-test caller: `scripts/ci/checkhelpdrift/main.go:174`,
  inside `checkAll(lib *manual.Library, …)`. Walks every topic id to compare
  each locale's translation against the English original.
- **`Library.Locales`** — sole non-test caller: `scripts/ci/checkhelpdrift/main.go:169`,
  same function. The sibling guard `scripts/ci/checkhelptopics` deliberately
  avoids this method (its own source comment explains why: "at least one
  topic file" is blind to a locale whose whole `web/help/<locale>/` tree is
  missing) — now cross-referenced in the doc comment.
- **`Library.MissingTranslations`** — sole non-test caller:
  `scripts/ci/checkhelptopics/main.go:60`. Fails the build when a shipped
  locale is missing a topic English has.
- **`Library.RouteCovered`** — sole non-test caller:
  `scripts/ci/checkhelptopics/main.go:74`, passed as a **method value**
  (`uncoveredRoutes(routes, lib.RouteCovered)`) rather than called with
  parens — a plain `grep -n "\.RouteCovered("` misses this call site
  entirely (the review's first pass hit exactly this and briefly concluded
  the function was genuinely dead before finding the method-value call).
  Its pre-existing doc comment already named the right caller; it lacked
  the `ut-docs#1566`/baseline cross-reference the other three now carry, so
  that was added for consistency.

Each `manual.Library`-typed call site was confirmed by reading `manual.Load(...)`
at its actual call site (`checkhelpdrift/main.go:334`, `checkhelptopics/main.go:33`)
— not assumed from the method name. A decoy exists in this codebase,
`(*barcode.Registry).IDs` (`internal/barcode/barcode.go:92`, called from
`internal/data/barcode_settings.go:118` and `internal/pages/settings_page.go:618-619`)
— confirmed as a different type, not conflated anywhere in the diff.

Diff: `internal/manual/manual.go` doc comments only, +37/−6 across two
commit passes (initial + review fixes) — no executable line touched, no
tests added or changed, no behaviour change. Baseline file
(`scripts/ci/deadcode-baseline.txt`) is unchanged (70 lines, same as
before this slice) — correct outcome, since nothing was deleted; matches
the `internal/data` slice's own precedent (also zero deletions, baseline
unchanged that cycle). The card's own acceptance criterion "each PR shrinks
the baseline" doesn't apply to a documentation-only resolution — the file
tracks *currently unreachable*, not *unresolved*, same note as the
`internal/pages` slice's review record.

## Found during review (fixed before merge)

The Dev pass's first draft, in all three of `IDs`/`Locales`/`MissingTranslations`'s
new comments, said the deadcode analysis "roots at **two** product
binaries." Verified against the actual guard invocation
(`scripts/ci/guard-deadcode-baseline.sh:72`):

```
go run "${DEADCODE_PKG}" -tags=desktop -test=false . ./cmd/unitill-desktop ./cmd/unitill-uninstall
```

Three roots (`.`, `cmd/unitill-desktop`, `cmd/unitill-uninstall`), not two.
The error was inherited from the guard script's own header comment, which
has said "two roots" since it was first written (commit `736cdf3`,
ut-docs#1581) — predating `cmd/unitill-uninstall` being added as a third
root. Fixed in both places:

- `internal/manual/manual.go` — all three comments reworded to "not one of
  the shipped-binary roots the whole-program deadcode analysis starts
  from" (drops the wrong count rather than repeating it with a corrected
  number, so a future root-count change doesn't need a matching edit here).
- `scripts/ci/guard-deadcode-baseline.sh`'s own header — "unreachable from
  either of this script's two roots" → "unreachable from any of this
  script's three roots (`.`, `./cmd/unitill-desktop`,
  `./cmd/unitill-uninstall` — see the `go run` invocation below)". In
  scope for this slice since it's the root cause of the error this slice's
  own diff was about to repeat, and it's the same one-line class of fix as
  everything else in this PR.

Also tightened while fixing the above (non-blocking nits, applied anyway
since they were adjacent one-sentence edits): each comment now says "its
only non-test caller" rather than "its caller" (test-only callers exist for
all four functions and aren't load-bearing per this card's own rules, but
"caller" read as exclusive); `Locales`'s comment now documents its
sorted-order return (its one real caller redundantly re-sorts — a genuine
follow-up cleanup, but changing that call site is a behaviour-adjacent code
change out of scope for a comment-only slice, so left as a documented fact,
not fixed); `checkhelptopics` is now named identically ("CI
page-route/manual-completeness guard") everywhere it's cited in this file,
where before `RouteCovered` and `MissingTranslations` used two different
descriptions of the same script.

No blocking findings. No deletion candidates found — all 4 entries have a
verified real caller.

## Verified beyond automated tests

- Independently re-derived every caller claim from source (not trusted from
  the Dev pass's report): confirmed each `lib` receiver is genuinely
  `*manual.Library` by reading the `manual.Load(...)` call site upstream of
  it, and confirmed the `barcode.Registry.IDs()` decoy is not conflated
  anywhere.
- Ran `scripts/ci/guard-help-topics.sh` and `scripts/ci/guard-help-drift.sh`
  directly (not just `go test`) — these are the exact two call paths the
  new comments describe; both passed (`guard-help-drift.sh`'s two "known
  drift" lines are pre-existing baselined entries tracked on ut-docs#1962/
  #1973, unrelated to this diff).
- `git diff -U0 origin/main -- internal/manual/manual.go`, filtered for
  non-comment lines: empty. Confirms the "behaviour-free" claim rather than
  assuming it from the PR description.
- `git diff --stat origin/main`: only `internal/manual/manual.go` and
  `scripts/ci/guard-deadcode-baseline.sh` (the review-found header fix)
  touched.
- `bash -n scripts/ci/guard-deadcode-baseline.sh` (shellcheck itself wasn't
  available in this sandbox — confirmed syntactically valid; CI's
  `guard-shellcheck-version.sh`-pinned shellcheck is the real gate for
  style/lint issues on this file, and only a comment block changed).
- Full gate, re-run after the review's own fixes: `gofmt -l .` (clean),
  `go build ./...`, `go vet ./...`, `go test ./...` (all packages `ok`, 0
  `FAIL` lines), `golangci-lint run ./...` (0 issues). `guard-deadcode-baseline.sh`
  itself needs GTK/WebKit headers not present in this sandbox — same
  substitution as every prior slice (a rootless `deadcode` run instead);
  real CI has the headers and is the actual gate.

## Baseline

`scripts/ci/deadcode-baseline.txt`: unchanged at 70 lines — this slice's
resolution was documentation for all 4 entries, not deletion.

~70 baseline entries remain, concentrated in `internal/pos` (16, tax code —
the card's own body says to triage carefully), `internal/plugins/marketplace`
(4), and misc single-entry files across the rest of the tree. Next slice
should pick one of those per-package, per the card's "split into several
small PRs by package" acceptance criterion.
