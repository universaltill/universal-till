# CI guard against gobind's silent skipped-binding (ut-docs#1735)

**Branch:** `fix/1735-gobind-skip-guard` · **Card:** ut-docs#1735 ·
**Complexity:** medium (Sonnet dev, Opus review)

## What shipped

`gomobile bind`/`gobind` can silently drop an exported function from the
generated Java/Kotlin API when one of its parameter/return types isn't
declared in the bound package (`./mobile`) itself — instead of failing, it
emits a `// skipped function <Name> with unsupported parameter or return
types` (or the field/variable/const equivalent, `unsupported type: ...`)
comment in its generated output and still **exits 0**. This is the exact
class of bug ut-docs#1721 found in the wild
(`mobile.SetBluetoothBridge` referencing an `internal/bluetooth`-declared
type), and `android-ci.yml`'s `compile` job — compile-only, `./gradlew
assembleDebug` — cannot see it: the Kotlin side never references a method
that was never generated, so nothing there fails either.

New:

- `scripts/ci/guard-gobind-skip.sh` — runs `gobind -lang=java` itself
  (against real `./mobile`, or in test/fixture mode against a given
  directory) and scans the generated `.java` output for the skip-comment
  shape, naming the dropped binding on failure.
- `scripts/ci/guard-gobind-skip_test.sh` — regression test against
  throwaway fixture trees, following the repo's existing
  `guard-*_test.sh` convention.
- Two workflow changes: the real-mode guard runs in `android-ci.yml`'s
  `compile` job (needs `gobind` on PATH, gated on the existing
  `android/**`/`mobile/**` path filter); its self-test runs unconditionally
  in `ci.yml`'s build job (pure bash, no toolchain, same convention every
  other guard's `_test.sh` follows).
- `CLAUDE.md`'s "Known residual gap" note updated to say this is closed.

## Independent review (Opus, isolated worktree)

Verdict: **safe to merge after fixes — no blockers.** The reviewer
installed `gobind` itself (confirmed no Android SDK/NDK needed for
`-lang=java` output), ran the guard against the real repo (clean pass),
planted a throwaway unbound-type function in a worktree copy of
`mobile/mobile.go` and confirmed the guard catches it and names it
correctly (then reverted — `git diff -- mobile/` empty), cross-checked
`SKIP_PATTERN` against every skip-message shape in
`x/mobile/bind/genjava.go` (all 7 covered), and mutation-tested the test
suite itself.

Findings, all fixed in this branch before merge:

1. **Self-test gated behind the Android path filter** (real-but-minor) —
   `guard-gobind-skip_test.sh` needs no toolchain but sat behind
   `steps.filter.outputs.match`, so a PR touching only the guard script
   shipped it unverified — against this repo's own convention (every
   other guard's `_test.sh` runs unconditionally in `ci.yml`). **Fixed:**
   moved the self-test step into `ci.yml`'s build job.
2. **No fail-closed check on a vacuous scan** (real-but-minor) — the
   guard could pass green having scanned zero `.java` files (gobind's
   output layout changing, or emitting nothing), the same "silent green"
   failure class this card exists to eliminate, one layer up. **Fixed:**
   `scan_dir` now counts files scanned (`SCANNED_COUNT`); real-CI mode
   fails closed if that count is 0 (fixture mode exempt, same pattern
   `guard-htmx-loaded.sh` already uses).
3. **Test case 4 killed no mutation** (real-but-minor) — the reviewer
   mutation-tested the suite and found the fixture line-wrapped gobind's
   phrase across a `/** */`-style Javadoc so no single line ever
   contained it, meaning neither the `//`-anchor nor the phrase-
   specificity was actually exercised; a reflowed real-world doc comment
   with the phrase on one flush-left line (verified against real gobind
   output) would produce a false positive with an anchor-less regex, and
   the field/variable/const skip-wording family was never covered at
   all. **Fixed:** added three cases — the real flush-left single-line
   Javadoc shape (kills the anchor-less mutant), a genuine `//` comment
   using the word "skip" without the full phrase (kills the phrase-
   dropped mutant), and gobind's field-skip wording naming a dotted field
   (kills the "only covers function/method" gap). All four (fixed
   regex vs. all three targeted mutants, plus the pre-existing suite)
   re-verified passing.
4. **Workflow comment's stated rationale was inaccurate** (real-but-minor)
   — claimed the guard runs "before paying for the NDK/Gradle compile",
   but it sits after the NDK/build-tools install; only the Gradle build
   itself is actually saved. **Fixed:** reworded the comment.
5. **Host GOOS instead of `GOOS=android`** (nitpick) — harmless today
   (`mobile/` has no platform-tagged files; reviewer confirmed linux vs.
   android/arm64 generated output is byte-for-byte identical) but a
   future `mobile/*_android.go` file could be invisible to a host-GOOS
   scan. **Fixed:** pinned `GOOS=android GOARCH=arm64` on the real-mode
   `gobind` invocation, matching what `generateAar`'s actual
   `gomobile bind -target=android/arm64,android/arm` binds against.
6. **`grep`'s error exit masked by `|| true`** (nitpick) — mostly closed
   by fixing #2 (a real scan failure would now also tend to produce a
   0-file vacuous scan); left as a known low-likelihood residual, not
   worth its own fix given #2's coverage.

Non-findings (reviewer explicitly verified, no bug): `set -euo pipefail`
interaction with `scan_dir`'s `return`-inside-`if` and the process-
substitution `while read` loops (verified with a real multi-file fixture:
both hits reported, no lost state); workflow `if:` gating and step
placement; no money/tax/security/data-loss surface, no secrets, no real
client/shop names; no UI surface touched, so no `web/help/` topic owed;
the `docs/code-reviews/2026-08-31-android-bluetooth-backend-seam-1721.md`
review record correctly left unedited (review records are immutable
history, not live docs).

## Verified beyond automated tests

- Real end-to-end run against the actual `./mobile` package (not just
  fixtures), both by the Dev subagent and independently by this
  orchestrator: clean pass, and a planted unbound-type function correctly
  caught and named, reverted afterward (`git diff mobile/` empty both
  times).
- `gofmt -l .` clean, `go build ./...` clean (no Go source touched by the
  final diff).
- `python3 -c "import yaml; yaml.safe_load(...)"` on both modified
  workflow files.
- Full updated test suite (7 cases) re-run after applying all review
  fixes: all pass.

## Deferred

None — all findings were fixed in this branch; nothing pushed to a
follow-up card.
