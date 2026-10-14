# Review: a Bluetooth scanner's dropped beat no longer cuts the code, and device scans never search item names (ut-docs#4342)

**Reported (owner, tablet till v0.34.2):**
- Scanning voucher `5000000000029` first added "Pfand zurück 5,00 EUR (19.0%)" at €0.00; the second scan offered the voucher.
- Later: "sometimes works, sometimes item not found".
- Scanner: a Netum L8BL Pro over Bluetooth.

**Measured on the tablet** (`getevent -lt`, 10 scans):
- One key every 62.5 ms (the HID report interval), so a scan takes about 0.9 s. That is the slowness the owner saw.
- 2 of the 10 scans had one ~124 ms gap.
- `app.js` reset its scan buffer on any gap over 100 ms, so only the tail was submitted (`9`, `0000029`).
- `/api/pos/scan` then fell through to the `name LIKE '%code%'` tier: `9` matched "(19.0%)".

**Shipped:**
- **`web/public/app.js` wedge buffer:**
  - A burst survives gaps up to 300 ms.
  - On Enter it counts as a scanner read when its median gap is under 120 ms with at most one slower gap. A person typing (150–250 ms per key) does not count.
  - `utScan.submit(code, input, src)` sets the scan form's new hidden `src` field to `scanner` or `camera`, and resets it after the submit.
- **Camera scan:** a code is submitted only after two identical reads in a row.
- **Server:**
  - `POSRepo.ResolveShortcutLineDecodedExact` (the existing chain without the name tier).
  - `ui.PriceResolverAdapter.ResolveExact`, and `pos.ExactResolver` with `Service.ResolveExact`.
  - In `/api/pos/scan`, `src=scanner|camera` resolves items by exact code only. That covers the base and variant guard, the scan-cache fast path and `ScanQtyWithResult`.
  - Typed entry keeps the name search.
- **Help** (en/de/tr/ar/fa):
  - `bluetooth-devices`: a "slow scans" bullet (whole code awaited, no name search, the transmit-speed tip).
  - `sell`, step 2: the camera reads a code twice.
  - The `ar` help-drift baseline is re-recorded; the gap is unchanged.

**Tests:**
- `internal/pages/pos_scan_source_test.go`, real resolver and DB:
  - scanner and camera reads of `9` add nothing;
  - typed `Pfand` still finds by name;
  - exact barcode and SKU device scans work;
  - a voucher number isn't shadowed by an item named after it.
- `e2e/tests/scan-bluetooth-gap-4342.spec.ts`, Playwright's fake clock with the exact measured gaps:
  - 62 ms keys with one 124 ms gap → the full code, `src=scanner`;
  - a 100 ms per character scanner → `scanner`;
  - 180 ms typing → typed;
  - camera `9` then the full code twice → one submit of the full code, `src=camera`.
- **TDD:**
  - With the old `app.js`, the 124 ms test received `0000029` and the camera test `9`.
  - With the old `pos_api.go`, both device-scan Go tests ring up the name match.
  - Both re-verified independently by the reviewer.

**Independent review:** Fable (built by Opus 5.5). Safe to merge.
- **Medium, fixed (test first):** a scanner on a ~100 ms per character transmit setting was classed as typing. `SCAN_GAP_MS` raised from 100 to 120, with a new e2e case.
- **Low, accepted:** with Shift held, auto-repeat keeps the gaps small, so an all-caps query typed into the search box can read as a scan. This predates the change and is not made worse.
- **Nits, no change:**
  - the gift voucher popup carries `src` through; harmless, because the resubmit is an exact code;
  - the kiosk's `/api/self-order/scan` is fed only by tiles with exact codes.
- **Verified OK:**
  - search box, qty box, OSK ↵, the Add button and `data-scan-own` are unchanged or better;
  - `src` never persists;
  - the exact-first tier order means a device scan resolves the same row as `ResolveExact`;
  - the toast quoted in the help matches every locale and the DE pack.

**Gates:**
- `go build`/`vet`; tests in `pos`, `ui`, `data` and `pages` pass.
- `guard-i18n`, `guard-help-drift`, `guard-data-access`, `guard-help-topics`, `guard-compliance-claims`, `guard-no-showmodal` pass.
- 23 + 11 related e2e tests pass.
- golangci-lint is left to CI (local toolchain mismatch).

**Not verified:** on the tablet itself. The owner forbade installs, so the next release proves it.

**Follow-ups:**
- ut-docs#4344: resolve a code as every kind at once and ask when it matches more than one (owner direction).
- ut-docs#4343: variant barcode attach fails silently on inactive variants.

**Verdict:** safe to merge.

## Follow-up: CI failure on the first push (2026-10-14, Fable review)

**What failed:** "a scanner on a slow 100 ms transmit setting is still a scanner read" got `src=""` in CI only. `page.clock.install()` leaves the fake clock running in real time, so each `keyboard.press` round trip on the runner was added to every 100 ms gap and pushed the median past 120 ms.

**Fix:** `openWithPausedClock` pauses the clock after load. Gaps now move only on `runFor`.

**Verified:**
- With 40 ms of real latency injected per key, the old spec fails with CI's exact error and the new one passes.
- Mutations: `SCAN_GAP_MS` 100 fails the 100 ms case. `slow <= 2` and `SCAN_GAP_MS` 140 fail the two new negative cases.
- The other CI failures on that run (locations dialog at the kiosk floor, phone /backoffice width, phone sell, popup fit) were all checked on the branch rebased onto main: 112 passed. They came from the stale base.
- `web/help/img/manifest.json` regenerated (hashes only, no PNG changed). Guard fresh.

**Fable findings:**
- *Minor*, fixed: the rule's negatives weren't pinned. Added "two slow gaps → typed" and "steady 130 ms → typed".
- *Nit*, fixed: commented the `+1000` in `pauseAt`.
- No blockers.

**Verdict:** safe to merge once CI is green on the rebased head.
