# Code review: a second, unicast source for "never write to an office printer" (ut-docs#1607)

## What shipped

`internal/discovery/ipp.go` (new) plus wiring in `sweep.go` and `printers.go`.

ut-docs#1606 established the safety property this package lives by: the ESC/POS
status query (`10 04 01`) is print data to anything that does not implement it,
and the product owner's HP OfficeJet printed one character per page for every
probe it received. The rule that came out of it — read a device's own
declaration before writing to it — was enforced from exactly one source, the
mDNS `pdl=` TXT record.

That source is the one a shop LAN routinely breaks. `DiscoverPrinters`
deliberately continues when the browse fails (`printers.go:66-72`), because a
LAN with no usable multicast is precisely when the :9100 sweep earns its keep —
but with no advertisements the exclusion set is empty, so phase 2 writes to
every listener it found, inkjet included. Guest/VLAN'd Wi-Fi, an AP filtering
multicast, or a wired/wireless segment split all produce it.

The fix adds a second declaration channel that needs no multicast: an office
printer is an IPP printer, and answers `Get-Printer-Attributes` on :631 over
plain unicast TCP. That is IPP's read operation — the write-shaped one is
`Print-Job` (0x0002), which this code never sends. Before phase 2 writes to a
listener, `officePrinterOverIPP` asks :631 what document formats the device
accepts and excludes it if the answer names a page description language.

## Measured on the real hardware, not inferred

From a machine on the product owner's LAN, 2026-09-05:

| | :80 | :515 | :631 | :9100 | mDNS |
|---|---|---|---|---|---|
| thermal 192.168.1.111 | open | – | – | open | none |
| HP OfficeJet 192.168.1.245 | open | open | open | open | `_pdl-datastream`, `_printer`, `_ipp`, `_http` |

The HP's real IPP answer:

```
document-format-supported: application/vnd.hp-PCL, image/jpeg, image/urf,
                           image/pwg-raster, application/PCLm,
                           application/octet-stream
```

**This measurement changed the design.** The obvious rule — reuse
`nonESCPOSPDL`, which reads `application/octet-stream` in an mDNS `pdl=` list
as a positive raw-printing claim — is correct for mDNS (the HP's `pdl=` does
NOT contain octet-stream) and **exactly backwards for IPP** (its IPP list
does). Nearly every IPP printer advertises octet-stream as "send anything and I
will sniff it". Shipping the obvious rule would have let the one printer this
guard exists for straight through.

`namesPageDescriptionLanguage` therefore asks a POSITIVE question — does the
list name a language from a known set (PCL, PCLm, PostScript, PDF, URF,
PWG-raster, CUPS-raster, XPS, any `image/`)? The HP is caught four times over;
a receipt printer matches nothing. The negative form was tried and rejected
during review: it reads `text/plain`, `application/vnd.cups-raw` and vendor
raw types as page description languages, and those appear in real receipt
printers' lists.

## The regression that would be worse than the bug

Hiding the shop's only receipt printer is worse than a stray page — that is
what ut-docs#1606 was filed about. Three deliberate conservatisms:

- **:631 refused is not a declaration.** Silence never excludes, the same rule
  `nonESCPOSPDL` applies to an absent `pdl=`. The thermal printer has no IPP
  service, so the check ends at a refused connection.
- **An unambiguous mDNS declaration beats the IPP inference.**
  `DiscoverPrinters` builds a `trusted` set alongside `skip`: an address whose
  advertisement names a raw format AND no page description language bypasses
  the guard. Receipt printers with IPP-capable Ethernet interfaces exist, and
  their own advertisement is better evidence than an inference from open
  ports. "Unambiguous" is load-bearing — see F1 below.
- **An IPP printer that names no page description language is left alone.** A
  format list of raw plus `text/plain` is not a declaration against us.

The one place the guard is deliberately aggressive: something listening on :631
that will not complete an IPP exchange is excluded. A device running an
unidentifiable service on the IPP port is not a POS58/80 box, and the cost of
being wrong that way is a printer we do not offer, against paper in a shop.

## Verification

**On real hardware**, with the mDNS browse forced to fail (the ut-docs#1607
case), the till's own subnet:

```
officePrinterOverIPP(192.168.1.245) = true      <- HP, excluded, never written to
officePrinterOverIPP(192.168.1.111) = false     <- thermal printer, still probed
DiscoverPrinters (browse forced to fail): n=1
  OFFERED 192.168.1.111:9100
```

The HP printed nothing.

**Unit tests** (`ipp_test.go`, plus six in `printers_test.go`) were written
first and were red — the package did not compile — before `ipp.go` existed.
Every safety-critical one is mutation-checked, i.e. the implementation was
broken in the specific way the test exists to catch and the test was confirmed
to fail:

| Mutation | Test that catches it |
|---|---|
| guard removed from `probeListeners` | `..._ProtectsAnOfficePrinterWhenTheBrowseFails` |
| guard removed from `mergePrinterCandidates` | `..._BrowsedCandidateWithNoPDLIsGuarded` |
| `trusted` = any non-empty `pdl=` | `..._AmbiguousAdvertisementDoesNotExemptTheIPPGuard` |
| IPP status-code check dropped | `TestOfficePrinterOverIPP/200_OK_carrying_an_IPP_error_status` |
| classifier reverted to the negative form | `TestNamesPageDescriptionLanguage`, `TestDeclaresRawPrinting` |

