# Code review — claim-by-phone QR + quiet registration hint (2026-07-17)

Branch `feat/claim-qr-and-nag`. Two queue items.

## Claim QR
The claim panel response now includes a QR of the claim URL (go-qrcode,
pure-Go vendored dep; PNG data-URI, no network). The owner scans it and
claims FROM THEIR PHONE — the escape hatch for shells that can't open an
external browser (Pi kiosk cage/chromium, windows/linux webview_go), and
honestly the best flow everywhere (the owner signs in on their own device
with their own session/2FA). `settings.enrol.claim_scan` ×4 locales;
logical-CSS styling.

## Registration hint (ADR-0015)
Registration is optional, but the status bar still shouted an amber
"Register this till" chip. Now a quiet outline chip, muted colors, copy
"Marketplace: not connected" (×4 locales) — informative, not nagging.

Tests: pages+enroll suites + both guards green. QR content = ClaimURL
(same value as the link — no new data exposure).
