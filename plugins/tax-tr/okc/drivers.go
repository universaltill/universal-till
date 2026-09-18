package okc

import "fmt"

// DriverNames lists the drivers this build knows, for settings validation
// and the status page. Only "bridge" is complete today; the maker drivers
// are scaffolds that fail closed until their wire format is filled in
// against the maker's integrator documentation and a test device
// (docs/arch/turkey-launch-playbook.md steps 3–4, ut-docs#1280).
//
// There is deliberately no "gmp3" entry. GİB's GMP-3 v5.0 §3.3 requires
// PC-hosted sales software to link the ÖKC maker's own compiled GMP-3
// library, and requires browser-served software to reach it only through a
// separate middleware process running on that PC — a from-spec GMP-3 driver
// inside this WASM plugin can never be built (ADR-0102 Decision 4). A maker
// whose only interface is that library is reached through a small bridge
// process on the machine wired to the device, speaking the Universal Till
// ÖKC bridge protocol v0 (`bridge.go`, simulator `okc/sim`; wire format in
// `ut-docs/reference/okc-bridge-protocol.md`). Whether a maker's own
// REST/cloud API (the hugin-pclink / pavo-rest / token-x scaffolds below)
// counts as "the maker's library" under §3.3 is confirmed per maker at
// integrator registration — the ADR leaves it open on purpose.
var DriverNames = []string{"bridge", "hugin-pclink", "pavo-rest", "token-x"}

// NewDriver picks the driver named in cfg.Driver, wrapped with
// NewValidatingDriver so every driver it returns enforces the "no usable
// receipt_no → refuse" invariant, whether or not that driver's own Sale/
// Refund already checks it (ut-docs#1780).
func NewDriver(t Transport, cfg Config) (Driver, error) {
	cfg = cfg.Normalize()
	var d Driver
	switch cfg.Driver {
	case "bridge":
		d = NewBridgeDriver(t, cfg)
	case "hugin-pclink":
		d = &HuginPCLinkDriver{Transport: t, Config: cfg}
	case "pavo-rest":
		d = &PavoRESTDriver{Transport: t, Config: cfg}
	case "token-x":
		d = &TokenXDriver{Transport: t, Config: cfg}
	default:
		return nil, fmt.Errorf("%w: %q (known: %v)", ErrUnknownDriver, cfg.Driver, DriverNames)
	}
	return NewValidatingDriver(d), nil
}

// HuginPCLinkDriver will speak Hugin PC Link — Hugin's HTTPS/REST
// integration for external sales software (developer.hugin.com.tr). A
// scaffold like the other maker drivers below: it fails closed until
// endpoint paths and payloads come from Hugin's developer portal after
// integrator registration.
type HuginPCLinkDriver struct {
	Transport Transport
	Config    Config
}

func (d *HuginPCLinkDriver) Sale(SaleRequest) (Evidence, error) {
	return Evidence{}, fmt.Errorf("%w: hugin-pclink (needs Hugin PC Link API access from developer.hugin.com.tr)", ErrDriverNotImplemented)
}
func (d *HuginPCLinkDriver) Refund(RefundRequest) (Evidence, error) {
	return Evidence{}, fmt.Errorf("%w: hugin-pclink", ErrDriverNotImplemented)
}
func (d *HuginPCLinkDriver) Status() (Status, error) {
	return Status{}, fmt.Errorf("%w: hugin-pclink", ErrDriverNotImplemented)
}

// PavoRESTDriver will speak Pavo's REST integration for sales applications
// (API key issued in the Pavo portal, device set to "REST" under Satış
// Uygulamaları). A scaffold like the other maker drivers: it fails closed
// until Pavo's sales-application REST documentation and a test API key
// are in hand.
type PavoRESTDriver struct {
	Transport Transport
	Config    Config
}

func (d *PavoRESTDriver) Sale(SaleRequest) (Evidence, error) {
	return Evidence{}, fmt.Errorf("%w: pavo-rest (needs Pavo's sales-application REST documentation and an API key)", ErrDriverNotImplemented)
}
func (d *PavoRESTDriver) Refund(RefundRequest) (Evidence, error) {
	return Evidence{}, fmt.Errorf("%w: pavo-rest", ErrDriverNotImplemented)
}
func (d *PavoRESTDriver) Status() (Status, error) {
	return Status{}, fmt.Errorf("%w: pavo-rest", ErrDriverNotImplemented)
}

// TokenXDriver will speak Token Finansal Teknolojiler's TokenX Connect
// (Beko-branded devices; client-id/secret from developer.tokeninc.com,
// terminal pairing by QR). TokenX Connect is a cloud API, so this driver
// would go through the http_request host function (net:<host>) rather
// than tcp_*; kept on the same Driver interface so core never cares.
type TokenXDriver struct {
	Transport Transport
	Config    Config
}

func (d *TokenXDriver) Sale(SaleRequest) (Evidence, error) {
	return Evidence{}, fmt.Errorf("%w: token-x (needs TokenX Connect credentials from developer.tokeninc.com)", ErrDriverNotImplemented)
}
func (d *TokenXDriver) Refund(RefundRequest) (Evidence, error) {
	return Evidence{}, fmt.Errorf("%w: token-x", ErrDriverNotImplemented)
}
func (d *TokenXDriver) Status() (Status, error) {
	return Status{}, fmt.Errorf("%w: token-x", ErrDriverNotImplemented)
}
