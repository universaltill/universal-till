# 2026-08-24 — Turkey: service-charge/cover line made structurally unreachable (ut-docs#962)

**Date:** 2026-08-24 · **Branch:** `fix/962-turkey-service-charge-ban` ·
**Card:** [universaltill/ut-docs#962](https://github.com/universaltill/ut-docs/issues/962)
(complexity:medium) · **Base:** `c6a2f2b`

## What shipped

Turkey's Fiyat Etiketi Yönetmeliği amendment (Resmî Gazete 33153, in force
**2026-01-30**, no transition period) bans any additional customer payment on a
food-and-beverage bill under the name *servis ücreti / masa ücreti / kuver ve
benzeri*. Enforcement is **per adisyon** — ₺3,973 per receipt under Law 6502
art. 77 — so a till that adds the line automatically manufactures one offence
per sale. Universal Till had exactly one global, merchant-settable
`store.service_charge_rate_pct` with no country input.

The fix makes the line **structurally unreachable** for a `store.country = TR`
shop, and deliberately does **not** block the sale — omitting an
illegal-to-charge component the till can simply drop, rather than refusing to
trade, is the only reading compatible with offline-first (ADR-0003).

- **`internal/pages/common/state.go`** — two new pure helpers:
  - `ServiceChargeForbidden(country string) bool` — the single predicate for
    "this jurisdiction bans the line outright", so the settings refusal and the
    engine backstop can never disagree about who is covered.
  - `EffectiveServiceChargeRateBP(st RuntimeState) int` — the configured rate,
    forced to 0 when the country is covered.
- **Every `pos.Config{ServiceChargeRateBasisPoints: …}` construction site in
  `internal/pages`** now takes the helper instead of the raw field:
  `init.go` ×3 (cashier engine, kiosk engine, applied-config refresh),
  `settings_page.go` ×2, `setup_page.go` ×1.
- **`internal/pages/pos_api.go`** — the tender handler's own server-side
  recompute of the tendered total takes the helper too. This is the real
  enforcement point: it is what decides `sales.service_charge_amount` and the
  total actually collected, independent of whatever the live engine config says.
- **`internal/pages/settings_page.go`, `POST /api/settings/upsert`** — a
  nonzero `store.service_charge_rate_pct` for a covered shop is refused with
  `400` and a localized explanation citing the regulation and the lawful
  alternative (price it into the menu). Zeroing it back out stays allowed.
- **Locales** — `settings.service_charge.tr_forbidden` (+
  `settings.service_charge.invalid_rate`, see Finding 3) in `en`/`tr`/`ar`/`fa`.
- **`web/ui/pages/settings.html`** — the All-settings card now renders a
  refused upsert's message to the operator (Finding 3).
- **`README.md`** — the service-charge feature bullet no longer claims an
  unconditional "till-set percentage automatically added to the sale total"
  (Finding 4).
- **Tests** — `state_test.go` (helper table incl. casing), `pos_api_test.go`
  (`TestTenderHandler_TurkeyNeverAppliesServiceCharge`), `settings_page_test.go`
  (upsert refusal, zero still allowed, country-switch config refresh, casing).

