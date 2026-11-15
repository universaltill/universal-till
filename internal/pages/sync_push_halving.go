package pages

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// A sales push the primary refuses with HTTP 413 is retried with the leading
// half of the batch, down to one sale (ut-docs#5428). Any source of a 413
// counts (the primary's body cap, a reverse proxy); the status code alone is
// read, never the body.

// errPayloadTooLarge marks an HTTP 413 from the primary for a sales batch.
var errPayloadTooLarge = errors.New("payload too large")

// SalePushTooLargeProblemKey keys the "one sale is too large for the main
// till" Problem; resolved when a push gets past that sale, and on promote.
const SalePushTooLargeProblemKey = "sync.push_sale_too_large"

// salesPushHeldKey holds the id of the sale the primary refused alone with a
// 413 (empty = none). Read by the sync chip; written only by syncPushSales.
const salesPushHeldKey = "sync.push_held_sale"

// salesPushBatchKey is the batch size learned from the last 413 (absent =
// salesPushMaxBatch), so a restart or the next tick does not re-upload a
// refused body. It doubles back towards the max after a fully accepted tick.
const salesPushBatchKey = "sync.push_batch_size"

const (
	salesPushMaxBatch    = 50 // sales per push request when nothing was refused
	salesPushMaxRequests = 16 // requests one tick may spend on the sales step
)

// postSalesHalving sends batch in order, in sub-batches of size (clamped to
// [1, len(batch)]). post returns nil on a 200, an error wrapping
// errPayloadTooLarge on a 413, any other error otherwise. On a 413 the same
// leading slice is retried at ceil(n/2); the working size never grows within
// a call. After each 200, onAccepted (may be nil) runs for that sub-batch
// before the next request; its error stops the call. maxRequests <= 0 means
// no cap.
//
// It returns how many leading sales got a 200 (sent), the final working size
// (the size passed in, unclamped, when no 413 shrank it — so a short batch
// is not mistaken for a learned limit), and: tooLarge != nil when batch[sent] was refused alone; err != nil for a
// non-413 failure (or an onAccepted error); both nil with sent < len(batch)
// when the request cap was hit. Nothing is reordered or skipped.
func postSalesHalving(
	batch []journalSale, size, maxRequests int,
	post func(sub []journalSale) error,
	onAccepted func(sub []journalSale) error,
) (sent, finalSize int, tooLarge *journalSale, err error) {
	finalSize = size
	size = max(1, min(size, len(batch)))
	reqs := 0
	for sent < len(batch) {
		if maxRequests > 0 && reqs == maxRequests {
			return sent, finalSize, nil, nil
		}
		n := min(size, len(batch)-sent)
		sub := batch[sent : sent+n]
		e := post(sub)
		reqs++
		if e == nil {
			if onAccepted != nil {
				if e2 := onAccepted(sub); e2 != nil {
					return sent, finalSize, nil, e2
				}
			}
			sent += n
			continue
		}
		if errors.Is(e, errPayloadTooLarge) {
			if n == 1 {
				return sent, 1, &batch[sent], nil
			}
			size = (n + 1) / 2
			finalSize = size
			logging.L().Infof("sync push: main till refused %d sale(s) as too large (413) — retrying with %d", n, size)
			continue
		}
		return sent, finalSize, nil, e
	}
	return sent, finalSize, nil, nil
}

// salesPushBatchSize is the batch size for this tick: the size learned from
// the last 413, else salesPushMaxBatch.
func salesPushBatchSize(ctx context.Context, d *common.Deps) int {
	v, _, _ := d.Settings.Get(ctx, salesPushBatchKey)
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 || n > salesPushMaxBatch {
		return salesPushMaxBatch
	}
	return n
}

// storeSalesPushBatchSize persists a smaller size after a 413, or doubles a
// learned size back towards the max after a tick the primary fully accepted.
// It writes only on change.
func storeSalesPushBatchSize(ctx context.Context, d *common.Deps, fetched, final int, accepted bool) {
	next := fetched
	switch {
	case final < fetched:
		next = final
	case accepted && fetched < salesPushMaxBatch:
		next = min(2*fetched, salesPushMaxBatch)
	}
	if next == fetched {
		return
	}
	_ = d.Settings.Set(ctx, salesPushBatchKey, strconv.Itoa(next))
}

// holdOversizedSale records a sale the primary refused alone (413 at one
// sale): the queue stays behind it, the chip warns and one keyed Problem names
// it. The cursor is untouched, so the sale and every later one are retried each
// tick until the main till accepts them.
func holdOversizedSale(ctx context.Context, d *common.Deps, repo *data.POSRepo, id, receiptNo string) {
	prev, _, _ := d.Settings.Get(ctx, salesPushHeldKey)
	if strings.TrimSpace(prev) != id {
		_ = d.Settings.Set(ctx, salesPushHeldKey, id)
	}
	if strings.TrimSpace(prev) == id && logging.HasOpenProblem(SalePushTooLargeProblemKey) {
		return
	}
	cur, _, _ := d.Settings.Get(ctx, "sync.push_cursor")
	queued, _ := countQueuedSales(ctx, repo, cur)
	logging.L().WarnProblemf(SalePushTooLargeProblemKey,
		"sync push: sale %s (receipt %s) is too large for the main till to accept (HTTP 413 for that sale alone) — it and %d later sale(s) stay on this till and are retried every tick until the main till accepts it",
		id, receiptNo, max(queued-1, 0))
}
