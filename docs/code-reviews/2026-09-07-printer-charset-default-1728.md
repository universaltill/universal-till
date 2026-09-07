# ut-docs#1728 — receipts print a correct currency symbol by default

Date: 2026-09-07
Branch: `fix/1728-printer-charset-default`
Reviewer: independent subagent, fresh context (Opus) — `complexity:medium`
per the scrum-master skill's model routing.

## Report

Product owner, 2026-09-07: "still in the receipt the euro character is wrong."

A regression report against **ut-docs#1243**, closed Done on 2026-08-29 for
the same symptom (`âÎ¬2.50` instead of `€2.50`).

## Root cause

#1243's work was correct and is still in the tree: `encodeText`'s `cp858`
branch transcodes `€`→`0xD5` / `£`→`0x9C`, `codepageSelectCmd` emits
`ESC t 19`, and Settings → Printer → Characters offers it. But it shipped
**opt-in**, with the resolved default left at `"utf8"`
(`internal/pages/print_api.go`), whose branch is a raw pass-through that
sends `€` as `E2 82 AC` and selects no code page at all. Fixing the
capability changed nothing for any till nobody reconfigured by hand.

A second trap made a read-time default alone insufficient:
`POST /api/settings/printer` writes all seven printer keys on every save and
defaults an absent charset field to `"utf8"`, so any shop that ever opened
that page to enter a printer address carries an explicit `utf8` row.

## Change

- `internal/print/charset.go` — `DefaultCharset(currency, locale)`, returning
  `cp858` only for EUR/GBP **and** a language CP858 actually covers.
- `internal/pages/print_api.go` — an unset charset resolves through it,
  lazily (Go evaluates arguments eagerly, so passing it as `get`'s default
  would fire two extra settings reads on every call).
- `internal/settings/printer_charset.go` — `AdoptDefaultPrinterCharset`, a
  one-time boot pass moving a stored `utf8` onto the resolved default.
- `internal/app/app.go` — calls it at boot, and audits the change.

## Independent review: NOT SAFE TO MERGE, two blockers — both fixed

**Blocker 1 — the one-time pass burned its only chance at first boot.** The
draft deferred on `store.currency == ""`. Verified the review's claim
directly: `001_init.sql:1123` seeds `('store.currency', 'GBP')`, and
`settings.SaveRuntimeConfig` writes `store.locale` from the config default
`"en-US"` immediately before the pass runs. So the currency is never empty
and the guard could never fire — the marker would be written at first boot on
placeholder data. Concrete failure: a shop sets up as US/USD, configures its
printer (storing `utf8`), later switches country to Germany; marker already
spent, stored `utf8` blocks the read-time default, till prints `âÎ¬2.50`
forever. **Fixed** by gating on `setup.completed`, which the setup wizard
already writes (`internal/pages/setup_page.go:631`), and which the test now
exercises against the real seeded first-boot state instead of a manufactured
empty currency.

**Blocker 2 — CP858 as a default silently degraded shops that were fine.**
The `cp858` branch mapped anything unmappable to `?`, and its own comment
justified that as "acceptable for an **opt-in** option". This change removes
that premise. Verified against `charmap.CodePage858`: `œ Œ – — “ ” „ ’ … •
ĳ ŀ` are all unencodable. So a German or UK shop — GBP is also switched —
whose catalog came out of Excel, or whose footer reads `Vielen Dank – bis
bald`, would have gone from a perfect UTF-8 receipt to one full of `?`.
**Fixed** by folding before the fallback: an explicit typographic map, then
NFKD with combining marks stripped, then `?` as a genuine last resort. Note
`œ`/`Œ` needed an explicit row — they are French *letters* with no Unicode
decomposition, unlike the `ﬁ` ligature, so NFKD alone left them at `?`. The
`?` path is still tested and still reachable.

**Finding 3 — tests that could not fail.** The deferral test manufactured a
state the schema cannot produce, and `TestPrinterConfig_Defaults` pinned
`utf8` only because its harness never sets a store identity. Both corrected;
the latter now says what it actually proves and points at the test that
covers the real case.

**Finding 4 — the boot wiring had no coverage at all**: deleting the
`app.go` call left the whole suite green, and that call is the half of the
fix that repairs tills already in the field. Added
`TestRun_AdoptsPrinterCharsetDefaultOnBoot`, which boots the real `Run`
against a seeded German EUR till and asserts the stored value moved.
**Verified by hand that it fails when the call is removed** (`printer.charset
after boot = "utf8", want cp858`) and passes when restored.