**Design note, confirmed against ADR-0060:** this is a **core-only** check. It
deliberately does **not** route through `charge.policy.ask`. ADR-0060 (accepted
same day, for the larger #961) names #962 in its own non-goals as *"a core-only
`store.country == "TR"` check"* precisely because there is no `ut-plugin-tax-tr`
to ask, and ADR-0050's boundary test puts an obligation that must hold with **no
plugin installed** in core. The code matches that framing — no hook, no plugin
surface, no new contract. `internal/pos` was verified to have gained **no**
country awareness: it still reads only `s.cfg.ServiceChargeRateBasisPoints`, and
all country knowledge stays at the `internal/pages` boundary. That is correct,
not merely convenient — `internal/pos` is the replay/sync engine, and teaching it
"TR means zero" would let a *replayed historical* sale silently lose a service
charge it legitimately carried before 2026-01-30. AC 3 ("existing sales are not
rewritten") holds for exactly that reason.

## What I verified beyond reading the diff

Independently re-run in a clean worktree, not taken on report.

### TDD re-verification (required, done for real)

Reverted `pos_api.go`'s single-line fix back to the raw field and re-ran the
card's own tender test:

```
=== RUN   TestTenderHandler_TurkeyNeverAppliesServiceCharge
    pos_api_test.go:623: expected service_charge_amount 0 for a TR shop
                         despite a configured 12.5% rate, got 13
--- FAIL: TestTenderHandler_TurkeyNeverAppliesServiceCharge (0.02s)
```

A real assertion mismatch on the persisted amount — not a compile error, not a
panic, not a skip. Restored:

```
=== RUN   TestTenderHandler_TurkeyNeverAppliesServiceCharge
--- PASS: TestTenderHandler_TurkeyNeverAppliesServiceCharge (0.02s)
```

Both tests I added for Findings 1 and 2 were likewise written first and observed
failing against the pre-fix tree with real assertion output
(`cashier engine rate after switching to TR = 1250, want 0`;
`nonzero rate for country "tr" = 204, want 400`), then passing after.

### Real driven run (not just tests)

Booted the auth till (`e2e/run-till-auth.sh`) and drove it with Playwright to
confirm the refusal actually reaches a human — the thing no Go handler test can
prove, and the thing Finding 3 turned out to be:

```
REFUSAL NOTICE SHOWN: "Turkish law bans a service charge, table fee or cover on
  the bill (Fiyat Etiketi Yönetmeliği, in force since 30 Jan 2026). Price it
  into your menu items instead."
TR NOTICE SHOWN: "Türk hukuku, adisyonda servis ücreti, masa ücreti veya kuver
  alınmasını yasaklar (Fiyat Etiketi Yönetmeliği, 30 Ocak 2026'dan beri
  yürürlükte). Bunun yerine bu tutarı menü fiyatlarına dahil edin."
INVALID NOTICE SHOWN: "Servis ücretini sıfır veya daha büyük bir yüzde olarak
  girin, örneğin 12,5."
```

Screenshot confirmed a dismissible red `pos-notice error` banner, and
`store.service_charge_rate_pct` still showing `0` in the settings table after the
rejected save.

### Full gate

`gofmt -l .` clean · `go build ./...` · `go vet ./...` · `go test ./...` (whole
module, all green) · all sixteen CI-blocking guards from `.github/workflows/ci.yml`'s
`build` job, plus the guard meta-tests (`guard-i18n_test.sh`,
`guard-i18n_toast_test.sh`, `guard-compliance-claims_test.sh`,
`guard-docs-shots_test.sh`, `guard-docs-shots-cross-check_test.sh`).

## Findings

### 1. Blocker — fixed. Switching country to TR left the live engines charging

`/api/settings/upsert` reflects `store.country` into runtime state, but its
engine-refresh switch only covered `KeyCurrency`, `KeyTaxInclusive` and
`KeyServiceChargeRate`. A GB shop with 12.5% configured that became a TR shop
therefore kept **both** engines (cashier and kiosk) configured at 1250bp until
the process restarted.

This is worse than it first looks. The tender path already recomputed the charge
as 0, so the shop would have had a **basket and customer-facing display quoting
an illegal service-charge line and an inflated total, while the recorded sale and
the money actually taken said something else** — a compliance exposure and a
till-disagrees-with-itself bug at once.

Fixed by adding `common.KeyCountry` to that switch. Leaving TR restores the
still-stored rate through the same path, so the suppression is a country-scoped
*suppression*, never a destructive erase — asserted in the test.

Not hypothetical for the other two country-write sites: `/api/settings/save`
(the currency card) and the setup wizard both already re-pushed config, so
`upsert` was the one hole.

### 2. Major — fixed. The TR match was case-sensitive, in the unsafe direction

The original check was `st.Country == "TR"`. The setup wizard persists uppercase,
but `/api/settings/upsert`, a restored backup and a hand-edited settings row can
all carry `"tr"`, `"Tr"` or padding — and this is explicitly billed as the
*fail-closed backstop for exactly those paths*, so an exact match undercut its
own stated purpose.

Fixed via `ServiceChargeForbidden` (trimmed, `strings.EqualFold`). I checked the
apparent inconsistency with `fiscal.RequiresHardGate`, which matches `"DE"`
strictly on purpose (its own test pins `{"de", false}` with the comment "no fuzzy
matching"). The asymmetry is correct and is now documented at both ends: a loose
match in `fiscal` would **block a sale**, so strictness is right there; a loose
match here can only **omit a line that is illegal to print**, so leniency is the
fail-closed direction here. `"TRX"` is asserted not to match, so the leniency does
not become a prefix match.

ADR-0060's non-goals section spells the check as `store.country == "TR"`; the
shipped predicate is a strict superset of that and contradicts nothing in the
ADR, which is describing *where the check lives*, not its string comparison.

### 3. Major — fixed. The localized refusal was never shown to anyone

The card's scope item 1 and AC 2 require the refusal to be **explained**, in
Turkish, with the lawful alternative. The handler returned exactly that text —
and the operator saw nothing at all.

Every upsert trigger in settings.html's All-settings card is `hx-swap="none"`,
and htmx discards a non-2xx body regardless. `app.js`'s ut-docs#916 force-swap
rescue only fires for a `text/html` body, and `http.Error` always answers
`text/plain`; its `htmx:responseError` fallback writes into `#pos-alert`, which
exists on the sale screen but not on `/settings`. So a carefully-worded
compliance explanation was being written to a response body nothing rendered.

Fixed with a small card-scoped `htmx:afterRequest` listener in settings.html —
the same event-delegation + `renderNotice()` pattern the barcode-symbologies card
already uses — that renders the server's (already localized) body as a persistent
`error` notice into the existing `#settings-upsert-msg` span. Guarded on
`status >= 400` and on the body **not** being `text/html`, so the 200 `text/html`
OOB elevation prompt is doubly excluded. No user-facing string is hardcoded in
the block: the text is the server's own `httpx.T` output, rendered verbatim.
Verified in a real browser, in both `en` and `tr` (output above).

This was pre-existing for the malformed-rate refusal too, and survivable there
(the operator can see their own typo). It is not survivable for a refusal whose
entire job is to explain a law.

### 4. Major — fixed. Two docs went stale

- **`web/help/img/**` / `guard-docs-shots.sh`**: the diff changed non-test
  `internal/pages/**.go` (incl. `settings_page.go`, which registers the
  screenshotted `/settings`), so this **CI-blocking** guard failed. Confirmed it
  passes on the base commit `c6a2f2b`, i.e. genuinely introduced here, not
  pre-existing. Ran `make docs-shots` and committed the regenerated
  manifest/screenshots; guard now green.
- **`README.md`**: the service-charge bullet claimed a "till-set percentage
  (`store.service_charge_rate_pct`) automatically added to the sale total" with
  no qualification — now false for a TR shop. Per this repo's standing README
  rule, added the TR carve-out inline with a link to the card, and stated
  explicitly that the sale still completes.

### 5. Minor — fixed. Newly-visible error string was English-only

Surfacing refused upserts (Finding 3) meant `http.Error(w, "invalid service
charge rate", …)` — a raw English literal — would now show through in every
locale. Localized it as `settings.service_charge.invalid_rate` across all four
locale files. (`guard-i18n.sh` had not flagged it; it was invisible before.)

### 6. Minor — accepted, no change. Turkish wording

Reviewed `tr.json` for legal accuracy rather than fluency alone. The string
correctly names the instrument (*Fiyat Etiketi Yönetmeliği*), the in-force date
(30 Ocak 2026), all three banned labels (*servis ücreti / masa ücreti / kuver*),
and the lawful alternative — and it **does not** overclaim: it says what the law
does, never that the till makes the shop compliant, so ADR-0040's outcome-claim
rule is respected (`guard-compliance-claims.sh` green). One change made: the bill
is now *"adisyonda"* rather than *"faturada"* — `adisyon` is the restaurant check
(and the unit the ₺3,973 penalty is assessed per), `fatura` is an invoice. `ar`
and `fa` read accurately and idiomatically; both are RTL locales and the string is
plain prose with no directional markup.

### 7. Accepted, no change. No manual topic went stale

Checked `web/help/**` in all four locales for any topic describing the
service-charge setting: **there is none**, and there is no shipped UI control for
it either — `store.service_charge_rate_pct` is reachable only through the generic
All-settings key/value editor (consistent with the ut-docs#244 review record,
which recorded the same finding). So no topic now reads as false or incomplete
for a TR shop, and there is nothing to update. `guard-help-topics.sh` passes: this
change adds no page route, so it needs no `routes:` claim. If a dedicated
service-charge settings field is ever built, its manual topic must carry the TR
carve-out from the start — noted here rather than pre-emptively documenting a
control that does not exist.

### 8. Accepted, no change. Other checks

- Every non-test site reading `ServiceChargeRateBasisPoints` that feeds a live
  computation was enumerated (`grep -rn` over `internal/`, `cmd/`, `e2e/`,
  `scripts/`) and goes through the helper. The remaining reads are the field
  declaration, `LoadState`, `SaveState` (persists the *configured* value — correct;
  the suppression must not erase the merchant's setting) and tests.
- Receipt/invoice/journal rendering and sync/replay read the **persisted
  `sales.service_charge_amount`**, not the rate, so they need no change and AC 3
  (history stays as recorded) holds by construction.
- Self-order kiosk: `KioskEngine` is configured from the same helper at all three
  sites; `guard-kiosk-engine.sh` green.
- No file writes in the diff, so the recurring `os.MkdirAll` / `paths.Data(...)`
  bug classes do not apply (verified by grepping the diff, not by assumption).
- No real client or shop name used as demo/seed/test data; no secret-shaped
  literals. Test fixtures are the existing `Demo Shop` / `ABC` / `mgrUser`.

## Deferred to a new card

- **`yüzde usulü` distribution records** (İş Kanunu 4857 art. 51) — required by
  the card's own AC 4 to be captured, not built. It did not exist; filed as
  **[ut-docs#965](https://github.com/universaltill/ut-docs/issues/965)**. It is a
  payroll/distribution ledger, not a bill line — reintroducing a bill line to feed
  it would recreate the exact offence this card removes — and it almost certainly
  belongs in a future `ut-plugin-tax-tr` rather than core, since nothing illegal
  happens if the feature is simply absent (ADR-0050's boundary test).
- **No new-card-worthy issues found in the diff itself.** Findings 1–5 were all
  in-scope and fixed here.

## Verdict

**Safe to merge.** The mechanism is right — enforce at the `internal/pages`
boundary, keep `internal/pos` country-blind so replay stays faithful, never block
a sale — and it matches ADR-0060's explicit framing of this card. The two defects
that mattered (a country switch leaving the live engines quoting the illegal line;
a compliance explanation that no operator could ever see) are fixed and covered by
tests that were observed failing first.
