# Review: catalog item-form tab-strip fade resize test is ~50% flaky (ut-docs#2182)

## What shipped

`e2e/tests/catalog-tab-strip-fade-2024.spec.ts`'s "resize while the dialog
stays open recomputes the fade across the 700px breakpoint" test was
reported ~50% flaky in CI. Its final assertion read the tab bar's
`className` synchronously, immediately after `page.setViewportSize(...)`,
racing the app's `resize` event listener (`recomputeLiveTabBarFade` in
`web/ui/pages/catalog.html`) that recomputes the fade classes — the same
class of race the rest of this spec file already guards against via its
`expectFade()`/`expect.poll()` helper, just missed on this one assertion.

- `e2e/tests/catalog-tab-strip-fade-2024.spec.ts`: the final assertion now
  polls (`expect.poll(() => bar.evaluate((el) => el.className), …)`)
  instead of reading once. Deliberately still asserts on the **class list**,
  not the computed pseudo-element opacity `expectFade()` uses elsewhere in
  the same file — above the 700px breakpoint the `.tab-bar::before`/`::after`
  rules (including the `content` that gives them a box at all) don't exist,
  so a computed-opacity read there would read CSS's initial value `'1'`
  regardless of the fade's real state (this is the same reasoning the
  file's own pre-existing "kiosk floor" test already uses for the same
  width). `let classes` → `const classes` at its one remaining use site
  (no longer reassigned).

No app/production code changed — this is a test-file-only fix. No locale,
no Go, no docs-shots surface touched.

## Independent review

One fresh-context Sonnet pass (card is `complexity:easy`), isolated
worktree, no findings requiring a fix.

Checked specifically:
- **Is `expect.poll` a real fix or a paper-over?** Real: it retries the
  actual DOM read to a settled value using Playwright's default poll/expect
  timeout (5s, unconfigured in `playwright.config.ts`), not a fixed sleep —
  ample margin for a same-tick `resize` listener.
- **Does the "pseudo-elements don't exist above 700px" claim hold?**
  Verified against `web/public/app.css`: the `.catalog-form-head
  .tab-bar::before`/`::after` rules (including their `content: ""`) live
  entirely inside `@media (max-width: 700px)`. Above that width there is no
  `content` rule for either pseudo-element, so the claim is correct as
  stated.
- **Is asserting on `className` (not opacity) the right call?** Yes, for
  the reason above — a computed-opacity assertion at 1024px would silently
  assert against a meaningless always-`'1'` value, not the fade's real
  state.
- **Any other flake risk nearby?** None — the other reads adjacent to a
  `setViewportSize` call in this test already go through `expectFade()`
  (itself a poll), or aren't raced (the baseline read right after
  `openNewItemForm`, before any resize happens).
- **Style**: consistent with the file's existing conventions.

## Verified beyond the automated gate

**Pre-fix flake reproduced, not just taken on the issue's word.** The
implementer could not reproduce it locally in 8 runs and said so honestly
rather than claiming a repro. The independent reviewer went further:
reverted just this hunk back to the synchronous read and ran the test in
batches — 20 runs (0 failures), 30 runs (**1 failure** — the race,
reproduced), 40 runs (0 failures). ~1 failure in 90 local runs (~1%),
consistent with the issue's own premise that CI hardware is slower/more
contended than a local dev machine, making the resize-event-dispatch race
far more likely to lose there (~50%) than here. This is a genuine, rare-
locally race, not a phantom flake or a misdiagnosis. The file was restored
afterward and confirmed to match the fix commit exactly
(`git diff` empty, `git status --short` clean).

**Post-fix.** Targeted test alone: 10/10 consecutive runs green (the
issue's own acceptance criterion), confirmed twice independently — once by
the implementer, once by the reviewer in a separate worktree. Full 5-test
spec file: 5/5 green, confirmed independently twice.

**Acceptance criteria, checked individually:**
- [x] Passes 10 consecutive runs — verified twice (implementer + reviewer).
- [x] Does not weaken what the test proves — still asserts the fade class
  actually changes across the breakpoint; the class-vs-opacity choice is
  the more correct one for this specific width, not a weaker one.
- [x] No `waitForTimeout` — `expect.poll` polls the real DOM state.

## Gate

| Check | Result |
| --- | --- |
| `gofmt -l .` | no output |
| `go build ./...` | exit 0 |
| `guard-e2e-fixtures-import.sh` | ✓ 121 specs checked, unaffected (import unchanged) |
| targeted test, `--repeat-each=10` | 10/10 passed (implementer run) |
| targeted test, `--repeat-each=10` | 10/10 passed (independent reviewer run, separate worktree) |
| full spec file | 5/5 passed (both runs) |
| pre-fix repro (reviewer, reverted hunk) | 1 failure / 90 runs — race confirmed real |

No `go test ./...`/`go vet`/`golangci-lint`/full CI guard sweep run locally
for this one — this diff touches no Go source, no locale file and no
`web/ui`/`web/public`/`internal/pages/**.go` surface, so none of those
gates can be affected; the PR's own CI run is still the authoritative
check before merge.

## Deferred / accepted

None — no findings needed deferral.

## Verdict

**Safe to merge.** The fix removes a real, independently-reproduced race
condition, matches the file's own established `expect.poll` pattern, does
not weaken the assertion (and is in fact the *more* correct assertion for
this specific viewport width, per the file's own pre-existing reasoning at
the kiosk-floor test), and both the targeted test and the full spec file
pass repeatedly across two independent runs.
