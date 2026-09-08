package okc

import "fmt"

// DriverNames lists the drivers this build knows, for settings validation
// and the status page. Only "bridge" is complete today; the maker drivers
// are scaffolds that fail closed until their wire format is filled in
// against the maker's integrator documentation and a test device
// (docs/arch/turkey-launch-playbook.md steps 3–4, ut-docs#1280).
var DriverNames = []string{"bridge", "gmp3", "hugin-pclink", "pavo-rest", "token-x"}

// NewDriver picks the driver named in cfg.Driver.
func NewDriver(t Transport, cfg Config) (Driver, error) {
	cfg = cfg.Normalize()
	switch cfg.Driver {
	case "bridge":
		return NewBridgeDriver(t, cfg), nil
	case "gmp3":
		return &GMP3Driver{Transport: t, Config: cfg}, nil
	case "hugin-pclink":
		return &HuginPCLinkDriver{Transport: t, Config: cfg}, nil
	case "pavo-rest":
		return &PavoRESTDriver{Transport: t, Config: cfg}, nil
	case "token-x":
		return &TokenXDriver{Transport: t, Config: cfg}, nil
	default:
		return nil, fmt.Errorf("%w: %q (known: %v)", ErrUnknownDriver, cfg.Driver, DriverNames)
	}
}

// GMP3Driver will speak GİB's "ÖKC – Harici Donanım ve Yazılım Haberleşme
// Protokolü GMP-3" (v5.0, 2 Aug 2018) wired mode ("kasa modu"): the till
// and the device share a LAN, the till pushes the basket, the device takes
// payment on its own EFT-POS and prints the mali fiş. The message framing,
// field codes and the pairing/activation handshake are in the GMP-3 PDF on
// ynokc.gib.gov.tr and in each maker's integrator pack — neither was
// reachable from the session that wrote this file, so nothing here is
// guessed: every call fails closed with ErrDriverNotImplemented until the
// format is filled in against a real device.
type GMP3Driver struct {
	Transport Transport
	Config    Config
}

func (d *GMP3Driver) Sale(SaleRequest) (Evidence, error) {
	return Evidence{}, fmt.Errorf("%w: gmp3 (needs the GMP-3 v5.0 framing from ynokc.gib.gov.tr and the maker's activation for this device)", ErrDriverNotImplemented)
}
func (d *GMP3Driver) Refund(RefundRequest) (Evidence, error) {
	return Evidence{}, fmt.Errorf("%w: gmp3", ErrDriverNotImplemented)
}
func (d *GMP3Driver) Status() (Status, error) {
	return Status{}, fmt.Errorf("%w: gmp3", ErrDriverNotImplemented)
}

// HuginPCLinkDriver will speak Hugin PC Link — Hugin's HTTPS/REST
// integration for external sales software (developer.hugin.com.tr). Same
// status as GMP3Driver: endpoint paths and payloads come from Hugin's
// developer portal after integrator registration.
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
// Uygulamaları). Same status as GMP3Driver.
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
