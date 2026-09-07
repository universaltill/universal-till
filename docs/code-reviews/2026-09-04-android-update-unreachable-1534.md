# Code review — Android in-app update unreachable (ut-docs#1534)

- **Date:** 2026-09-04
- **Branch:** `fix/1534-android-update-reachable`
- **Card:** ut-docs#1534 (`bug`, `p1`, `source:user`, `complexity:medium`)
- **Author model:** Opus 5 · **Independent review model:** Fable 5.1, fresh
  context, isolated git worktree

## What shipped

The product owner reported from a real TECLAST tablet on v0.10.3, with v0.10.5
available:

> "I can see the 0.10.5 is available but when I click on it, it just brings me
> to the software update section in the setting and not install the update."

The ut-docs#1246 install machinery (`window.AndroidKiosk.installUpdate()` →
`DownloadManager` → `FileProvider` → the system package installer) was fine.
Nobody could reach it. The route was:

1. `base.html:94` chip → `/settings#android-update`.
2. Operator lands on the **Software update** card, which offered only
   *Check for updates*.
3. That returned, on Android, `updateUnavailableHTML`'s link with a **bare
   same-page fragment** `#android-update` — inert on every page but
   `/settings`.
4. The only control that installs was a manager-PIN form inside the **Display**
   card under the **Exit to OS** heading, far below.

`settings.html:11` is an `hx-trigger="load"` panel *above* the anchor, so it
reflows the page after the browser's jump and the jump lands short — near the
top, which is the card the owner described.

Changes:

- The install form moved into the **Software update** card.
- The card's heading line now names both versions ("Universal Till v0.10.3 —
  ⬆ Update available v0.10.5").
- `updateUnavailableHTML`'s Android branch emits `/settings#android-update`.
- A hash re-scroll that survives the enrol panel's late settle.
- `selfupdate.InstallBridgeAvailable(goos)` — one owner for the platform
  predicate — plus the `httpx.UpdateInstallBridge` seam so the Android branch
  is renderable in tests on any host.
- Manual updated (`web/help/{en,tr,fa,ar}/updates.md`) with the Android
  procedure; screenshots regenerated.

## What the independent review found

Reviewed at Fable 5.1 in an isolated worktree, briefed with the diff scope, the
`CLAUDE.md` rules for the area, the ut-docs#1246 precedent, and an instruction
to run the build/vet/tests/guards rather than read only. It ran all of them and
re-verified the TDD claim itself by reverting `settings.html` +
`update_api.go` to the parent commit, confirming the two key tests fail with
the real defect (form at byte 12893, card spanning 6966–8350; bare-fragment
href), then restoring.

**Fixed:**

1. **The re-scroll listener was consumed by the wrong event (SHOULD-FIX).**
   `htmx:afterSettle` bubbles, and `nav.html` has four `hx-trigger="load"`
   chips that settle in milliseconds — a `body`-level `{once:true}` listener
   was burned by whichever landed first, long before the enrol panel it
   guards against. That panel calls `enroll.Fleet` with a **15 s** HTTP
   timeout, so on shop wifi it also outlasts the 800 ms fallback. The fix that
   "made the anchor reliable" would have done nothing on the exact hardware
   that reported the bug. Now listens on the panel element itself.
2. **A regression this change introduced into the first-boot wizard
   (SHOULD-FIX).** `setup_update_check.go` calls the same helper, so making
   the link absolute pointed a first-boot Android till at `/settings`, which
   `auth/middleware.go` 303s to `/login` — losing the Alpine wizard's step
   state, for a destination that cannot work anyway (no manager account
   exists yet). Invisible before only because a bare fragment was an inert
   no-op there. New `setupUnavailableHTML` states the fact with no link on
   Android; every other platform unchanged. Regression test added.
