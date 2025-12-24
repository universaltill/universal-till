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
- Payment processors (Stripe, Square, PayPal, regional providers)
- Marketplaces (eBay, Amazon, Shopify integration)
- Delivery services (Uber Eats, DoorDash, Deliveroo)
- Accounting systems (QuickBooks, Xero, Wave)
- ERP integration (Odoo, ERPNext)
- Industry-specific features

---

## 🚀 Quick Start

### Option 1: Download Pre-Built Binary

```bash
# Download latest release
wget https://github.com/universaltill/universal-till/releases/latest/download/universal-till-linux-amd64

# Make executable
chmod +x universal-till-linux-amd64

# Run
./universal-till-linux-amd64
```

Open http://localhost:8080 in your browser.

### Option 2: Build from Source

```bash
# Clone the repository
git clone https://github.com/universaltill/universal-till.git
cd universal-till

# Build
make build

# Run
./bin/edge
```

Open http://localhost:8080

### Option 3: Docker

```bash
# Clone the repository
git clone https://github.com/universaltill/universal-till.git
cd universal-till

# Copy and configure environment
cp edge.env.example edge.env.dev
# Edit edge.env.dev with your settings

# Run with Docker Compose
docker compose -f docker-compose.edge.yml up --build
```

Open http://localhost:8080

### Option 4: Development Mode (with Mock Marketplace)

For development and testing with marketplace features:

```bash
# 1. Start mock marketplace (in one terminal)
go run scripts/mock-marketplace/main.go
# Mock runs on :8082

# 2. Start POS with dev environment (in another terminal)
./scripts/dev.sh
# Or manually:
# export $(grep -v '^#' pos.env.dev | grep -v '^$' | xargs)
# make build && ./bin/unitill-pos
```

This loads configuration from `pos.env.dev` which includes:
- Mock marketplace endpoint (http://localhost:8082)
- Dev OAuth credentials
- BCP 47 locale settings

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

# Marketplace integration (cloud plugin store)
UT_MARKETPLACE_ENDPOINT_URL=http://127.0.0.1:8081  # Production marketplace API
# For local testing with mock: http://localhost:8082
UT_MARKETPLACE_CLIENT_ID=              # OAuth2 client ID (leave empty for local dev)
UT_MARKETPLACE_CLIENT_SECRET=          # OAuth2 client secret
UT_MARKETPLACE_API_VERSION=1.0.0       # Marketplace API version
UT_MARKETPLACE_TELEMETRY_OPT_IN=false  # Send usage telemetry to marketplace
UT_DEV_MODE=false                      # Enable developer mode features
UT_MARKETPLACE_DEV_OVERRIDE_URL=       # Local marketplace override (dev mode only)

# Optional
UT_SAMPLES_DIR=/path/to/images        # Sample product images
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

Want to extend Universal Till? Plugins are easy to build!

```go
package main

import "github.com/universaltill/plugin-sdk"

type MyPlugin struct{}

func (p *MyPlugin) Initialize(config plugin.Config) error {
    // Setup your plugin
    return nil
}

func (p *MyPlugin) ProcessTransaction(tx plugin.Transaction) error {
    // Handle transaction
    return nil
}

func main() {
    plugin.Serve(&MyPlugin{})
}
```

**See [PLUGIN_GUIDELINES.md](docs/plugin_guidelines.md) for complete documentation.**

### Plugin Store

Submit your plugins to our **free** plugin store:
- Free hosting and distribution
- Automatic security scanning
- User ratings and reviews
- Build free or paid plugins (20% commission on paid)

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

Currently supported languages:
- English (en)
- More coming soon!

Want to add your language? See [CONTRIBUTING.md](CONTRIBUTING.md) for translation guidelines.

---

## 📊 Roadmap

### Current Focus (Q1 2025)
- [x] Core POS functionality
- [x] SQLite database support
- [ ] Plugin system architecture
- [ ] Payment processor plugins (Stripe, Square)
- [ ] Cloud sync service (optional)
- [ ] Mobile app

### Upcoming (Q2-Q3 2025)
- [ ] Advanced inventory management
- [ ] Employee scheduling
- [ ] Customer loyalty programs
- [ ] Marketplace integrations (eBay, Amazon)
- [ ] Accounting integrations (QuickBooks, Xero)
- [ ] Multi-location support
- [ ] Kitchen display system
- [ ] Table management (restaurants)

### Long-term
- [ ] Hardware manufacturer partnerships
- [ ] White-label licensing
- [ ] Advanced analytics and BI
- [ ] AI-powered insights
- [ ] Multi-channel commerce platform

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

**Cloud services and some premium plugins are proprietary.** See [LICENSE](LICENSE) for complete details.

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
