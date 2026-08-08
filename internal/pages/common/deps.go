package common

import (
	"context"
	"database/sql"
	"strings"
	"sync"

	"github.com/universaltill/universal-till/internal/ai"
	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/ui"
)

// StateMu guards Deps.State: settings handlers replace fields while every
// request renders from them. Readers use CurrentState(), writers UpdateState.
type Deps struct {
	StateMu sync.RWMutex

	// PluginMu serializes the plugin reload-and-rebuild sequence
	// (Pm.Reload + the Menu reassignment, together — see ReloadPlugins).
	// Since ut-docs#460 that sequence fires routinely from the replica
	// sync-pull goroutine every 30s, not just from HTTP handlers, so two
	// writers could otherwise rebuild the Installed map / Menu slice
	// concurrently. Write-side only: handlers still READ Menu/Pm.Installed
	// without a lock — a pre-existing, wider-scope gap tracked separately.
	PluginMu sync.Mutex

	Cfg      *config.Config
	Pm       *plugins.Manager
	Db       *sql.DB
	Settings *settings.Store
	State    RuntimeState
	BaseMenu []MenuItem
	Menu     []MenuItem
	Engine   *pos.Service
	// KioskEngine is the self-order kiosk's own basket engine — deliberately
	// a SEPARATE instance from Engine (ut-docs#449): the kiosk surface is
	// auth-exempt and reachable by any LAN client, so it must never be able
	// to read or mutate the cashier's live sale (before the split, merely
	// landing on GET /self-order wiped the cashier's in-progress basket).
	KioskEngine *pos.Service
	BtnStore    *ui.ButtonStore
	CatalogRepo *marketplace.CatalogRepository
	AuthSvc     *auth.Service
	AI          *ai.Service

	// SyncPushNow, when non-nil, nudges the replica journal-push loop
	// (pages.StartSyncPush) to run one push attempt immediately instead of
	// waiting for its next 30s tick (ut-docs#404, ADR-0036). Set once by
	// StartSyncPush at boot, before the server accepts requests; nil in
	// tests/paths that never start the loop. Capacity-1: one pending nudge
	// already covers any number of sales completed before the loop drains it.
	SyncPushNow chan struct{}
}

// RuntimeState mirrors fields needed from pages.state (theme, tax, currency).
type RuntimeState struct {
	Theme        string
	Currency     string
	Country      string
	Region       string
	TaxInclusive bool
	TaxRatePct   int
	// ServiceChargeRateBasisPoints (ut-docs#244) is the till-set service
	// charge rate in basis points (1bp = 0.01%), added to the sale total
	// (distinct from tip); 0 = disabled. Basis-point granularity is finer
	// than whole percent, so fractional rates like the UK's standard 12.5%
	// (1250bp) are expressible exactly — unlike TaxRatePct, which stays
	// whole-percent (a separate, explicitly out-of-scope limitation).
	ServiceChargeRateBasisPoints int
	AllowNegativeInventory       bool
	UIScale                      float64 // interface scale for this till's screen (0 = unset)
	IdleLockMinutes              int     // idle auto-lock window in minutes (0 = off)
	OSKMode                      string  // on-screen keyboard: auto|on|off ("" = auto)
	KioskIdleResetSeconds        int     // self-order kiosk: reload to start after N idle seconds (ADR-0020); 0 = off
}

// CurrentState returns a consistent copy of the runtime state for rendering.
func (d *Deps) CurrentState() RuntimeState {
	d.StateMu.RLock()
	defer d.StateMu.RUnlock()
	return d.State
}

// UpdateState applies fn to the state under the write lock and returns the
// resulting copy (for persisting via SaveState).
func (d *Deps) UpdateState(fn func(*RuntimeState)) RuntimeState {
	d.StateMu.Lock()
	defer d.StateMu.Unlock()
	fn(&d.State)
	return d.State
}

// SetState replaces the runtime state wholesale under the write lock. Callers
// that persist a candidate state via SaveState before committing it in
// memory (ut-docs#157) use this instead of UpdateState, so a failed save
// never becomes the new in-memory state — otherwise it would silently ride
// along on the next unrelated successful save.
func (d *Deps) SetState(st RuntimeState) {
	d.StateMu.Lock()
	defer d.StateMu.Unlock()
	d.State = st
}

type MenuItem struct {
	Href  string
	Label string
}

// SyncPrimaryURL returns the primary's URL when this till is a replica
// (empty otherwise) — admin pages use it for the "edit on the primary"
// banner (ADR-0011 D4: catalog is primary-wins, local edits get overwritten).
func (d *Deps) SyncPrimaryURL(ctx context.Context) string {
	if d.Settings == nil {
		return ""
	}
	v, _, _ := d.Settings.Get(ctx, "sync.primary_url")
	return strings.TrimSpace(v)
}

// ReloadPlugins is THE way to refresh plugin-derived state after any plugin
// lifecycle change (install/uninstall/enable/disable/update/rollback/import):
// it reloads the plugin manager and rebuilds the nav menu as one critical
// section under PluginMu, so the background sync-pull goroutine (ut-docs#460)
// and HTTP handlers can never interleave the two writes and corrupt the
// Installed map / Menu. Nil-safe on Pm (deps without a plugin manager are a
// no-op, matching cloudRemovePlugin's historical check). The returned error
// is the reload's — the menu is still rebuilt from whatever loaded, matching
// every call site's historical log-and-continue behavior.
func (d *Deps) ReloadPlugins(ctx context.Context) error {
	if d.Pm == nil {
		return nil
	}
	d.PluginMu.Lock()
	defer d.PluginMu.Unlock()
	err := d.Pm.Reload(ctx)
	d.Menu = BuildMenu(d.BaseMenu, d.Pm)
	return err
}

// RequestSyncPush asks the replica journal-push loop for one immediate push
// attempt — called after a locally completed sale (ut-docs#404, ADR-0036) so
// the primary hears about it in seconds rather than at the next 30s tick.
// Non-blocking and best-effort by design: checkout must never wait on the
// network (ADR-0003). With no loop running (primary/single till, tests) it
// is a no-op, and a full buffer means a push is already pending.
func (d *Deps) RequestSyncPush() {
	if d.SyncPushNow == nil {
		return
	}
	select {
	case d.SyncPushNow <- struct{}{}:
	default:
	}
}
