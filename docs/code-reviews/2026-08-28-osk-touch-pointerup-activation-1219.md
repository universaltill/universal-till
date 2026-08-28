# OSK key/toggle activation moves to pointerup (ut-docs#1219)

**Date:** 2026-08-28
**Branch:** `fix/1219-osk-touch-pointerup-activation`
**Repo:** `universal-till`

## What shipped

`web/public/osk.js`'s in-app on-screen keyboard activated its keys and the
`[data-osk-toggle]` button on `click`, while both `preventDefault()` their own
`pointerdown` (deliberately, so keys never steal focus from the field being
typed into). On the kiosk's real browser — WebKitGTK, Wayland/labwc, raw
touch, `mouseEmulation="no"` — a canceled `pointerdown` means WebKit never
synthesizes the follow-up `click` at all (the Pointer Events spec says it
SHOULD; WebKitGTK disagrees). Every OSK key and the keyboard-toggle button
were therefore dead under touch on the actual device — reported live on
Pi5-1, v0.6.12 — while a plugged-in mouse worked fine, which is why neither
mouse-driven e2e nor desktop testing ever caught it. Field taps (opening the
OSK) were unaffected, because a plain input's `pointerdown` is never
`preventDefault()`-ed.

Fix: key and toggle activation now bind to `pointerup` (fires reliably for
both touch and mouse on every tested engine), and the old `click` listeners
for keys/toggle are removed entirely — not left alongside `pointerup` — so a
platform that *does* still fire `click` after a canceled `pointerdown`
(Android WebView, per the issue) can't double-fire. Field-tap-to-open stays
on `click`, unchanged (ut-docs#155's deliberate-action rule).

Two new e2e regression tests added to `e2e/tests/settings-osk.spec.ts`
dispatch a synthetic `pointerdown` + `pointerup` pair with no `click` in
between (mirroring `tables-touch-drag-1170.spec.ts`'s honesty-note pattern
for the same reason: Playwright's `.click()`/`.tap()` helpers, and even a
`hasTouch` context, still synthesize a `click` in desktop
Chromium/WebKit — they cannot reproduce the actual WebKitGTK gap this bug
lives in).

## Independent review

Spawned a fresh-context Sonnet subagent (`complexity:easy` per the
model-routing rubric) in an isolated worktree. Verdict: **safe to merge**.

- Traced the toggle-handler restructuring (open/close/retarget-to-a-different-
  form's-field/no-op-outside-a-form logic) line by line against the original
  `click` handler — confirmed byte-for-byte preserved, only re-homed from
  `click` to `pointerup`.
- Confirmed the Android-WebView double-fire guard is real, not just
  asserted: `wantsOSK()` only matches `INPUT`/`TEXTAREA`, so the surviving
  `click` listener's `if (wantsOSK(ev.target)) show(ev.target)` is a
  guaranteed no-op for a `<button data-osk-toggle>` even if `click` still
  fires there.
- Grepped the whole repo for `data-k`, `data-osk-toggle`, `#osk`, `osk.js` —
  every existing caller (7+ e2e spec files) drives the OSK via Playwright's
  `.click()`, which dispatches a real pointerdown→pointerup→click sequence
  in Chromium, so `pointerup` already fires and every pre-existing test
  passed unmodified. No other caller relies on the removed `click` binding.
- Ran the full affected suite (`settings-osk.spec.ts`,
  `osk-central-guard.spec.ts`, `sale-screen-osk-scan-submit-1177.spec.ts`,
  `index-keyboard-1023.spec.ts`): **24/24 green.**
- **Independently re-verified the TDD claim**, not taken on trust: reverted
  only `web/public/osk.js` to the pre-fix version, reran the two new
  `ut-docs#1219` tests — both failed exactly as claimed (key test: field
  value stayed `""`; toggle test: `#osk` never appeared). Restored the fix,
  reran — both passed. This is a genuine regression test, not a false-pass.
- `gofmt -l .` and `go build ./...` both clean (frontend-only diff, as
  expected).
- Checked for this pipeline's two recurring bug classes (missing
  `os.MkdirAll` on a file-write handler; a cwd-relative path where
  `paths.Data(...)` belongs) — confirmed not applicable, no Go/file-I/O in
  this diff.
- No real client/shop name or secret-shaped literal anywhere in the diff.

**One non-blocking finding, fixed:** a pre-existing comment a few lines
below the toggle-handling code still said "...BEFORE the click handler
runs..." — stale once activation moved to `pointerup`. Updated the wording
in place (comment-only change, re-verified `gofmt -l .` clean and the diff
otherwise untouched) rather than deferring it, since this diff is exactly
what made the comment go stale.

## Verified beyond automated tests

- Full 24-test affected-suite run green, independently re-run by the
  reviewer in its own isolated worktree (not just trusted from Dev/Tester).
- TDD claim independently reproduced red-then-green by the reviewer, per
  above — not just re-stated from the implementer.
- Manually traced every other repo location touching `data-k`/
  `data-osk-toggle`/`osk.js` for a caller that could regress from the
  removed `click` binding; none found.

## Scope notes

- No new user-facing string was introduced (pure activation-event rewiring,
  no new UI/copy), so no `web/locales/*.json` or manual/help-topic change
  is needed — the behavior change is "keys that were silently dead now
  work," not a new screen or flow for `web/help/` to document.
- Root-caused and hot-fixed live on Pi5-1 by a human first (per the issue);
  this PR ports that verified fix into the repo so it survives the next
  `.deb` build, plus adds the regression coverage the on-device hotfix
  never had.

## Verdict

**Safe to merge.** No blocking findings; the one non-blocking comment-
accuracy finding was fixed in this same branch before commit.
