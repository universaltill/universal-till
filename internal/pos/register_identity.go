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
// mechanism as sync.till_id/sync.till_name.
//
// MUST keep the "sync." prefix (independent review finding, ut-docs#268
// round 2): sync_admin_repo.go's PerTillSettingPrefixes is what keeps a
// per-till settings key from being dumped from the primary and upserted
// onto every replica on each admin pull. A bare "till."-prefixed key is
// NOT covered (till.name is deliberately the one exception, and IS shop-
// wide synced on purpose — ut-docs#396/#405) — this key first shipped as
// "till.register_id" and was caught, before merge, silently clobbering
// every replica's own register identity with the primary's on the very
// topology this card exists to protect.
const SettingsKeyTillRegisterID = "sync.till_register_id"

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

	// Read the persisted choice FIRST (independent review finding,
	// ut-docs#268 round 2): listing registers before checking it opened a
	// window where a POST /api/settings/till-register landing between the
	// list and the check would look "stale" against the snapshot taken
	// before it, and get silently overwritten by re-resolution below — a
	// resolver quietly undoing a manager's just-made explicit choice on a
	// money path. Reading first still needs ListRegisters to validate
	// staleness, but only fetches it once, against current state.
	persisted, ok, err := st.Get(ctx, SettingsKeyTillRegisterID)
	if err != nil {
		return "", fmt.Errorf("resolve till register: %w", err)
	}

	regs, err := repo.ListRegisters(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve till register: %w", err)
	}

	if ok && persisted != "" {
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
