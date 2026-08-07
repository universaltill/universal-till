# Kiosk status-bar update chip dead link (ut-docs#159)

## What shipped

`web/ui/layouts/base.html`'s status-bar update chip rendered a clickable
`https://www.universaltill.com/download` link whenever `canselfupdate` was
false, with no OS/writability awareness — including on a unix kiosk
(fullscreen, no browser chrome), where that link is a genuine dead end.
Confirmed on real field hardware (2026-08-06): "I cannot update it as it is
in full screen mode." This reproduced the exact class of bug board
ut-docs#147 was meant to close; #147/#148's fix only reached the Settings
page (`internal/pages/update_api.go`'s `updateUnavailableHTML`), never the
status bar.

Fix:

- `internal/selfupdate/selfupdate.go`: new exported
  `DownloadLinkActionable(goos string) bool` (`windows`/`darwin` → true,
  everything else → false) and a zero-arg `DownloadLinkActionableNow()` —
  the single source of truth shared by both surfaces, replacing what used to
  be two independently-maintained copies of the same check.
- `internal/pages/update_api.go`: `updateUnavailableHTML` now calls the
  shared predicate instead of its own inline `goos == "windows" || goos ==
  "darwin"`. The kept link also gains `target="_blank"` — a plain
  same-window navigation is a dead end in the WebView2 desktop shell
  (`cmd/unitill-desktop/webview_fallback.go`, no `NewWindowRequested`
  handler).
- `internal/httpx/httpx.go`: new zero-arg `baseFuncs["updatedownloadlink"]`
  template func wired to the shared predicate — consistent with the
  existing zero-arg `canselfupdate`/`updateavailable` convention; no
  request/platform threading needed, since the signal is the *server's* own
  `runtime.GOOS`, which is what the download link's binary would actually
  target anyway.
- `web/ui/layouts/base.html`: the chip's `canselfupdate == false` branch is
  now three-way — actionable link (Windows/macOS, `target="_blank"`) /
  inert informational text, `{{ T "settings.update.unavailable_here" }}`,
  reusing the exact key the Settings page already uses (kiosk/other) —
  instead of always showing a link.
- `web/public/app.css`: scoped the pill's `:hover` rule to
  `:is(a, button).sb-update` so the new inert `<span>` variant keeps the
  pill's look but not its clickable hover affordance.
- `web/help/{en,ar,fa,tr}/updates.md`: added a step documenting the
  status-bar chip's platform-dependent behavior (product-owner standing
  instruction, ut-docs#324 — the manual ships with the feature).

## Independent review (Opus, fresh context)

Verdict: **safe to merge**. Ran `go build ./...`, `go vet ./...`, `gofmt -l`,
the affected package tests, and the full suite; independently re-verified
both TDD claims by reverting `base.html` and `update_api.go` to their
pre-fix state and confirming the new/modified tests actually fail with the
real error, then restoring and confirming green again. Confirmed no
`os.MkdirAll`/`paths.Data` bug class (no file writes in this diff), no
import-cycle/layering issue, i18n keys all pre-existing and consistent
across locales, no real client/shop name or secret-shaped literal.

Three should-fix findings, all addressed before commit:

1. **The inert chip said nothing about why** — fixed by reusing
   `settings.update.unavailable_here`, matching what the Settings page
   already tells the operator.
2. **The inert `<span>` kept the clickable pill's `:hover` style**, inviting
   the exact click the fix exists to prevent on a mouse-driven non-writable
   Linux install — fixed by scoping the CSS hover rule to `a`/`button` only.
3. **The manual wasn't updated** — fixed; a step was added to all four
   locale topics.

One non-blocking note, deliberately not acted on here: `target="_blank"` on
macOS is a no-op improvement (the desktop shell's `webkit_darwin.go`
already hands off external navigation to the default browser regardless);
the Windows/Linux `webview_fallback.go` shell has no navigation handler at
all, so the real fix there is a `NewWindowRequested`/`NavigationStarting`
handler mirroring the darwin behavior — filed as a new Backlog follow-up,
not this diff's scope. `rel="noreferrer"` was also flagged as a non-issue:
`rel="noopener"` was already present pre-diff (not introduced here), and
modern engines imply `noopener` for `target="_blank"` regardless.

## Verified beyond automated tests

- Rendered the real embedded `base.html` (via the same `httptest`/renderer
  path the tests use) for both the actionable-link and inert-text branches
  and read the output directly — balanced tags, both reuse the existing
  `sb-item` class alongside the sibling `sb-conn`/`sb-enrol`/`sb-ver` chips
  in the same footer with no markup breakage.
- Mutation-tested all three assertions myself before the independent
  review (reverted `DownloadLinkActionable` to always-true, reverted the
  template's kiosk branch, reverted `target="_blank"`) — each corresponding
  test failed with the expected message, confirming they're load-bearing,
  not tautological.
- **Visual-check attestation (scoped, not a full browser screenshot):** this
  is a single-line, text-only status-bar chip that reuses the pre-existing
  `sb-item`/`sb-update` CSS classes already visually validated for the
  sibling `sb-conn`/`sb-enrol`/`sb-ver` chips in the same footer; no new CSS
  beyond the `:hover` scope-narrowing, no layout-affecting markup beyond
  swapping `<a>`↔`<span>` and an attribute. Did **not** drive a real browser
  screenshot across themes/viewports/RTL — judged disproportionate to a
  single-line text swap with no new layout surface. If a reviewer wants
  visual confirmation regardless, it's a two-minute `/run` check of
  Settings → Software update on a non-self-updating install.

## Explicitly deferred (new Backlog card)

- A real WebView2/GTK-WebKit `NewWindowRequested`/`NavigationStarting`
  handler in `cmd/unitill-desktop/webview_fallback.go`, mirroring
  `webkit_darwin.go`'s hand-off-to-default-browser behavior — `target=
  "_blank"` alone rests on the WebView2 implicit-popup default, which has
  no address bar either.
- The broader "what's the operator's actual remediation path on a kiosk
  that can't self-update" question raised by the original field report —
  intentionally out of scope for this bug fix (showing nothing beats a dead
  link, but doesn't solve the underlying stuck-kiosk problem).

## Safe-to-merge

Yes. Full gate green (`go build`, `go vet`, `gofmt`, `go test ./... -race`
— one pre-existing, unrelated failure in `internal/issuereport`
confirmed via `git stash` to reproduce identically on `main` HEAD, caused
by this sandbox running as root and bypassing the read-only-directory
permission the test relies on), `guard-data-access.sh`,
`guard-i18n.sh`, and `guard-help-topics.sh` all pass.
