# Code review: Bluetooth pairing panel cleanup (ut-docs#1582)

**Branch:** `fix/1582-bluetooth-pairing-panel-cleanup`
**Complexity:** `easy` (mechanical cleanup, no new architecture)
**Dev model:** Sonnet (inline) · **Review model:** Sonnet (independent,
fresh-context, isolated worktree) — per the easy-tier routing table.

## What shipped

Seven follow-up findings from the independent review of ut-docs#76's
Bluetooth pairing panel (`universal-till` PR #782,
`docs/code-reviews/2026-09-05-bluetooth-device-pairing-76.md`), filed
separately as cleanup rather than expanding that PR further. None are
money/tax/security-blocking class; all are now fixed:

1. **Unbounded read-path D-Bus timeouts.** `GET /bluetooth-devices`,
   `GET /api/bluetooth-devices`, and the scan handler used to pass a bare
   `r.Context()` into `ListDevices`/`Scan`, unlike `pair`/`forget` which
   already bound theirs. Added `listBluetoothTimeout` (5s) for the two
   list call sites and `scanCallTimeout` (`discoverBluetoothTimeout` + 15s
   = 25s) for the scan handler — the extra margin is deliberate: a ctx
   deadline at or below `discoverBluetoothTimeout` would race Scan's own
   internal wait-then-stop sequence and return `ctx.Err()` instead of the
   candidates it just found, on every ordinary successful scan.
2. **`GET /api/bluetooth-devices/scan` mutated adapter state** (could
   power on the adapter, always started discovery) — flipped to POST,
   like `pair`/`forget`. Fixed together with the identical shape in
   `kitchen_stations_page.go`'s `GET /api/kitchen-stations/discover-printers`
   (per the finding's own note: fixing one alone would just be an
   inconsistency). Every client-side caller updated to send
   `{method: 'POST'}`: `bluetooth_devices.html`, `kitchen_stations.html`,
   and `settings.html` (the Settings → Printer card reuses the same
   discover-printers endpoint — a caller easy to miss, caught in review).
3. **Dead form-encoded fallback in `readAddress`** removed — the page
   only ever sends JSON; the `ParseForm` branch was unreachable from the
   product and only widened what the mutating endpoints accepted.
4. **`agent.go`'s `DisplayPinCode`/`DisplayPasskey` now refuse outright**
   (`rejected()`), matching `RequestPinCode`/`RequestPasskey`, instead of
   acknowledging and letting BlueZ park the pairing on the 45s handler
   timeout. `RequestConfirmation`/`RequestAuthorization`/`AuthorizeService`
   are untouched — still correctly scoped to the one device via
   `a.check(device)`.
5. **`apiError`'s redundant `"message": code`** dropped — the JS only
   ever reads `.code`.
6. **`nav.bluetooth_devices` duplicate key collapsed** into
   `bluetoothdevices.title`, removed from all four locale files
   (en/tr/fa/ar); `menu_page.go` now points at the page's own title key,
   same pattern every sibling nav entry already follows.
7. **Dead intermediate assignment removed** — the original finding named
   `internal/bluetooth/client_test.go` at "~line 406-407", but that
   pattern does not exist there. The exact described pattern (an
   `errors.New(...)` assignment immediately overwritten by
   `errors.Join(...)`) was found at that same line range in
   `bluetooth_devices_page_test.go`'s
   `TestBluetoothPairAPI_ErrorPathsMapToStatusesWithoutLeaking` instead,
   and fixed there. Independent review confirmed this file-name
   discrepancy is real (the card was wrong, not the fix).

## Independent review

Full independent pass (fresh-context Sonnet subagent, isolated git
worktree). Verdict: **SAFE TO MERGE — no findings.**

All 7 items above were checked against the actual diff and confirmed
correct, including:
- The `scanCallTimeout` margin was checked against `internal/bluetooth/client.go`'s
  actual `Scan` implementation (not just the comment's claim) and confirmed
  sufficient.
- Every `fetch()` caller of both flipped-to-POST endpoints was grepped
  across `web/ui/**/*.html` — all three (including the easy-to-miss
  `settings.html` reuse) were confirmed updated; none left on a bare GET.
- The locale-key collapse was verified as a clean removal across all four
  locale files with no dangling reference left anywhere in the tree.
- The finding-7 file-name discrepancy was independently re-derived from
  the pre-fix diff, not taken on the implementer's word.

**TDD re-verified independently, not taken on trust:** in its own
isolated worktree, the reviewer reverted the scan route back to GET and
confirmed `TestBluetoothScanAPI_RejectsGET` failed with `200, want 405`;
separately reverted both `ListDevices` call sites back to bare
`r.Context()` and confirmed `TestBluetoothListAPI_BoundsContextWithTimeout`
failed on both assertions while `TestBluetoothScanAPI_ReturnsCandidatesWithBoundedTimeout`
still passed (proving the revert was correctly scoped, no
cross-contamination). Restored both fixes and confirmed green again
before finishing. This mirrors the same revert-then-restore the
implementer already ran during Dev/Tester.

## Testing

- Full `go test ./...` — green (48 packages), run independently twice
  (once by Dev/Tester, once by the review subagent).
- `gofmt -l .`, `go vet ./...`, `golangci-lint run ./...` (0 issues) —
  clean, both times.
- Every CI-blocking guard relevant to this diff:
  `guard-data-access.sh`, `guard-i18n.sh` (1418 keys, all locales
  match), `guard-help-topics.sh`, `guard-docs-shots.sh` — all pass.
- `make docs-shots` re-run (required: three `internal/pages/**.go` files
  changed, which the guard hashes as a whole-file surface regardless of
  whether the change is visible) — 100/100 screenshots captured; only
  `web/help/img/en/sell.png` differs from the prior commit, by one byte —
  a non-deterministic PNG-encoding artifact from re-running the capture
  (visually pixel-identical), unrelated to this diff's behavior. The
  `bluetooth-devices` and `kitchen-stations` screenshots themselves are
  byte-identical to before (no visible change, matching that no markup
  changed).
- Real Playwright e2e run (not just Go tests):
  `e2e/tests/bluetooth-devices-76.spec.ts` and
  `e2e/tests/printer-discovery-http-error-1556.spec.ts` — all 3 tests
  pass against the built app.
- Visual check: viewed the regenerated `bluetooth-devices.png`,
  `kitchen-stations.png`, and `sell.png` — all render correctly, no
  layout regressions, no stale UI.
- New/updated Go tests: `TestBluetoothScanAPI_RejectsGET`,
  `TestBluetoothListAPI_BoundsContextWithTimeout`, plus every existing
  scan test switched from `btGet` to `btPostJSON` and the permission/
  kitchen-stations tests switched to `http.MethodPost`.

## Deferred / out of scope

- No real-hardware Bluetooth verification — already a documented
  Tester-side gap for this whole feature (no adapter in CI or this
  session).
- The sibling "Settings → Printer" LAN-discovery copy (ut-docs#1556) was
  updated only where it calls the now-POST `discover-printers` endpoint;
  no other change to that card's scope.
