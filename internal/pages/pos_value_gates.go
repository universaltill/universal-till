package pages

import (
	"context"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// auditValueGate (ut-docs#5416) writes the audit row for a manual
// value-removal action the cashier-facing gates cover (a line or sale
// discount change, New sale on a priced basket, a tender discount raise).
// A PIN-approved action records both people (InsertAuditElevated: the
// approver acts, the session user is the blocked one); anything else records
// the session user alone, which is also the row for a lowered discount and
// for a manager or auth-off session. Best-effort like every other audit
// here: offline-first, a failed insert is logged and never undoes the action.
func auditValueGate(ctx context.Context, repo *data.POSRepo, r *http.Request, elev elevationCheck, entityType, entityID, action string, detail map[string]any) {
	// The action already happened; a client going away must not cancel the row.
	ctx = context.WithoutCancel(ctx)
	now := time.Now().UTC().Format(time.RFC3339)
	if elev.Outcome == elevated {
		if err := repo.InsertAuditElevated(ctx, nil, elev.ApproverID, elev.ActorID, entityType, entityID, action, detail, now, ""); err != nil {
			logging.L().Errorf("insert elevated %s audit: %v", entityType, err)
		}
		return
	}
	if err := repo.InsertAudit(ctx, nil, getSessionUserID(r), entityType, entityID, action, detail, now, ""); err != nil {
		logging.L().Errorf("insert %s audit: %v", entityType, err)
	}
}
