# Review: iOS LAN discovery through Bonjour, not raw multicast (ut-docs#3218)

- **Branch**: `fix/3218-ios-bonjour-discovery`
- **Author**: Opus 5.5 (pipeline lane `lane:cloud-54`)
- **Reviewer**: Fable, an independent subagent with a fresh context. The card is `complexity:hard`, so the reviewer was a different model from the author.
- **Date**: 2026-10-02

## What shipped

- `internal/discovery/native.go` adds a platform seam, `NativeBridge`, with three methods: `Browse` (returns JSON), `Advertise` and `StopAdvertising`. It uses only types gomobile can bind, and errors come back inside the return value, so the Swift conformance is a plain method with no `NSError**`.
  - `scan` uses the bridge when one is registered. That covers `Browse`, `BrowsePrinters` and `PrimaryWatch`.
  - `Advertiser.startLocked` publishes through the bridge when one is registered.
  - Bridge entries are turned into `*mdns.ServiceEntry` values, so the existing service filter, parsers and `maxCandidates` cap apply unchanged.
  - A refused Local Network permission becomes `ErrLocalNetworkDenied`.
- `mobile.DiscoveryBridge` and `SetDiscoveryBridge` mirror the seam, because gobind only binds types declared in `./mobile`. A compile-time assertion in both directions keeps the two shapes in step.
- `ios/UniversalTill/BonjourBridge.swift` is the Swift side:
  - `NWBrowser` (`bonjourWithTXTRecord`) finds services. A UDP `NWConnection` per instance resolves it to IPv4 and a port without sending anything.
  - `DNSServiceRegister` advertises the Go server's own port with the same TXT record hashicorp/mdns publishes.
  - `TillServer.start` registers the bridge before `MobileStart`.
- `/api/sync/discover-primaries` and `/api/setup/discover-primaries` answer 403 `{"data":null,"error":{"code":"local_network_denied"}}` when the permission is refused. The Tills page and the setup wizard then show the new key `tills.discovery.denied` (en/tr/fa/ar, plus de/es pack PRs).
- Docs:
  - `ios/README.md`: the "needs the multicast entitlement" limitation is replaced by how discovery works on iOS.
  - `project.yml`: comment updated.
  - `web/help/*/multitill.md` step 5 covers the iOS permission in all five help locales. `make docs-shots` ran; no PNG changed, only `manifest.json`.
- Linux, Windows, macOS and Android register no bridge, so their hashicorp/mdns path is unchanged.

## Review findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | `NWBrowser` can report an instance before its TXT record arrives and send the TXT later as a metadata change. The first version read the TXT only when it first saw the name, so a till could arrive with no `id=` and be dropped. | **Fixed.** The TXT is refreshed from every results set and read when the scan ends. |
| 2 | minor | Retain cycle: the `NWConnection` handler captured the connection, which leaked one connection per resolve. | **Fixed.** The handler now captures `[weak conn]`, and `finish()` nils the handlers. |
| 3 | minor | A transient `.waiting`/`.failed` during resolution lost that peer for the rest of the scan. | **Fixed.** The claim is released, so the next results change resolves it again. |
| 4 | minor (UX) | While the system Local Network prompt is still showing, a 4 s scan already reports "denied". | **Fixed in wording.** The message now says "If you were just asked, allow it and search again; otherwise …". |
| 5 | nit | `DNSServiceSetDispatchQueue`'s result was not checked. | **Fixed.** On failure the ref is deallocated and an error is returned. |
| 6 | nit | Truncating a TXT string at 255 bytes could split a UTF-8 sequence. | **Fixed.** It now cuts on a character boundary. |
| 7 | nit | The setup-wizard branch had no e2e test. | **Fixed.** A second spec case was added. It fails on the pre-fix `setup.html`. |
| 8 | nit | A doc-comment line in `mobile.go` was overlong. | **Fixed.** |
| 9 | process | The de/es language packs need the new key. | PRs follow the core merge, in the same cycle. |

The reviewer also checked these and found them fine:
- The gobind ObjC protocol (`MobileDiscoveryBridgeProtocol`) and the selector shapes match the Swift.
- The `dnssd` constant comparisons (Int vs Int32).
- No deadlock in `registerQueue.sync`.
- `nativeScan` cancellation is bounded and panics are recovered.
- A Bonjour-published record carries everything hashicorp/mdns on Android needs (SRV target, A, TXT `id=`).
- No file writes, no raw SQL, and no hardcoded UI strings.

## TDD and verification

- `TestDiscoverPrimariesAPI_LocalNetworkDeniedIsItsOwnError` fails on the pre-fix handler (500 "discovery scan failed"). Verified by the author and again by the reviewer.
- Ten `TestNativeBrowse_*`/`TestNativeAdvertiser_*` tests fail with the `scan`/`startLocked` hooks reverted. Verified by the reviewer.
- `e2e/tests/discovery-local-network-denied-3218.spec.ts` has two cases, Tills and setup, at 390px. Each fails on its pre-fix template ("No primaries found …"). The Tills case also asserts no horizontal overflow.
- The existing discovery e2e tests pass: `tills-lan-discovery`, `discovery-list-disambiguation-1295`.
- Full `go test ./...`, `golangci-lint` (0 issues) and every `ci.yml` build-job guard pass. The exceptions are local only: shellcheck is not installed here, and no shell changed.

## Not verified here

- **The Swift code has not been compiled or run by the author or the reviewer**, because no Mac is available. Compilation is proven by `ios-ci` (macos-26, Xcode 26) on this PR.
- On-device behaviour is not proven yet: the iPhone/iPad scan finding the Android main till, the Android scan finding an iOS main till, and the permission prompt. That check is the owner's device run (ut-docs#3070, TestFlight). The close-out comment says so.

## Verdict

Safe to merge once `ios-ci` is green. No blockers remain.
