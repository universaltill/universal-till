# Universal Till Plugin Development Guidelines

**Updated 2026-07-24** — reflects the current plugin runtime (ADR-0001,
ADR-0002 in the `docs` repo: `reference/adr/`).

Welcome to Universal Till plugin development! This guide will help you build, test, and publish plugins for the Universal Till ecosystem.

Some sections below (Getting Started, Plugin Types interfaces, Publishing)
describe a CLI-tool/SDK workflow that hasn't been built yet — they're kept
as a design sketch of where plugin tooling is headed, not something you can
run today. **For the actual, working way to build a plugin right now,
skip to [Plugin Architecture](#plugin-architecture) below and copy a real
example**: `ut-plugin-payment-stripe` (wasm, payment), `ut-plugin-faq`
(none, asset-only), or `ut-plugin-integration-webhook` (wasm, integration)
— all real, shipping plugins in the marketplace today.

---

## 📋 Table of Contents

1. [Overview](#overview)
2. [Plugin Architecture](#plugin-architecture)
3. [Getting Started](#getting-started)
4. [Plugin Types](#plugin-types)
5. [Development Guide](#development-guide)
6. [Testing](#testing)
7. [Security Requirements](#security-requirements)
8. [Publishing to Plugin Store](#publishing-to-plugin-store)
9. [Monetization](#monetization)
10. [Best Practices](#best-practices)
11. [Support](#support)

---

## Overview

### What are Universal Till Plugins?

Plugins extend Universal Till's functionality without modifying the core system. They can:

- Process payments (Stripe, Square, PayPal, etc.)
- Integrate with marketplaces (eBay, Amazon, Shopify)
- Connect to delivery services (Uber Eats, DoorDash)
- Sync with accounting software (QuickBooks, Xero)
- Add industry-specific features (table management, appointments)
- Integrate with hardware (custom receipt printers, scales)
- Export data to external systems

### Plugin Runtime (ADR-0001)

Every plugin declares a `runtime` in its manifest:

- **`"wasm"`** (the default for logic plugins) — a single
  architecture-independent `.wasm` module, executed **in-process** by the
  till via [wazero](https://wazero.io) (pure Go, no cgo, no separate
  process). Write it in Go (`GOOS=wasip1 GOARCH=wasm`), Rust, TinyGo, or
  anything else that targets WASM. The module gets no capabilities by
  default — the manifest's `permissions` array (e.g. `net:api.stripe.com`,
  `pos.tender`, `storage`) is what the host grants. This is what payment,
  integration, and most other logic plugins use. Go plugins use the guest SDK
  [`sdk/plugin`](../sdk/plugin/README.md) for the event loop and every host
  call, and unit-test with its fake host.
- **`"none"`** — asset-only: content bundles, themes, language packs. No
  code runs at all; the till renders/serves the files directly.
- **`"go"`** — a separately supervised OS process. Reserved for hardware/
  device plugins that need raw USB/serial access; the minority case.

All three talk to the rest of the system the same way once loaded: through
event hooks declared in the manifest (see below), not a direct function-call
API.

---

## Plugin Architecture

### Plugin Structure

```
my-plugin/
├── manifest.json          # Plugin metadata
├── plugin.go             # Main plugin code (or main.py, main.js)
├── config.schema.json    # Configuration schema
├── README.md             # Documentation
├── LICENSE               # Your license choice
├── icon.png              # 512x512 icon
├── screenshots/          # Screenshots for store listing
│   ├── screenshot1.png
│   └── screenshot2.png
└── tests/                # Unit tests
    └── plugin_test.go
```

### Manifest File (manifest.json)

This is the real, current schema — trimmed from the shipping
`ut-plugin-payment-stripe` manifest:

```json
{
  "id": "com.example.stripe",
  "name": "Stripe Card Payments",
  "version": "1.2.0",
  "description": "Take card payments through Stripe...",
  "author": "Your Name",
  "website": "https://example.com",
  "canonical_type": "payment",
  "runtime": "wasm",
  "device_arch": "any",
  "min_pos_version": "1.0.0",
  "permissions": [
    "pos.tender",
    "events:receive",
    "net:api.stripe.com",
    "storage"
  ],
  "locales": ["en-US"],
  "entries": [
    {
      "type": "payment",
      "key": "stripe",
      "label": "Card (Stripe)",
      "sort_order": 4,
      "trigger_event": "payment.stripe.requested"
    }
  ],
  "settings": [
    { "key": "stripe_secret_key", "default_value": "", "scope": "global" }
  ],
  "entrypoint": "./bin/plugin.wasm",
  "hooks": [
    { "event": "payment.stripe.requested", "action": "stripe.settled" }
  ]
}
```

`canonical_type` is one of the 20 fixed types in the plugin taxonomy
(ADR-0002) — payment, page, theme, integration, etc. `entries` control
where the plugin shows up in the UI; `hooks` wire manifest-declared events
to the plugin's exported functions (for `runtime: "wasm"`) — there is no
`modes.local`/`modes.cloud` split and no separate `pricing` block in the
manifest itself.

---

## Getting Started

### 1. Install Plugin SDK

```bash
# For Go
go get github.com/universaltill/plugin-sdk

# For Python
pip install universaltill-plugin-sdk

# For JavaScript
npm install @universaltill/plugin-sdk
```

### 2. Create Plugin Scaffold

```bash
# Using CLI tool
ut-plugin-cli create my-awesome-plugin

# Or manually
mkdir my-awesome-plugin
cd my-awesome-plugin
```

### 3. Implement Plugin Interface

**Go Example:**

```go
package main

import (
    "github.com/universaltill/plugin-sdk/go/plugin"
)

type MyPlugin struct {
    config plugin.Config
}

// Initialize is called when plugin loads
func (p *MyPlugin) Initialize(config plugin.Config) error {
    p.config = config
    // Setup connections, load config, etc.
    return nil
}

// Metadata returns plugin information
func (p *MyPlugin) Metadata() plugin.Metadata {
    return plugin.Metadata{
        ID:          "com.example.myplugin",
        Name:        "My Awesome Plugin",
        Version:     "1.0.0",
        Description: "Does awesome things",
    }
}

// HandleTransaction processes a transaction
func (p *MyPlugin) HandleTransaction(tx plugin.Transaction) (*plugin.TransactionResult, error) {
    // Your transaction logic here
    
    result := &plugin.TransactionResult{
        Success:       true,
        TransactionID: "tx_12345",
        Message:       "Payment successful",
    }
    
    return result, nil
}

// HandleRefund processes a refund
func (p *MyPlugin) HandleRefund(refund plugin.Refund) (*plugin.RefundResult, error) {
    // Your refund logic here
    return &plugin.RefundResult{Success: true}, nil
}

// Cleanup is called when plugin unloads
func (p *MyPlugin) Cleanup() error {
    // Close connections, save state, etc.
    return nil
}

func main() {
    plugin.Serve(&MyPlugin{})
}
```

**Python Example:**

```python
from universaltill_plugin_sdk import Plugin, Transaction, TransactionResult

class MyPlugin(Plugin):
    def initialize(self, config):
        self.config = config
        # Setup your plugin
        
    def handle_transaction(self, transaction: Transaction) -> TransactionResult:
        # Process transaction
        return TransactionResult(
            success=True,
            transaction_id="tx_12345",
            message="Payment successful"
        )
    
    def cleanup(self):
        # Cleanup resources
        pass

if __name__ == "__main__":
    MyPlugin().serve()
```

### 4. Test Locally

```bash
# Build your plugin
go build -o my-plugin

# Run Universal Till in development mode
UT_DEV_MODE=true ./universal-till

# Install your plugin locally
ut-plugin-cli install ./my-plugin
```

---

## Plugin Types

### 1. Payment Plugins

Process payments through external payment processors.

**Interface:**
```go
type PaymentPlugin interface {
    ProcessPayment(payment Payment) (*PaymentResult, error)
    Refund(refundRequest RefundRequest) (*RefundResult, error)
    Void(transactionID string) error
    GetStatus(transactionID string) (*PaymentStatus, error)
}
```

**Examples:**
- Stripe Terminal
- Square Reader
- PayPal Here
- SumUp
- Regional payment processors

### 2. Marketplace Plugins

Sync products and orders with online marketplaces.

**Interface:**
```go
type MarketplacePlugin interface {
    SyncInventory(products []Product) error
    PublishProduct(product Product) (*PublishedProduct, error)
    FetchOrders() ([]Order, error)
    UpdateOrderStatus(orderID string, status OrderStatus) error
}
```

**Examples:**
- eBay integration
- Amazon Seller Central
- Shopify sync
- Etsy integration

### 3. Delivery Plugins

Integrate with food delivery and logistics services.

**Interface:**
```go
type DeliveryPlugin interface {
    CreateDelivery(order Order) (*Delivery, error)
    TrackDelivery(deliveryID string) (*DeliveryStatus, error)
    CancelDelivery(deliveryID string) error
}
```

**Examples:**
- Uber Eats
- DoorDash
- Deliveroo
- Local delivery services

### 4. Accounting Plugins

Export financial data to accounting systems.

**Interface:**
```go
type AccountingPlugin interface {
    SyncTransactions(transactions []Transaction) error
    SyncInventory(products []Product) error
    GenerateReport(reportType string, dateRange DateRange) (*Report, error)
}
```

**Examples:**
- QuickBooks
- Xero
- Wave Accounting
- Sage

### 5. Hardware Plugins

Interface with specialized hardware.

**Interface:**
```go
type HardwarePlugin interface {
    Initialize(deviceConfig DeviceConfig) error
    SendCommand(command HardwareCommand) error
    ReadData() ([]byte, error)
}
```

**Examples:**
- Custom receipt printers
- Digital scales
- Customer displays
- Barcode scanners

### 6. Analytics Plugins

Provide business intelligence and reporting.

**Interface:**
```go
type AnalyticsPlugin interface {
    TrackEvent(event Event) error
    GenerateDashboard(config DashboardConfig) (*Dashboard, error)
    GetInsights(dateRange DateRange) ([]Insight, error)
}
```

---

## Development Guide

### Configuration Management

Plugins receive configuration through the config object:

```go
func (p *MyPlugin) Initialize(config plugin.Config) error {
    // Get configuration values
    apiKey := config.GetString("api_key")
    environment := config.GetString("environment", "production") // with default
    timeout := config.GetInt("timeout", 30)
    
    // Validate required config
    if apiKey == "" {
        return errors.New("api_key is required")
    }
    
    return nil
}
```

### Configuration Schema

Define your configuration in `config.schema.json`:

```json
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {
    "api_key": {
      "type": "string",
      "title": "API Key",
      "description": "Your Stripe API key",
      "secret": true,
      "required": true
    },
    "environment": {
      "type": "string",
      "title": "Environment",
      "enum": ["test", "production"],
      "default": "test"
    },
    "auto_capture": {
      "type": "boolean",
      "title": "Auto Capture Payments",
      "default": true
    }
  }
}
```

### Error Handling

Always provide meaningful error messages:

```go
func (p *MyPlugin) ProcessPayment(payment Payment) (*PaymentResult, error) {
    resp, err := p.client.Charge(payment.Amount)
    if err != nil {
        // Return user-friendly error
        return nil, fmt.Errorf("payment failed: %w", err)
    }
    
    if resp.Status == "declined" {
        return &PaymentResult{
            Success: false,
            Error:   "Card declined - insufficient funds",
            Code:    "CARD_DECLINED",
        }, nil
    }
    
    return &PaymentResult{Success: true, TransactionID: resp.ID}, nil
}
```

### Logging

Use the SDK logger for debugging:

```go
import "github.com/universaltill/plugin-sdk/go/logging"

func (p *MyPlugin) ProcessPayment(payment Payment) (*PaymentResult, error) {
    logging.Info("Processing payment", "amount", payment.Amount)
    
    result, err := p.charge(payment)
    if err != nil {
        logging.Error("Payment failed", "error", err)
        return nil, err
    }
    
    logging.Info("Payment successful", "transaction_id", result.TransactionID)
    return result, nil
}
```

### State Management

Store plugin state using the SDK storage:

```go
import "github.com/universaltill/plugin-sdk/go/storage"

func (p *MyPlugin) SaveToken(token string) error {
    return storage.Set("oauth_token", token)
}

func (p *MyPlugin) LoadToken() (string, error) {
    return storage.Get("oauth_token")
}
```

### Plugin view pages (ADR-0121 §7)

A plugin page should not ship HTML. Give the `page` entry a `view`, hold
`ui:page` and `events:receive`, and hook `ui.view.ask` and
`ui.action.ask`. Answer each with a JSON **view document** built from the
till's fixed components (`heading`, `text`, `notice`, `stat_tiles`,
`table`, `list`, `empty_state`, `button`, `form`). The till draws it with
its own templates in the operator's language and layout (RTL included).
Money is sent as integer minor units plus currency. Text is a key from
your own `locales/*.json`, or a literal the till escapes. Buttons and
forms post an action name back to your entry's route. You never supply a
URL, script or style. A slow, broken or invalid answer shows an
"unavailable" notice and blocks nothing else.

An action that may take longer than an event's deadline (2 s, 10 s with
`net:*`) answers `{"job": {"event": "<your-plugin-id>.<name>"}}` instead.
The event must be in your own namespace and in your `hooks`. The till
answers the operator at once with a progress poll, then sends you that
event with the action's payload plus `job_id`. The deadline is your
`limits.long_call_s` (at most 300 s), and a job never runs on the sale
path. Inside a job, `http_request` and `http_open` wait for a response's
headers until that deadline (an ordinary event gives up after 30 s), so a
non-streamed call to a slow self-hosted model can finish. Report progress with the `job_progress(pct, msg_key)` host function
(ABI 3; `msg_key` from your own locale bundle; outside a job it returns
`-1`, and a foreign key returns `-4`). Answer with a document or a
redirect, as for an action. You can run at most two jobs at once (one on
Android/iOS), and the till also caps jobs across all plugins; a job past
either cap is refused with a "try again later" notice, never queued. A job
nobody polls for about 15 s is cancelled, and a result nobody fetches
within about 30 s is dropped; at most four unfetched results are kept
per plugin (a fifth drops the oldest). The query parameter `_job` is
reserved for the till's poll: it is never passed to your view as a
`params` entry, so don't use it in your own links.

A form may ask the operator for a file with a field of kind `file`
(no `value`). Your page entry must declare the largest file it accepts
as `config.upload_max_mb` (an integer 1–32; anything else refuses the
install), or a document with a file field is refused. The till streams
each chosen file to a temporary file (at most four per post); a file
over the limit is left out and its field named in `invalid`. Your
`ui.action.ask` payload's `upload_handles` lists each file as
`{"field", "handle", "filename", "size", "content_type"}`. Pass the
`handle` to `upload_open` (ABI 3) and stream the file with `upload_read`
(same plain-read rules as `import_file_read`, at most 256 KiB per call),
then `upload_close`, which also deletes it. A handle works only for the
plugin it was sent to. The file is deleted when the ask ends, or, if you
answer with a job, when the job ends, even if you never close it.

The format, limits and payloads are in
[`ut-docs/reference/plugin-views.md`](https://github.com/universaltill/ut-docs/blob/main/reference/plugin-views.md).

### Content slots (ADR-0121 §7, ut-docs#3872)

A view entry can also fill one core content slot — `item.edit.actions`,
`reports.panels`, `eod.footer`, `settings.sections`, `admin.pages` or
`setup.wizard.steps` — by naming it in the entry's `slot` and holding
`ui:slot:<slot>` (`ui:page` alone is not enough). The till asks every
plugin filling the slot with `ui.view.ask` in parallel; `params` carry the
host request's query plus `"slot"`. Each answer gets **2 s**: a slow,
broken or invalid answer is skipped (logged, nothing shown) and never
holds up the screen. Panels are drawn in (plugin id, entry key) order, at
most 8 per slot. The five signed-in slots load lazily from
`GET /ui/slot/{slot}`, gated by the host screen's permission (`admin.pages`
also needs `reports`, ADR-0149 §6). A panel's actions post to the entry's
own route, as on its page, and answer into that panel only; they are
checked against the same `ui:slot:<slot>` grant (so a slot-only plugin
needs no `ui:page`). Every request to a slot entry's own route -- its
page, a poll, any action -- also needs the host screen's permission, or
the till answers 403 without asking the plugin (ut-docs#3963,
ut-docs#3973). The setup wizard draws `setup.wizard.steps` inline and
read-only (no buttons or forms: the wizard runs before anyone signs in),
so that entry's route is 403 for everyone. A slot entry's `/menu` tile
follows the same gate: it shows only to staff who pass the host screen's
permission, and a `setup.wizard.steps` entry gets no tile (ut-docs#3982).
Details:
[`plugin-views.md` → Content slots](https://github.com/universaltill/ut-docs/blob/main/reference/plugin-views.md#content-slots).

A `docs` page entry (the Plugins page's Docs button, ADR-0037) that
names a slot gets its button only for staff who pass that slot's gate, and
never for a `setup.wizard.steps` or unknown slot, so the button is never
shown and then refused (ut-docs#3994).

### Camera identify: `catalog.identify` (ADR-0121 §7)

A plugin that recognises products from a photo subscribes to
`catalog.identify` (hook it, and hold `events:receive`). The sell screen
then shows core's own "Identify by camera" button and overlay (in place
of the built-in AI one; if several plugins answer, the lexically first
plugin id wins). The overlay posts one photo (JPEG, PNG or WebP, at most
8 MiB) and the till runs `catalog.identify` as a **job** on your plugin:
the payload is `{"upload_handles": [{"field": "photo", "handle", "filename",
"size", "content_type"}], "locale", "job_id"}` — read the photo with
`upload_open`/`upload_read` as above and report `job_progress` while you
work. Answer with a document holding only `text`, `notice` and one
`suggestions` component whose items use the `add_to_basket {sku, qty}`
effect (qty 1–999, default 1); the cashier taps one to add it through the
normal scan path. Anything else — another component, an `apply_fields`
effect, a redirect or a job — is refused and the overlay says identify
failed. `suggestions` is refused on your own plugin pages and slots: it
only renders in a core seam. The cashier can close the overlay at any
time; the job is then dropped. When the cashier taps a suggestion, the
till tries to store the photo as the item's newest `ai_ref` and then sends
`catalog.identify.confirmed` `{job_id, item_id, sku, stored}` to your
plugin alone (hook it too, and hold `view:inventory`); the answer is
ignored. Format and limits:
[`ut-docs/reference/plugin-views.md`](https://github.com/universaltill/ut-docs/blob/main/reference/plugin-views.md).

### Scheduled work (ADR-0121 §8)

A wasm plugin has no background threads. For periodic work (a retry
queue, a poll), declare `schedules` in the manifest and hold the
`schedule` permission:

```json
"permissions": ["schedule"],
"schedules": [ { "event": "com.example.sync.retry.tick", "every_s": 300, "jitter_s": 60 } ]
```

The event must start with your plugin `id` plus a dot. `every_s` is at
least 30, and `jitter_s` is from 0 to `every_s`. The till saves the
schedules at install and replaces them on every update or rollback.
While the plugin is active, its module is loaded and `schedule` is
granted, the till sends each event to your plugin alone, as an ordinary
event. The wait between ticks is `every_s` plus a random 0–`jitter_s`
seconds. The payload is `{"scheduled_at": "<RFC 3339 time the tick fell
due>"}`. You need no `hooks` entry for your own schedule event, and no
other plugin receives it.

- A tick is skipped while the previous run of the same schedule is
  still going.
- A tick that falls due within 3 s of a completed sale waits until 3 s
  pass with no new sale, then fires. It is delayed, never dropped.
- A tick is ordinary work, even if its name ends in `.ask`. It waits for
  an ordinary call slot and never takes the slot reserved for sales.
- Ticks stop when the plugin is disabled or uninstalled, or the till
  shuts down. A revoked `schedule` permission stops them at the next tick.
- The interval restarts whenever plugins are reloaded on the till
  (any install, update, enable, disable or uninstall) and at start-up, so
  the first tick comes `every_s` plus jitter after that. Don't rely on a
  long interval firing at a fixed wall-clock time.
- If the till can't read the saved schedules at a reload, it logs it and
  nothing ticks until the next reload. A plugin installed before this till
  version has no saved schedules: reinstall or update it to start ticks.

---

## Testing

### Unit Tests

```go
package main

import (
    "testing"
    "github.com/universaltill/plugin-sdk/go/plugin"
)

func TestProcessPayment(t *testing.T) {
    p := &MyPlugin{}
    config := plugin.Config{
        "api_key": "test_key",
    }
    
    if err := p.Initialize(config); err != nil {
        t.Fatalf("Initialize failed: %v", err)
    }
    
    payment := plugin.Payment{
        Amount:   1000, // $10.00
        Currency: "USD",
    }
    
    result, err := p.ProcessPayment(payment)
    if err != nil {
        t.Errorf("ProcessPayment failed: %v", err)
    }
    
    if !result.Success {
        t.Error("Payment should succeed")
    }
}
```

### Integration Tests

Use the Universal Till test environment:

```bash
# Start test instance
ut-test-server start

# Run integration tests
go test -tags=integration ./...

# Stop test instance
ut-test-server stop
```

### Manual Testing

```bash
# Install plugin in development mode
ut-plugin-cli install --dev ./my-plugin

# View logs
ut-plugin-cli logs my-plugin

# Uninstall
ut-plugin-cli uninstall my-plugin
```

---

## Security Requirements

### 1. Secret Management

**NEVER hardcode secrets:**

```go
// ❌ BAD
const API_KEY = "sk_live_abc123"

// ✅ GOOD
apiKey := config.GetString("api_key")
```

### 2. Input Validation

Always validate and sanitize inputs:

```go
func (p *MyPlugin) ProcessPayment(payment Payment) (*PaymentResult, error) {
    // Validate amount
    if payment.Amount <= 0 {
        return nil, errors.New("amount must be positive")
    }
    
    // Validate currency
    if !isValidCurrency(payment.Currency) {
        return nil, errors.New("invalid currency code")
    }
    
    // Sanitize card data (if applicable)
    // ...
}
```

### 3. Network Security

Use HTTPS for all external communications:

```go
import "crypto/tls"

client := &http.Client{
    Transport: &http.Transport{
        TLSClientConfig: &tls.Config{
            MinVersion: tls.VersionTLS12,
        },
    },
}
```

### 4. Permissions

Declare all permissions in manifest.json. The till refuses to install a
manifest declaring a permission it does not recognise (ut-docs#3328 — the
allow-list is `internal/plugins/permission_allowlist.go`):

```json
{
  "permissions": ["events:receive", "sales:read", "net:api.example.com", "storage"]
}
```

| Permission | Grants |
|---|---|
| `storage` | the plugin's key/value store |
| `events:receive` | event delivery to the plugin's hooks |
| `payments:reconciliation` | payment details on `sale.completed` |
| `devices:printer` | marks the plugin as providing a printer |
| `<entity>:read` / `<entity>:write` | read/write one data entity (`sales:read`, `items:read`, `inventory:read`, …) |
| `net:<host>` / `net:*` | HTTPS to that host (a DNS name or IP literal — no wildcard pattern, scheme, port or path) / any public host. An exact grant reaches a LAN address (RFC 1918, ULA, link-local, CGNAT) only together with `http:lan`, and loopback only when the host is itself `localhost` or a loopback IP literal (ADR-0121 §3, ut-docs#3794); a refusal for the missing `http:lan` is audited. |
| `tcp:<host>:<port>` / `tcp:*` | a raw TCP socket to that address (same host rule; IPv6 bracketed) / any public address |
| `net:@setting:<urlKey>`, `tcp:@setting:<hostKey>:<portKey>` | the address an admin saves in those settings (ut-docs#2899) |
| `http:lan` | lets an exact `net:` / `net:@setting:` / endpoint grant reach a LAN address at all, over https or plain `http`. Plain `http` (not only https) to a host the plugin holds an exact `net:<host>` / `net:@setting:<key>` grant for, or to the host of one of its own `"type": "endpoint"` settings (which, with `http:lan`, counts as an exact grant). A host let in only by `http:lan` must resolve to a LAN address (RFC 1918, ULA, link-local, loopback) — never a public one. Cloud metadata (`169.254.169.254`, `fd00:ec2::254`) is refused whatever the grant. `net:validation:<host>` hosts stay public-only. |
| `http:stream` | `http_open` / `http_write` / `http_status` / `http_read` / `http_close`: a request body sent from a buffer and a response body read in chunks (NDJSON/SSE), each up to `limits.http_body_mb` (default 8 MiB; over it: `-5`); at most 4 open handles per event (a fifth: `-6`); a read gives up after 30 s without data; every handle is closed when the event returns. Same egress rules as `http_request`. |
| `blob:own` | `blob_put_open` / `blob_write` / `blob_commit` / `blob_get_open` / `blob_read` / `blob_delete` / `blob_list`: the plugin's own files under `data/plugin-data/<id>/blobs` (ADR-0121 §6). Names are `[a-z0-9._-]{1,128}` with no paths (else `-4`); a put is written to a temp file and appears only on `blob_commit` (an uncommitted put leaves nothing); committed blobs plus every still-open put total at most `limits.storage_mb` (default 50 MiB; over it: `-5`, put discarded); at most 8 open handles per event (`-6`); a get handle is released when `blob_read` returns 0; every handle is closed when the event returns. |
| `device-info` | ★ review-gated, read-only (ADR-0140, ut-docs#3862); bare — no wildcard or parameter form. `device_id_get`: this till's stable device id, a v4 UUID minted on first use, kept across a marketplace re-pair and unrelated to the marketplace device id. `device_local_ips_get`: a JSON array of the host's interface addresses (IPv4 and IPv6, loopback/unspecified excluded, zone stripped, deduplicated, sorted; none is `[]`). `device_timezone_get`: the local offset at call time as `UTC±hh:mm` (e.g. `UTC+01:00`, `UTC-05:30`). All three use the buffer ABI; without the grant they return `-2`. Every call is audited — a denial as `permission_denied`, a grant as `device_info_read` naming the function; if the grant's audit row can't be written the call returns `-3`. |
| `db:own`, `schedule`, `cloud:directive`, `secret:write`, `ui:page`, `view:<class>`, `ui:slot:<slot>` | ADR-0121 §2 (ABI 3); the plugin store explains each to the operator in plain words. `view:<class>` names a class of core read views (`view:sales`, `view:inventory`, `view:audit`, `view:users` — ADR-0121 §5; the view itself goes in `views_used`), read through the `view_query` host function; the views, their arguments and result fields are in ut-docs `reference/contracts/plugin-views.md` (ut-docs#3158). `view:inventory` also gates `item_image_open` / `item_image_read` (ADR-0121 R1, ut-docs#4005): an item's reference photo by role (`ai_ref`, `thumb`, or `ref` = `ai_ref` if it decodes, else `thumb`), re-encoded by the till as a JPEG ≤ 160 px (never the file or a path); ≤ 64 opens (`-5`) and 4 open handles (`-6`) per event. `ui:slot:<slot>` names one of the §7 content slots (`item.edit.actions`, `reports.panels`, `setup.wizard.steps`, `eod.footer`, `settings.sections`, `admin.pages`), the same set an entry's `slot` is checked against. |

`storage.local.<n>KB|MB|GB` and the dotted `ui.locale`, `ui.theme`, `ui.page`,
`ai.configure`, `pos.tender` are accepted only so already-published plugins
keep installing — nothing checks them. Don't use them in a new plugin.

A setting declared `"type": "endpoint"` holds an operator-entered
`http(s)://host[:port][/path]`; the settings page refuses anything else
(`"type": "secret"` masks and seals a credential, ADR-0082). A manifest's own
`default_value` for an `"endpoint"` setting is checked the same way at
install time (ut-docs#3552): a non-empty default must be a valid http(s) URL
or the manifest is rejected; an absent or empty-string (`""`) default is
fine — the operator sets it later.

### 5. Data Protection

- Encrypt sensitive data at rest
- Never log sensitive information (card numbers, passwords)
- Implement secure data deletion
- Follow PCI DSS if handling payments

---

## Publishing to Plugin Store

### 1. Prepare for Submission

**Checklist:**
- [ ] Plugin builds without errors
- [ ] All tests passing
- [ ] manifest.json complete and valid
- [ ] README.md with clear documentation
- [ ] LICENSE file included
- [ ] Icon (512x512 PNG)
- [ ] Screenshots (1280x720 or higher)
- [ ] No hardcoded secrets
- [ ] Security scan passes locally

### 2. Test Security Scan

```bash
# Run local security scan
ut-plugin-cli scan ./my-plugin

# Fix any issues reported
```

### 3. Submit to Store

**Option A: CLI**
```bash
# Login to Universal Till
ut-plugin-cli login

# Package and submit
ut-plugin-cli publish ./my-plugin
```

**Option B: Web Interface**
1. Go to https://plugins.universaltill.com
2. Click "Submit Plugin"
3. Upload plugin package (.zip)
4. Fill in store listing details
5. Submit for review

### 4. Review Process

**Automated (Instant):**
- Malware scan
- Vulnerability scan
- API key exposure check
- Permission audit
- Code signing verification

**Manual (1-3 business days):**
- Functionality testing
- Documentation review
- UI/UX check (if applicable)
- Compliance verification

### 5. Approval & Launch

Once approved:
- Plugin appears in store
- Users can install it
- You receive developer dashboard access
- Analytics and download stats available

---

## Monetization

### Free Plugins

- 0% commission
- Free hosting forever
- Great for open source projects
- Builds reputation

### Paid Plugins

**One-Time Purchase:**
- Set price ($5 - $500)
- 20% commission to Universal Till
- User owns plugin forever

**Subscription:**
- Monthly or yearly pricing
- 20% commission on recurring revenue
- Automatic renewal handling

**Freemium:**
- Basic version free
- Premium features require payment
- Upgrade path in-app

### Example Pricing Models

**Payment Processor Plugin:**
- Free (user pays transaction fees to processor)
- OR $29 one-time (premium features like fraud detection)

**Accounting Integration:**
- Free (basic export)
- $9/month (real-time sync, advanced features)

**Industry-Specific Plugin:**
- $99 one-time (restaurant table management)
- $19/month (includes updates and support)

### Payment Processing

Universal Till handles:
- Payment collection
- Tax calculation (where applicable)
- Refunds and disputes
- Payouts (monthly, to your bank account)

---

## Best Practices

### 1. User Experience

- **Clear error messages**: "Payment failed - card declined" not "Error code 402"
- **Loading indicators**: Show progress for long operations
- **Offline support**: Queue operations when offline
- **Graceful degradation**: Work without cloud when possible

### 2. Performance

- **Lazy loading**: Load resources only when needed
- **Caching**: Cache API responses appropriately
- **Async operations**: Don't block the UI
- **Resource cleanup**: Close connections, free memory

### 3. Compatibility

- **Version pinning**: Specify minimum Universal Till version
- **Feature detection**: Check for features before using
- **Backward compatibility**: Support older versions when possible
- **Migration paths**: Help users upgrade smoothly

### 4. Documentation

- **Clear README**: Installation, configuration, usage
- **Code examples**: Show common use cases
- **Troubleshooting**: Common issues and solutions
- **API reference**: For complex plugins

### 5. Support

- **Responsive**: Answer user questions quickly
- **Changelogs**: Document what changed in each version
- **Issue tracking**: Use GitHub issues or similar
- **Community**: Be active in Discord/forums

---

## Versioning

Follow [Semantic Versioning](https://semver.org/):

- **MAJOR** (1.0.0 → 2.0.0): Breaking changes
- **MINOR** (1.0.0 → 1.1.0): New features, backward compatible
- **PATCH** (1.0.0 → 1.0.1): Bug fixes

### Update Strategy

```json
{
  "version": "1.2.3",
  "min_ut_version": "1.0.0",
  "changelog": {
    "1.2.3": "Fixed payment timeout issue",
    "1.2.0": "Added refund support",
    "1.1.0": "Added multi-currency support",
    "1.0.0": "Initial release"
  }
}
```

---

## Support

### Documentation

- **Plugin SDK Docs**: https://docs.universaltill.com/plugins
- **API Reference**: https://api.universaltill.com/docs
- **Examples**: https://github.com/universaltill/plugin-examples

### Community

- **Discord**: https://discord.gg/universaltill (#plugin-dev channel)
- **Forums**: https://forum.universaltill.com
- **Stack Overflow**: Tag `universal-till`

### Direct Support

- **Email**: plugins@universaltill.com
- **GitHub Issues**: https://github.com/universaltill/plugin-sdk/issues

---

## Examples

### Official Example Plugins

Check out these open source examples:

- **[Stripe Payment](https://github.com/universaltill/plugin-stripe)** - Payment processing
- **[CSV Export](https://github.com/universaltill/plugin-csv-export)** - Data export
- **[Shopify Sync](https://github.com/universaltill/plugin-shopify)** - Marketplace integration
- **[Receipt Printer](https://github.com/universaltill/plugin-escpos)** - Hardware integration

---

## FAQ

**Q: Can I charge for my plugin?**  
A: Yes! You can offer paid plugins. We take 20% commission.

**Q: What languages can I use?**  
A: Any language that can compile to a binary or run in our plugin runtime (Go, Python, JavaScript, Rust).

**Q: Can I sell plugins outside the store?**  
A: Yes, but users will need to manually install them. Store plugins auto-update and are easier to discover.

**Q: What if my plugin needs cloud infrastructure?**  
A: You can run your own cloud services. Universal Till Cloud is optional.

**Q: How do updates work?**  
A: Upload new version to store. Users can auto-update or update manually.

**Q: Can I have closed-source plugins?**  
A: Yes! Choose any license you want. Open source is encouraged but not required.

**Q: What about support obligations?**  
A: You're responsible for supporting your plugin. We recommend Discord/GitHub issues.

---

## Next Steps

1. **Join Discord**: https://discord.gg/universaltill (#plugin-dev)
2. **Clone example**: `git clone https://github.com/universaltill/plugin-examples`
3. **Build something awesome**!
4. **Submit to store**: Make money while helping merchants

---

**Happy coding! 🚀**

*Questions? Email plugins@universaltill.com or ask in Discord.*