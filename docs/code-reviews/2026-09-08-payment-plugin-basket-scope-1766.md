# Payment plugins stopped receiving the whole basket — scoping the ÖKC authorize extras to the ÖKC leg (ut-docs#1766)

- **Date**: 2026-09-08
- **Branch / PR**: `fix/1766-scope-device-extras-to-okc`
- **Card**: ut-docs#1766 (`p1`, `security`)
- **Related**: ut-docs#1750 (the ÖKC merge + country gate this regression rode in on), ADR-0003 (offline-first), `reference/payment-provider-contract.md`
- **Reviewer**: independent pass, different model, fresh context, own isolated worktree, all gates re-run locally

## What shipped

`completeTender()` (`internal/pages/pos_api.go`) computed `deviceAuthorizePayloadExtras()` — every basket line with its name, quantity, unit price, tax rate and line discount, plus the sale-level discount, service charge, tax-inclusive flag, currency and total — and merged it into **every** payment plugin's `payment.<key>.authorize` payload, unconditionally, for every payment leg in every country. Those fields exist for exactly one plugin: Turkey's YN ÖKC (`tax-tr`), whose certified device prints the legal receipt itself and therefore needs the lines and VAT. Every other payment plugin — Stripe, SumUp, QR Pay, demo — only ever asked for `method`/`amount`/`reference`, which is also all `reference/payment-provider-contract.md` documents for that event. Least-privilege regression, silent: no interface change, no error, nothing on screen.

The fix is one `if`:

- `internal/fiscal/device.go` — new exported `MethodKeyOKC = "okc"`, documented as the tax-tr manifest's `entries[0].key`.
- `internal/pages/pos_api.go` — the extras merge is now gated on `p.MethodID == fiscal.MethodKeyOKC`. Everything else in `completeTender` is untouched.
- `internal/pages/pos_api_test.go` — two tests, both driving a real HTTP request through the mux and capturing what a subscribed plugin actually received: `TestTenderHandler_NonOKCPluginDoesNotReceiveBasketDetail` (a demo plugin on method `demopay` must not see `lines`/`sale_discount`/`service_charge`/`tax_inclusive`/`currency`/`total`, and must still see `method`/`amount`/`reference`), and `TestTenderHandler_OKCPluginReceivesBasketDetail` (method `okc` must still see `lines`).

## Independent review findings

**No blockers.** The diff's blast radius really is the one `if` — read against the surrounding 200 lines of `completeTender`, not just the hunk.

### Is `p.MethodID == fiscal.MethodKeyOKC` the right comparison? — yes, verified end to end

The concern worth chasing was whether a case or whitespace difference could silently stop a real Turkish till from getting its line detail. It cannot, and the chain is short enough to state completely:

1. `plugins.validatePaymentEntryKeys` (`internal/plugins/manifest.go:241`) **rejects at install time** any payment entry key with surrounding whitespace (`payment entry key %q has surrounding whitespace`). A `" okc "` entry can never exist in the database.
2. `PluginRepo.SyncPluginPaymentMethods` derives `payment_methods.id` **verbatim from the entry key** — the doc comment says so and the upsert does it (`id = entry key`).
3. The tender request's `method` becomes `pos.PaymentInput.MethodID` **with no normalization** — `pos_api.go:1066` and the hx-vals fallback at `:1094` both assign it raw; `POSRepo.EnsurePaymentMethod` (`internal/data/pos_repo.go:6137`) also neither trims nor case-folds.
4. `blockingPaymentEventWithResponse` (`refund_page.go:956`) routes with **Go string equality in the same direction**: `if e.EntryKey != method { continue }`.

So the new gate uses the identical comparison, on the identical value, as the code that decides whether the plugin is called at all. A hypothetical `"OKC"` would fail the gate — but it would equally fail the routing one line later, so the plugin would never be invoked in the first place. The gate can never be stricter than the dispatch it guards. This is the property that makes the fix safe, and it is why gating on `MethodID` rather than inventing a new lookup was the right call.

### Does the base payment-provider contract change for anyone? — no

`method`, `amount`, `reference` are built before the gate and are untouched; `plugin_id` is still stamped by `blockingPaymentEventWithResponse` for every method. That is exactly the four-field payload `reference/payment-provider-contract.md` line 41 documents. The fix moves the code **back onto** the documented contract rather than away from it.