3. **The manual had gone false in all four locales (SHOULD-FIX).**
   `web/help/*/updates.md` still said "click Update now — the app restarts"
   and that non-self-updating installs show "plain text with nothing to tap".
   Both wrong on Android. Added the real procedure including the
   install-unknown-apps prompt and the ~140 MB download, and the first-boot
   caveat from finding 2 above.
4. **The cashier dead end (SHOULD-FIX).** `base.html` shows the update chip to
   every role, but the form sat behind `{{ if .isManager }}` — so a cashier
   tapping it landed on a page with no form, no anchor and no explanation:
   this card's own bug, one role down. The `isManager` wrap was never the
   security boundary (the endpoint verifies the PIN server-side and never
   reads the session role, and `login.html` renders exit-to-os to anonymous
   visitors on exactly that reasoning). Form now renders for all roles; the
   check/schedule controls stay manager-only. The test that asserted the
   opposite was inverted and extended to assert the boundary that does exist:
   blank PIN → 403 from a cashier *and* from a manager session, wrong PIN
   never 200.
5. **The placement test proved less than it claimed (NIT).** Asserting the
   form fell between the "Software update" and "Display" headings would also
   have passed with the form filed under **Theme**, which sits between them.
   Now walks back to the enclosing `<div class="card">` and asserts that card
   is the update one, with an `<h2>` count guarding the walk; the anchor
   assertion was narrowed to "inside the form", not "anywhere on the page".
6. **The seam deviated from the precedent it cited (NIT).** `httpx` inlined
   `runtime.GOOS == "android"` while `update_api.go` kept its own
   `goos == "android"`, "kept in step by comment" — which is exactly how the
   Settings page and the status chip drifted apart originally. Both now call
   `selfupdate.InstallBridgeAvailable`.

**Accepted / deferred:**

- **Finding 7 (NIT, pre-existing):** the form's error states are a bare `✗`
  for wrong PIN, 429 lockout and network failure alike, and a `res.ok` with no
  bridge present consumes the PIN and writes an `update_authorized` audit row
  while showing nothing. Predates this change and needs new i18n keys across
  five locales. Filed as **ut-docs#1536** rather than widened into this card.
- The review confirmed the seam is a process-local Go var with no HTTP or
  user-input path reaching it, `-race` clean, and no other `runtime.GOOS` read
  making the same decision.

## Verified beyond automated tests

- TDD claim re-verified independently, in an isolated worktree, with the exact
  pre-fix failure output captured.
- Template structure after the block move: 106 `{{ if }}`/`{{ range }}` vs 106
  `{{ end }}`, 67/67 `<div>`, 24/24 `<form>`; the Display card's Exit-to-OS
  subsection still well-formed.
- A Go template action inside an HTML comment is still parsed — an explanatory
  comment mentioning the `isManager` gate broke the page with
  `unexpected EOF` until reworded. Caught by the render test, not by eye.
- i18n: no new keys; `status.update_available`, `settings.update.download`,
  `shifts.manager_pin` verified present in en/tr/fa/ar and in
  `ut-plugin-language-de`. German heading line checked for width against the
  24rem masonry column.
- `MainActivity.kt`'s `shouldOverrideUrlLoading` compares authority only, so
  the same-origin absolute path with a fragment is followed by the WebView.
- Manual screenshots regenerated (`make docs-shots`, 96 passed) and the
  freshness guard re-run.

## Gate

`go build ./...`, `go vet ./internal/...`, `go test ./internal/...` and every
`scripts/ci/guard-*.sh` pass, with two exceptions, both unrelated to this diff
and both confirmed to behave identically on a clean `main`:

- `internal/server`'s `TestListenWithFallback_WildcardHostFallsBackToLoopback`
  fails on darwin — filed as **ut-docs#1535** (p2: the assertion is a
  security one about never binding wildcard).
- `guard-commit-attribution.sh` reads commits from stdin and is a CI-only
  check.

## Verdict

**Safe to merge.** The user-visible defect is fixed at its cause, the two
regressions the review found (one of them introduced by this change) are fixed
with tests, and the manual matches what the app now does.
