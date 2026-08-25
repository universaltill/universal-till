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
	// (Pm.Reload + the Menu reassignment, together — see ReloadPlugins)
	// against every read of Menu/Pm.Installed/Pm.MenuPlugins (via
	// MenuSnapshot / InstalledPlugin / MenuPluginByKey below). Since
	// ut-docs#460 that sequence fires routinely from the replica
	// sync-pull goroutine every 30s, not just from HTTP handlers — an
	// RWMutex lets concurrent request handlers keep reading in parallel
	// while still serializing against the reload's writes. Pm.Reload
	// reassigns Installed AND MenuPlugins to fresh maps and repopulates
	// both key-by-key (see plugins.Manager.Reload) — an unlocked
	// concurrent read of EITHER isn't just stale data, it's a fatal
	// concurrent map access (ut-docs#478). Read call sites MUST go
	// through MenuSnapshot / InstalledPlugin / MenuPluginByKey, never
	// touch Menu/Pm.Installed/Pm.MenuPlugins directly — if Manager grows
	// another field reassigned inside Reload's critical section, it
	// needs the same treatment, not just the fields a report happened to
	// name.
	PluginMu sync.RWMutex

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
	// WindowCtl is the host-OS hook for the till's own window/process
	// (ut-docs#608 scaffold) — exiting kiosk/fullscreen to the OS desktop,
	// and (later) actually applying a window-mode change. NoopWindowController
	// until #609/#610/#611 wire a real per-platform implementation.
	WindowCtl WindowController

	// SyncPushNow, when non-nil, nudges the replica journal-push loop
	// (pages.StartSyncPush) to run one push attempt immediately instead of
	// waiting for its next 30s tick (ut-docs#404, ADR-0036). Set once by
	// StartSyncPush at boot, before the server accepts requests; nil in
	// tests/paths that never start the loop. Capacity-1: one pending nudge
	// already covers any number of sales completed before the loop drains it.
	SyncPushNow chan struct{}

	// OrderStatus is the in-process pub/sub for order lifecycle status
	// changes (ut-docs#526): the one-tap status endpoint Publishes on every
	// APPLIED change (stale writes dropped by the conflict rule never
	// publish), and the future KDS/pager/customer-tracking surfaces
	// (#516/#517/#528/#527) Subscribe instead of polling the table. Set once
	// in pages.Init; handlers nil-check it so bare-Deps tests stay valid.
	OrderStatus *pos.OrderStatusBroadcaster

	// AsyncWork tracks best-effort, fire-and-forget goroutines started
	// after a request already responded — printReceiptAsync (ut-docs#425)
	// is the first user: checkout must never block on a slow/absent
	// printer (offline-first), so it's dispatched via `go func()` with no
	// caller left holding a handle to it. That's correct for production,
	// but it means the goroutine can still be mid-flight (reading
	// Settings/writing an audit row through Db) after the request that
	// started it has already returned — a real, reproducible bug: a test
	// that closes Db and removes its TempDir right after the HTTP
	// response raced this goroutine's own DB access, intermittently
	// tripping "sql: database is closed" and (via t.TempDir()'s
	// RemoveAll racing SQLite's WAL sidecar files) "directory not empty"
	// on cleanup. Any such goroutine should `AsyncWork.Add(1)` before
	// starting and `defer AsyncWork.Done()`; callers that need every
	// background effect to have settled before tearing down shared state
	// (tests closing Db, graceful shutdown) call WaitForAsyncWork first —
	// same shape as WasmRuntime's own wg/Close (ut-docs#380).
	//
	// Known sharp edge, not yet fixed (ut-docs#513 code review, 2026-08-12):
	// since app.Run's shutdown now calls WaitForAsyncWork in production
	// (previously test-only), sync.WaitGroup's own documented misuse case
	// is reachable there — Add taking the counter 0→1 concurrently with an
	// in-flight Wait panics ("WaitGroup misuse: Add called concurrently
	// with Wait"). server.Start bounds its own graceful shutdown at a fixed
	// timeout but does not kill still-running handlers past it, so a
	// tender/invoice handler still executing when that bound elapses could
	// call AsyncWork.Add(1) just as the drain's Wait sees the counter hit
	// zero. Narrow (only reachable on an already-degraded shutdown path)
	// and not yet guarded — a "refuse new Add once shutdown has begun" flag
	// would close it; tracked as a follow-up rather than fixed inline here.
	AsyncWork sync.WaitGroup

	// BrokenRefetchMu guards BrokenRefetch below.
	BrokenRefetchMu sync.Mutex
	// BrokenRefetch tracks consecutive marketplace re-fetch attempts per
	// locally-broken plugin listing (pages.convergePluginSet, ut-docs#368
	// round-2 review): a plugin broken for a reason a re-fetch can never fix
	// (e.g. a binary that installs fine but can never load on this device)
	// must not hammer the marketplace every 30s forever, so past a small
	// attempt cap the loop degrades to a much slower retry cadence. Lazily
	// initialized under BrokenRefetchMu; in-memory only BY DESIGN — the
	// count is retry-policy bookkeeping, not till state, and a restart
	// granting a fresh burst of attempts is exactly the manual-recovery
	// affordance an operator restarting the till would expect.
	BrokenRefetch map[string]*BrokenRefetchState
}

