# Universal Till

> Ultra-light, offline-first point of sale system. Free forever. Run anywhere.

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?logo=go)](https://golang.org)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)
[![Discord](https://img.shields.io/badge/Discord-Join%20Us-7289da?logo=discord)](https://discord.gg/universaltill)

---

## 🚀 What is Universal Till?

**Universal Till** is a modern, free, and open-source point of sale (POS) system designed to run on **any hardware** — from Raspberry Pis to old tablets to professional POS terminals.

### Why Universal Till?

- ✅ **100% Free Core** - No monthly fees, ever
- ✅ **Offline-First** - Works without internet, syncs when available
- ✅ **Run Anywhere** - Raspberry Pi, Android tablets, old hardware, or dedicated POS terminals
- ✅ **Plugin Ecosystem** - Extend functionality with free or paid plugins
- ✅ **No Lock-In** - Export your data anytime, switch systems freely
- ✅ **Open Source** - MIT licensed, fork and modify as you wish
- ✅ **Fast & Lightweight** - Written in Go for blazing performance on cheap hardware
- ✅ **Multi-Platform** - Phone app, web interface, or dedicated terminal

---

## 🎯 Perfect For

- 🛒 Retail stores (clothing, electronics, convenience, etc.)
- ☕ Coffee shops and cafes
- 🍕 Restaurants and food trucks
- 🌮 Pop-up shops and market stalls
- 💊 Pharmacies and health stores
- 📚 Bookstores and specialty shops
- 🌍 Businesses in emerging markets
- 🔐 Privacy-conscious businesses
- 💰 Anyone tired of expensive POS subscriptions

---

## 🌟 Key Features

### Core POS (Free Forever)
- Fast checkout and sales processing
- Inventory management
- Customer management
- Receipt printing (thermal and regular)
- Barcode scanning
- Multi-currency support
- Tax calculation (configurable by region)
- Employee management
- Basic reporting and analytics
- Offline queue (syncs when online)

### Hardware Support
- USB barcode scanners
- Thermal receipt printers (ESC/POS)
- Cash drawers
- Customer displays
- Payment terminals (via plugins)
- Digital scales
- Any standard keyboard/mouse/touchscreen

### Optional Cloud Services
- Multi-device synchronization
- Cloud backup and restore
- Advanced analytics
- Multi-location management
- Enhanced security features

### Plugin Marketplace
Available now: Stripe / QR Pay / demo card payments, German & Spanish
language packs, themes, a "No Sale" register-drawer plugin, a self-hosted
AI assistant, a webhook/ERP connector, and a help/FAQ plugin. One-click
install from the in-app store
(`ut-cloud`), Ed25519-signature verified before anything runs.

Categories the taxonomy supports and welcomes contributions for: more
payment processors (Square, PayPal, regional providers), marketplaces
(eBay, Amazon, Shopify), delivery services (Uber Eats, DoorDash,
Deliveroo), accounting systems (QuickBooks, Xero, Wave), ERP integration
(Odoo, ERPNext), and industry-specific features.

---

## 🚀 Quick Start

### Option 1: Install a release (no toolchain needed)

**Raspberry Pi / Debian / Ubuntu** — the recommended shop install
(creates the `pos` service user, installs a systemd service, survives
upgrades, keeps item photos in `/var/lib/unitill`):

```bash
# arm64 = Raspberry Pi OS 64-bit; use _amd64.deb on a PC
sudo apt install ./unitill-pos_*_arm64.deb
```

On a **Raspberry Pi (Pi OS Lite, fresh install)** this also stages the
fullscreen kiosk automatically: the first boot after installing sets up
cage + Chromium and boots straight into the till. To keep a Pi kiosk-free
(dev box), `sudo touch /etc/unitill/no-kiosk` before rebooting. Desktop
images and upgrades are never auto-converted — run
`sudo /usr/lib/unitill/unitill-kiosk-setup` there yourself if you want the
kiosk.

**Windows**: download the `windows_amd64.zip`, extract anywhere,
double-click `run-unitill.bat`.

**macOS / manual Linux**: download the `.tar.gz`, extract, and run
`./unitill-pos` from inside the extracted folder (it needs the bundled
`web/` directory next to it).

All downloads: https://github.com/universaltill/universal-till/releases —
then open http://localhost:8080; the first-boot wizard sets language,
currency and the admin PIN.

### Option 2: Build from Source

```bash
# Clone the repository
git clone https://github.com/universaltill/universal-till.git
cd universal-till

# Build
make build

# Run
make run          # or: ./bin/unitill-pos
```

Open http://localhost:8080

### Option 3: Docker

```bash
# Clone the repository
git clone https://github.com/universaltill/universal-till.git
cd universal-till

# Configure environment (docker-compose.edge.yml reads pos.env.dev)
cp pos.env.example pos.env.dev
# Edit pos.env.dev with your settings

# Run with Docker Compose
docker compose -f docker-compose.edge.yml up --build
```

Open http://localhost:8080

### Option 4: Development Mode (with the marketplace)

For development and testing with plugin-marketplace features:

```bash
# Start the POS with the dev environment
./scripts/dev.sh
# Or manually:
# set -a; source <(grep -v '^#' pos.env.dev | grep -v '^$'); set +a
# make build && ./bin/unitill-pos
```

`./scripts/dev.sh` loads `pos.env.dev`, which points at the **deployed dev
marketplace** in the homelab cluster
(`https://cloud.home.taskrunnertech.co.uk/api`). It runs with auth disabled,
so **no OAuth client secret / API key is required** — you can browse the plugin
store and one-click install the FAQ plugin out of the box. `pos.env.dev` also
carries the marketplace's Ed25519 signing public key, which the POS uses to verify
plugin signatures before installing.

To point at a different marketplace, change `UT_MARKETPLACE_ENDPOINT_URL` (the URL
must include `/api`) and `UT_MARKETPLACE_PUBLIC_KEY` in `pos.env.dev`.

---

## ⚙️ Configuration

Universal Till uses a single configuration file: `pos.env`

**On first installation**, create your configuration:

```bash
# Copy the example template
cp pos.env.example pos.env

# Edit with your settings
nano pos.env
```

**Configuration options:**

```bash
# Server configuration
UT_LISTEN_ADDR=:8080                  # Port to listen on
UT_DEFAULT_LOCALE=en                  # Default language

# Database
UT_STORE=sqlite                        # Database type (sqlite recommended)

# Business settings
UT_CURRENCY=USD                        # Currency code (USD, GBP, EUR, etc.)
UT_TAX_INCLUSIVE=true                  # Tax included in prices?
UT_TAX_RATE=20                         # Tax rate percentage

# Marketplace integration (cloud plugin store). The endpoint must include /api.
UT_MARKETPLACE_ENDPOINT_URL=https://cloud.home.taskrunnertech.co.uk/api  # Dev marketplace
UT_MARKETPLACE_PUBLIC_KEY=             # Ed25519 signing key (hex) used to verify plugin signatures
UT_MARKETPLACE_CLIENT_ID=              # Doubles as merchant_id on the install path
UT_MARKETPLACE_STORE_ID=               # Store identifier for entitlement/install
UT_MARKETPLACE_DEVICE_ID=              # Device identifier (defaults to hostname)
UT_MARKETPLACE_CLIENT_SECRET=          # OAuth2 client secret (only if the marketplace enforces auth)
UT_MARKETPLACE_API_VERSION=1.0.0       # Marketplace API version
UT_MARKETPLACE_TELEMETRY_OPT_IN=false  # Send usage telemetry to marketplace
UT_DEV_MODE=false                      # Enable developer mode features
UT_MARKETPLACE_DEV_OVERRIDE_URL=       # Local marketplace override (dev mode only)

# Optional
UT_SAMPLES_DIR=/path/to/images        # Sample product images

# Assistive AI (optional): camera item identification on the sale screen and
# "Ask your till" — plain-language questions about sales/stock on /reports
# (managers only, every question audited).
# Self-hosted first — point at an Ollama server running open models
# (a machine in the store, your homelab, or a VM you provide); photos, item
# names and sales figures never leave your infrastructure. Nothing configured
# = invisible, and checkout never depends on it.
UT_AI_ENDPOINT=http://localhost:11434  # Ollama server URL (self-hosted)
UT_AI_MODEL=llama3.2-vision            # Open vision model (camera identify)
UT_AI_ASK_MODEL=llama3.2               # Tool-capable text model (Ask your till)
UT_AI_PROVIDER=                        # Optional: "claude" for the hosted paid API
UT_AI_API_KEY=                         # Only for the claude provider (no ask loop yet)
```

### System Settings

Access `/settings` in the web interface to configure:
- Currency and region
- Tax rules
- Store information
- Employee permissions
- Hardware devices

## 🏎️ Performance checks
- Target hardware: Raspberry Pi 4 (8GB) or equivalent mini PC.
- Run sale + micro-interaction checks: `go test ./internal/pos -run "(TestSalePerformanceThresholds|TestMicroInteractionLatency)"`
- Optional deep benchmark: `go test -bench=BenchmarkCompleteSale -benchtime=10x ./internal/pos`
- Override thresholds if your runner differs: `UT_BENCHMARK_SALE_WARN_MS`, `UT_BENCHMARK_SALE_FAIL_MS`, `UT_BENCHMARK_INTERACT_WARN_MS`, `UT_BENCHMARK_INTERACT_FAIL_MS`.
- See `docs/performance.md` for full thresholds, hardware assumptions, and offline smoke instructions.

---

## 🔌 Plugin Development

Want to extend Universal Till? Plugins run **in-process as WASM modules**
(via [wazero](https://wazero.io), no cgo) — write the logic in Go
(`GOOS=wasip1 GOARCH=wasm`), Rust, TinyGo, or anything else that compiles to
WASM. One architecture-independent `.wasm` artifact per plugin, Ed25519-signed
and capability-gated (a module gets nothing until the manifest grants it).
Asset-only plugins (themes, language packs) use `runtime: "none"` instead —
no code, just files. Hardware/device plugins needing raw OS access (USB,
serial) run as a supervised process (`runtime: "go"`), the minority case.

**See [PLUGIN_GUIDELINES.md](docs/plugin_guidelines.md) for complete documentation.**

### Plugin Store

Submit your plugins to the marketplace:
- Free hosting and distribution
- Ed25519 signature verification before install
- Build free or paid plugins

### Available Plugins

Every plugin below is free, open source, and downloadable directly —
no account, no marketplace, no payment required, ever. The in-app
marketplace is a one-click convenience layer on top of these same
public releases, nothing more (see
[ADR-0027](https://github.com/universaltill/ut-docs/blob/main/adr/0027-plugin-availability-independent-of-payment.md)).
Also browsable at [universaltill.com/plugins](https://www.universaltill.com/plugins).

Each "Download" link always points at the current latest release —
GitHub's own `/releases/latest/download/` mechanism, no automation to
keep it updated.

| Plugin | Type | Repo | Download |
|---|---|---|---|
| SumUp Card Payments | payment | [ut-plugin-payment-sumup](https://github.com/universaltill/ut-plugin-payment-sumup) | [latest](https://github.com/universaltill/ut-plugin-payment-sumup/releases/latest/download/latest.tar.gz) |
| Stripe Card Payments | payment | [ut-plugin-payment-stripe](https://github.com/universaltill/ut-plugin-payment-stripe) | [latest](https://github.com/universaltill/ut-plugin-payment-stripe/releases/latest/download/latest.tar.gz) |
| QR Pay | payment | [ut-plugin-payment-qrpay](https://github.com/universaltill/ut-plugin-payment-qrpay) | [latest](https://github.com/universaltill/ut-plugin-payment-qrpay/releases/latest/download/latest.tar.gz) |
| Demo Card Terminal | payment | [ut-plugin-payment-demo](https://github.com/universaltill/ut-plugin-payment-demo) | [latest](https://github.com/universaltill/ut-plugin-payment-demo/releases/latest/download/latest.tar.gz) |
| AI Assistant (self-hosted) | integration | [ut-plugin-integration-ai](https://github.com/universaltill/ut-plugin-integration-ai) | [latest](https://github.com/universaltill/ut-plugin-integration-ai/releases/latest/download/latest.tar.gz) |
| Webhook / ERP Connector | integration | [ut-plugin-integration-webhook](https://github.com/universaltill/ut-plugin-integration-webhook) | not packaged yet |
| No-Sale Button | button | [ut-plugin-button-nosale](https://github.com/universaltill/ut-plugin-button-nosale) | [latest](https://github.com/universaltill/ut-plugin-button-nosale/releases/latest/download/latest.tar.gz) |
| Spanish (Español) | language | [ut-plugin-language-es](https://github.com/universaltill/ut-plugin-language-es) | [latest](https://github.com/universaltill/ut-plugin-language-es/releases/latest/download/latest.tar.gz) |
| German (Deutsch) | language | [ut-plugin-language-de](https://github.com/universaltill/ut-plugin-language-de) | [latest](https://github.com/universaltill/ut-plugin-language-de/releases/latest/download/latest.tar.gz) |
| Screen Top Theme | theme | [ut-plugin-theme-screen-top](https://github.com/universaltill/ut-plugin-theme-screen-top) | [latest](https://github.com/universaltill/ut-plugin-theme-screen-top/releases/latest/download/latest.tar.gz) |
| Buttons Left Theme | theme | [ut-plugin-theme-buttons-left](https://github.com/universaltill/ut-plugin-theme-buttons-left) | [latest](https://github.com/universaltill/ut-plugin-theme-buttons-left/releases/latest/download/latest.tar.gz) |
| Midnight Theme | theme | [ut-plugin-theme-midnight](https://github.com/universaltill/ut-plugin-theme-midnight) | [latest](https://github.com/universaltill/ut-plugin-theme-midnight/releases/latest/download/latest.tar.gz) |
| FAQ Page | page | [ut-plugin-faq](https://github.com/universaltill/ut-plugin-faq) | [latest](https://github.com/universaltill/ut-plugin-faq/releases/latest/download/latest.tar.gz) |

---

## 🤝 Contributing

We **love** contributions! Universal Till is built by the community, for the community.

### Ways to Contribute

- 🐛 Report bugs
- 💡 Suggest features
- 📝 Improve documentation
- 🔧 Submit pull requests
- 🔌 Build plugins
- 🌍 Translate to new languages
- 💬 Help others in Discord

### Getting Started

1. Read [CONTRIBUTING.md](CONTRIBUTING.md)
2. Check [open issues](https://github.com/universaltill/universal-till/issues)
3. Join our [Discord](https://discord.gg/universaltill)
4. Fork, code, and submit a PR!

**All contributors are credited in [CONTRIBUTORS.md](CONTRIBUTORS.md) and release notes.**

---

## 🍴 Forking

Want to create a specialized version? **Go for it!**

Universal Till is MIT licensed — you can:
- Fork for specific industries (restaurants, healthcare, etc.)
- Adapt for regional requirements
- Build custom versions for your hardware
- Experiment with new features

**Just maintain the MIT license attribution.**

We'll even link to notable forks in this README! 🎉

### Notable Forks

*Submit a PR to add your fork here!*

---

## 📱 Multi-Platform

### Supported Platforms

- **Linux** (x64, ARM, ARM64) ✅
- **Windows** (x64, ARM64) ✅
- **macOS** (Intel, Apple Silicon) ✅
- **Raspberry Pi** (3, 4, 5, Zero 2 W) ✅
- **Android** (tablets, POS terminals) ✅
- **Web** (any modern browser) ✅

### Tested Hardware

- Raspberry Pi 4 (4GB RAM) - Excellent
- Raspberry Pi 3 (1GB RAM) - Good
- Old Android tablets (2GB+ RAM) - Good
- Sunmi POS terminals - Excellent
- Generic Windows tablets - Excellent
- Old laptops/PCs - Excellent

---

## 🌍 Internationalization

Built into core (`web/locales/`), including full RTL layout:
- English (en)
- Arabic (ar) — RTL
- Persian / Farsi (fa) — RTL
- Turkish (tr)

Available as install-time language plugins:
- German (de) — `ut-plugin-language-de`
- Spanish (es) — `ut-plugin-language-es`

Want to add your language? See [CONTRIBUTING.md](CONTRIBUTING.md) for translation guidelines.

---

## 📊 Roadmap

_Checked against real code and the [ut-docs ADRs](https://github.com/universaltill/ut-docs/tree/main/adr) as of 2026-07-28, not just aspirational — see `docs/germany-pos-parity-backlog.md` and `docs/vertical-features-backlog.md` for the full detail behind the "not built" items below._

### Shipped
- [x] Core POS functionality, offline-first checkout (ADR-0003)
- [x] SQLite database support
- [x] Plugin system architecture — in-process WASM runtime (wazero),
      Ed25519-signed manifests, 20-type plugin taxonomy (ADR-0001, ADR-0002)
- [x] Plugin marketplace with one-click install (`ut-cloud`, ADR-0018) — live at [cloud.universaltill.com](https://cloud.universaltill.com)
- [x] Payment plugins: Stripe, QR Pay, demo card terminal, SumUp (reader-driven)
- [x] Language plugins: German, Spanish (core ships en/ar/fa/tr)
- [x] Theme plugins, FAQ / help plugin
- [x] Self-hosted AI assistant plugin (camera item ID, "Ask your till")
- [x] Webhook connector plugin (`ut-plugin-integration-webhook`) — reference/template for real ERP connectors, not itself a finished SAP/Dynamics integration (ADR-0014)
- [x] Cloud sync service (optional, self-hostable)
- [x] Multi-till LAN sync — one primary, replicas join by QR scan (ADR-0011)
- [x] Universal Till ID — self-hosted Zitadel (ADR-0012)
- [x] Self-order kiosk + item modifiers (ADR-0020) — **in-store, network-attached device; not the same as remote online ordering or per-table ordering below**
- [x] Android app, live-verified (ADR-0023)
- [x] Self-serve vendor registration for the marketplace (ADR-0024)
- [x] Every plugin listed here is free, open source, and directly downloadable — see [Plugins](#available-plugins) and [ADR-0027](https://github.com/universaltill/ut-docs/blob/main/adr/0027-plugin-availability-independent-of-payment.md)
- [x] Tips — `payments.tip_amount` in the core domain model and manual entry via `/api/pos/tender`, tested end to end. Automatic sync of a tip the customer picks on a SumUp reader also ships, but that half is **not yet verified against a live SumUp sandbox** — see [ut-plugin-payment-sumup](https://github.com/universaltill/ut-plugin-payment-sumup)'s README "Tips" section before relying on it in production
- [x] Service charge — `sales.service_charge_amount`, a till-set percentage (`store.service_charge_rate_pct`) automatically added to the sale total, distinct from a discretionary tip (which never affects the total). Shown as its own receipt line when non-zero

### In progress / partially built
- [ ] **ERP integration plugins** — the webhook connector (above) is a working *template*; real per-system connectors (`core-universaltill`/Universal Core, SAP, Dynamics/LS Central) aren't built yet (ADR-0014)
- [ ] **Payment orchestration / least-cost routing** — target markets decided (UK, UAE, Qatar, +), but which providers to route between and build-vs-buy for the multi-acquirer connections are still open (ADR-0016)
- [ ] **iOS app** — Android is real and shipped; iOS is untouched (ADR-0023)
- [ ] **Delivery-platform integrations** (Deliveroo, Uber Eats, Just Eat) — the plugin taxonomy already has a `delivery` type and kitchen ticket printing already understands an order-type/table concept, but no actual provider plugin exists yet
- [ ] Advanced inventory management
- [ ] Multi-location support
- [ ] More payment/marketplace/accounting integrations (Square, PayPal, eBay, Amazon, QuickBooks, Xero, QuickFile)

### Confirmed not built yet (verified against the code, not guessed)
- [ ] **Tip reporting/export line** — tips are captured and persisted but not yet broken out on any report/export (often handled separately for German payroll/tax purposes; needs an accountant-verified spike, not guessed at)
- [ ] **Per-staff sale/commission attribution** — needed for e.g. a barber shop splitting one payment's revenue across the different staff who each served that customer
- [ ] **Booking / calendar / reservation** — needed for both restaurant table bookings and appointment-based shops (barbers, salons); one underlying feature, not two
- [ ] **Table-side ordering** (customer's own phone, QR-per-table) and **true remote/online ordering** — neither exists; don't confuse either with the self-order kiosk above
- [ ] **Order-for-collection** as its own order type (dine-in/takeaway/delivery/phone exists; collection doesn't yet)
- [ ] **Germany fiscal compliance** (TSE signing, DSFinV-K export) — a legal requirement to sell into Germany, zero code exists (see `docs/germany-pos-parity-backlog.md`)
- [ ] **Dine-in vs. takeaway VAT rate switching** — the per-line tax rate field exists, and an `OrderType` field exists for kitchen printing, but nothing connects the two yet
- [ ] Setup wizard shop type + address capture, eager best-effort cloud registration (ADR-0026, drafted not built)

### Long-term
- [ ] Employee scheduling, customer loyalty programs
- [ ] Kitchen display system
- [ ] iOS app, further mobile platform work
- [ ] Advanced analytics and BI
- [ ] Hardware manufacturer partnerships, white-label licensing

**See [our project board](https://github.com/universaltill/universal-till/projects) for detailed progress.**

---

## 💰 Pricing

| Feature | Free (Local) | Cloud Starter | Cloud Pro | Enterprise |
|---------|-------------|---------------|-----------|------------|
| **Core POS** | ✅ Unlimited | ✅ Unlimited | ✅ Unlimited | ✅ Unlimited |
| **Devices** | ✅ Unlimited | ✅ 3 devices | ✅ Unlimited | ✅ Unlimited |
| **Offline Mode** | ✅ Forever | ✅ Forever | ✅ Forever | ✅ Forever |
| **Local Plugins** | ✅ All | ✅ All | ✅ All | ✅ All |
| **Cloud Sync** | ❌ | ✅ | ✅ | ✅ |
| **Cloud Backup** | ❌ | 30 days | Unlimited | Unlimited |
| **Advanced Analytics** | ❌ | Basic | ✅ Advanced | ✅ Custom |
| **Multi-location** | ❌ | ❌ | ✅ | ✅ |
| **Support** | Community | Email | Priority | Dedicated |
| **Price** | **FREE** | **$??/mo** | **$??/mo** | **Custom** |

**Note:** Core POS is free forever. Cloud services are optional enhancements.

---

## 🛡️ Security

We take security seriously:

- Regular security audits
- Automatic plugin scanning
- Encrypted data transmission
- Local data encryption (optional)
- Role-based access control
- Audit logs

**Found a security issue?** Please email security@universaltill.com

**DO NOT** create a public GitHub issue for security vulnerabilities.

---

## 📄 License

Universal Till Core is licensed under the **MIT License** - see [LICENSE](LICENSE) for details.

This means you can:
- Use it commercially
- Modify it
- Distribute it
- Fork it
- Build plugins for it

No restrictions, no fees, forever.

**Cloud services are proprietary; both Universal Till and third-party vendors may offer paid/closed-source plugins too (see LICENSE's "Plugin Licensing").** What's guaranteed instead ([ADR-0027](https://github.com/universaltill/ut-docs/blob/main/adr/0027-plugin-availability-independent-of-payment.md)): core POS stays free forever, and any plugin — ours or a vendor's — that *is* published free and open source never quietly goes proprietary later. See the [Available Plugins](#available-plugins) table above for everything that's free today, and [LICENSE](LICENSE) for complete details.

---

## 🙏 Credits

Universal Till is built by an amazing community of developers, designers, and contributors from around the world.

**See [CONTRIBUTORS.md](CONTRIBUTORS.md) for the full list.**

Special thanks to:
- All our open source contributors
- The Go community
- Hardware partners
- Plugin developers
- Beta testers
- Community moderators

---

## 📞 Contact & Support

- **Website**: https://universaltill.com
- **Documentation**: https://docs.universaltill.com
- **Discord**: https://discord.gg/universaltill
- **GitHub Issues**: https://github.com/universaltill/universal-till/issues
- **Email**: hello@universaltill.com

### Community

- 💬 [Discord](https://discord.gg/universaltill) - Chat with the community
- 🐦 [Twitter](https://twitter.com/universaltill) - Latest news and updates
- 📺 [YouTube](https://youtube.com/@universaltill) - Tutorials and demos
- 📝 [Blog](https://blog.universaltill.com) - Deep dives and announcements

---

## ⭐ Star History

If Universal Till helps your business, please star the repo! It helps others discover the project.

[![Star History](https://api.star-history.com/svg?repos=universaltill/universal-till&type=Date)](https://star-history.com/#universaltill/universal-till&Date)

---

## 🚀 Join the Revolution

Tired of expensive POS systems that lock you in? Join thousands of businesses worldwide using Universal Till.

**Get Started**: [Download Now](https://github.com/universaltill/universal-till/releases/latest) | [Read Docs](https://docs.universaltill.com) | [Join Discord](https://discord.gg/universaltill)

---

<p align="center">
Made with ❤️ by the Universal Till community
</p>

<p align="center">
<a href="LICENSE">MIT License</a> •
<a href="CONTRIBUTING.md">Contributing</a> •
<a href="docs/plugin_guidelines.md">Plugin Development</a> •
<a href="https://discord.gg/universaltill">Discord</a>
</p>
