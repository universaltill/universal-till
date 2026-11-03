package pages

// Promote with a sales copy (ut-docs#5074 M6; design §9.2): the promoted
// till adopts the copy into its own record, verbatim, under each sale's
// origin till, and its next Z starts after the previous main till's last
// copied close.

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

const (
	// salesMirrorAdoptProblemKey keys the problem a promote leaves when
	// some copied sales could not be adopted, or the adoption stopped.
	salesMirrorAdoptProblemKey = "sales_mirror.adopt_failed"
	// salesMirrorAdoptPendingKey (per till): an adoption is not finished;
	// the maintenance tick runs it again until it is.
	salesMirrorAdoptPendingKey = "sync.sales_mirror_adopt_pending"
)

// salesMirrorAdoption is what a promote adopted, for the promote panel.
type salesMirrorAdoption struct {
	Ran          bool
	Result       data.MirrorAdoptResult
	OwnOutsideZ  int // this till's own sales before the floor that never reached the main till
	LateOutsideZ int // copied sales before the floor the main till received after its close
	Err          error
}

// adoptSalesMirrorOnPromote runs right after the replica identity is
// cleared. pushedUpTo is the journal push cursor read before that.
func adoptSalesMirrorOnPromote(ctx context.Context, d *common.Deps, actorID, pushedUpTo string) salesMirrorAdoption {
	// Not tied to the request: a closed browser must not stop an adoption
	// (M6 review finding 3); it is idempotent and resumed if cut short.
	ctx = context.WithoutCancel(ctx)
	repo := data.NewSalesMirrorRepo(d.Db)
	has, err := repo.HasCopy(ctx)
	if err != nil {
		logging.L().Warnf("sales mirror: check for a copy on promote: %v", err)
	}
	if err != nil || !has {
		return salesMirrorAdoption{}
	}
	out := salesMirrorAdoption{Ran: true}
	// The Z floor first: it only reads the copy, and must hold even if the
	// adoption below stops part-way.
	if last, err := repo.PreviousMainLastClose(ctx); err != nil {
		logging.L().Warnf("sales mirror: read the previous main till's last close: %v", err)
	} else if last != "" {
		own, _ := data.NewPOSRepo(d.Db).LatestArchivedAt(ctx, "eod")
		if lt, err := time.Parse("2006-01-02 15:04:05", last); err == nil && (own == nil || lt.After(*own)) {
			if err := d.Settings.Set(ctx, data.EODWindowFloorKey, last); err != nil {
				logging.L().Warnf("sales mirror: set the Z window floor: %v", err)
			}
			ownLast := ""
			if own != nil {
				ownLast = own.UTC().Format("2006-01-02 15:04:05")
			}
			out.OwnOutsideZ, out.LateOutsideZ, _ = repo.SalesOutsideAnyZ(ctx, last, ownLast, strings.TrimSpace(pushedUpTo))
		}
	}
	_ = d.Settings.Set(ctx, salesMirrorAdoptPendingKey, "1")
	out.Result, out.Err = runSalesMirrorAdoption(ctx, d)
	_ = data.NewPOSRepo(d.Db).InsertAudit(ctx, nil, actorID, "till", "-", "sales_adopted",
		map[string]any{"adopted": out.Result.Adopted, "failed": out.Result.Failed, "voided": out.Result.Voided,
			"own_outside_z": out.OwnOutsideZ, "late_outside_z": out.LateOutsideZ},
		time.Now().UTC().Format(time.RFC3339), "")
	return out
}

// runSalesMirrorAdoption adopts what is missing with this till's change log
// quiet (no other till consumes it after a promote — every re-join re-syncs —
// so logging each adopted sale would only grow it; M6 review finding 4), and
// clears the pending mark once nothing is left to adopt.
func runSalesMirrorAdoption(ctx context.Context, d *common.Deps) (data.MirrorAdoptResult, error) {
	repo := data.NewSalesMirrorRepo(d.Db)
	prevOff, hadOff, _ := d.Settings.Get(ctx, data.SalesMirrorTillOffKey)
	_ = d.Settings.Set(ctx, data.SalesMirrorTillOffKey, "1")
	res, err := repo.AdoptCopy(ctx)
	if hadOff {
		_ = d.Settings.Set(ctx, data.SalesMirrorTillOffKey, prevOff)
	} else {
		_ = d.Settings.Set(ctx, data.SalesMirrorTillOffKey, "0")
	}
	switch {
	case err != nil:
		logging.L().WarnProblemf(salesMirrorAdoptProblemKey, "sales mirror: adopting the copy stopped (%v); it is kept and adoption resumes by itself", err)
	case res.Failed > 0:
		logging.L().WarnProblemf(salesMirrorAdoptProblemKey, "sales mirror: %d copied sale(s) could not be added to this till's record (a receipt number or reference this till refuses); they stay in the copy", res.Failed)
	default:
		logging.ResolveProblems(salesMirrorAdoptProblemKey)
	}
	if err == nil {
		if left, lerr := repo.HasUnadopted(ctx); lerr == nil && (!left || res.Failed > 0) {
			// Done, or only refused rows remain (retrying cannot help).
			_ = d.Settings.Set(ctx, salesMirrorAdoptPendingKey, "")
		}
	}
	return res, err
}

// resumeSalesMirrorAdoption finishes an adoption a promote left unfinished
// (called from the maintenance tick; a no-op otherwise: one settings read).
func resumeSalesMirrorAdoption(ctx context.Context, d *common.Deps) {
	if v, _, _ := d.Settings.Get(ctx, salesMirrorAdoptPendingKey); v != "1" || d.SyncPrimaryURL(ctx) != "" {
		return
	}
	res, err := runSalesMirrorAdoption(ctx, d)
	if err == nil {
		logging.L().Infof("sales mirror: finished adopting the copy (%d sales, %d refused)", res.Adopted, res.Failed)
	}
}

// writeAdoptionNote adds the adoption's outcome to the promote answer.
func writeAdoptionNote(w http.ResponseWriter, locale string, a salesMirrorAdoption) {
	if !a.Ran {
		return
	}
	line := func(prefix, key string, n int) {
		fmt.Fprintf(w, `<p class="muted">%s%s</p>`, prefix, html.EscapeString(fmt.Sprintf(httpx.T(locale, key), n)))
	}
	line("", "tills.promote_adopted", a.Result.Adopted)
	if a.Result.Voided > 0 {
		line("", "tills.promote_adopt_voided", a.Result.Voided)
	}
	if a.Result.Failed > 0 {
		line("✗ ", "tills.promote_adopt_failed", a.Result.Failed)
	}
	if a.Err != nil {
		fmt.Fprintf(w, `<p class="muted">✗ %s</p>`, html.EscapeString(httpx.T(locale, "tills.promote_adopt_resumes")))
	}
	if n := a.OwnOutsideZ + a.LateOutsideZ; n > 0 {
		line("⚠ ", "tills.promote_outside_z", n)
	}
}

// eodWindowFloor is a promoted till's Z-window floor (zero when none).
func eodWindowFloor(ctx context.Context, d *common.Deps) time.Time {
	if d.Settings == nil {
		return time.Time{}
	}
	v, _, _ := d.Settings.Get(ctx, data.EODWindowFloorKey)
	t, err := time.Parse("2006-01-02 15:04:05", strings.TrimSpace(v))
	if err != nil {
		return time.Time{}
	}
	return t
}
