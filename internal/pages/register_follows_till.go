package pages

import (
	"context"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// renameOwnRegisterAfterTill gives the main till's own register the till's
// new name (ut-docs#4022), so the Registers page shows the name the shop
// knows the till by. Main till only: registers is primary-wins synced
// (sync_admin_repo.go), so a joined till's write would be reverted by the
// next admin pull. The own register is the persisted
// pos.SettingsKeyTillRegisterID when it is still active, else the single
// active register; with several and nothing persisted there is no
// non-guessing answer, and nothing is renamed. Best-effort: the till rename
// has already been saved and must never fail over its register.
func renameOwnRegisterAfterTill(ctx context.Context, d *common.Deps, name string) {
	if tillFollowsMain(ctx, d) {
		return
	}
	repo := data.NewPOSRepo(d.Db)
	regs, err := repo.ListRegisters(ctx)
	if err != nil {
		logging.L().Warnf("register follows till: list registers: %v", err)
		return
	}
	id := ""
	if persisted, ok, err := d.Settings.Get(ctx, pos.SettingsKeyTillRegisterID); err == nil && ok {
		for _, reg := range regs {
			if reg.ID == persisted {
				id = persisted
				break
			}
		}
	}
	if id == "" && len(regs) == 1 {
		id = regs[0].ID
	}
	if id == "" {
		return
	}
	renameRegisterAfterTill(ctx, repo, id, name)
}

// renameRegisterAfterTill renames one register to its till's new name and
// audits it; a name another register holds is skipped (logged).
func renameRegisterAfterTill(ctx context.Context, repo *data.POSRepo, id, name string) {
	renamed, err := repo.RenameRegisterToFollowTill(ctx, id, name)
	if err != nil {
		logging.L().Warnf("register follows till: rename register %s: %v", id, err)
		return
	}
	if !renamed {
		logging.L().Infof("register follows till: register %s keeps its name (already %q, or another register has it)", id, name)
		return
	}
	_ = repo.InsertAudit(ctx, nil, "system", "register", id, "register_renamed_with_till",
		map[string]any{"name": name}, time.Now().UTC().Format(time.RFC3339), "")
}
