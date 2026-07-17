# Code review — claim UX fixes (2026-07-17)

Branch `fix/claim-code-reuse`. Two field issues Farshid hit while claiming his
store (both reproduced from data/code, not guesswork).

## 1. Repeat "Claim this store" clicks invalidated the code on screen

Marketplace `IssueCode` REPLACES the store's code on every mint. The till
button minted on every click, so clicking again (or re-opening the panel and
clicking) killed the code the operator was already typing — Farshid's first
claim failed with "wrong or expired code"; DB showed the successful code was
minted 8 seconds before redemption (i.e. a re-issue immediately before).

Fix (`internal/enroll/enroll.go`): `claimCodeCache` — repeat `ClaimCode`
calls within 13 minutes return the SAME code without re-minting. The window
is duration-based on the till's own clock (not the server's `expires_at`)
so marketplace clock skew can't stretch it past the server's 15-minute TTL;
2-minute margin leaves typing time. Keyed by store id; a re-registered till
mints fresh. Test `TestClaimCodeReusedWithinWindow` (reuse, then re-mint
after the window).

## 2. The claim link hijacked the POS webview (no way back)

The mac shell's `createWebViewWithConfiguration` handled `target=_blank` by
loading the request in the SAME view — the claim link (marketplace + Zitadel
login) took over the till UI with no navigation chrome to return.

Fix (`cmd/unitill-desktop/webkit_darwin.go`), class-wide not link-specific:
- `isExternalURL` — any http(s) host that isn't localhost/127.0.0.1/::1.
- `target=_blank`/`window.open` to an external URL → `NSWorkspace openURL`
  (default browser); till-local popups keep the previous same-view behavior.
- NEW `decidePolicyForNavigationAction` (delegate now also
  `WKNavigationDelegate`, wired via `webview.navigationDelegate`): any
  main-frame navigation to an external URL is CANCELLED and opened in the
  default browser. The POS webview is pinned to the till — it can never be
  stranded on an external page again.
- Non-http(s) schemes (about:blank etc.) are left alone; sub-frame loads
  (none expected) unaffected.

Verified: `go build -tags desktop` compiles the ObjC; full suite + both CI
guards green. ⚠️ Behavior test needs the real app (next release's dmg
launch-test: click the claim link → Safari opens, till stays).

## Known remaining (backlogged, not in this branch)
- Pi kiosk (cage + chromium --kiosk) still navigates in place on the claim
  link — kiosk has no default-browser escape. Right answer there is a QR
  code on the claim panel so the owner claims from their phone; do with the
  kiosk field-test round.
- windows/linux webview_go fallback has no navigation-policy API; same QR
  mitigation applies.