The one field worth a second look is `currency`, which non-ÖKC plugins will now stop receiving. Assessed and closed rather than waved past: the extras first shipped 2026-09-03 (`5fbf17a`), the contract doc has never listed `currency` for `authorize`, and the only plugin in this repo that unmarshals it is `plugins/tax-tr/main.go`, which keeps it. A third-party plugin would have had to start depending, within five days, on an undocumented field nobody announced. *Deferred, one line:* `ut-plugin-payment-{stripe,sumup,qrpay,demo}` are not readable from this session, so "no external plugin reads `currency`/`total`/`lines` off `authorize`" is reasoned, not grepped.

### Was this the only leak of this shape? — yes, checked, not assumed

- `deviceAuthorizePayloadExtras` has exactly one production call site (`pos_api.go:193`); the others are its own definition and its unit test.
- `"authorize"` as an event suffix has exactly one call site in `internal/` — there is no second, kiosk-side authorize loop to fix. The self-order kiosk reaches this same `completeTender`, so it is covered by the same gate.
- The refund leg (`refund_page.go:797`) sends only `method`/`amount`/`currency`/`original_sale_id`/`original_receipt` — no line detail, so there is no mirror-image leak to fix there.
- Grepped every `for k, v := range` map-merge under `internal/`: none of the others builds a plugin payload.

### The inbound mirror image — already decided, deliberately not reopened

`pickDeviceEvidence` still parses a `fiscal_device` object off **any** plugin's authorize response, not just the ÖKC leg's, so a non-fiscal plugin could still have a receipt row recorded against a sale. This is not a new gap and not something to change here: ut-docs#1750 examined exactly this and recorded its reasoning in `fiscal_device_hook.go` — the receipt is evidence and keeping it is right; what mattered was the **gate flag** flip, which that card scoped behind `fiscalDeviceMarketActive`. Noted so the next reader does not re-litigate it.

### Nits — accepted, not fixed

| # | Note | Why not fixed |
|---|---|---|
| 1 | `deviceAuthorizePayloadExtras` still runs on **every** tender, allocating one map per basket line, and the result is discarded on the ~all tills with no ÖKC | The comment documents "computed once per tender" as a deliberate property, and the cost is a handful of small maps against a SQLite write and a blocking plugin round-trip on the same path. Trading a documented invariant for a micro-optimisation on the money-critical path is the wrong trade |
| 2 | The gate keys on the **entry key**, so a hypothetical *different* plugin declaring key `okc` (on a till without tax-tr) would receive basket detail. Gating on `fiscal.PluginIDTaxTR` — already a constant, and available as `e.PluginID` inside `blockingPaymentEventWithResponse` — would be strictly tighter | The entry key is what routes the call today; using it keeps the gate and the dispatch on one basis, which is the property that makes point 1 of the analysis above hold. Tightening to plugin id means threading the owning plugin into the payload build, a bigger change than a p1 leak fix should carry. Worth a backlog note, not a change here |
| 3 | Both new tests call `t.Fatalf` from inside the bus subscriber callback (only on an unmarshal failure) | The blocking publish is synchronous on the request goroutine — confirmed by a clean `-race` run — so this is correct today; it is a latent trap only if the bus ever dispatches asynchronously |

### Checked and clean

- **No file writes in the diff at all**, so neither of the two recurring bug classes this pipeline watches for applies: no handler needing `os.MkdirAll`, no cwd-relative path that should be `paths.Data(...)`.
- **No real client or shop name** in the seed data: `com.universaltill.payment-demo` / "Demo Pay", `com.universaltill.tax-tr` / "Turkiye fiscal device", `example.test`, `deadbeef` as a placeholder sha256. **No literal secrets.**
- The two remaining `"okc"` string literals under `internal/` (`fiscal/device.go:82`, `data/fiscal_device_repo.go:58`) are a **device *kind***, a different concept that merely shares the spelling. Correctly left alone — collapsing them into `MethodKeyOKC` would have coupled two unrelated vocabularies.
- Raw SQL in the new tests: `guard-data-access.sh` exempts `_test.go` by design (line 36), and the same seed shape already appears 30 times across `internal/pages/*_test.go`, including the neighbouring `TestTenderHandler_AppliesPluginReportedTipFromAuthorizeResponse` the new tests were modelled on.
- No user-visible string, screen or route changes, so no help topic, screenshot or README update is owed. `reference/payment-provider-contract.md` needs no correction either — the code now matches what it already says. (*Deferred, docs repo:* it documents neither the ÖKC-only additive fields nor that they are method-scoped. Worth one line there now that the scoping is a real rule rather than an accident.)

