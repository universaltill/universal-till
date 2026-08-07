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

	Cfg         *config.Config
	Pm          *plugins.Manager
	Db          *sql.DB
	Settings    *settings.Store
	State       RuntimeState
	BaseMenu    []MenuItem
	Menu        []MenuItem
	Engine      *pos.Service
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
	Theme                  string
	Currency               string
	Country                string
	Region                 string
	TaxInclusive           bool
	TaxRatePct             int
	ServiceChargeRatePct   int // till-set service charge %, added to the sale total (distinct from tip); 0 = disabled
	AllowNegativeInventory bool
	UIScale                float64 // interface scale for this till's screen (0 = unset)
	IdleLockMinutes        int     // idle auto-lock window in minutes (0 = off)
	OSKMode                string  // on-screen keyboard: auto|on|off ("" = auto)
	KioskIdleResetSeconds  int     // self-order kiosk: reload to start after N idle seconds (ADR-0020); 0 = off
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