// BrokenRefetchState is one listing's broken-plugin re-fetch bookkeeping —
// see Deps.BrokenRefetch.
type BrokenRefetchState struct {
	// Version the attempts were counted against; the primary moving the
	// listing to a different version resets the count (a new version is a
	// genuinely new thing to try, not the same doomed fetch again).
	Version string
	// Attempts is the count of consecutive re-fetch attempts without an
	// observed heal.
	Attempts int
	// TicksSkipped counts sync ticks skipped since the last attempt, once
	// past the attempt cap.
	TicksSkipped int
}

// WaitForAsyncWork blocks until every in-flight best-effort background
// goroutine tracked via AsyncWork (e.g. printReceiptAsync) has finished.
// Call this before closing Db or removing any directory Db's file lives in
// — see AsyncWork's doc comment for why skipping it is a real race, not a
// theoretical one.
func (d *Deps) WaitForAsyncWork() {
	d.AsyncWork.Wait()
}

// RuntimeState mirrors fields needed from pages.state (theme, tax, currency).
type RuntimeState struct {
	Theme    string
	Currency string
	Country  string
	Region   string
	// Locale is the shop's default locale (ut-docs#861) — settable live via
	// Settings' Language card, distinct from a manager's own per-browser
	// ?lang=/ut_lang cookie preference. Empty means "use the compiled-in/
	// UT_DEFAULT_LOCALE fallback", same as every other RuntimeState field.
	Locale       string
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
	WindowMode                   string  // ut-docs#608 scaffold: fullscreen|kiosk|maximized|normal
	LaunchOnStartup              bool    // ut-docs#608 scaffold: launch this till on OS boot
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

// MenuSnapshot returns the current nav menu under PluginMu's read lock — the
// only safe way to read Menu, since ReloadPlugins (the sync-pull goroutine,
// every 30s since ut-docs#460, or any plugin lifecycle handler) reassigns it
// concurrently (ut-docs#478). Callers that used to read d.Menu directly (nav
// templates, menu_page.go's active-item lookup) call this instead.
func (d *Deps) MenuSnapshot() []MenuItem {
	d.PluginMu.RLock()
	defer d.PluginMu.RUnlock()
	return d.Menu
}

// InstalledPlugin returns the installed plugin for id (if any) under
// PluginMu's read lock. Manager.Reload repopulates Pm.Installed key-by-key
// into a fresh map, so an unlocked concurrent read racing that isn't just
// stale data — it's a fatal concurrent map read/write crash (ut-docs#478).
// Nil-safe on Pm, matching every existing call site's own nil handling.
func (d *Deps) InstalledPlugin(id string) (plugins.Plugin, bool) {
	if d.Pm == nil {
		return plugins.Plugin{}, false
	}
	d.PluginMu.RLock()
	defer d.PluginMu.RUnlock()
	p, ok := d.Pm.Installed[id]
	return p, ok
}

// MenuPluginByKey returns the registered external menu plugin for key (if
// any) under PluginMu's read lock. Manager.Reload reassigns Pm.MenuPlugins
// to a fresh map and repopulates it key-by-key in the exact same critical
// section as Installed (internal/plugins/plugins.go), so this has the same
// fatal-crash-on-unlocked-read profile as InstalledPlugin (ut-docs#478
// review round 1 — the original sweep grepped for the two field names named
// in the ticket and missed this third one; every field Manager.Reload
// reassigns needs a locked accessor, not just the two the ticket happened
// to name). Nil-safe on Pm.
func (d *Deps) MenuPluginByKey(key string) (plugins.MenuPlugin, bool) {
	if d.Pm == nil {
		return plugins.MenuPlugin{}, false
	}
	d.PluginMu.RLock()
	defer d.PluginMu.RUnlock()
	mp, ok := d.Pm.MenuPlugins[key]
	return mp, ok
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
