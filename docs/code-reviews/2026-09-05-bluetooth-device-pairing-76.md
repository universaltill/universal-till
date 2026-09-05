# Code review: In-POS Bluetooth device pairing panel (ut-docs#76)

**Branch:** `feat/76-bluetooth-device-pairing` · **PR:** universaltill/universal-till#782
**Complexity:** `hard` (escalated from `medium` at Architect time — new
external dependency, first D-Bus consumer, new OS permission surface)
**Dev model:** Fable (subagent) · **Review model:** Opus (independent,
fresh-context, isolated worktree) — per the hard-tier routing table.

## What shipped

A manager-gated "Bluetooth devices" panel (`/bluetooth-devices`) that
scans, pairs, trusts, connects and forgets a Bluetooth HID device (barcode
scanner, scale) entirely from the kiosk UI, closing the field-reported gap
(a Metum scanner previously paired manually via `bluetoothctl` over SSH):

- `internal/bluetooth`: new package wrapping BlueZ over D-Bus
  (`github.com/godbus/dbus/v5`), fully seamed for testing against a fake
  D-Bus connection — no real adapter needed in CI.
- `internal/pages/bluetooth_devices_page.go`: 5 manager-gated routes
  (page, list, scan, pair, forget), coded JSON error envelope, never leaks
  raw D-Bus errors to the client.
- `web/ui/pages/bluetooth_devices.html`: HTMX-free plain-fetch panel,
  RTL-safe (logical CSS), page-local `T` lookup for inline JS status text.
- `packaging/linux/dbus-unitill-bluetooth.conf` + `.goreleaser.yaml`: the
  scoped D-Bus system-bus policy ADR-0078 decided (six `org.bluez`/D-Bus
  interfaces), Linux-only, package-owned via nfpm `contents:`.
- Help topic (en/ar/fa/tr) + 32 locale keys per file + nav entry.
- `e2e/tests/bluetooth-devices-76.spec.ts`.

No new DB table — BlueZ itself is the source of truth for what's
paired/trusted; the panel reads live state on every load. Design and
requirement history: BA + Architect sections on ut-docs#76. Binding
permission decision: ADR-0078 (`ut-docs/adr/0078-till-service-dbus-
system-bus-grants.md`, merged).

## Independent review — findings

Full independent pass (Opus, fresh context, isolated worktree — see
"Verification" below for how). Two **blocking** issues found and fixed
before merge; the rest triaged.

### Fixed (blocking)

1. **`Pair()` reported a successful pairing as a failure.** Pair and Trust
   are already committed in BlueZ by the time the final `Connect` call
   runs; a HID device commonly drops the ACL right after "Just Works"
   pairing as it switches into HID mode, so BlueZ can answer a generic
   Connect failure even though the (now trusted) device reconnects on its
   own seconds later. The old code treated any non-`AlreadyConnected`
   Connect error as `ErrPairingFailed` → HTTP 409 → a "scan again" message
   the manager could never act on, because `Scan` deliberately filters out
   devices that are already paired. Fixed in `internal/bluetooth/client.go`:
   a Connect failure after successful Pair+Trust is now non-fatal — the
   page's on-success reload re-reads `Connected` from BlueZ either way.
   Regression test:
   `TestPair_ConnectFailureAfterSuccessfulTrustIsNotAFailure`.
2. **The new D-Bus policy grant shipped with zero regression coverage.**
   The file itself was correct (verified byte-for-byte against ADR-0078),
   but nothing pinned it, despite this exact directory already carrying
   that pattern for the same threat class (`packaging/kiosk_setup_test.go`'s
   pos-writable-tree guards). ADR-0078's own text records that the first
   draft of this file omitted the `AgentManager1` grant and looked correct
   in review — there's no real BlueZ in CI to catch a silent regression.
   Added `packaging/dbus_bluetooth_policy_test.go`: parses the real XML,
   asserts exactly the six ADR-0078 interfaces are granted to `user="pos"`
   and nothing wider (no `group=`, no `<deny>`, no bare `send_type`
   wildcard, no duplicate/extra interface), and that nfpm actually ships
   the file to `/etc/dbus-1/system.d/`. **Verified experimentally**:
   deleting the `AgentManager1` line (the exact ADR-0078 near-miss)
   fails the new test with a clear message; restored and re-passed.

### Accepted / deferred (not blocking, filed as follow-up)

Minor findings, none security- or data-loss-class, filed as
universaltill/ut-docs#1582 rather than expanding this PR further:

- Read paths (`GET /bluetooth-devices`, `GET /api/bluetooth-devices`, and
  the `GetManagedObjects`/`StartDiscovery`/`Powered` calls inside `Scan`)
  pass a bare `r.Context()` to godbus calls, which applies no default
  reply timeout — inconsistent with `pair`/`forget`'s deliberate
  `context.WithTimeout` bounding. A wedged `bluetoothd` would park the
  handler goroutine indefinitely.
