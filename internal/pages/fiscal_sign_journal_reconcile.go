package pages

import (
	"context"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

const fiscalSignJournalScanMax = 500

// ReconcileFiscalSignJournal is the boot pass over fiscal_sign_journal
// (ut-docs#7021). An asked row older than startedAt whose sale exists is
// settled (deleted); one with no sale becomes warned. Every open row then
// raises its keyed Problems item again, as the ring is in-memory. It makes no
// network call and never re-asks a signer (ADR-0056).
func ReconcileFiscalSignJournal(ctx context.Context, d *common.Deps, startedAt time.Time) (settled, warned, open int) {
	repo := data.NewFiscalSignJournalRepo(d.Db)
	asked, err := repo.ListAskedBefore(ctx, startedAt, fiscalSignJournalScanMax)
	if err != nil {
		logging.L().Errorf("fiscal signing: boot pass could not read the sign journal: %v (ut-docs#7021)", err)
		return
	}
	for _, e := range asked {
		if ctx.Err() != nil {
			break
		}
		exists, err := repo.SaleExists(ctx, e.SaleID)
		if err != nil {
			logging.L().Warnf("fiscal signing: boot pass could not check sale %s: %v (ut-docs#7021)", e.SaleID, err)
			continue
		}
		if exists {
			if err := repo.Delete(ctx, e.SaleID); err == nil {
				settled++
			}
			continue
		}
		if err := repo.MarkWarned(ctx, e.SaleID, data.FiscalSignReasonRestart); err != nil {
			logging.L().Warnf("fiscal signing: boot pass could not warn about sale %s: %v (ut-docs#7021)", e.SaleID, err)
			continue
		}
		warned++
	}
	rows, err := repo.ListOpen(ctx, fiscalSignJournalScanMax)
	if err != nil {
		logging.L().Errorf("fiscal signing: boot pass could not list open sign journal rows: %v (ut-docs#7021)", err)
		return
	}
	for _, e := range rows {
		raiseFiscalSignedNoSale(e)
	}
	open = len(rows)
	if settled+warned+open > 0 {
		logging.L().Infof("fiscal signing: boot pass over the sign journal: %d settled, %d newly warned, %d open (ut-docs#7021)", settled, warned, open)
	}
	return
}
