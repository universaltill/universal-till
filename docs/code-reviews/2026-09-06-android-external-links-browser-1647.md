# Android external links → system browser (ut-docs#1647)

**Date:** 2026-09-06
**Branch:** `fix/1647-android-external-links-to-browser`
**Card:** ut-docs#1647 (p1, `complexity:easy`, `lane:local`)
**Reported by:** the product owner, directly, on the TECLAST test tablet

## What was reported

On `/my-reports` the Status column carries a **View on GitHub** link. Tapping
it on the tablet did **nothing at all** — no browser, no navigation, no error
message. The owner also asked that it open in a separate browser instance
rather than replacing the POS screen (the second half of the report — showing
the GitHub ticket's own open/closed state — is a different change and is
tracked separately as ut-docs#1648).

## Root cause

`MainActivity.kt`'s `WebViewClient.shouldOverrideUrlLoading`:

```kotlin
val target = request?.url ?: return true
if (target.authority != allowedHost) {
    return true // block: refuse to navigate off-origin
}
```

`return true` means "the app has handled this navigation" — and the app then
did nothing with the URL. So *every* off-origin link in the till UI was
silently dead on Android, not just this one. macOS
(`cmd/unitill-desktop/webkit_darwin.go`) has opened external links in the
system browser since it shipped; Android was the outlier.

`target="_blank"` on the link could not have saved it either:
`setSupportMultipleWindows` is off and there is no
`WebChromeClient.onCreateWindow`, so a new-window request is dropped too.
(With multiple windows unsupported, WebView routes `target="_blank"` through
`shouldOverrideUrlLoading` as an ordinary navigation, which is why the fix
lands in the right place.)

The origin confinement itself is correct and stays: ut-docs#1254 established
that `window.AndroidKiosk` is injected into every page this WebView shows, so
letting the WebView navigate off-origin would hand the kiosk-unlock bridge to
content this app never authored. The bug was the missing *else* branch, not
the block.

## Change

- `MainActivity.openInSystemBrowser(Uri)` — `Intent.ACTION_VIEW` +
  `FLAG_ACTIVITY_NEW_TASK`, so the browser opens alongside the till rather
  than inside its task. The blocked branch calls it and still returns `true`,
  so the WebView stays exactly where it was.
- `scripts/ci/guard-android-external-links.sh` + `_test.sh`, wired into
  `ci.yml`.
- `external_link_failed` string in all four locales (`values`, `-fa`, `-tr`,
  `-ar`).
- `android/README.md`: the navigation bullet and device-test step 10.

## Review findings (self-review — see "Process deviation" below)

**1. Subframe navigations would also have popped the browser. (fixed)**
`shouldOverrideUrlLoading` fires for subframes too, not only main-frame
navigations. My first draft called `openInSystemBrowser` unconditionally in
the off-origin branch, so a single off-origin `<iframe>` anywhere in the till
UI — or in a plugin-rendered page — would have launched the browser on every
page load, unprompted, over the live sale screen. There is no such iframe in
`web/ui/` today (checked), which is exactly why this would have shipped
unnoticed and detonated later. Now gated on
`request?.isForMainFrame == true`; subframes stay blocked and silent, exactly
as they were before this change, so nothing regresses.

**2. Arbitrary schemes must never reach `ACTION_VIEW`. (designed in)**
The URL comes from a web page and `ACTION_VIEW` resolves whatever app claims
a scheme — Android's own `intent:` scheme can name a component directly. So
`openInSystemBrowser` allowlists `http`/`https` and returns for everything
else, leaving it blocked exactly as before. Per the ecosystem's security-first
rule, the narrower behaviour is the default and the widening would have needed
justifying, not the reverse.

**3. A failed hand-off must not fail silently. (fixed)**
The module's existing convention is a silent `catch` with a comment
(`engageKioskLock`, `releaseKioskLock`). Following it here would have
reproduced the exact symptom this ticket is about — tap, nothing happens — on
a device with no browser app. The catch now shows a translated Toast.

**4. No kiosk release in the hand-off. (deliberate)**
`launchPackageInstaller` drops the Lock Task pin before launching, because
Android silently refuses to start a non-allowlisted activity from a pinned
app. This does **not** do that: `launchPackageInstaller` is reachable only
through a manager-PIN-gated form, whereas this is reachable from any link on
any page, so releasing the pin here would be a kiosk escape available to page
content. In a pinned self-order session the hand-off is therefore a no-op —
acceptable, because every page carrying an external link is manager-gated and
unreachable from the self-order kiosk that pins in the first place.

**5. The first version of the guard silently passed two of its own
regressions. (fixed)**
Written as whole-file greps, it matched `authority != allowedHost` in
`onPermissionRequest` and `return true` in four unrelated places, so removing
the real origin check — or flipping the blocked branch to `return false`,
reopening the ut-docs#1254 hole — both passed. Only writing the regression
test surfaced this. The guard now extracts the two relevant function bodies
(comments stripped, string literals blanked before brace counting) and asserts
within them. A guard that has never been shown to fail is indistinguishable
from one whose grep quietly stopped matching.

## Verification

- `./gradlew :app:compileDebugKotlin` — **BUILD SUCCESSFUL** (real Android
  SDK/NDK, API 36).
- `bash scripts/ci/guard-android-external-links_test.sh` — 9 planted
  regressions, all rejected; the committed source accepted. Fixtures cover the
  original bug, a dead function, the origin check removed, `return false`, the
  scheme allowlist dropped, `FLAG_ACTIVITY_NEW_TASK` dropped, the main-frame
  restriction dropped, the hand-off commented out, and a swallowed failure.
- `bash scripts/ci/guard-android-i18n.sh` — 7 keys across ar/fa/tr, parity and
  placeholders intact.
- `go vet ./...` clean; `go test ./internal/pages/... ./internal/cloudsync/...
  ./internal/data/...` pass. No Go source changed.
- All other `scripts/ci/guard-*.sh` pass except `guard-commit-attribution.sh`
  (needs CI's git log on stdin) and `guard-deadcode-baseline.sh` (flags
  `cmd/unitill-desktop/*`, GOOS-specific) — both verified pre-existing by
  re-running them against a stashed, clean tree.

## Not verified

**On-device confirmation is still outstanding** (AC 6): tap **View on GitHub**
on the tablet, confirm GitHub opens in the browser app and `/my-reports` is
still behind it. A source-level guard and a successful compile cannot prove
the OS actually resolved the intent on that hardware. The card stays open
until that is done.

## Process deviation

The pipeline's Reviewer step calls for an independent different-model subagent
review. This session was configured not to spawn subagents, so the review
above is a self-review. It is recorded here rather than omitted so the
difference is visible: findings 1, 3 and 5 are real defects caught and fixed
before commit, but they were caught by the same author who wrote the code.

## Follow-ups filed

- **ut-docs#1648** — `/my-reports` Status shows the delivery status, not the
  GitHub ticket's own open/closed state (the other half of the owner's
  report). Needs an ut-cloud change; the till must not call GitHub itself.
- **ut-docs#1649** — the Linux/Windows desktop shells have no external-link
  policy at all (only macOS does). Unverified which way they fail; the card's
  first step is to check on the Pi before designing anything.
