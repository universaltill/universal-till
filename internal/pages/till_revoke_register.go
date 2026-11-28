package pages

import (
	"context"
	"errors"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// revokeTillAndRetireRegister removes a till's enrolment and retires the
// register it owned (ut-docs#7098, AC8). The till security action always
// wins: a register the guards refuse to retire (the shop's own, the last
// active one, an open shift, unsent sales) is skipped, audited as
// register_retire_skipped, and the till is revoked anyway. The register is
// retired FIRST and re-activated if the till delete then fails, so there is
// no state where the till row is gone and its register is still active other
// than a guarded skip. Never touches sales or shifts.
func revokeTillAndRetireRegister(ctx context.Context, repo *data.TillsRepo, posRepo *data.POSRepo, tillID, reason, actorID string) error {
	retiredID := ""
	if link, ok, err := posRepo.RegisterByTill(ctx, tillID); err != nil {
		logging.L().Warnf("revoke till %s: look up its register: %v", tillID, err)
	} else if ok && link.RegisterID != "" {
		res, rerr := posRepo.RetireRegister(ctx, link.RegisterID, reason, "", data.RetireOpts{ActorID: actorID})
		switch {
		case rerr == nil && !res.AlreadyRetired:
			retiredID = link.RegisterID
		case rerr == nil:
			// already retired: nothing to do
		default:
			sentinel := "error"
			for _, s := range []error{data.ErrRetireSelf, data.ErrRetireLast, data.ErrRetireOpenShift, data.ErrRetireUnsent} {
				if errors.Is(rerr, s) {
					sentinel = s.Error()
				}
			}
			if sentinel == "error" {
				logging.L().Warnf("revoke till %s: retire register %s: %v", tillID, link.RegisterID, rerr)
			}
			_ = posRepo.InsertAudit(ctx, nil, actorID, "register", link.RegisterID, "register_retire_skipped",
				map[string]any{"reason": reason, "why": sentinel, "till_id": tillID}, time.Now().UTC().Format(time.RFC3339), "")
		}
	}
	if err := repo.DeleteTill(ctx, tillID); err != nil {
		if retiredID != "" {
			if aerr := posRepo.ReactivateRegister(ctx, retiredID, actorID); aerr != nil {
				logging.L().Errorf("revoke till %s failed and register %s could not be re-activated: %v", tillID, retiredID, aerr)
			}
		}
		return err
	}
	return nil
}
