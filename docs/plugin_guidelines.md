# Universal Till Plugin Development Guidelines

**Version 1.0 - December 2025**

Welcome to Universal Till plugin development! This guide will help you build, test, and publish plugins for the Universal Till ecosystem.

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

### Plugin Execution Models

Plugins can run in two modes:

**1. Local Mode (Runs on Device)**
```
POS Device → Plugin (local process) → External API
```
- Best for: Simple integrations, privacy-focused merchants
- Pros: Works offline, no cloud dependency
- Cons: Merchant manages API keys

**2. Cloud-Enhanced Mode (Optional)**
```
POS Device → Universal Till Cloud → Plugin (cloud) → External API
```
- Best for: Advanced features, multiple devices
- Pros: Centralized config, advanced features, analytics
- Cons: Requires internet, cloud subscription

**Your plugin can support both!**

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

```json
{
  "id": "com.example.stripe",
  "name": "Stripe Payments",
  "version": "1.0.0",
  "description": "Accept credit card payments via Stripe Terminal",
  "author": {
    "name": "Your Name",
    "email": "you@example.com",
    "url": "https://example.com"
  },
  "license": "MIT",
  "category": "payment",
  "tags": ["payment", "credit-card", "stripe"],
  "repository": "https://github.com/yourusername/ut-stripe",
  
  "modes": {
    "local": {
      "supported": true,
      "requires": ["stripe_api_key", "stripe_secret_key"],
      "permissions": ["network", "storage"],
      "features": ["process_payment", "refund", "void"]
    },
    "cloud": {
      "supported": true,
      "requires": ["ut_cloud_token"],
      "permissions": ["network", "storage", "analytics"],
      "features": ["process_payment", "refund", "void", "fraud_detection", "reconciliation"]
    }
  },
  
  "compatibility": {
    "min_version": "1.0.0",
    "platforms": ["linux", "windows", "darwin", "android"]
  },
  
  "pricing": {
    "model": "free",  // or "one_time", "subscription"
    "price": 0,
    "currency": "USD"
  }
}
```

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

Declare all permissions in manifest.json:

```json
{
  "permissions": [
    "network",      // Make external HTTP requests
    "storage",      // Store local data
    "hardware",     // Access USB/serial devices
    "sensitive_data" // Access customer/payment data
  ]
}
```

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