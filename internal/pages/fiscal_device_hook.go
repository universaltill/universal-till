package pages

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// Fiscal-device tender support (Turkey's YN ÖKC, internal/fiscal/device.go):
// the device is a payment method whose plugin takes the money and prints
// the legal receipt during the blocking payment.<key>.authorize call. This
// file is the core-side half — what the till sends such a plugin beyond
// the payment-provider contract's `method, amount, reference, plugin_id`,
// and what it keeps from the answer.

// fiscalDeviceAuditConfirmed is the audit action written the first time a
// fiscal device answers with a receipt on this till — the moment
// fiscal.tse_configured flips true for a device market (the device has
// proven it prints), mirroring Germany's flip on confirmed TSE credential
// receipt (setup_tse.go). Also written by the /fiscal-device page's manual
// confirm.
const fiscalDeviceAuditConfirmed = "fiscal_device_confirmed"

// fiscalDeviceAuditUnpaired is written when a manager unpairs the device on
// /fiscal-device (fiscal.tse_configured back to false — the next TR sale as
// system of record is refused again, ADR-0048 Decision 2.2).
const fiscalDeviceAuditUnpaired = "fiscal_device_unpaired"

// deviceAuthorizePayloadExtras returns the additive authorize-payload
// fields a fiscal-device plugin needs to have the device print a legal
// receipt: the basket lines with their VAT rates, the sale-level amounts
// and the currency. Additive on purpose — every existing payment plugin
// (card terminal, QR, demo) ignores fields it never asked for, so the
// payment-provider contract's base shape is untouched. Amounts stay
// integer minor units (ADR-0004).
func deviceAuthorizePayloadExtras(in pos.SaleInput, payments []pos.PaymentInput) map[string]any {
	var total int64
	for _, p := range payments {
		total += p.Amount.Minor()
	}
	lines := make([]map[string]any, 0, len(in.Lines))
	for _, l := range in.Lines {
		lines = append(lines, map[string]any{
			"name":          l.Name,
			"qty":           l.Qty,
			"unit_price":    l.UnitPrice.Minor(),
			"tax_rate_bp":   l.TaxRateBasisPoints,
			"line_discount": l.LineDiscount.Minor(),
		})
	}
	return map[string]any{
		"currency":       in.Currency,
		"total":          total,
		"tax_inclusive":  in.TaxInclusive,
		"sale_discount":  in.SaleDiscount.Minor(),
		"service_charge": in.ServiceCharge.Minor(),
		"lines":          lines,
	}
}

// pickDeviceEvidence keeps the first valid `fiscal_device` answer across a
// sale's payment legs. A sale gets one device receipt (the plugin refuses a
// split tender), so first-wins is the only sensible rule; a later leg's
// evidence, if a plugin ever returned one, is logged and dropped rather
// than silently replacing what was already printed.
func pickDeviceEvidence(current *fiscal.DeviceEvidence, resp json.RawMessage) *fiscal.DeviceEvidence {
	ev, ok := fiscal.ParseDeviceEvidence(resp)
	if !ok {
		return current
	}
	if current != nil {
		logging.L().Warnf("fiscal device: a second payment leg returned receipt %s after %s was already recorded for this sale — keeping the first", ev.ReceiptNo, current.ReceiptNo)
		return current
	}
	return ev
}

// recordFiscalDeviceEvidence persists what the device printed against the
// completed sale (or refund) and, on the FIRST receipt this till ever
// records, flips fiscal.tse_configured true with an audit marker: for a
// device market that flag means "the device is paired and has proven it
// prints", and the proof is the receipt itself. Best-effort after the
// fact, exactly like recordFiscalTSEEvidence: the sale is committed and
// the device has printed — a failed bookkeeping write is journaled, never
// unwinds either.
func recordFiscalDeviceEvidence(ctx context.Context, d *common.Deps, repo *data.POSRepo, saleID, actorID string, ev *fiscal.DeviceEvidence) {
	if ev == nil || !ev.Valid() {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := repo.RecordFiscalDeviceReceipt(ctx, data.FiscalDeviceReceipt{
		SaleID:      saleID,
		DeviceKind:  ev.Kind,
		Maker:       ev.Maker,
		Serial:      ev.Serial,
		ReceiptNo:   ev.ReceiptNo,
		ReceiptKind: ev.ReceiptKind,
		ZNo:         ev.ZNo,
		IssuedAt:    ev.IssuedAt,
	}); err != nil {
		logging.L().Errorf("fiscal device: persist receipt %s for sale %s: %v", ev.ReceiptNo, saleID, err)
		if auditErr := repo.InsertAudit(ctx, nil, actorID, "sale", saleID, "fiscal_evidence_persist_failed", map[string]any{
			"reason":    err.Error(),
			"failed_at": now,
		}, now, ""); auditErr != nil {
			log.Printf("fiscal device: fiscal_evidence_persist_failed audit marker for sale %s failed: %v", saleID, auditErr)
		}
		return
	}
	if d == nil || d.Settings == nil {
		return
	}
	configured, _, err := d.Settings.Get(ctx, fiscal.KeyTSEConfigured)
	if err != nil || settingIsTrue(configured) {
		return
	}
	if err := d.Settings.Set(ctx, fiscal.KeyTSEConfigured, "true"); err != nil {
		logging.L().Errorf("fiscal device: mark device confirmed after first receipt: %v", err)
		return
	}
	if auditErr := repo.InsertAudit(ctx, nil, actorID, "fiscal_device", saleID, fiscalDeviceAuditConfirmed, map[string]any{
		"maker":      ev.Maker,
		"serial":     ev.Serial,
		"receipt_no": ev.ReceiptNo,
		"source":     "first_receipt",
	}, now, ""); auditErr != nil {
		log.Printf("fiscal device: %s audit marker failed: %v", fiscalDeviceAuditConfirmed, auditErr)
	}
}

// settingIsTrue mirrors fiscal.boolSetting's accepted spellings.
func settingIsTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "on":
		return true
	}
	return false
}
