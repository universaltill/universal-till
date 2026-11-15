package cloudsync

// Generic directive idempotency (ut-docs#4797). A directive whose result
// post failed stays pending on the cloud and is delivered again. Most hooks
// are NOT idempotent (adjust_stock books a second movement, print_report
// prints twice), so the first result is stored by directive id
// (data.DirectiveResultsRepo, migration 088) and a re-delivery re-posts it
// without applying again. One mechanism for every directive on the generic
// apply path; import_customers (idempotent by design) and guest_order (its
// own store, keyed by guest order id) do not come through here.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// handleGenericDirective is tick's path for one directive on the generic
// apply path. A stored result for d.ID is re-posted as it is; otherwise the
// directive is applied, its result stored, then posted. The stored row is
// marked posted once the cloud acknowledged it (200, or 404 for a
// directive it no longer holds);
// otherwise the cloud delivers the directive again and the stored result is
// re-posted. *fetchDeferred is set by the image-fetch hook during apply: a
// failed directive that deferred a fetch is left pending with nothing
// stored or posted.
//
// Residual window, not closed here: a crash between the hook's commit and
// Save below re-applies the directive once on re-delivery.
func handleGenericDirective(ctx context.Context, cfg *config.Config, db *sql.DB, d directive, hooks Hooks, fetchDeferred, catalogApplied *bool) {
	repo := data.NewDirectiveResultsRepo(db)
	stored, found, err := repo.Get(ctx, d.ID)
	if err != nil {
		// Never apply blind: without the lookup a re-delivery could book twice.
		logging.L().Warnf("cloudsync: directive %s (%s) left pending: read stored result: %v", d.ID, d.Type, err)
		return
	}
	if found {
		// Nothing changed on this till, so catalogApplied is not set again:
		// the tick that applied it already did, and the snapshot push is
		// hash-gated anyway.
		postStoredDirectiveResult(ctx, cfg, repo, stored)
		return
	}
	*fetchDeferred = false
	status, msg := apply(ctx, d, hooks)
	if *fetchDeferred && status == "failed" {
		logging.L().Warnf("cloudsync: directive %s (%s) left pending: %s", d.ID, d.Type, msg)
		return
	}
	if status == statusRetry {
		logging.L().Warnf("cloudsync: directive %s (%s) left pending, will retry: %s", d.ID, d.Type, msg)
		return
	}
	if status == "applied" && catalogTypes[d.Type] {
		*catalogApplied = true
	}
	// A failed apply is stored too (the cloud shows it failed and the owner
	// saves again under a new directive id), except one a hook reports as
	// retryable (statusRetry above), which stays pending.
	stored = data.DirectiveResult{DirectiveID: d.ID, DirectiveType: d.Type, Status: status, Message: msg}
	if err := repo.Save(ctx, stored); err != nil {
		// Same as guest orders: still report it; only the dedupe of a
		// re-delivery is lost.
		logging.L().Errorf("cloudsync: directive %s (%s) result not stored (a re-delivery would re-apply it): %v", d.ID, d.Type, err)
	}
	postStoredDirectiveResult(ctx, cfg, repo, stored)
}

// postStoredDirectiveResult posts a stored result under its directive id and
// marks it posted once the cloud acknowledged it.
func postStoredDirectiveResult(ctx context.Context, cfg *config.Config, repo *data.DirectiveResultsRepo, r data.DirectiveResult) {
	err := postResult(ctx, cfg, r.DirectiveID, r.Status, r.Message, nil)
	var se *statusError
	switch {
	case err == nil:
		logging.L().Infof("cloudsync: directive %s (%s) %s: %s", r.DirectiveID, r.DirectiveType, r.Status, r.Message)
	case errors.As(err, &se) && se.StatusCode == http.StatusNotFound:
		// The cloud no longer holds it pending (already resolved): done.
		logging.L().Infof("cloudsync: result for %s: the cloud no longer holds the directive (404)", r.DirectiveID)
	case errors.As(err, &se) && resultStatusIsFinal(se.StatusCode):
		// Any other refusal leaves the directive pending on the cloud, so
		// the row stays unposted (kept 30 days, not 7): a re-delivery keeps
		// getting the stored result instead of a second apply.
		logging.L().Warnf("cloudsync: result for %s refused by the cloud (%d); re-posted on re-delivery: %v", r.DirectiveID, se.StatusCode, err)
		return
	default:
		// Left pending on the cloud; the next delivery re-posts this stored
		// result — the directive is not applied again.
		logging.L().Warnf("cloudsync: result for %s not delivered (re-posted when the cloud re-delivers it): %v", r.DirectiveID, err)
		return
	}
	if merr := repo.MarkPosted(ctx, r.DirectiveID); merr != nil {
		logging.L().Warnf("cloudsync: directive %s result delivered but not marked (it will be re-sent): %v", r.DirectiveID, merr)
	}
}
