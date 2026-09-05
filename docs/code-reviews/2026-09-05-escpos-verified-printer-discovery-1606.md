# Code review — ESC/POS-verified printer discovery (ut-docs#1606)

**Change:** printer discovery now combines an mDNS browse with a bounded
`:9100` sweep of the till's own subnet, and offers only devices that answer an
ESC/POS status query. Devices that declare another page description language
in their own advertisement are excluded **without being written to**.

Reported by the product owner 2026-09-05 and measured against their real
hardware. Follow-on from #1605.

## The defect

The shop has two printers. Discovery got it exactly backwards:

| Device | Address | mDNS | :9100 | ESC/POS | Was discovered? |
|---|---|---|---|---|---|
| Thermal receipt printer | 192.168.1.111 | advertises nothing | open | **yes** | ✗ invisible |
| HP OfficeJet Pro 9025 | 192.168.1.245 | `_pdl-datastream`, `_printer`, `_ipp`, `_http` | open | **no** | ✓ the only result |

The operator was offered the one printer that cannot print a receipt, and the
one that can was invisible. The owner had to type the address in by hand.

Cheap network ESC/POS units — the Epson TM-T clones and generic POS58/80 boxes
a small shop actually buys — ship no Bonjour responder. **mDNS discovery
therefore cannot find them by construction**, and there was no non-mDNS
discovery path in the codebase.

## The serious mistake made while fixing it

The first implementation swept `:9100` and sent the ESC/POS status query
(`DLE EOT 1`) to **every listener it found**, then used the reply to classify
the device.

That made the product owner's HP OfficeJet **print one character per page**.

The reasoning error is worth stating plainly, because it is not obvious from
the spec: `DLE EOT` is a *real-time* command that an ESC/POS printer answers
without printing — which is true, and is why it looked like a safe probe. But
that property belongs to ESC/POS firmware. To a printer that does **not**
implement ESC/POS, those three bytes are not a command at all; they are job
data, and an office printer faithfully puts them on paper. The probe is only
safe on devices already known to be ESC/POS — which is precisely what it was
being used to determine. Circular, and it costs real paper in a real shop
every time a manager taps "Find printers".

**This was found by running against real hardware, not by any test.** Every
unit test passed throughout, because a stubbed connection cannot print.

### How it is prevented now

Two changes, both structural rather than advisory:

1. **Read the advertisement before writing a byte.** The HP publishes what it
   speaks in its own mDNS TXT record:

   ```
   pdl=application/vnd.hp-PCL,image/jpeg,image/urf,image/pwg-raster,application/PCLm
   ```

   `nonESCPOSPDL` treats a non-empty `pdl=` naming no raw-byte/ESC/POS entry
   as a positive declaration that the device cannot print our receipts. Those
   addresses go into a `skip` set that is built from advertisements alone and
   handed to the sweep, so they are never dialled in phase 2 and never written
   to. It is deliberately conservative in the safe direction: an **absent or
   empty** `pdl=` is not a declaration and must not be read as one, or the
   sweep would exclude the very thermal printers it exists to find.

2. **Split the sweep into two phases.** Phase 1 connects and closes
   immediately, writing nothing — every printer discards an empty job, so
   being *found* can never put anything on paper. Only phase 2 writes, and it
   runs solely against the short list phase 1 produced, minus `skip`.

`TestDiscoverPrinters_NeverWritesToADeviceThatDeclaresAnotherLanguage` asserts
zero bytes written to the HP. Disabling the exclusion makes it fail with
`wrote 10 04 01 10 04 01` — two writes, confirming the guard covers both the
sweep path and the mDNS-verification path.

## The second bug real hardware caught

The sweep initially shared one 700ms timeout between connect and read. Against
the real LAN it found **nothing at all** — not even the thermal printer it was
written to find:

```
SpeaksESCPOS(192.168.1.111:9100, 700ms) = false
SpeaksESCPOS(192.168.1.111:9100, 3s)    = true
raw read 1 bytes = 0x16  valid=true
```

The printer completes a TCP handshake in milliseconds but takes over a second
to answer `DLE EOT`. Cheap embedded hardware connects fast and responds
slowly. Now `escposDialTimeout` (700ms) and `escposReadTimeout` (3s) are
separate constants, with `TestSpeaksESCPOS_WaitsLongerThanTheDialBudget`
pinning the relationship and driving a deliberately slow stub — a stub that
answers instantly cannot catch this, which is why it reached real hardware.

## Points raised in review

**Sequencing vs. latency (found in review, fixed).** The exclusion set must
exist before anything is written, which naively means browse-then-sweep, end
to end — roughly doubling how long a manager waits. Resolved by running the
mDNS browse concurrently with sweep **phase 1**, which is safe precisely
because phase 1 writes nothing; only phase 2 waits for the browse. The handler
budget was re-derived from the real numbers and raised to 12s, with the
arithmetic recorded at the constant.

**Sequential probing was a latency bug waiting to happen (found in review,
fixed).** `mergePrinterCandidates` probed mDNS candidates one at a time. With
`maxCandidates` at 64 and a 3s read timeout, a noisy LAN could hold the
operator on a spinner for minutes. Now bounded-concurrent, like the sweep.

**A data race in the test helpers (found by `-race`, fixed).** The sweep dials
concurrently by design, but the test recorders appended to a slice and wrote a
map unsynchronised. It first appeared as an *intermittent* plain-test failure
— Go's concurrent-map-write panic — which is a far worse failure mode than a
consistent one. Both recorders are now mutex-guarded; verified with
`-race -count=50`.

**Scope of the sweep (security-first).** The till's own IPv4 `/24` and no
wider — `realLocalSubnetHosts` skips any mask outside `/24`–`/30`, so a `/16`
is never enumerated. Port 9100 only, pinned by
`TestSweepPrinters_OnlyTouchesPort9100`. The endpoint stays manager-gated, and
discovery still only *presents* candidates — the operator confirms and submits.

**Residual risk, stated rather than hidden.** A non-ESC/POS printer that
advertises *no* mDNS at all would still be probed in phase 2 and could print a
stray page. It cannot be excluded by a declaration it never makes, and
eliminating phase 2 entirely would mean never finding the thermal printers
this card exists to find. In practice office printers advertise (the HP
advertises on four service types); silent `:9100` devices are overwhelmingly
receipt printers. Worth revisiting if a shop reports it.

## Verification

- `TestDiscoverPrinters_OffersTheThermalPrinterAndNotTheInkjet` reproduces the
  owner's LAN and asserts the outcome they asked for: `.111` offered, `.245`
  not.
- Safety: `..._NeverWritesToADeviceThatDeclaresAnotherLanguage`,
  `TestSweepPrinters_DoesNotWriteToSkippedAddresses`, `TestListens_WritesNothing`.
- Classification: `TestNonESCPOSPDL` (the HP's verbatim advertised value),
  `TestPrinterCandidateFromEntry_CapturesPDL`,
  `TestValidESCPOSStatus_ChecksOnlyTheSpecFixedBits` (a printer out of paper
  is still a printer).
- Both gates mutation-tested: disabling either makes the corresponding tests
  fail with the real buggy output.
- `go test ./internal/...` — 40 packages, 0 failures; `-race -count=50` on the
  concurrent paths clean. `go vet`, `gofmt` clean.

**Not re-run against the owner's LAN.** After the HP incident the product
owner asked that nothing further be sent to it, so verification from that point
was stub-driven only. The real-hardware facts this change is built on — the
`0x16` status byte, the verbatim `pdl=` string, the 700ms/3s timing — were all
captured before that point and are pinned in the tests as constants.
