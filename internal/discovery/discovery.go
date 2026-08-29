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
	"github.com/universaltill/universal-till/internal/logging"
)

// TillIDSettingKey is the settings-table key holding this till's LAN
// discovery identity.
const TillIDSettingKey = "lan_discovery.till_id"

// TillID returns this till's LAN-discovery id, generating and persisting a
// fresh uuid the first time it's called (get-or-create) — every caller,
// across the whole process and across restarts, must see the same value,
// which is why this is the one function anything needing this id should
// call rather than each generating its own.
//
// Falls back to SettingsRepo.GetOrCreate, not a Get-then-Set of its own,
// once the id doesn't exist yet: on a fresh DB, two concurrent callers doing
// read-then-write can each miss the Get, each generate a different uuid,
// and last-Set-wins — the caller holding the losing uuid then returns an id
// nothing else agrees on (ut-docs#271). GetOrCreate resolves that atomically
// in the data layer so every caller, racing or not, converges on the same
// persisted value. The steady-state case (id already exists, true on every
// call after the first) is checked with a plain Get first — GetOrCreate
// takes a database-wide write lock even when nothing needs writing, and
// this id changes at most once per install, so paying that cost on every
// call (this is polled every 30s by the manager's pending-pairings view)
// would be pure waste; the race this whole function exists to close only
// happens on the absent path anyway.
func TillID(ctx context.Context, settings *data.SettingsRepo) (string, error) {
	if v, ok, err := settings.Get(ctx, TillIDSettingKey); err != nil {
		return "", err
	} else if ok {
		return v, nil
	}
	v, err := settings.GetOrCreate(ctx, TillIDSettingKey, uuid.NewString())
	if err != nil {
		return "", err
	}
	return v, nil
}

// RoleCheckFromSettings builds a RoleCheck (see advertiser.go) backed by a
// real *data.SettingsRepo: empty "sync.primary_url" means primary/
// standalone (advertise), non-empty means replica (don't) — the same rule
// as pages.Deps.SyncPrimaryURL. internal/app.Run wires this straight into
// discovery.NewAdvertiser rather than hand-rolling the same settings.Get +
// strings.TrimSpace inline, so the actual production role-check logic has
// its own direct test coverage instead of only Advertiser's tick logic
// being tested against a synthetic injected bool.
func RoleCheckFromSettings(settings *data.SettingsRepo) RoleCheck {
	return func(ctx context.Context) bool {
		v, _, _ := settings.Get(ctx, "sync.primary_url")
		return strings.TrimSpace(v) == ""
	}
}

// FirstBootChecker is the subset of *auth.Service GateOnFirstBoot depends
// on — narrowed to a seam (same pattern as RoleCheck and mdnsServer) so
// tests never need a real *auth.Service backed by the full auth schema.
type FirstBootChecker interface {
	NeedsFirstBoot(ctx context.Context) (bool, error)
}

// GateOnFirstBoot wraps a RoleCheck so a till also withholds mDNS
// advertising until its own first-boot setup is actually complete
// (ut-docs#1263). RoleCheckFromSettings' rule — empty "sync.primary_url"
// means primary — is equally true of a brand-new, never-configured till,
// so without this gate a device wiped or freshly flashed starts
// advertising itself as a join target within seconds of boot, before a
// human has even opened the setup wizard. Being a blank, not-yet-
// configured device is a different state from being a deliberately
// configured standalone primary, and only the latter should be
// discoverable in someone else's "join an existing shop" scan.
//
// Short-circuits without calling firstBoot at all when inner already
// reports not-primary, mirroring Advertiser.tick's own primary/replica
// switch — a replica has nothing to gate.
//
// Fails closed: if the first-boot check itself errors, this reports false
// (withhold) rather than advertising blind on an unknown setup state.
func GateOnFirstBoot(inner RoleCheck, firstBoot FirstBootChecker) RoleCheck {
	return func(ctx context.Context) bool {
		if !inner(ctx) {
			return false
		}
		needsFirstBoot, err := firstBoot.NeedsFirstBoot(ctx)
		if err != nil {
			logging.L().Warnf("lan discovery: first-boot check failed, withholding advertisement: %v", err)
			return false
		}
		return !needsFirstBoot
	}
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
