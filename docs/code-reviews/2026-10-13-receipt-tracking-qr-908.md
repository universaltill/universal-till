# Review — order-tracking QR on counter receipts (ut-docs#908)

**Shipped:**
- **Printed and on-screen receipts.** A sale rung up at the till now carries the customer order-tracking QR, with the caption "Scan to track your order".
  - On the printed ESC/POS receipt (`print.Doc.TrackingQR`/`TrackingCaption`), it prints after the fiscal QR and before the footer. A plain-text printer gets the caption and the URL.
  - It also appears on the on-screen receipt after tender.
- **Gate (`receiptTrackingQROffered`).** All of these must hold:
  - a completed normal sale (never a refund or a voided sale);
  - not a till that follows a main till;
  - the new receipt setting `receipt.show_tracking_qr` is on. When it is unset, it is on unless the dine-in/takeaway prompt is off;
  - the Shop-and-up plan gate (`orderTrackingOffered`).
- **Link.**
  - Printed: `http://<this till's LAN address>:<listen port>/o/{token}?lang=<shop locale>`.
  - On screen: built from the request host, like the kiosk.
  - The token comes from the idempotent `EnsureOrderTrackingToken`, so reprints reuse it.
  - Any failure prints the receipt without a QR and logs a warning that never includes the token.
- **Receipt designer.** New checkbox **Order tracking QR (paid plans)**. The preview shows a sample QR.
- **Liveness.** An untracked (`order_status = ''`) tokened sale now expires 12h after `created_at`. This applies in `pos.OrderTrackingVisible`, in the SQL prune in `ListLiveTrackedOrders` (`julianday`, so it accepts both RFC3339 and SQLite datetime) and in the cloudsync push. Without it, every tokened counter sale would ride the cloud push forever. Tracked and terminal statuses behave as before. Recorded as ADR-0070 amendment (2) in ut-docs.
- **Docs and translations.**
  - Keys: `receipt.tracking.caption` and `designer.receipt.show_tracking_qr` in en/ar/fa/tr. The de/es/pt packs follow up.
  - Help: `customer-order-tracking` and `designer` updated in en/ar/de/fa/tr, with docs-shots regenerated for both topics.
  - README feature line updated.

**Built by:** an Opus 5.5 dev subagent, TDD-first. Each new test failed first (build errors on the new API, then real assertions).

**Reviewer:** an independent Fable subagent, working in a detached worktree.
- **Gate:** build, vet, gofmt, the affected package tests, golangci-lint and the i18n, data-access, help-drift and help-topics guards were all green.
- **TDD re-verified by revert → fail → restore → pass:**
  - the untracked-expiry branch (`TestOrderTrackingVisible`, 5 cases fail);
  - the sale-type gate (`TestBuildReceiptDoc_NoTrackingQRWhenGateFails/refund`);
  - the SQL prune (`TestListLiveTrackedOrders_PrunesOldUntrackedRowsInSQL`).

**Findings and resolution:**
- **M1 (major), fixed.** An additional (replica) till printed a QR pointing at itself. Order-status taps on a replica are applied on the main till, which never holds the token: the LAN sale journal doesn't carry it (#4238). So the page would have shown "untracked" for 12h and never "Ready". The gate now refuses on any till with `sync.primary_url` set. This is recorded in ADR-0070 amendment (2) §1, the help (all languages) and a comment. #4238 lifts it.
- **m1 (minor), fixed.** A reprint of a voided sale carried a QR. The gate now requires `status = completed`. The on-screen path is post-tender, so it always passes "completed".
- **m2, fixed.** The help said the default holds "until you change it". In fact any designer save pins the shown value. The help now says "until you first save the receipt designer".
- **m3, fixed in en/de.** The help didn't say that the on-screen receipt's link follows the till screen's language (the printed one follows the shop locale). ar/fa/tr don't have that sentence.
- **Nit, fixed.** The `mainTillLANAddress` doc now says it is this till's address whatever its role.
- **Nit, handled.** The language packs: follow-up PRs in ut-plugin-language-de/es/pt this cycle.
- **Accepted.** The ut-cloud read endpoint keeps its own copy of the liveness rule for terminal statuses only. It needs no change: the cloud is replace-on-push, so an expired untracked token disappears on the next tick.
- The 32-hex token wraps over two 42-column rows on plain-text printers. That is intentional and tested.
- The fixes were re-checked TDD-first: `TestReceiptTrackingQROffered` failed to build against the old signature, then passed with the voided and replica cases.

**Verified beyond unit tests:**
- The on-screen receipt with the QR block was rendered from the real template (real `renderReceipt`, a real QR) and screenshotted with Playwright Chromium at 360×740 in en and fa (RTL) and at 1024×600 in tr. The QR is centred, the caption sits under it, nothing is clipped, `scrollWidth` equals the viewport (no horizontal scroll), and RTL mirrors correctly.
- docs-shots of the receipt designer and tracking topic were regenerated with the new checkbox visible.
- **Not checked:** a real thermal printer, and a phone scanning the printed QR. No hardware here.

**Verdict:** safe to merge once CI is green. Language-pack PRs follow the merge (new keys: core first).