- `GET /api/bluetooth-devices/scan` can mutate adapter state
  (`Adapter1.Powered=true`, `StartDiscovery`) on a GET. Reachable by
  prefetch/link since the session cookie is `SameSite=Lax`. **Note:**
  this mirrors the exact same GET-based shape already shipped for
  `kitchen_stations_page.go`'s `discover-printers` endpoint — fixing one
  without the other would be an inconsistency, not a fix, so the
  follow-up covers both.
- `readAddress`'s form-encoded fallback in
  `bluetooth_devices_page.go:readAddress` is unreachable from the product
  (the page only ever sends JSON) and only widens what the mutating
  endpoints accept.
- `internal/bluetooth/agent.go`'s `DisplayPinCode`/`DisplayPasskey`
  acknowledge (return nil) for the expected device instead of returning
  `Rejected` like `RequestPinCode`/`RequestPasskey` already do — a device
  that needs a code typed on it dies on the 45s handler timeout instead of
  failing fast with an accurate message.
- `apiError`'s JSON envelope sets `"message": code` — redundant with
  `code`, harmless (the JS maps on `code`), but leaves `message` useless
  to any other consumer.
- `nav.bluetooth_devices` duplicates `bluetoothdevices.title`'s value
  across 4 locale files instead of reusing the page's own title key (the
  pattern every sibling nav entry uses).
- A dead intermediate assignment in `client_test.go` (immediately
  overwritten by `errors.Join(...)`) — cosmetic.
- Test device names ("Metum Scanner", "Zebra Scanner") are real hardware
  vendor names (not a client/shop name, so no rule violation, but a
  generic name would be marginally safer test data).

## Verification (beyond the independent review's own pass)

- `go build ./...`, `go vet ./...`, `gofmt -l .` — clean, before and
  after the fixes.
- `go test ./...` (full suite) — green, before and after the fixes.
- CI guards re-run after the fixes: `guard-data-access`,
  `guard-kiosk-engine`, `guard-page-http-error`, `guard-i18n`,
  `guard-compliance-claims`, `guard-help-topics` — all pass. (Full
  guard suite, including `guard-docs-shots`, already re-verified when
  merging current `main` into this branch — see that merge commit.)
- Independent review's own TDD re-verification (reported in full in its
  transcript, summarized here): picked
  `TestPair_AuthenticationFailureIsPairingFailedAndAgentIsUnregistered`,
  reverted the agent-unregister-on-failure behavior in `client.go`,
  re-ran the test, got a real assertion failure (not a compile error)
  naming the missing call, then restored and re-passed all
  `TestPair_*` tests. Confirms the Tester's "these are real, non-
  tautological tests" claim independently rather than on trust.
- This session's own TDD proof for the new packaging test: deleted the
  `AgentManager1` grant line from the `.conf` file, confirmed
  `TestBluetoothDBusPolicyGrantsExactlyTheADR0078Interfaces` fails with a
  clear message naming the missing interface, restored the file
  (`git status` clean afterward), confirmed it passes again.
- Independently re-verified (this review): repository pattern (no SQL
  outside `internal/data`/`internal/db` — this feature adds none at
  all), self-order kiosk isolation (no `/self-order` route, no `Engine`
  reference), i18n (all 4 locale files carry the new keys, no hardcoded
  inline-JS status strings), RTL (logical CSS properties only, grepped
  for `left`/`right`), manager gating (`canPerform(d, r, "settings")` on
  all 5 routes), error handling (no raw D-Bus error reaches the client),
  smallest-grant design (byte-for-byte XML match to ADR-0078, six
  interfaces, `user="pos"`, no wildcard), offline-first degrade (missing
  adapter → status notice, HTTP 200, never an error page), the two
  recurring bug classes (no file I/O at all in the new Go files — neither
  applies), Linux-only packaging (single nfpm `contents:` entry, no dead
  Windows/macOS stub), and an audit of the pairing agent's lifecycle for
  a leak on an untested error path (none found — the connection-per-
  request design means BlueZ drops the agent when the connection dies
  even in the worst case).
- Help topic prose (all 4 locales) read against the actual handlers and
  template — accurate, with the one caveat that B1 (now fixed) would
  have broken step 3's "the page refreshes and the device appears in the
  paired list" promise.

## Safe-to-merge verdict

**Safe to merge.** Both blocking findings are fixed and independently
re-verified (a live regression proof for each, not just a read-through);
the full gate is green; the remaining minor findings are genuinely minor
(no money/tax/security/data-loss class among them) and are tracked as
universaltill/ut-docs#1582 rather than expanding this PR.
