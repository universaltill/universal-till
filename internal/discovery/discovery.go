// Package discovery implements LAN till discovery over mDNS (ADR-0033 part
// 1, universaltill/ut-docs#264 — #183 was closed "completed" with zero code
// and this is the actual implementation).
//
// It has two halves: Advertiser (a primary announces itself on the LAN) and
// Browse (a replica-to-be finds primaries on demand). Both sides need the
// SAME stable per-install identifier for a primary — TillID below is that
// single source of truth, shared with the pairing verification-code flow so
// a replica that learns a primary's id off mDNS and a primary computing its
// own verification code server-side agree on the identical value.
package discovery

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/data"
)

// TillIDSettingKey is the settings-table key holding this till's LAN
// discovery identity.
const TillIDSettingKey = "lan_discovery.till_id"

// TillID returns this till's LAN-discovery id, generating and persisting a
// fresh uuid the first time it's called (get-or-create) — every caller,
// across the whole process and across restarts, must see the same value,
// which is why this is the one function anything needing this id should
// call rather than each generating its own.
func TillID(ctx context.Context, settings *data.SettingsRepo) (string, error) {
	if v, ok, err := settings.Get(ctx, TillIDSettingKey); err != nil {
		return "", err
	} else if ok && strings.TrimSpace(v) != "" {
		return v, nil
	}
	id := uuid.NewString()
	if err := settings.Set(ctx, TillIDSettingKey, id); err != nil {
		return "", err
	}
	return id, nil
}

// storeNameOrDefault duplicates internal/pages's storeNameOrDefault
// (same setting key "store.name", same "this shop" fallback) rather than
// importing internal/pages — internal/pages will need to import
// internal/discovery (for the discover-primaries API handler), so the
// reverse import would be a cycle. Confirmed empirically, not assumed: see
// the code review record for this change.
func storeNameOrDefault(ctx context.Context, settings *data.SettingsRepo) string {
	if v, ok, _ := settings.Get(ctx, "store.name"); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return "this shop"
}
