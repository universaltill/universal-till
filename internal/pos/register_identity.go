package pos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/settings"
)

// SettingsKeyTillRegisterID is the settings key holding this till's own
// register identity (ut-docs#268) — established at setup or in
// Settings → Tills, and persistent across restarts and upgrades, the same
// mechanism as till.name / marketplace.device_id.
const SettingsKeyTillRegisterID = "till.register_id"

// ErrRegisterIdentityAmbiguous is returned when this till has no persisted
// register identity and more than one active register exists — there is no
// non-guessing answer, and per the ut-docs#268 decision a write path must
// fail loudly rather than silently pick one.
var ErrRegisterIdentityAmbiguous = errors.New("till register identity not set and multiple registers exist")

// ResolveTillRegisterID returns THIS till's register id — never "whichever
// register was used most recently" (the heuristic ut-docs#268 retires for
// write paths). Resolution order:
//
//  1. A persisted till.register_id that still names an active register is
//     the till's established identity — returned as-is.
//  2. Unset (or stale — the register was since deactivated/removed):
//     zero active registers self-heals via the existing EnsureRegister
//     default; exactly one active register IS the answer (only one
//     possibility is not a guess). Either way the result is persisted so
//     the identity survives restarts.
//  3. Two or more active registers with nothing persisted is genuinely
//     ambiguous: ErrRegisterIdentityAmbiguous, and nothing is persisted —
//     a human picks in Settings → Tills.
func ResolveTillRegisterID(ctx context.Context, sqlDB *sql.DB, st *settings.Store) (string, error) {
	repo := data.NewPOSRepo(sqlDB)

	regs, err := repo.ListRegisters(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve till register: %w", err)
	}

	if persisted, ok, err := st.Get(ctx, SettingsKeyTillRegisterID); err != nil {
		return "", fmt.Errorf("resolve till register: %w", err)
	} else if ok && persisted != "" {
		for _, reg := range regs {
			if reg.ID == persisted {
				return persisted, nil
			}
		}
		// Stale: the persisted register is no longer active — fall through
		// to re-resolution below rather than writing against a retired
		// register.
	}

	var resolved string
	switch len(regs) {
	case 0:
		// Same self-heal first-boot setup and checkout already use.
		resolved, err = repo.EnsureRegister(ctx)
		if err != nil {
			return "", fmt.Errorf("resolve till register: %w", err)
		}
	case 1:
		resolved = regs[0].ID
	default:
		return "", ErrRegisterIdentityAmbiguous
	}

	if err := st.Set(ctx, SettingsKeyTillRegisterID, resolved); err != nil {
		return "", fmt.Errorf("persist till register identity: %w", err)
	}
	return resolved, nil
}
