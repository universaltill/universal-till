# ut-plugin-tax-tr — Türkiye fiscal device (YN ÖKC) payment plugin

Turkey does not let a software till be the legal point of sale: every retail
sale must be documented by a GİB-approved **Yeni Nesil Ödeme Kaydedici Cihaz
(YN ÖKC)** that takes the payment and prints the fiscal receipt (mali fiş)
itself — `docs/arch/turkey-fiscal-compliance.md`, `turkey-launch-playbook.md`.

This plugin makes the device a **tender method** of the till: the cashier
taps **Yazarkasa (ÖKC)**, the till hands the basket to the device, the device
takes cash or card on its own reader and prints the legal receipt, and the
till records the device's receipt number, Z number and serial on the sale
(`fiscal_device_receipts`) and prints them on its own receipt copy.

It lives in this repository (not its own repo yet) so it can be built and
tested against the till in CI while no certified device exists; it moves to
`universaltill/ut-plugin-tax-tr` when it is first published to the
marketplace (ADR-0009 naming).

## Why a payment plugin and not `fiscal.sign.ask`

Germany's TSE signs a receipt the till prints, *after* payment, and the sale
may proceed unsigned and declared (ADR-0044). A Turkish ÖKC takes the money
and prints the receipt, so it must be reached *at* tender and a sale cannot
complete without it. That is exactly the blocking
`payment.<key>.authorize` seam (ut-docs `reference/payment-provider-contract.md`):
a non-zero exit refuses the tender and **no sale row is created** — fail
closed by construction, no override path. Core's `fiscal.RequiresHardGate("TR")`
still refuses any TR sale as system of record until the device is confirmed
(`fiscal.tse_configured`), which the till flips the first time the device
answers (see `/fiscal-device`).

## Contract (v0, additive to the payment-provider contract)

**Authorize payload** — the standard `method, amount, reference, plugin_id`
plus, since this plugin: `currency`, `total`, `tax_inclusive`,
`sale_discount`, `service_charge`, `lines[]` (`name, qty, unit_price,
tax_rate_bp, line_discount`). Amounts are integer minor units (kuruş).
Existing payment plugins ignore the extra fields.

**Answer** on stdout, exit 0:

```json
{"status":"approved","fiscal_device":{"kind":"okc","maker":"beko","serial":"AV0001234",
 "receipt_no":"0000042","receipt_kind":"mali_fis","z_no":7,"issued_at":"2026-09-03T10:12:00+03:00"}}
```

Core persists `fiscal_device` verbatim (`data.FiscalDeviceReceipt`); evidence
without `receipt_no` is ignored. `receipt_kind` is `mali_fis` for a sale,
`iade_fisi` for a refund, `bilgi_fisi` when the device printed an
information slip instead (invoice-documented sale).

**Rules the plugin enforces:** the ÖKC method must take the whole sale
(`amount == total`) — the device prints one fiscal receipt per sale, so a
split tender across the device and another method is refused; a device that
declines, times out or is unreachable refuses the tender (basket kept, the
cashier retries); a retried authorize carries the same event id, which the
device side uses as an idempotency key so nothing prints twice.

## Drivers (`okc.driver`)

| Driver | Status | Talks to |
|---|---|---|
| `bridge` | **complete, tested** | The Universal Till ÖKC bridge protocol v0 (JSON lines over TCP, one request per connection) — the simulator `scripts/okc-sim`, or a small LAN bridge process wrapping a maker SDK |
| `gmp3` | scaffold, fails closed | GİB's GMP-3 v5.0 wired mode; needs the protocol PDF (ynokc.gib.gov.tr) and the maker's per-device activation |
| `hugin-pclink` | scaffold, fails closed | Hugin PC Link HTTPS API (developer.hugin.com.tr) |
| `pavo-rest` | scaffold, fails closed | Pavo sales-application REST (API key from the Pavo portal) |
| `token-x` | scaffold, fails closed | TokenX Connect cloud API for Beko devices (developer.tokeninc.com); would use `http_request` + `net:` |

A scaffold driver refuses every tender with a clear log line rather than
pretending, so a shop can never take an unsigned sale by misconfiguration.
Filling one in needs the maker's integrator documentation and a test device
on the LAN (playbook steps 3–4, ut-docs#1280); the `okc` package and its
tests show the exact shape to implement.

## Settings

| Key | Default | Meaning |
|---|---|---|
| `okc.driver` | `bridge` | one of the drivers above |
| `okc.host` | `127.0.0.1` | device (or bridge) address on the shop LAN |
| `okc.port` | `4711` | device (or bridge) port |
| `okc.maker` | | maker name to record when the device does not report one |
| `okc.connect_timeout_ms` | `3000` | dial deadline |
| `okc.read_timeout_ms` | `8000` | how long to wait for the device to finish (card presented, paper printed) |

Permissions: `tcp:*` (the device address is a setting, so the exact host:port
cannot be declared in the manifest — same review-gated convention as other
LAN-device plugins, ADR-0001 amendment) and `storage.local.1MB`.

## Build and run

```sh
GOOS=wasip1 GOARCH=wasm go build -o dist/plugin.wasm ./plugins/tax-tr
go run ./scripts/okc-sim            # simulated device on 127.0.0.1:4711
go test ./plugins/...               # driver + simulator tests (host Go)
go test ./internal/plugins -run OKC # the real .wasm through the till's runtime
```

Then install the plugin (manifest `plugin.json` + `plugin.wasm`), grant the
`tcp:*` permission, set the country to Türkiye, and choose **Yazarkasa
(ÖKC)** at tender. `/fiscal-device` shows the device's last receipt and
today's count.
