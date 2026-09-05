# Code review — printer discovery offers non-printers as printers (ut-docs#1605)

**Change:** `internal/discovery/scanOnce` now drops any mDNS answer that is
not published under the service type that was queried, before it reaches the
entry parser. Applies to `Browse` (tills) and `BrowsePrinters` alike.

**Reported from the shop floor** by the product owner on the TECLAST test till
(v0.12.0) and reproduced live over adb on 2026-09-05.

## The defect

Kitchen stations → "Find printers on the network" listed four devices. One was
a printer:

| Shown as | Port | Actually |
|---|---|---|
| `HP OfficeJet Pro 9020 series [79A4B2]` — 192.168.1.245 | 9100 | the genuine printer |
| `Printer` — 192.168.1.223 | 7000 | an AirPlay receiver |
| `Printer` — 192.168.1.93 | 55627 | an unrelated host, ephemeral port |
| `Printer` — 192.168.1.251 | 7000 | an AirPlay receiver |

`BrowsePrinters` queries the right service type (`_pdl-datastream._tcp`), but
nothing verified that an answer belonged to it.

hashicorp/mdns's receive loop assembles a `ServiceEntry` from the PTR/SRV/TXT/A
records in **any** response packet on the multicast group during the query
window, keyed by record name, and never compares them against `params.Service`.
mDNS is multicast, so that window also catches other hosts' browses — an Apple
TV answering some Mac's `_airplay._tcp` browse on :7000 arrives looking exactly
like a printer answering ours.

`printerCandidateFromEntry` then failed to reject it:

```go
if name == "" {
    if instance, _, ok := strings.Cut(e.Name, "."+PrinterServiceName); ok {
        name = instance
    }
}
```

The service name is consulted **only to derive a display name**. When the `Cut`
fails — i.e. precisely when the answer is *not* a printer — `ok` is discarded,
`name` stays empty, and execution falls through to build and return a valid
candidate anyway.

The empty name is what made it dangerous rather than merely untidy. Foreign
services carry no Bonjour `ty=` key, so every junk entry rendered under the
UI's generic localized "Printer" label, visually indistinguishable from the
real device, each with its own **"Use for receipt printer"** button. Tapping
one silently points the till at something that cannot print; receipts then stop
appearing with no error the shop can act on. The panel's own help text promises
it scans "AppSocket/JetDirect (raw socket, port 9100)", so the list also
contradicted the UI's stated contract.

"After a few searches it shows a lot" is the same cause: each scan window
catches whatever unrelated mDNS traffic happened during it, so junk accumulates
across repeated scans.

## Why the fix is at the scan layer, not in the parser

`scanOnce` is the layer that knows which service was asked for, so one check
covers tills, printers, and any device class added later, instead of each
parser having to remember.

Till discovery was never actually safe — it only looked safe. `candidateFromEntry`
requires an `id=` TXT key that no other service happens to publish, so foreign
answers were rejected by accident. That is a property of our own advertiser,
not a stated rule, and it would evaporate the moment a TXT convention changed.
`TestBrowse_DropsAnswersFromOtherServices` pins the real rule by feeding an
`_airplay._tcp` answer that *does* carry an `id=` key.

## Points raised in review

**Case sensitivity (found in review, fixed).** The first version used
`strings.Contains(e.Name, "."+serviceName+".")`. DNS labels are
case-insensitive (RFC 6762 §16, RFC 4343) and a responder may echo back any
case it likes, so a printer advertising `._PDL-DataStream._TCP.` would have
been dropped — a real, working printer missing from the operator's list, the
same harm as the original bug in the opposite direction. Now lower-cased on
both sides, covered by `TestBrowsePrinters_MatchesServiceNameCaseInsensitively`.

**Not a port check.** The service type is the filter, not the port number. A
`_pdl-datastream._tcp` responder may legitimately advertise a port other than
9100, and `print.TransportForAddress` already speaks an explicit `host:port`.
`TestBrowsePrinters_KeepsPrinterOnNonStandardPort` guards against anyone
tightening this into a `== 9100` test later.

**Dot-bounded match.** Matching `"."+serviceName+"."` rather than a bare
substring means a longer instance name cannot satisfy the check incidentally.

**Four pre-existing test fixtures were changed — deliberately, and this is the
one change worth scrutinising.** `TestBrowse_ReturnsCollectedCandidatesDespite
ALateQueryError`, `..._RetriesIPv4OnlyWhenTheFullQueryFailsOutright`,
`..._SkipsV4V6AttemptWhenHostHasNoIPv6Support` and the late-answer test built
`mdns.ServiceEntry` values with **no `Name` field at all**, because the name was
irrelevant to the retry semantics they assert. The new filter correctly rejects
a nameless entry, so they failed. They were given the fully-qualified instance
name a real mDNS answer always carries. No assertion was relaxed and no
behaviour under test changed — the fixtures were incomplete, and the tests
still assert exactly what they did before.

## Verification

- New: `TestBrowsePrinters_DropsAnswersFromOtherServices` drives the full
  `BrowsePrinters` path with the exact mixed batch observed on the LAN (real
  HP + AirPlay on :7000 + unrelated host on :55627) and asserts only the
  printer survives. Confirmed failing before the fix, reproducing the four-row
  screen verbatim.
- New: `TestBrowse_DropsAnswersFromOtherServices`,
  `TestBrowsePrinters_KeepsPrinterOnNonStandardPort`,
  `TestBrowsePrinters_MatchesServiceNameCaseInsensitively`.
- `go test ./internal/...` — 39 packages, 0 failures. `go vet`, `gofmt` clean.

## Out of scope, filed separately

The reporter believed the printer was at `192.168.1.11`; it is at
`192.168.1.245` (MAC `04:0e:3c:79:a4:b3`, matching the `mac=` field in the
printer's own mDNS TXT record — `.11` has an incomplete ARP entry). Discovery
had it right and a hand-typed address would have been the stale one. That an
operator can hold a confidently wrong address for their own printer is the
argument for **ut-docs#1525** (re-discover LAN devices when their address moves
on a DHCP lease change); no change made here.