The DiscoverPrinters-level tests assert on **bytes written to the office
printer's raw port**, not just on the returned list, so they fail for the
right reason.

Gate: `gofmt -l` clean, `go build ./...`, `go test ./...` (48 packages, exit 0),
`go test ./internal/discovery/ -race`, `golangci-lint run` 0 issues,
`guard-data-access`, `guard-i18n`, `guard-page-http-error` all pass.

## Independent review

A fresh-context reviewer (no shared history with the author) went over the
diff and reproduced its findings with tests against a scratch copy. It found
**three blocking defects**, all of them in the first draft of this change, and
all now fixed. They are worth recording because two of them re-created the
very bug the change exists to fix.

**F1 — the exemption let the target class of device straight through.**
`trusted` (the set that bypasses the IPP guard) was built from "whatever
`nonESCPOSPDL` did not reject", and `nonESCPOSPDL` returns false the moment
any entry contains `octet-stream`. But the browse is of
`_pdl-datastream._tcp` — the *raw datastream* service — where an office
printer listing `application/octet-stream` alongside its real languages is
entirely normal. Reproduced: `pdl=application/octet-stream,application/vnd.hp-PCL`
→ guard skipped → six bytes written to the HP's raw port. Fixed with
`declaresRawPrinting`, which requires a raw claim AND no page description
language; `TestDiscoverPrinters_AmbiguousAdvertisementDoesNotExemptTheIPPGuard`
pins it.

(The reviewer also noted the six bytes: `probeListeners` and
`mergePrinterCandidates` probe the same address independently, so a device
both sources see gets probed twice. That double-probe pre-dates this change
and is left alone here.)

**F2 — the classifier hid genuine receipt printers, and the code did not do
what its own comment said.** The comment promised "setting octet-stream
aside, does it name a page description language"; the code asked "is there
anything here that is not raw", which reads `text/plain` — in virtually every
real `document-format-supported` list — as a page description language.
Reproduced: a printer answering `DLE EOT` correctly with
`["application/octet-stream","text/plain"]` on :631 was hidden entirely.
Fixed by inverting to a positive match against a named list
(`namesPageDescriptionLanguage`). The cost is that an unrecognised vendor PDL
falls through and the device is probed — the same failure already accepted for
a device with no IPP service, and the direction that cannot make discovery
useless.

**F3 — HTTP 200 was trusted as a successful IPP response.** `ippFormats`
checked the HTTP status but never IPP's own status-code, so a printer
answering `200 OK` carrying `client-error-not-found` and no attributes parsed
as an empty format list — "names no page description language" — i.e.
permission to write. Not hypothetical: `/ipp/print` is not universal (Brother
uses `/ipp/port1`, CUPS `/printers/<name>`), so this is the normal reply to
asking the wrong path. Fixed two ways: `ippSuccessful` now checks the IPP
status-code and rejects a body too short to carry one, and `ippPaths` tries
`/ipp/print`, `/ipp/port1` and `/` before giving up — with the whole guard
bounded by `ippGuardBudget` so the extra attempts cannot stretch the scan.

**F4/F5 — two false passes that explain why the suite missed all of the
above.** The classifier table listed a PDL before `octet-stream` in every
case, so mutating the function to the wrong rule kept the suite green; and
deleting the entire guard from `mergePrinterCandidates` also kept it green,
because no test covered "browse OK, `pdl=` absent, office printer over IPP".
Both are now covered, and all four repairs were mutation-checked: reverting
each one individually fails a named test.

**F6 — the scan budget no longer covered the worst case.** With two probe
passes each carrying the new IPP step, the stacked worst case exceeded
`discoverPrintersTimeout`, and going over budget does not fail loudly — it
silently returns fewer printers, which is the "my printer isn't in the list"
bug this whole line of work exists to fix. `ippGuardBudget` now caps the guard
per device and the timeout is 20s, with the arithmetic written into the
comment.

**F7 — parser.** Delimiter tags 0x06-0x0F are reserved (RFC 8010 §3.5.1) and
were being read as value tags, truncating the list — the permissive
direction. One-character fix, with a test.

**F8/F9 — clean.** The reviewer fuzzed the parser (16.9M execs, no crash, no
timeout) and confirmed the connection deadline is set before every read, so
`http.ReadResponse` over the raw conn cannot hang past it. It noted the
`ippMaxResponse` comment overstated its role (it bounds allocation, not the
read); reworded.

## Found while verifying, filed not fixed

ut-docs#1608: phase 1 of the sweep intermittently misses an online receipt
printer on the **first** scan after its ARP entry expires — the 700ms
per-host dial budget has to cover ARP resolution while the sweep itself floods
the ARP queue with ~250 useless addresses. Reproduced: cold cache → the printer
is missing and `DiscoverPrinters` returns nothing; warm cache → found on three
runs out of three. A till that has just booted has a cold cache for every host,
which is exactly when "Find printers" gets tapped. Out of scope here; the fix
needs a decision about the scan-time budget.
