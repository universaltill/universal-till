package pages

import (
	"context"
	"encoding/json"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#7021 (ADR-0044 D1 amendment): a sent fiscal.sign.ask is journaled
// before it is sent. If CompleteSale then fails the signer's answer is kept as
// "signed, no sale" with an audit row and a keyed Problems item. The
// transaction is never re-signed or voided by core (ADR-0056).

// fiscalSignedNoSaleProblemPrefix + sale id is the Problems-ring key.
const fiscalSignedNoSaleProblemPrefix = "fiscal.signed_no_sale."

// beginFiscalSignJournal writes the asked row. Amount is the payload's total
// (int64 minor units, the DTO boundary).
func beginFiscalSignJournal(ctx context.Context, d *common.Deps, in *pos.SaleInput, p fiscalSignAskPayload, actorID string) error {
	return data.NewFiscalSignJournalRepo(d.Db).Begin(ctx, data.FiscalSignEntry{
		SaleID:      in.SaleID,
		SaleType:    defaultedSaleType(in.SaleType),
		AmountMinor: p.Total,
		Currency:    p.Currency,
		ActorID:     actorID,
		AskedAt:     time.Now().UTC(),
	})
}

// fiscalOutcomeName is the journal's outcome column value.
func fiscalOutcomeName(o fiscalSignOutcome) string {
	switch o {
	case fiscalSignApproved:
		return "approved"
	case fiscalSignNoOpinion:
		return "no-opinion"
	case fiscalSignFailedBackend:
		return "failed-backend"
	case fiscalSignFailedEntry:
		return "failed-entry"
	case fiscalSignCannotSign:
		return "cannot-sign"
	default:
		return "unknown"
	}
}

// recordFiscalSignedNoSale is called after a failed CompleteSale. It is a
// no-op unless a signer was asked and did not refuse the sale outright.
func recordFiscalSignedNoSale(ctx context.Context, d *common.Deps, in pos.SaleInput, res fiscalSignResult, actorID string, cause error) {
	if !res.Asked || res.Outcome == fiscalSignCannotSign || in.SaleID == "" {
		return
	}
	repo := data.NewFiscalSignJournalRepo(d.Db)
	e := data.FiscalSignEntry{
		SaleID:  in.SaleID,
		Outcome: fiscalOutcomeName(res.Outcome),
		Reason:  classifyTenderError(cause), // a localized-key class, never raw SQL
	}
	if res.Evidence != nil {
		if b, err := json.Marshal(res.Evidence); err == nil {
			e.EvidenceJSON = string(b)
		}
	}
	if res.Receipt != nil {
		if b, err := json.Marshal(res.Receipt); err == nil {
			e.ReceiptJSON = string(b)
		}
	}
	if err := repo.MarkSignedNoSale(ctx, e); err != nil {
		// The row stays asked; the boot pass turns it into warned.
		logging.L().Errorf("fiscal signing: could not mark sale %s signed-no-sale: %v (ut-docs#7021)", in.SaleID, err)
	}
	got, ok, err := repo.Get(ctx, in.SaleID)
	if err != nil || !ok {
		got = data.FiscalSignEntry{SaleID: in.SaleID, SaleType: defaultedSaleType(in.SaleType), Outcome: e.Outcome, Reason: e.Reason}
	}
	payload := map[string]any{
		"sale_type":    got.SaleType,
		"amount_minor": got.AmountMinor,
		"currency":     got.Currency,
		"outcome":      e.Outcome,
		"reason":       e.Reason,
	}
	if res.Evidence != nil {
		payload["signature_counter"] = res.Evidence.SignatureCounter
		payload["transaction_number"] = res.Evidence.TransactionNumber
	}
	if aerr := data.NewPOSRepo(d.Db).InsertAudit(ctx, nil, actorID, "sale", in.SaleID, "fiscal_signed_no_sale", payload, time.Now().UTC().Format(time.RFC3339), ""); aerr != nil {
		logging.L().Warnf("fiscal signing: audit row for signed-no-sale %s failed: %v (ut-docs#7021)", in.SaleID, aerr)
	}
	raiseFiscalSignedNoSale(got)
}

// closeFiscalSignJournal drops the asked row once the sale committed.
func closeFiscalSignJournal(ctx context.Context, d *common.Deps, saleID string) {
	if err := data.NewFiscalSignJournalRepo(d.Db).Delete(ctx, saleID); err != nil {
		logging.L().Warnf("fiscal signing: could not close the journal row for sale %s: %v (ut-docs#7021)", saleID, err)
	}
}

// raiseFiscalSignedNoSale raises the keyed Problems item. No compliance claim
// (ADR-0040): it says what happened and what to keep.
func raiseFiscalSignedNoSale(e data.FiscalSignEntry) {
	logging.L().WarnProblemf(fiscalSignedNoSaleProblemPrefix+e.SaleID,
		"fiscal signing: %s %s (%d %s) was signed by the TSE but the till could not save it (%s); no sale, no receipt; the payment was returned; keep this record for your tax adviser (ADR-0044 D1, ut-docs#7021)",
		e.SaleType, e.SaleID, e.AmountMinor, e.Currency, fiscalSignJournalReasonText(e))
}

func fiscalSignJournalReasonText(e data.FiscalSignEntry) string {
	if e.Reason == data.FiscalSignReasonRestart {
		return "the till restarted before the sale was saved"
	}
	if e.Reason == "" {
		return "reason not recorded"
	}
	return e.Reason
}
