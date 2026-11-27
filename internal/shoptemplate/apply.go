package shoptemplate

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// LedgerKey is the shop-wide setting recording that a template was applied
// (shop. prefix: shop-wide, syncs main -> satellite via the admin bundle).
const LedgerKey = "shop.template_applied"

// LegacyLayoutOffKey records that the shop had switched its old built-in salon
// layout plugin off before that plugin was retired (ADR-0190 Q1: a layout is
// never forced). While it is set and no template was accepted (no ledger), the
// template's legacy_layout does not apply. Shop-wide, so a satellite agrees.
const LegacyLayoutOffKey = "shop.legacy_layout_off"

// ThemeSeededKey is the per-till marker that this till's theme was seeded.
const ThemeSeededKey = "template.theme_seeded"

// Ledger is the JSON stored under LedgerKey: what was applied and what
// values it actually wrote, so ownership can be derived (a value that still
// equals the seeded one was never touched by the owner).
type Ledger struct {
	ID      string            `json:"id"`
	Version int               `json:"version"`
	Seeded  map[string]string `json:"seeded"`
	At      string            `json:"at"`
}

// SettingsStore is the slice of the settings repository Apply needs; the
// caller adapts it so this package stays free of internal/data.
// SetPerTill writes a key that belongs to this till only.
type SettingsStore interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key, value string) error
	SetPerTill(ctx context.Context, key, value string) error
}

// Apply seeds the template's whitelisted defaults once. It is a no-op for a
// nil template or when the ledger already exists; a value that is already
// set is never overwritten; any seed off the closed whitelist is skipped.
// The ledger is written last, so a failed write leaves no ledger and the
// next call retries. The caller gates it to the main/single till.
func Apply(ctx context.Context, st SettingsStore, r *Resolved) error {
	if r == nil {
		return nil
	}
	if cur, _, err := st.Get(ctx, LedgerKey); err != nil {
		return fmt.Errorf("shoptemplate: read ledger: %w", err)
	} else if cur != "" {
		return nil
	}
	seeded := map[string]string{}
	for _, s := range r.Seeds() {
		if !SeedValueValid(s.Key, s.Value) {
			continue
		}
		cur, _, err := st.Get(ctx, s.Key)
		if err != nil {
			return fmt.Errorf("shoptemplate: read %s: %w", s.Key, err)
		}
		if cur != "" {
			continue
		}
		if IsPerTillSeedKey(s.Key) {
			if err := st.SetPerTill(ctx, s.Key, s.Value); err != nil {
				return fmt.Errorf("shoptemplate: seed %s: %w", s.Key, err)
			}
			if err := st.SetPerTill(ctx, ThemeSeededKey, s.Value); err != nil {
				return fmt.Errorf("shoptemplate: seed marker: %w", err)
			}
		} else if err := st.Set(ctx, s.Key, s.Value); err != nil {
			return fmt.Errorf("shoptemplate: seed %s: %w", s.Key, err)
		}
		seeded[s.Key] = s.Value
	}
	b, err := json.Marshal(Ledger{ID: r.ID(), Version: r.Version(), Seeded: seeded, At: time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return err
	}
	if err := st.Set(ctx, LedgerKey, string(b)); err != nil {
		return fmt.Errorf("shoptemplate: write ledger: %w", err)
	}
	return nil
}
