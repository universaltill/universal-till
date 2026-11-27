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
	"math"
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
func handleGenericDirective(ctx context.Context, cfg *config.Config, db *sql.DB, d directive, hooks Hooks, fetchDeferred *bool, applied *appliedKinds) {
	repo := data.NewDirectiveResultsRepo(db)
	stored, found, err := repo.Get(ctx, d.ID)
	if err != nil {
		// Never apply blind: without the lookup a re-delivery could book twice.
		logging.L().Warnf("cloudsync: directive %s (%s) left pending: read stored result: %v", d.ID, d.Type, err)
		return
	}
	if found {
		// Nothing changed on this till, so applied is not set again:
		// the tick that applied it already did, and the snapshot push is
		// hash-gated anyway.
		postStoredDirectiveResult(ctx, cfg, repo, stored)
		return
	}
	*fetchDeferred = false
	var reason reasonSink
	status, msg := apply(context.WithValue(ctx, reasonSinkKey{}, &reason), d, hooks)
	if *fetchDeferred && status == "failed" {
		logging.L().Warnf("cloudsync: directive %s (%s) left pending: %s", d.ID, d.Type, msg)
		return
	}
	if status == statusRetry {
		logging.L().Warnf("cloudsync: directive %s (%s) left pending, will retry: %s", d.ID, d.Type, msg)
		return
	}
	if status == "applied" && catalogTypes[d.Type] {
		applied.catalog = true
	}
	if status == "applied" && deviceReportTypes[d.Type] {
		applied.deviceReport = true
	}
	// A failed apply is stored too (the cloud shows it failed and the owner
	// saves again under a new directive id), except one a hook reports as
	// retryable (statusRetry above), which stays pending.
	stored = data.DirectiveResult{DirectiveID: d.ID, DirectiveType: d.Type, Status: status, Message: msg, MessageKey: reason.key}
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
	err := postResult(ctx, cfg, r.DirectiveID, r.Status, r.Message, resultKeyData(r.MessageKey))
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

// ReasonError is an error a hook returns for a directive that failed for a
// reason my. can translate (ut-docs#6975): Key is the en.json message key,
// Message the safe plain-English text (Error() returns only this, so a raw
// path or URL in Cause never reaches the cloud). Cause stays for errors.Is/As.
type ReasonError struct {
	Key     string
	Message string
	Cause   error
}

func (e *ReasonError) Error() string { return e.Message }
func (e *ReasonError) Unwrap() error { return e.Cause }

// reasonSink collects the key of the ReasonError a directive's apply hit;
// it rides the apply context because apply returns only (status, msg).
type reasonSink struct{ key string }

type reasonSinkKey struct{}

func noteReason(ctx context.Context, err error) {
	var re *ReasonError
	if s, ok := ctx.Value(reasonSinkKey{}).(*reasonSink); ok && errors.As(err, &re) {
		s.key = re.Key
	}
}

// resultKeyData is the result post's data block for a reason key: nil (no
// data field) when there is none.
func resultKeyData(key string) map[string]any {
	if key == "" {
		return nil
	}
	return map[string]any{"message_key": key}
}

// Reason keys of a set_till_setting directive the till refused on the shop-
// config version/tier rules (design-B 4.2/4.3). They are codes my. translates;
// nothing was written.
const (
	ReasonSettingConflict     = "conflict"
	ReasonSettingStale        = "stale_version"
	ReasonSettingTierMismatch = "tier_mismatch"
)

// TillSettingCond is the optional conditional part of a set_till_setting
// payload, all additive: Ver is the version this write mints, BaseVer the
// version the editor saw, Tier the registry tier the cloud believes the key
// has (the till refuses when its own registry says otherwise).
type TillSettingCond struct {
	Ver, BaseVer *int64
	Tier         string
}

// tillSettingCondFrom reads ver/base_ver/tier from a directive payload. ok is
// false when one is present but malformed (not a whole number >= 1, or tier
// not a string), so a bad directive is refused rather than read as
// unconditional.
func tillSettingCondFrom(p map[string]any) (TillSettingCond, bool) {
	var c TillSettingCond
	num := func(k string) (*int64, bool) {
		raw, present := p[k]
		if !present || raw == nil {
			return nil, true
		}
		f, isNum := raw.(float64)
		if !isNum || f != math.Trunc(f) || f < 1 || f > 1<<53 {
			return nil, false
		}
		n := int64(f)
		return &n, true
	}
	var ok bool
	if c.Ver, ok = num("ver"); !ok {
		return c, false
	}
	if c.BaseVer, ok = num("base_ver"); !ok {
		return c, false
	}
	if raw, present := p["tier"]; present && raw != nil {
		t, isStr := raw.(string)
		if !isStr {
			return c, false
		}
		c.Tier = t
	}
	return c, true
}