## Verified beyond the automated tests

**The TDD claim was re-proved personally, by mutation** — the fix was reverted in this worktree (merge put back to unconditional), the regression test run, then the fix restored:

```
# with the gate removed:
$ go test ./internal/pages/ -run 'TestTenderHandler_NonOKCPluginDoesNotReceiveBasketDetail|TestTenderHandler_OKCPluginReceivesBasketDetail' -v
--- FAIL: TestTenderHandler_NonOKCPluginDoesNotReceiveBasketDetail (0.08s)
    pos_api_test.go:605: demopay is not the fiscal-device plugin — its authorize payload must
    not carry "lines", got map[amount:120 currency:GBP lines:[map[line_discount:0 name:Apple
    qty:1 tax_rate_bp:2000 unit_price:100]] method:demopay plugin_id:com.universaltill.payment-demo
    reference: sale_discount:0 service_charge:0 tax_inclusive:false total:120]
--- PASS: TestTenderHandler_OKCPluginReceivesBasketDetail (0.07s)
FAIL

# fix restored:
--- PASS: TestTenderHandler_NonOKCPluginDoesNotReceiveBasketDetail (0.13s)
--- PASS: TestTenderHandler_OKCPluginReceivesBasketDetail (0.07s)
```

The failure output is the leak itself — the demo plugin holding an itemised basket, priced, with the VAT rate on each line. That is the bug, printed.

**Test quality, stated honestly.** `NonOKC…` is a genuine mutation-killing regression guard: it fails against the reintroduced bug, and it cannot pass vacuously — it asserts `received != nil` first, so a plugin that was never called fails rather than silently satisfying the negative checks. `OKCPluginReceivesBasketDetail` passes both with **and** without the fix, so it proves nothing about *this* bug; that is fine and it is not a tautology — it is the guard for the opposite failure, a future over-tightening (re-keying the gate, or dropping the extras entirely) that would break Turkey, and there was no coverage for that direction before.

**Gates, all re-run in this worktree, not taken on trust:**

| Command | Result |
|---|---|
| `gofmt -l .` | no output |
| `go build ./...` | clean |
| `go vet ./...` | clean |
| `go test ./...` | 51 packages `ok`, 0 FAIL |
| `go test ./internal/pages/ -run 'TestTenderHandler_NonOKC…\|TestTenderHandler_OKC…' -race` | PASS, no race |
| `golangci-lint run ./...` | `0 issues.` |
| `guard-data-access.sh` | ✓ |
| `guard-kiosk-engine.sh` | ✓ |
| `guard-plugin-menu-read.sh` | ✓ |
| `guard-page-http-error.sh` | ✓ |
| `guard-i18n.sh` | ✓ 1497 keys, all locales match |
| `guard-compliance-claims.sh` | ✓ 251 files |
| `guard-help-topics.sh` | ✓ |
| `guard-docs-shots.sh` | ✓ 26 topics × 4 locales fresh |
| `guard-migration-version-collision.sh`, `guard-price-history-sync.sh` | ✓ |

**Not verified, stated rather than implied:** no run against real ÖKC hardware or `scripts/okc-sim` — the ÖKC half of the behaviour is covered only by the new HTTP-level test, which is the right level for a payload-scoping change but is not a device round-trip.

## Verdict

**Safe to merge.** The fix is minimal, uses the same comparison basis as the dispatch it guards, restores the documented payment-provider contract for every non-ÖKC plugin, and is proved by a test that genuinely fails without it. Three nits accepted with reasons; two one-line follow-ups explicitly deferred (confirm no external `ut-plugin-payment-*` reads `currency`/`total`/`lines` off `authorize`; document the ÖKC-only additive fields, and their method scoping, in `reference/payment-provider-contract.md`).
