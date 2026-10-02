package pages

import (
	"context"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// forgetCustomerOnBaskets (ut-docs#3253) detaches an erased customer from
// the till's open baskets, so the next Hold or sale can't write their id
// and name back into held_sales after EraseCustomer stripped them.
func forgetCustomerOnBaskets(d *common.Deps, customerID string) {
	if customerID == "" {
		return
	}
	for _, e := range []*pos.Service{d.Engine, d.KioskEngine} {
		if e != nil && e.CustomerID() == customerID {
			e.SetCustomer("", "")
		}
	}
}

// basketCustomersKnown returns the open baskets' customer ids that resolve
// to a customer row right now. A replica checks them again after an admin
// pull: one that no longer resolves was erased on the primary.
func basketCustomersKnown(ctx context.Context, d *common.Deps, repo *data.POSRepo) []string {
	var ids []string
	for _, e := range []*pos.Service{d.Engine, d.KioskEngine} {
		if e == nil {
			continue
		}
		if id := e.CustomerID(); id != "" {
			if _, _, ok := repo.LookupCustomer(ctx, id); ok {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// forgetCustomersGoneSince detaches every id from basketCustomersKnown
// that no longer resolves (deleted, or retired as an anonymous shell).
func forgetCustomersGoneSince(ctx context.Context, d *common.Deps, repo *data.POSRepo, known []string) {
	for _, id := range known {
		if _, _, ok := repo.LookupCustomer(ctx, id); !ok {
			forgetCustomerOnBaskets(d, id)
		}
	}
}
