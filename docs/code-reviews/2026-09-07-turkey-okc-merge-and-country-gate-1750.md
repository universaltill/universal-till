# ut-docs#1750 — PR #750 (Turkey YN ÖKC) brought up to main, and its two Germany regressions closed

Date: 2026-09-07
Branch: `claude/universal-tilt-project-k6tmpw` (PR universal-till#750)
Reviews: two independent subagents, fresh context, Opus (`complexity:hard`).

## Why this record exists

PR #750 was opened 2026-09-03 by another developer, sat 4 days, had **no
tracking card and no `docs/code-reviews/` record**, and drifted badly against
`main`. The product owner asked for it to be unblocked and merged. Domain
ownership of the Turkey track stays with the track owner; this covers making
the branch mergeable and safe, not re-deciding its compliance design.

## Part 1 — the merge

Five problems surfaced, three of them real defects rather than conflicts:

1. **It would have made every existing till refuse to boot.** The branch added
   `fiscal_device_receipts` by editing `internal/db/migrations/001_init.sql`.
   CLAUDE.md permits editing the baseline pre-revenue (ADR-0074), but
   permission is not the constraint — the mechanism is: `internal/db` compares
   each applied migration's checksum on every boot and `idempotentRerunVersions`
   is **empty**, so a till that already ran 001 hits *"a migration file was
   renamed or edited after being applied; delete the data directory and start
   again"*. Every install, including the test tablet and Pi with their real
   catalogs, would have had to be wiped. Moved to
   `010_fiscal_device_receipts.sql`.
2. **`fiscal.KeyTSEConfigured` no longer exists.** ADR-0081 / migration 009
   renamed the fiscal settings keys. The branch's new `fiscal_device_hook.go`
   and `fiscal_device_page.go` still called it, so the merge did not compile —
   the lucky outcome, since that key is what the TR hard gate reads.
3. **The new table was classified in neither `adminTables` nor
   `nonAdminTables`**; `TestSchemaTablesAreClassified` catches it. Classified
   per-till, mirroring `fiscal_tse_signatures` (also keyed 1:1 on `sale_id`).
4. `UpsertPluginSettingScoped` gained `declaredSecret bool`; `false` at the
   three branch call sites (driver/host/port/maker — no secrets).
5. Comment conflicts in `internal/fiscal/fiscal.go` and
   `internal/pages/{pos_api,print_api}.go`, resolved keeping both sides' code;
   all four `web/locales/*.json` merged keeping `main`'s newest keys and the
   branch's 40, with no reordering.

Five of the branch's own tests then failed against the real migrated schema:
a fixture hand-rolled a duplicate of the table (redundant now that
`openPagesTestDB` runs migrations — removed rather than made conditional);
`plugins.entrypoint` is NOT NULL; `plugins` needs its `plugin_catalog` row
first (the shape `seedTaxDeCatalogRow` already uses); `audit_log.actor_id`
FKs to `users(id)`; and `openAtPreRenameSchema` assumed 009 was the last
migration, which any 010 breaks.

**Not at risk:** the Android conflicts the 2026-09-06 sweep warned about
(`MainActivity.kt` vs the ut-docs#1639/#1647 kiosk-pin and off-origin-link
security fixes) are gone — the branch no longer differs from `main` under
`android/` at all.

## Part 2 — what the first review found

Verdict **NOT SAFE TO MERGE**, seven blockers. Two were **Germany**
regressions, not Turkey ones, and are fixed here:

- **`/api/fiscal-device/confirm` was a one-click lift of the German hard
  gate.** `registerFiscalDeviceTR` is registered on every till (`init.go:454`,
  no country check) and the handler's only guard was `requireManager` before
  setting `fiscal.KeySigningDeviceConfigured = "true"` — since ADR-0081 the
  same key `fiscal.EvaluateGate` reads for Germany. It lifted
  `BlockedNeverConfigured`, the one state ADR-0048 Decision 2.2 says has no
  override path, after which every sale completes unsigned. The German route
  to that flag costs a real TSE credential written to the credential store and
  read back off disk (`setup_tse.go:381-462`). `unpair` mirrored it, setting
  the key false and hard-blocking a German shop's checkout.
- **A simulator receipt satisfied Germany's TSE flag.**
  `recordFiscalDeviceEvidence` flipped that key from any plugin's evidence
  with no country check and no check that the answering plugin was `tax-tr`.
  `DeviceEvidence.Valid()` requires only a non-empty `receipt_no` — no
  signature, no attestation — and `plugins/tax-tr/plugin.json`'s defaults
  (`bridge`, `127.0.0.1:4711`) are byte-identical to `scripts/okc-sim`'s,
  which is `go run`-able with no flags. It also fired in shadow mode (where
  `EvaluateGate` returns `Allowed` outright) and on refunds.

**One finding corrected.** The review read the branch as reverting
ut-docs#1060, deleting `window_mode_status.html`, its route, its regression
test and its review record. It does not: #1060 landed on `main` *after* the
merge that had been taken, so the branch was stale, not reverting. Verified
with `git merge-base --is-ancestor`, and a fresh merge of `main` restored all
of it.

## Part 3 — the fix

One helper, three call sites:

```go
func fiscalDeviceMarketActive(ctx context.Context, d *common.Deps) bool {
	return d.CurrentState().Country == "TR" && fiscalDevicePluginActive(ctx, d)
}
```

on `confirm`, `unpair`, and the flag-flip inside `recordFiscalDeviceEvidence`
— the same pair `menu_page.go:170` already gates the tile on. Non-TR gets 404
rather than 403, so the endpoint is not advertised to a till that must never
reach it.

Two deliberate scope decisions:

- **The GET page stays reachable on any country.** The docs-shots screenshot
  harness renders it — the same constraint `fiscalRegisterPluginActive`
  documents for the German page. Only the paths that MUTATE the gate flag are
  gated.
- **Evidence is still persisted for any shop.** This narrows what the evidence
  is allowed to *prove*, it does not throw the evidence away.

Each new test fails against the unfixed code with the vulnerability as its
message: `confirm must be refused on a DE till, got 303`; `a DE till's
configured flag was cleared by the Turkish device page (now "false")`; `a
German till's TSE gate flag was satisfied by fiscal-device evidence`. The gate
is pinned from both sides — `..._AllowedForTurkeyWithPlugin` and
`..._RefusedForTurkeyWithoutPlugin`, the latter so a TR shop in shadow mode
beside its existing register still cannot declare a device confirmed.

## Explicitly NOT fixed — tracked, not absorbed

Merging this does not resolve these; they stay with the Turkey track and its
pilot (ut-docs#1157):

1. The TR gate is a **one-time posture flag, not a per-sale receipt
   requirement**. Once confirmed every tender is `Allowed`, cash included — a
   cashier tapping Nakit completes a sale with no *mali fiş*, no marker and no
   receipt notice, and at a GİB audit the shop cannot enumerate which sales
   lack one. Germany has `declareUnsignedFiscalSale`; Turkey has no analogue.
2. **An `{"ok":true}` with an empty `receipt_no` still commits the sale** —
   `roundTripOn` treats any ok as success, `BridgeDriver.Sale` never checks
   the receipt number, and `completeTender` carries on after core rejects the
   evidence. This falsifies `device.go:15-20`'s "a sale cannot complete
   without it".
3. **Retry after a timeout can double-charge and print two *mali fiş***: the
   device idempotency key is `ev.ID`, a fresh UUID per publish (`ipc.go:373`),
   against a 10s `netTimeout` shorter than a chip-and-PIN — so
   `protocol.go:32-34`'s "a retried authorize never prints twice" is never
   true here.
4. **The kiosk actor breaks the audit marker**: `audit_log.actor_id` FKs to
   `users(id)` and self-order passes the literal `"kiosk"`, so the flag flips
   with the marker silently dropped. TR-gating bounds the blast radius; the
   ordering (flag first, audit best-effort after) is still wrong for a
   compliance surface.
5. **`total` sent to the device is gross tender, not net of change**, so an
   overtender prints a larger fiş than the sale booked — the German path
   deliberately subtracts `ChangeGiven`. No amount is stored with the evidence,
   so nothing can reconcile the two afterwards.
6. The whole basket now reaches **every** payment plugin on every sale in every
   country (`pos_api.go:180-191`) — item names, prices, tax rates to Stripe,
   SumUp and any marketplace plugin. Worth narrowing under `security-first`.

## Verification

- Full Go suite green (0 failing packages); `gofmt`, `go vet` clean.
- Seven CI guards run locally: data-access, page-http-error, i18n,
  kiosk-engine, help-topics, compliance-claims, migration-version-collision.
- `guard-docs-shots` green after `make docs-shots` (26 topics × 4 locales).

## What no reviewer can determine without the hardware

All four maker drivers are `ErrDriverNotImplemented` stubs — `drivers.go:36-39`
records that the GMP-3 v5.0 spec was unreachable — so `bridge` speaks a
protocol we invented and every real deployment needs a bridge process nobody
has written. Whether a real YN ÖKC expects kuruş or a decimal lira string is
untested, and a units error there is a 100× receipt. Real card-present latency
decides how bad finding 3 is. Refund/*iade fişi* requirements, Z-close
boundaries and e-Arşiv paths are all unverified.

## Part 4 — two further review rounds on the fix itself

The country gate was reviewed twice more. Both rounds returned NOT SAFE TO
MERGE, and both **reproduced a working bypass against the then-current code**
rather than reasoning about one. Recording that, because the shape of the
mistake is the reusable part.

### Round 2 — the gate's own predicate was weaker than the flag it protected

`fiscalDeviceMarketActive` reads `store.country`. That is manager-writable
through `POST /api/settings/upsert`, while
`fiscal.KeySigningDeviceConfigured` is owner-only (`fiscal_tse_override`)
everywhere else in the codebase — and nothing reset fiscal state when the
country changed. So:

```
install tax-tr -> country=TR -> POST /api/fiscal-device/confirm -> country=DE
=> country="DE" signing_device_configured="true" gate=Allowed
```

Same manager role, same end state as the original regression, two extra
clicks. The fix had relocated the trust boundary onto a setting weaker than
the thing it was protecting.

### Round 3 — the invariant was in a handler, and the key has four writers

The round-2 fix cleared the flags on a country change **inside
`/api/settings/upsert`**. `store.country` is also written by
`POST /api/settings/save` (manager-level, accepts a `country` field even
though no shipped form posts one), by the cloud `set_setting` directive, and
by the setup wizard. The reviewer drove the same attack through
`/api/settings/save`, with **no owner and no manual confirm at any point** —
an ordinary cashier sale against the bundled simulator supplied the flag via
the auto-confirm hook, and the manager only had to relabel the shop twice.

Round 2 also **introduced a regression**: clearing on any country change let
a manager wipe a genuine, provisioned German TSE posture by bouncing the
country, hard-blocking checkout — precisely the capability that had just been
taken away from `/api/fiscal-device/unpair`.

### What the fix is now

`internal/pages/fiscal_country_change.go`, called by all three post-setup
writers (`/api/settings/upsert`, `/api/settings/save`, the cloud
`SetSetting` hook). The setup wizard is deliberately not a caller — it only
runs while `NeedsFirstBoot`, where there is no posture to protect.

- `requireFiscalAuthorityForCountryChange` — while a signing device IS
  confirmed, moving the country needs the same owner-only authority as
  writing the flag directly. This closes the round-trip (the attacker cannot
  make the second move) and fixes round 2's own regression in one stroke.
  Fails closed on an unreadable flag.
- `clearFiscalStateForCountryChange` — when the country really changes the
  posture does not travel with it, in both directions. Called **before** the
  country is persisted (round 3 found a fail-open ordering where a failed
  clear left the country moved with the flag still set), and audited on the
  posture key, since ADR-0048's fiscal-toggle audit only fires when the
  written key IS the fiscal key.

Also from round 3: the confirm/unpair actions are hidden unless the market
gate passes (the shipped English manual screenshot had been showing a
plugin-less till a green "Confirm device" button that would 404), a
`fiscaldevice.actions.unavailable` string in all four locales, the menu tile's
country test normalized to match the gate's, and a **positive** render
assertion restored — after the actions were hidden, every assertion about the
confirm button was negative, so a change hiding pairing from every legitimate
Turkish shop would have passed the suite.

### The structural point, not attempted here

All of this exists because **ADR-0081 merged Germany's and Turkey's fiscal
posture onto one settings key**, which is what made `store.country` — ordinary
shop config — load-bearing for a compliance gate. Both reviewers independently
recommended giving Turkey its own key as the root-cause fix. That supersedes an
accepted ADR and is a decision, not a merge repair, so it is left for the
Turkey track rather than slipped into this branch.

### Verified

Full Go suite green (0 failing packages); `gofmt`, `go vet` clean; guards
i18n, data-access, page-http-error, help-topics, compliance-claims,
migration-version-collision, docs-shots all pass. Every new test was confirmed
to fail against the pre-fix code by the reviewers, not only by me.
