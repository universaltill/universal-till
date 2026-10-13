# Review — no LAN order-tracking QR (ut-docs#4260)

**Shipped.** The customer order-tracking QR encoded `http://<till LAN IP>:8080/o/<token>`: useless to a customer's phone off the shop Wi-Fi, and it handed every customer the till's address. Now `orderTrackingLink` (`internal/pages/order_tracking.go`) is the only tracking-URL builder, fed only by `orderTrackingCloudBase()`, which is empty until the cloud page exists (#4238). So the kiosk confirmation, pay-at-counter confirmation, on-screen receipt and printed receipt show no QR. Token minting and the plan/receipt gates stay as the foundation for #4238. Receipt designer: the tracking box is disabled with a note (new key `designer.receipt.tracking_qr_needs_cloud`, en/tr/ar/fa + de/es/pt packs), the preview draws no sample, and saving keeps the stored `receipt.show_tracking_qr` (also on an additional till's write-through). Help `customer-order-tracking` + `designer` updated in en/de/tr/ar/fa.

**Review** (independent, Fable; Opus-built): approve. No remaining path puts a till host into a tracking link: `advertisableHost` is left only for pairing and the table self-order QR, and `mainTillLANAddress` only for cloudsync (the table QR and LAN reachability of `/o/` belong to #4261, noted on that card).
- Low, fixed: stale "LAN" wording in `self_order_shop.go` and `self_order_confirmation.html` comments.
- Low, fixed: added a Go-level test for the disabled box and its note (`TestReceiptDesignerPage_TrackingBoxDisabledWithNote`); before this, only the e2e spec covered it.

**TDD re-verified by the reviewer:** with `orderTrackingCloudBase()` returning `http://192.168.1.20:8080`, 7 of the new or rewritten tests fail; restored, they pass.

**Verified beyond unit tests:** `go vet ./...` and `go test ./...` (92 packages ok); guards i18n, help-drift, help-topics, data-access, compliance, competitor-naming, core-neutral, kiosk-engine, no-showmodal, no-inline-handlers, netaccess pass. `guard-deadcode-baseline` fails locally only on `internal/logging` (unrelated; it skips the desktop root here without GTK headers). Playwright: new `receipt-designer-tracking-4260.spec.ts` (en + fa at 1024×600) plus the existing self-order tracking and pay-at-counter specs pass. I looked at the designer screenshots in en and fa (RTL): the box is greyed out, the note sits next to it in the viewport, and the preview has no tracking block. Not looked at: phone width; the kiosk confirmation (the QR is simply gone there).

**Verdict:** safe to merge. Deferred: #4261 (LAN reachability, table QR), #4238 (cloud page).
