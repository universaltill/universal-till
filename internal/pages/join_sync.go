package pages

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/universaltill/universal-till/internal/catalogpull"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// The join flow's "Syncing your shop" state (ut-docs#5098, ADR-0166 §1 step
// 6): after a till pairs with an existing cloud shop the first check-in and
// catalogue pull run in the background. Until the pull has finished, the
// landing page says so instead of offering the empty-shop import welcome;
// once products exist the till goes to the sale screen. Never a blocker: the
// state has a time window, so an offline till is not stuck on it.

// joinSyncWindow bounds the syncing state: past it the landing page falls
// back to what the catalogue really holds.
const joinSyncWindow = 3 * time.Minute

type joinSyncResult int

const (
	// joinSyncSettled: nothing is pending and the catalogue is empty — the
	// import welcome is true.
	joinSyncSettled joinSyncResult = iota
	// joinSyncWaiting: the first pull has not finished and no product has
	// arrived yet.
	joinSyncWaiting
	// joinSyncHasProducts: the shop's products are on this till.
	joinSyncHasProducts
)

// markJoinedShop records that the wizard just paired this till with an
// existing cloud shop. Best-effort: without it the page behaves as before.
func markJoinedShop(ctx context.Context, d *common.Deps) {
	// settings-write:allow first-boot wizard of a main till
	if err := d.Settings.Set(ctx, catalogpull.KeyJoinSince, strconv.FormatInt(time.Now().Unix(), 10)); err != nil {
		logging.L().Warnf("setup wizard: record the join time: %v", err)
	}
}

// joinSyncPending reports whether a joined till still waits for its first
// catalogue pull: it joined within joinSyncWindow, the pull has not
// finished, and the cloud has not said the catalogue is the till's own (no
// pull will come). Any read error is "not pending": the page then tells the
// truth about the catalogue.
func joinSyncPending(ctx context.Context, d *common.Deps) bool {
	raw, ok, err := d.Settings.Get(ctx, catalogpull.KeyJoinSince)
	if err != nil || !ok {
		return false
	}
	since, perr := strconv.ParseInt(raw, 10, 64)
	// A clock that went backwards (no RTC) reads as expired, never as an
	// endless wait.
	if el := time.Since(time.Unix(since, 0)); perr != nil || el < 0 || el > joinSyncWindow {
		return false
	}
	if done, ok, err := d.Settings.Get(ctx, catalogpull.KeyFirstPullDone); err != nil || (ok && done != "") {
		return false
	}
	// A cached "till" master (a free or lapsed store) means no pull will
	// ever come; an absent value just means the first check-in has not
	// landed yet.
	if v, ok, err := d.Settings.Get(ctx, entitlement.KeyCatalogMaster); err == nil && ok && entitlement.CatalogMasterMode(v) == entitlement.CatalogMasterTill {
		return false
	}
	return true
}

// joinSyncStep decides what a joined till's landing page shows.
func joinSyncStep(ctx context.Context, d *common.Deps) joinSyncResult {
	if joinShopHasProducts(ctx, d) {
		return joinSyncHasProducts
	}
	if joinSyncPending(ctx, d) {
		return joinSyncWaiting
	}
	return joinSyncSettled
}

// joinShopHasProducts is true when an active item exists other than the
// gift voucher the wizard seeded.
func joinShopHasProducts(ctx context.Context, d *common.Deps) bool {
	repo := data.NewCatalogRepo(d.Db)
	var has bool
	var err error
	if seeded, ok, serr := repo.SeededGiftVoucherItemID(ctx); serr == nil && ok {
		has, err = repo.HasOtherActiveItems(ctx, seeded)
	} else {
		has, err = repo.HasActiveItems(ctx)
	}
	if err != nil {
		logging.L().Warnf("join sync: read the catalogue: %v", err)
		return false
	}
	return has
}

// registerJoinSyncPoll serves the syncing state's poll: 204 (htmx leaves the
// page alone) while the pull runs, then an HX-Redirect to the sale screen,
// or to the welcome when the shop is really empty.
func registerJoinSyncPoll(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /import/join-sync", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "import_export") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch joinSyncStep(r.Context(), d) {
		case joinSyncWaiting:
			w.WriteHeader(http.StatusNoContent)
		case joinSyncHasProducts:
			w.Header().Set("HX-Redirect", "/")
			w.WriteHeader(http.StatusOK)
		default:
			w.Header().Set("HX-Redirect", "/import?welcome=1")
			w.WriteHeader(http.StatusOK)
		}
	})
}
