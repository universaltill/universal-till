# 2026-08-25 — Till main page keyboard regression (ut-docs#1023)

**Branch:** `pipeline/1023-hold-modal-osk-guard`
**Author:** Sonnet (this pipeline cycle, inline) · **Reviewer:** independent
Opus subagent (complexity:medium → Opus review, per scrum-master's model
routing)

## What shipped

ut-docs#1023 reported the native OS keyboard popping on the till main page
again — the second time this class of bug has come back (the first was
ut-docs#155). The ticket's own text already suspected the cause: "Fixing
the general inputmode handling (the two-keyboards card) very likely fixes
this too — check that first before adding page-specific patches."

Investigation confirmed that suspicion. ut-docs#1022 — a separate card,
already merged into `main` before this cycle picked #1023 up — added a
central `guardField`/`guardSweep` mechanism to `web/public/osk.js` that
sweeps every OSK-able field on every page and sets `inputmode="none"` up
front, before any interaction, closing the exact "programmatic focus pops
the native keyboard" class of bug #1023 describes. That fix reaches both
sites #1023 named on `web/ui/pages/index.html`:

- the tender scan-barcode input (`autofocus`, line ~131) — already had its
  own static template guard from #155, unaffected either way;
- the hold-modal's `#hold-label-input` (`autofocus` + a `.focus()` call in
  the opener) — had **no guard at all** before #1022; now covered by the
  central sweep, which reaches it via `document.querySelectorAll('input,
  textarea')` regardless of the field being inside a (currently closed)
  `<dialog>`.