**Finding 5 — silent settings mutation.** Every other write to
`printer.charset` is audited; a boot-time rewrite was not, which is a
traceability gap under DSFinV-K/TSE. Fixed: the pass now returns old/new and
`app.go` writes a `printer_settings_changed` audit row with the `"system"`
actor, matching `provision.go`'s existing boot-time precedent.

**Finding 6 — `fr` was enumerated as CP858-safe and is not** (`œ`).
Fixed by the fold, and locked down by
`TestCP858Languages_EverydayTextSurvivesWithoutDegrading`, which asserts real
shop text per enumerated language does not degrade — the check whose absence
let the claim be wrong. The review's other checks came back clean: `ı ş İ Ş
ğ` are genuinely absent (so excluding Turkish is right), `€`/`£` are present,
and `el`/`mt`/`hr`/`sl`/`sk`/`et`/`lv`/`lt` are correctly excluded.

**Finding 7 — partial coverage.** 8 of 20 eurozone countries plus Turkey
cannot use CP858 at all and still print a broken symbol. Deliberately out of
scope — switching them would cost them their own alphabet — and filed as
**ut-docs#1733** (Backlog) rather than left implicit.

**Finding 8 — stale operator docs.** `web/help/{en,ar,fa,tr}/printing.md`
still told the operator to set Characters by hand. Rewritten in all four
locales to describe the automatic behaviour and when an override is still
wanted. The review also noted `RenderText` (the receipt-designer preview)
applies no charset, so the preview can differ from paper — pre-existing,
recorded, not changed here.

**Finding 9 — `internal/settings` now imports `internal/print`.** No cycle
today; recorded so a future `print` → `settings` read is recognised as one.

Positive finding worth keeping: `printer.charset.adopted` falls under the
`printer.` per-till prefix in `sync_admin_repo.go`, so it is already excluded
from admin sync and cannot leak a satellite's marker onto another till.

## Verification

- `go test ./internal/print/ ./internal/settings/ ./internal/app/` — pass.
- `go build ./...`, `go vet`, `gofmt` — clean.
- The reviewer confirmed no consumer of `Charset` bypasses `printerConfig`
  (receipts, kitchen tickets, EOD, invoices, labels, designer all route
  through it), so the byte stream really does change for the reported till.
- **Verified on the real thermal printer (192.168.1.111:9100), on paper.**
  A slip was sent through the real `encodeText` path carrying the same five
  lines twice — once as the `utf8` pass-through that ships today, once as the
  `cp858` default this change introduces — and the product owner photographed
  the result. This is the check #1243 lacked, and the only kind that can
  settle an encoding bug.

  | line | BEFORE (utf8, today) | AFTER (cp858 default) |
  |---|---|---|
  | `TOTAL EUR 2.50 = €2.50` | `âI¬2.50` | `€2.50` |
  | `CHANGE GBP 1.20 = £1.20` | `Â£1.20` | `£1.20` |
  | `Vielen Dank – bis bald` | `Vielen Dank â€ bis bald` | `Vielen Dank - bis bald` |
  | `Chef’s “Tageskarte”` | `Chefâ€s â€œTageskarteâ€` | `Chef's "Tageskarte"` |
  | `Bœuf … • Menü N° 3` | `BÅ uf â€¦ â€¢ MenÃ¼ NÂ° 3` | `Boeuf ... * Menü N° 3` |

  Both currency symbols come out right, and the fold behaves as designed:
  `œ`→`oe`, en-dash→`-`, curly quotes→`"`, `…`→`...`, `•`→`*`, while `ü` and
  `°` print natively because CP858 carries them. The BEFORE column is the
  product owner's original report reproduced exactly, which also confirms the
  diagnosis rather than just the fix.

  Incidental observation from the probe, NOT a defect in the product: the
  `ascii` branch maps `\n` to `?` (any byte outside 0x20-0x7e that is not a
  tab), which showed up in the probe's header because the probe embedded
  newlines inside an encoded string. `Render` never does that — it writes
  each line's bytes and then a raw `\n` — so no receipt path is affected.
  Already documented in `encodeText`'s own comment as a known difference
  between the `ascii` and `cp858` arms.
- Pre-existing unrelated local failure `TestImport_ConcurrentDirectCommitsOfSameFileRejectSecond`
  (ut-docs#1725) is untouched.
