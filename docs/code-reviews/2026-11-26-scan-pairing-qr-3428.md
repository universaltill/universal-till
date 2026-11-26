# Review: "Scan QR code" on the join screens (ut-docs#3428)

Branch `feat/3428-scan-pairing-qr`. Client-only: `web/public/qr-scan.js` (shared detector moved out of `app.js`, plus
`utQrScan.parse/open`), `web/public/join-scan.js` (binder), setup wizard "Enter a code" tab and Tills page button,
overlay and chip, `en.json` + `en.meta.json` keys, iOS/macOS camera usage text, `multitill.md` + regenerated shots,
`httpx.HeadAssets` entries, Go + Playwright tests. No server route or handler changed.

Pre-flight: till user-visible behaviour = updated (help `multitill.md`, docs-shots manifest + PNGs); en.json key =
updated (`en.meta.json`, English only); README/reference = n/a (no capability claim change); review record = this file.

## Findings
- Security, QR carries no more than the typed code: OK. The scanned string is filled into the existing `code` field and
  the existing form posts it; the server (`decodeEnrollCode` + `validPrimaryBaseURL`) stays the authority. Foreign-shop and
  wrong codes get the server's same message as a typed code (no new path).
- Token handling: OK. No `console.*`, web storage, URL, `data-*` or visible text carries it (grep clean); the chip shows
  the host only, in `textContent` inside `<bdi dir=ltr>` (no innerHTML). Scan never auto-submits (fill-then-confirm).
- URL/SSRF: OK. Client parse only displays `host` after http(s)-only, no userinfo/query/fragment/path check; it never
  fetches the QR-supplied URL. Server-side validation is unchanged and decides any outbound call. Payload capped at 1024
  chars, token regex `^[0-9a-f]{32}$`, two identical consecutive reads required.
- Moved detector regression: OK. Sell scan uses `window.utGetBarcodeDetector` (same FORMATS, same native-vs-vendored
  logic, decoder version read from `data-decoder-v`); plugin_settings still uses `utGetBarcodeDetector`. Sell-screen
  camera specs pass (tester).
- RTL / long locale: OK. Code input `direction:ltr` with ellipsis, chip wraps, host isolated by `bdi`.
- Tester finding (head-asset signature test) fixed before review: both scripts in `httpx.HeadAssets`.
- No findings requiring change. Nits accepted: none.

## Verification
`go build ./...`, `go test ./internal/pages ./internal/httpx`, gofmt, i18n/RTL guards, Playwright join-scan + sell camera
specs (Dev/Tester). Real-device camera checks not run (no device in this lane); deferred to the usual device pass.

Verdict: approve.
