# Android LAN-address discovery — pairing fix (ut-docs#1501, root cause of #1499)

**Date:** 2026-09-03
**Author:** farshidmirza
**Reviewer:** self-review against real-hardware evidence (local session; both
devices on the same LAN throughout)
**Scope:** `internal/lanip/` (new), `internal/discovery/advertiser.go`,
`internal/pages/sync_api.go` + tests.

## What changed and why

Pairing failed on real hardware in both directions (ut-docs#1499, reported by
the product owner). The evidence collected before writing any code:

- The Pi's own scan returned `base_url: http://127.0.0.1:38201` for the
  Android till — so "Request pairing" dialled the caller's own loopback.
- `dns-sd -L` showed the tablet's SRV target as `localhost.`.
- On the tablet, "show pairing code" rendered nothing at all.

One root cause, two call sites: **`net.Interfaces()` / `net.InterfaceAddrs()`
return nothing on Android 11+**, which withholds the netlink access they need.

- `discovery.localIPs()` fell back to advertising `127.0.0.1`.
- `pages.lanIPv4()` failed, so `advertisableHost` errored and the enrol-code
  handler answered **409** — which htmx does not swap, so the operator saw a
  blank panel rather than a reason.

The fix introduces `internal/lanip` as the single answer to "what address can
a peer dial me on": enumeration first, then a UDP *route probe* that sends no
packet (connecting a UDP socket only asks the kernel to pick a source
address). Both call sites now use it.

## Review

- **Offline-first is preserved (ADR-0003).** The probe's first target is the
  mDNS multicast group `224.0.0.251:5353` — link-local, needs no gateway, no
  DNS and no internet, which is the isolated shop LAN this product must pair
  two tills on. The public address is only a second chance for a host with no
  multicast route, is never sent to, and its failure is not an error. A test
  pins the ordering, so a later edit cannot quietly make internet access a
  precondition for pairing.
- **No loopback is ever published.** `IPv4s()` returns nil rather than a
  loopback placeholder, and the advertiser refuses to build a zone without an
  address (`ErrNoLANAddress`). This is deliberately a behaviour change:
  advertising an undialable address is worse than silence, because it
  produces a candidate that looks joinable and fails on the *other* device,
  which is exactly what made #1499 hard to read. `tick()` retries every 30s,
  so a till whose Wi-Fi associates late starts advertising by itself — the
  quiet case is logged at info, not warn, so it does not read as a fault.
- **SRV target no longer depends on the OS hostname.** It is now
  `<till-id>.local.`; on Android the hostname is literally `localhost`, which
  a third-party mDNS client resolves to its own loopback however correct the
  attached A record is. The till id is a UUID, so it is a valid DNS label.
- **The invisible-error half is fixed too** (#1455 class): the enrol-code
  handler now answers **200** with the localized `sync.error.no_lan_address`
  message. The status is the only thing that changed — it still refuses to
  mint an undialable code, still logs server-side, still leaks no raw error.
- **Security:** the probe opens and immediately closes a UDP socket to a
  fixed, non-secret target; nothing is transmitted, nothing is accepted
  inbound, no new listener, no new privilege. The advertised TXT record is
  unchanged (`v`, `name`, `id` — no secrets, still guarded by
  `TestAdvertiser_TXTRecordCarriesNoSecrets`). If anything the change reduces
  exposure: a till with no LAN address now stays off the air entirely.
- **Tests are real, and were verified to fail without the fix.** Reverting
  only the probe branch turns
  `TestIPv4s_FallsBackToRouteProbeWhenEnumerationIsDenied` and
  `TestProbe_ClosesTheSocketItOpens` red (confirmed by direct experiment, not
  assumed). The enumeration-denied state is driven through a seam, which is
  what makes this testable at all: `sync_api_test.go` previously documented
  this exact path as untestable "without mocking net.Interfaces()", and that
  coverage gap is now closed with a genuine test rather than a note.
- **Pre-existing failure, not introduced here:**
  `TestListenWithFallback_WildcardHostFallsBackToLoopback` fails on macOS and
  is green on Linux CI — already tracked as ut-docs#1413.

## Not fixed here, deliberately

- The desktop/Pi kiosk shell still spawns its POS on `127.0.0.1`
  (`cmd/unitill-desktop/desktop.go`), so a Pi under the kiosk shell is still
  unreachable as a main till — tracked on ut-docs#1097 with the working
  topology.
- Approve/deny still answer a bare `http.Error` when the manager PIN is
  missing (the same #1455 class), which is a separate, smaller fix.