**No production code changed.** #1022's own new tests only exercised
`/catalog` — a page picked specifically because it had zero prior
per-template guards. `index.html` was never touched by that PR, so nothing
proved the central sweep actually reached this page's specific
autofocus/`.focus()` sites, and #1023's own acceptance criteria explicitly
asked for a page-scoped regression test ("this is the second time it has
come back"). This PR is that coverage: `e2e/tests/index-keyboard-1023.spec.ts`.

## What the independent review found

Independent Opus subagent, fresh context. Ran the new spec for real, cross-
checked it against sibling specs, and — because the author's own framing
("already fixed by #1022, just needs a test") is exactly the kind of
conclusion worth distrusting — deliberately tried to break it rather than
take it on faith: reverted `osk.js` to its pre-#1022 commit and reran the
new spec to confirm it actually fails there, reproduced the ticket's exact
reported device state (`display.osk = auto`, touch-capable) with Playwright's
`hasTouch` fixture rather than trusting `setOskMode(page, 'on')` to be
equivalent, and read `internal/pages/index_osk_test.go` plus every sibling
e2e spec this change touches or duplicates.

Findings, all addressed in this round:

1. **should-fix — the spec never asserted the custom OSK stayed shut.**
   #1023's underlying rule (#155) is "opens ONLY from a deliberate user
   action," and neither original test asserted anything about `#osk`
   itself — only about `inputmode`. Added an explicit
   `expect(page.locator('#osk')).toBeHidden()` after the hold-modal opens
   (proving the *programmatic* `.focus()` call doesn't trigger it), paired
   with a liveness proof (`label.click()` → `#osk` visible) so "hidden"
   can't pass vacuously because `#osk` was never built at all — same
   pattern already used in `settings-osk.spec.ts` and pointed out by the
   reviewer as the reason a bare `toBeHidden()` isn't enough on its own.
2. **should-fix — the spec exercised the wrong `enabled` branch.**
   `setOskMode(page, 'on')` and the ticket's actual reported state (`auto`
   + touch-capable) reach `osk.js`'s `enabled = true` through two different
   code paths (`mode === 'on'` vs. the `matchMedia('(pointer: coarse)')`
   detection `auto` relies on) — testing `'on'` doesn't prove the config
   the product owner actually had. Switched both tests to
   `test.use({ hasTouch: true })` + `setOskMode(page, 'auto')`, verified
   independently by the reviewer to reach `enabled = true` via the coarse-
   pointer path.
3. **should-fix — the held-sale label collided with a sibling spec's
   assertion.** `hold-named-tab.spec.ts` asserts `#held-sales` contains
   `"Tab 1"` (substring match); this spec's original `"Tab 1023"` contains
   that substring, so a reordered/sharded/`--grep` run could let one
   spec's leftover silently satisfy the other's check. Renamed to
   `"OSK guard 1023"`.
4. **should-fix — comment overstated the assertion's actual strength.**
   "Must already be 'none' by the time focus lands," backed only by
   `toHaveAttribute`'s default 5s retry window, would still pass a
   hypothetical future *reactive* guard that fires a couple of seconds
   late. Replaced with one `page.evaluate` reading `document.activeElement`
   and its `inputmode` together, so what's asserted is what the comment
   claims: guarded at the instant of focus, not eventually.
5. **should-fix, card-level (not a defect in this diff) — ut-docs#1023's
   AC3 ("opening the hold modal focuses its field without popping the OS
   keyboard") is still false for locales `osk.js` has no OSK layout for**
   (`LAYOUTS` covers `en`/`tr`/`fa`/`ar` only — not `de`/`es`, which ship
   via language plugins). `guardField()` deliberately skips suppressing the
   native keyboard on a non-numeric field there — per #1022's own review,
   suppressing it with no on-screen replacement would leave literally no
   way to type at all, strictly worse than the bug being fixed. This is a
   real, known, already-tracked gap (ut-docs#1047, "Add de/es OSK
   layouts"), not something this PR's test-only scope can close. Recorded
   explicitly in the spec's own header comment and in the close-out below,
   rather than silently claiming full AC coverage.
6. **should-fix — AC5 ("verified on real touch hardware") is unmet by
   construction.** A headless-Chromium e2e spec, however faithful to the
   reported device state, is not a real-hardware check, and #1022's own
   history records a real Android-WebView-specific failure mode
   (`osk.js:294-300`, viewport resize breaking `position: sticky`) that
   this kind of test structurally cannot catch. This cold cloud session
   has no physical till to verify against — recorded as an open item below
   rather than claimed.

Nits (nice-to-have, not applied): a one-line comment at `index.html`'s
flagged `autofocus`/`.focus()` sites pointing at `osk.js`'s central sweep,
so a future audit doesn't re-derive the same investigation; cross-linking
ut-docs#1048 (the hold-modal/PIN-dialog discoverability question #1022's
review already routed to Admin Review) from this card, since this PR's new
test now pins the "no keyboard until a second tap" behavior #1048 is
deciding whether to change.

### Verdict

Ship the test-only fix, with the two acceptance-criteria caveats (AC3 for
unsupported locales, AC5 hardware verification) stated plainly rather than
silently marked done.

## Verification

- `gofmt -l .` clean, `go build ./...` clean, `go vet ./...` clean (no Go
  source touched — pure new e2e spec).
- Full `go test ./...` green (all packages).
- All 5 checked CI-blocking guards pass:
  `guard-data-access.sh`, `guard-i18n.sh`, `guard-compliance-claims.sh`,
  `guard-help-topics.sh`, `guard-docs-shots.sh` (the last confirms no
  template/screenshot surface moved — expected, since no template changed).
- New spec run standalone: 2/2 pass. Run alongside its closest siblings
  (`hold-named-tab.spec.ts`, `osk-central-guard.spec.ts`,
  `settings-osk.spec.ts`): 17/17 pass, no cross-spec interference.
- **TDD verification, done twice** (once by the author, independently
  reproduced by the reviewer): checked out `web/public/osk.js` from the
  commit immediately before ut-docs#1022's fix (`f6a2155`, parent of
  `ae94d8c`), reran the hold-modal test — it fails
  (`Expected: "none" / Received: ""` on the `inputmode` attribute,
  confirming the field really did carry no guard before #1022), then
  restored `osk.js` and confirmed both tests pass again. The load-time
  test does **not** discriminate old vs. new code (it exercises the
  pre-existing, unrelated #155 template guard) — its own header comment
  says so; it's kept for end-to-end confidence in the effective-autofocus
  target, not counted as coverage of the #1022 mechanism.

(Playwright's bundled browser build (`^1.61`, per `e2e/package.json`'s
`^1.48.0` range) didn't match this sandbox's pre-installed Chromium
(`chromium-1194`); verification here used a temporary, uncommitted
`launchOptions.executablePath` override in `e2e/playwright.config.ts`,
reverted immediately after each run — the pushed diff carries no trace of
it. `git diff origin/main --stat` before every push: exactly one file,
this spec.)

## Verified beyond automated tests

Not possible in this cloud session — no physical touch till available.
Per AC5 above, this is an explicit open item: a real-device pass (any
touch-capable Pi/Android till, `display.osk = auto`) confirming (a) the
till main page shows no keyboard until a tap, and (b) opening Hold Sale
shows no keyboard until a second tap on the field, would close it out
fully. Recommended as a DevOps/human follow-up rather than blocking this
merge, consistent with how #1022 itself shipped with a noted (different)
real-device risk.

## Explicitly deferred / out of scope

- **ut-docs#1047** (add de/es OSK layouts) — the real fix for AC3's
  remaining locale gap; this PR does not attempt it, and #1023 should stay
  linked to #1047 rather than being treated as fully resolving the German-
  pilot case.
- **ut-docs#1048** (Admin Review: whether hold-modal/PIN-entry dialogs need
  a visible affordance for a second tap) — a genuine UX/security tradeoff
  already routed to a human by #1022's own review; this PR's new test
  pins the current (correct-per-that-routing) behavior without deciding
  the open question.
- No ADR: this is test-coverage for an already-shipped fix, not a new
  architectural decision.
