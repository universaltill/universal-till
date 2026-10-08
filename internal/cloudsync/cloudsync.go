// Package cloudsync is the till side of ADR-0018: the shop's till talks to
// Universal Till Cloud on a periodic loop — pushes fleet state (heartbeat +
// health) up, pulls remote-management directives down, applies them through
// the SAME code paths a local operator action would take, and reports each
// result. Best-effort and entirely off the sale path (ADR-0003): a dead
// network just means the next tick retries; checkout never waits on it.
package cloudsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/diagnostics"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/iconid"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/netaccess"
	"github.com/universaltill/universal-till/internal/pos"
)

var (
	// httpClient is shared by every /v1/stores/* call this package makes:
	// post() and postJSON() (diagnostics.go) both use it, plus the two
	// direct httpClient.Do call sites in issue_reports.go. Its Transport is
	// a DEDICATED clone of http.DefaultTransport (ut-docs#2588), not the
	// default transport itself — see newHTTPTransport's own doc comment for
	// why the default's pooling knobs were the wrong fit here.
	httpClient = netaccess.NewClientWithTransport(30*time.Second, newHTTPTransport())
	started    = time.Now()
	// tickIntervalNS/firstDelayNS back the two interval knobs below.
	// atomic.Int64 (nanoseconds), not plain vars: Start()'s loop reads them
	// on every iteration from its own background goroutine, and tests
	// override them to run the loop fast — a plain var would race the
	// goroutine's read against a test's write with no synchronization.
	tickIntervalNS atomic.Int64
	firstDelayNS   atomic.Int64
)

func init() {
	// A till that was off picks up queued directives (e.g. a portal-side
	// "install to tills") right after boot, and a running till within a
	// couple of minutes — each tick is one tiny POST, so the short interval
	// is cheap (Farshid, 2026-07-19). A push channel could replace the poll
	// later; the poll stays as the NAT-safe fallback.
	tickIntervalNS.Store(int64(2 * time.Minute))
	firstDelayNS.Store(int64(15 * time.Second))
}

func tickInterval() time.Duration { return time.Duration(tickIntervalNS.Load()) }
func firstDelay() time.Duration   { return time.Duration(firstDelayNS.Load()) }

// newHTTPTransport builds cloudsync's dedicated transport (ut-docs#2588). A
// jittered normal tick ranges up to tickInterval()*1.2 — 144s in
// production — which already exceeds http.DefaultTransport's 90s
// IdleConnTimeout, so sharing that transport meant every routine tick
// reopened a fresh connection (and, against an https cloud endpoint, a
// fresh TLS handshake) instead of reusing the one pooled from the tick
// before. Cloned from DefaultTransport (not built from scratch) to keep its
// proxy-from-env, dial timeouts, TLS handshake timeout and
// ForceAttemptHTTP2 — only the idle-pool knobs below change.
// MaxIdleConns/MaxIdleConnsPerHost stay small: this is one till talking to
// one cloud host, never a fan-out client.
// Reuse ACROSS ticks also needs the server side to keep the connection idle
// that long; ingress-nginx's 75s default keep-alive closes it first, which
// is tracked as ut-docs#2607. Reuse within a tick (sync,
// snapshot, results, issue reports) works regardless.
func newHTTPTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.IdleConnTimeout = 5 * time.Minute // > the slowest jittered normal tick (144s), with headroom
	t.MaxIdleConns = 4
	t.MaxIdleConnsPerHost = 2
	return t
}

// Hooks are the till-local actions a directive may trigger. Each returns a
// short human message for the cloud's result column. A nil hook marks the
// directive type unsupported on this till.
type Hooks struct {
	SetSetting func(ctx context.Context, key, value string) (string, error)
	// SetTillSetting handles the "set_till_setting" directive (ut-docs#2289;
	// Decision 1 of the shared portal-till configuration design in
	// universaltill/ut-docs#2306 — proposed as ADR-0095, PR not yet merged):
	// the same {key, value} shape as set_setting, but the hook itself
	// (pages.cloudSetTillSetting) refuses any key outside an explicit
	// whitelist of safe, non-device-bound shop-config keys — the
	// till-side half of a check the portal also makes, so a stale or buggy
	// portal can never push a printer address, a TSE credential, a PIN or a
	// network setting through. Deliberately a separate hook from SetSetting:
	// that one stays the generic, unrestricted channel and is untouched.
	SetTillSetting func(ctx context.Context, key, value string) (string, error)
	InstallPlugin  func(ctx context.Context, listingID string) (string, error)
	RemovePlugin   func(ctx context.Context, pluginID string) (string, error)
	SetPrice       func(ctx context.Context, itemID string, priceMinor int64) (string, error)
	AdjustStock    func(ctx context.Context, itemID string, delta float64, reason string) (string, error)
	RenameItem     func(ctx context.Context, itemID, name string) (string, error)
	DeactivateItem func(ctx context.Context, itemID string) (string, error)
	CreateItem     func(ctx context.Context, name string, priceMinor int64, barcode string) (string, error)
	AddBarcode     func(ctx context.Context, itemID, barcode string) (string, error)
	// FiscalTSEReady handles the payload-less "fiscal_tse_ready" directive
	// (ADR-0053, ut-docs#802): the cloud finished reseller-provisioning this
	// shop's TSE, so the till must fetch its operational signing credential
	// (a separate, authenticated, single-use endpoint — the credential never
	// rides the directive itself) and store it locally. The hook only acks
	// success once the credential is confirmed stored on disk.
	FiscalTSEReady func(ctx context.Context) (string, error)
	// DiagnosticModeRevoke handles the "diagnostic_mode_revoke" directive
	// (ADR-0092 §1/§4, ut-docs#2169): Universal Till ended this till's
	// diagnostic session. Payload is {session_id} only — no secret material
	// rides the directive (ADR-0053's "signal, not secret" shape). The hook
	// clears the local active flag AND drains the whole on-disk pending
	// queue for that session in one step, so a revoked till doesn't spend
	// N more ticks rediscovering "not active" one 409 at a time.
	DiagnosticModeRevoke func(ctx context.Context, sessionID string) (string, error)
	// UpsertCategory handles the "upsert_category" directive (ut-docs#2323,
	// ADR-0095 Decision 1) — creates a category (empty id) or updates an
	// existing one's name/colour, through the same repo calls the local
	// admin category editor uses. Modifier-group and kitchen-station
	// attachment are deliberately out of scope here (need the read-side
	// StoreSnapshot extension ADR-0095 Decision 2 hasn't shipped yet).
	UpsertCategory func(ctx context.Context, id, name, color string) (string, error)
	// CreateStockLocation, RenameStockLocation and SetStockLocationActive
	// handle "create_stock_location" {name}, "rename_stock_location"
	// {location_id, name} and "set_stock_location_active" {location_id,
	// active} (ut-docs#3383): the stock locations node in my. Main-till only
	// (stock_locations is primary-wins synced, like the catalog), through
	// the same POSRepo calls the till's own /locations admin page makes,
	// audited and idempotent — an existing active location of the same
	// name, a rename to the current name or a location already in the
	// asked-for state applies with no write. Deactivating refuses with the
	// locations page's own wording (locations.error.in_use /
	// last_location), which becomes the directive's failure message. See
	// pages.cloudCreateStockLocation & co.
	CreateStockLocation    func(ctx context.Context, name string) (string, error)
	RenameStockLocation    func(ctx context.Context, id, name string) (string, error)
	SetStockLocationActive func(ctx context.Context, id string, active bool) (string, error)
	// UpdateCategory handles the "update_category" directive (ut-docs#2354):
	// a partial edit of an EXISTING category from the cloud's category
	// editor, which picks from the categories/groups/stations the till last
	// reported (#2472). A nil argument means "keep"; a non-nil one sets the
	// field (color "" clears it; an empty list clears that link set). A
	// separate type rather than new upsert_category keys so an older till
	// fails it visibly ("unknown directive type") instead of ignoring the
	// links and reading an absent colour as "clear".
	UpdateCategory func(ctx context.Context, id string, name, color *string, groupIDs, stationIDs *[]string) (string, error)
	// UpdateItemDetails handles the "update_item_details" directive
	// (ut-docs#2324, ADR-0095 Decision 1) — a partial update of an existing
	// item's sku/description/unit/colour/is_weighed/stock_untracked, through
	// the same read-modify-write the local admin item editor's
	// catalogtypes.ItemInput round-trip makes. Each *string/*bool pointer is
	// nil when its field is ABSENT from the directive's payload, meaning
	// "leave this field untouched" — not "clear it" or "set false". Only
	// item_id is required; every other field is optional. Category/brand/
	// tax-code assignment is deliberately out of scope here (needs the
	// read-side lookup sync ADR-0095 Decision 2 hasn't shipped yet,
	// ut-docs#2354).
	UpdateItemDetails func(ctx context.Context, itemID string, sku, description, unit, color *string, isWeighed, stockUntracked *bool) (string, error)
	// SetQuickButtonLayout handles the "set_quick_button_layout" directive
	// (ut-docs#2321, order-only slice of ADR-0095's Decision 1 — per-item
	// colour/tab reassignment from the cloud panel is explicit deferred
	// scope, tracked as a follow-up card, not implemented here): reorders
	// the shop's quick-sale (shortcut) buttons from the cloud's layout
	// panel — barcodes arrive as the FULL ordered list, display order
	// first, the same UpdateOrder call the till's own Designer
	// move-up/move-down reorder makes locally. Unlike the LAN route (which
	// trusts its own page to always post the full list), the hook rejects
	// a payload that doesn't cover exactly the till's current button set —
	// a missing or unrecognized barcode is refused, not a silent no-op.
	SetQuickButtonLayout func(ctx context.Context, barcodes []string) (string, error)
	// UpsertModifierGroup handles the "upsert_modifier_group" directive
	// (ut-docs#2322 "modifier groups editor" slice of ut-docs#2289, ADR-0095
	// Decision 1) — creates a NEW modifier group (with zero or more options)
	// through the same repo calls a local admin creator would use. itemID
	// is optional since ADR-0101 (ut-docs#2399): blank creates a shop-wide
	// group with no assignment; present, the group is also linked to that
	// existing till item. CREATE-ONLY, deliberately: there is no group id in
	// this directive's payload at all, the same scope cut UpsertCategory
	// above shipped with — the cloud panel has no way to discover an
	// existing group's id to edit by, or to offer an "attach an existing
	// group" picker, until the read-side StoreSnapshot extension (ADR-0095
	// Decision 2) ships. See pages.cloudUpsertModifierGroup.
	UpsertModifierGroup func(ctx context.Context, itemID, name string, required bool, minSelect, maxSelect int, options []ModifierGroupOption) (string, error)
	// The manage-shop catalog directives (ut-docs
	// reference/manage-shop-catalog-api.md §3). All five are main-till only:
	// Tick skips them on a satellite till (no apply, no result post). Each
	// hook applies its change atomically through the till's own repository
	// write path, writes one audit row and is idempotent. The save types
	// take a patch of pointer fields — nil means the field was absent.
	SaveItem            func(ctx context.Context, p data.ItemPatch) (string, error)
	SaveCategory        func(ctx context.Context, p data.CategorySave) (string, error)
	DeleteCategory      func(ctx context.Context, id, moveItemsTo string) (string, error)
	SaveModifierGroup   func(ctx context.Context, p data.ModifierGroupSave) (string, error)
	DeleteModifierGroup func(ctx context.Context, id string) (string, error)
	// DeleteItem handles "delete_item" {id} (ut-docs#3317): the owner
	// deleted an item in my., which offers Delete only while the cloud's
	// copy says the item was never sold. That flag is advisory: the hook
	// re-checks on the till's own data (never sold, not on a quick button,
	// not in an open or parked basket) and refuses with the reason —
	// "sold 14 times — deactivate it instead" — or deletes the item for
	// good through the catalog cleanup's own repository rule. Main-till
	// only; idempotent (an item already gone reports "already deleted").
	DeleteItem func(ctx context.Context, id string) (string, error)
	// SaveOptionSet / DeleteOptionSet handle "save_option_set" /
	// "delete_option_set" (ut-docs#3319): main-till only like the
	// modifier-group pair. Delete refuses a set still applied to an item;
	// the hook's error text names those items and is the directive's
	// failure message.
	SaveOptionSet   func(ctx context.Context, p data.OptionSetSave) (string, error)
	DeleteOptionSet func(ctx context.Context, id string) (string, error)
	// SaveItemVariant handles "save_item_variant" (ut-docs#3477): an
	// item's variant added, edited, deactivated or reactivated from my.
	// Main-till only like the catalog hooks above; one transaction
	// (CatalogRepo.SaveVariant), audited, idempotent. A variant is never
	// deleted — active=false retires it.
	SaveItemVariant func(ctx context.Context, p data.VariantSave) (string, error)
	// SetCategoryOrder handles "set_category_order" (contract §3.8,
	// ut-docs#3075): the owner's category order from my., as the full
	// ordered id list. Main-till only like the five above. The hook
	// refuses an id that is not a categories row (nothing written), then
	// makes the same CatalogRepo.SetCategorySortOrder call the till's own
	// category list and Designer reorders make — unlisted categories keep
	// their relative order after the listed ones. Audited, idempotent.
	SetCategoryOrder func(ctx context.Context, ids []string) (string, error)
	// SetCatalogImage handles "set_catalog_image" (contract §3.9,
	// ut-docs#3076): an item or category image set or removed in my.
	// Main-till only like the catalog hooks above. apply decodes and
	// validates the payload (rule 1's shape checks); the hook refuses an id
	// that is not on this till, then — for a set — calls img.Fetch (the
	// download from the cloud with the store credential, size-capped and
	// checksum-verified) and writes through the till's own upload path, or
	// — for a clear — the till's own Remove. Audited, idempotent.
	SetCatalogImage func(ctx context.Context, img CatalogImage) (string, error)
	// The till user directives (ut-docs reference/till-user-directives.md
	// §4, ADR-0115 amendment 2026-09-25): main-till only, like the catalog
	// ones above. Each opens pin_sealed (when present) with the main
	// till's directive key and checks/hashes the PIN before its single
	// write transaction, then applies through the same AuthRepo guards as
	// the till's own users screens, audited and idempotent.
	SaveUser       func(ctx context.Context, u UserDirective) (string, error)
	SetUserPIN     func(ctx context.Context, u UserDirective) (string, error)
	DeactivateUser func(ctx context.Context, u UserDirective) (string, error)
	// The custom role directives (ADR-0128 §3, ut-docs#3165): main-till
	// only. SaveRole creates or replaces an origin='cloud' role and its
	// complete grant set; DeleteRole removes one no user holds. Each runs
	// in one write transaction, audited and idempotent.
	SaveRole   func(ctx context.Context, r RoleDirective) (string, error)
	DeleteRole func(ctx context.Context, r RoleDirective) (string, error)
	// RenameTill handles "rename_till" (ut-docs#3272): the owner renamed
	// THIS till in the cloud (my. or the portal). Payload {device_id, name};
	// Tick already skipped it unless device_id is this till's own, so it
	// applies on the main till and on an additional till alike (not
	// main-till only). The hook (pages.cloudRenameTill) validates the name
	// like the Settings rename — refusing, never truncating, so the name the
	// till reports back equals the cloud's — then writes the key
	// enroll.DeviceName reads for this till's role, audited. An unchanged
	// name is applied with no write.
	RenameTill func(ctx context.Context, name string) (string, error)
	// PrintReport handles "print_report" (ut-docs#2537): print a report
	// the cloud already computed on this till's receipt printer. Tick
	// already skipped it unless its device_id is this till's own, so it
	// applies on the main till and on an additional till alike; apply has
	// decoded and bounded the payload (decodePrintReport). The hook
	// (pages.cloudPrintReport) only lays it out and prints — it never
	// recomputes — and returns an error (the directive's failure message)
	// when no printer is configured or the print fails, never a silent
	// "applied".
	PrintReport func(ctx context.Context, r PrintReport) (string, error)
	// KioskUnlock handles "kiosk_unlock" (ut-docs#3466, ADR-0142 D3): the
	// owner (or staff admin, both with step-up) asked from the cloud to
	// release THIS till's self-order kiosk pin. Payload {device_id} only;
	// Tick already skipped it unless device_id is this till's own, so it
	// applies on a main and an additional till alike (not main-till only,
	// D1). directiveID and createdBy are the directive's own id and
	// created_by, for the till's audit row. The hook
	// (pages.cloudKioskUnlock) re-checks display.mode = self_order itself —
	// the cloud's own check is a UX guard, this is the boundary — and
	// returns the refusal reason (not_self_order, kiosk_appliance,
	// not_supported, no_shell) as its error.
	KioskUnlock func(ctx context.Context, directiveID, createdBy string) (string, error)
	// DeviceExtra contributes extra fields to the device report (e.g. the
	// current theme + the themes this till can switch to, so the cloud can
	// render a real design picker instead of a raw key/value form). Keys must
	// not collide with the fixed report fields.
	DeviceExtra func(ctx context.Context) map[string]any
	// Kick, when non-nil, asks Start's loop for a check-in now (ADR-0117
	// §4, ut-docs#2824: a cloud-link nudge). Single-flight by
	// construction: the loop reads it only between check-ins, so a kick
	// while one runs schedules exactly one more — the sender makes it a
	// capacity-1 channel and sends without blocking, which coalesces any
	// number of kicks into that one. Ignored while the loop is backing off
	// after a failure, so a nudge can never beat the backoff or a
	// Retry-After; a kick left pending then is satisfied by the check-in
	// the backoff ends in (the loop drains it as each check-in starts).
	Kick <-chan struct{}
	// BeforeTick, when non-nil, runs on Start's goroutine as each check-in
	// starts: after any pending Kick was drained (this check-in satisfies
	// it), before the POST. A kick seen before BeforeTick is covered by the
	// check-in that follows; one after it runs one more (ut-docs#2893: the
	// main till relays a cloud nudge to its replicas only after a check-in
	// that started after the nudge). It must not block.
	BeforeTick func()
	// AfterTick, when non-nil, is told each check-in's outcome on Start's
	// goroutine (the cloud link re-reads its tier and role after every
	// check-in, ADR-0117 §1). contacted is true only when the check-in
	// really reached the cloud — false for an unregistered till's skip, an
	// unpaid till's gated skip (ADR-0148) and for a failed POST — so only a real contact lifts the link's "wait for
	// the next check-in". It must not block.
	AfterTick func(ctx context.Context, contacted bool, err error)
	// LinkVersion, when non-nil, returns the newest link_version the cloud
	// link has seen in a hello or nudge (0 = none) — ADR-0117 §3,
	// ut-docs#2827. A version newer than the one the last successful POST
	// processed makes the next check-in POST without asking (checkin.go);
	// the link records it before it kicks. Must not block.
	LinkVersion func() int64

	// fetchCatalogImage is set by tick (it needs the cloud endpoint and
	// credential from cfg) and bound into each set_catalog_image's
	// CatalogImage.Fetch. Nil when apply runs without a tick.
	fetchCatalogImage func(ctx context.Context, sha string) ([]byte, error)
}

type directive struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
	// CreatedBy is the cloud's queuing actor, when the cloud sends it (the
	// user directives' audit provenance); "" otherwise.
	CreatedBy string `json:"created_by"`
}

// ModifierGroupOption is one option inside an upsert_modifier_group
// directive's "options" payload field, decoded by modifierGroupOptions
// below. PriceDeltaMinor is already the raw int64 minor-units integer
// convention (internal/money's DB-boundary shape) — money never rides as a
// float here.
type ModifierGroupOption struct {
	Name            string
	PriceDeltaMinor int64
}

// tick runs one full sync round (heartbeat up, directives down, apply,
// report) and says whether the cloud was really contacted: true
// once the /v1/stores/sync POST succeeded, false for an unregistered
// till's early return or an unpaid till's gated one (ADR-0148; nil error,
// no contact) and for any failure.
func tick(ctx context.Context, cfg *config.Config, db *sql.DB, hooks Hooks) (contacted bool, err error) {
	// Issue-report uploads (ADR-0022, spec 012) get a chance on EVERY tick,
	// before the registration/connectivity gates below — ut-docs#637 review:
	// this used to sit at the tail of Tick (see the pull side, still there),
	// which meant an unregistered till (early return just below) or one
	// whose /v1/stores/sync call is failing (the pushSync error return
	// further down) never reached it at all — exactly the two cases this
	// ticket's "surfaced as failing" gate exists for, so the failure count
	// it depends on could never advance in either. Safe to run unconditionally
	// here: uploadPendingIssueReports/uploadIssueReport already self-guard on
	// registration internally (no network call when unregistered — see
	// issue_reports.go's own check) and issuereport.Pending() is a pure local
	// disk read.
	uploadPendingIssueReports(ctx, cfg, db)
	// Diagnostic-mode batches (ADR-0092 §4, ut-docs#2169) ride the same
	// slot for the same reasons: the flush to disk must happen even when
	// unregistered (the ring is time/size-capped, so a stalled flush loses
	// events), and the upload self-guards on registration inside.
	uploadPendingDiagnostics(ctx, cfg, db)

	if !enroll.HasCredentials(cfg) {
		// No store identity at start, no start-up check-in this run
		// (ADR-0148 amendment §1; ADR-0015's lazy registration unchanged).
		disarmStartupCheckin()
		return false, nil // not registered — nothing further to sync
	}

	settings := data.NewSettingsRepo(db)
	// ADR-0148 (ut-docs#3615): only a paid store checks in periodically; an
	// unpaid one only inside an operator window, or once at start-up when
	// its version or the local date changed since its last answered
	// check-in (amendment, ut-docs#3673; takeStartupCheckin). A gated
	// tick is quiet: no network, no warning, not an error (the scheduler
	// keeps its normal cadence and no sync-error chip appears).
	now := operatorNow()
	windowSeen := operatorDeadlineNS.Load()
	unpaid := !syncAllowedFn(ctx, settings)
	if unpaid && !takeStartupCheckin(ctx, settings, now) && !operatorWindowOpen(now) {
		return false, nil
	}

	// ADR-0117 §3 (ut-docs#2827): the conditional check-in decides whether
	// this tick needs the full POST. The body is built first because its
	// till-state part is what the hash covers. An unpaid check-in skips the
	// GET and POSTs (planUnpaidCheckin).
	req := buildSyncRequest(ctx, cfg, settings, hooks)
	sum, hashErr := stateHash(req.devices)
	var plan checkinPlan
	if unpaid {
		plan = planUnpaidCheckin(ctx, settings, sum)
	} else if plan, err = planCheckin(ctx, cfg, settings, sum, hashErr != nil, hooks.LinkVersion); err != nil {
		if _, ok := planRequired(err); ok {
			// A lapse learned from the GET is this start's contact: no
			// start-up check-in follows it (amendment §4).
			closeOperatorWindow(windowSeen)
			disarmStartupCheckin()
		}
		return false, err
	}
	var dirs []directive
	if plan.post {
		dirs, err = pushSync(ctx, cfg, settings, req)
		_, refused := planRequired(err)
		if unpaid && (err == nil || refused) {
			// Only a 2xx or a 402 records the unpaid check-in: the cloud saw
			// this till (amendment §2). Any other answer, or none, records
			// nothing; the next trigger tries again.
			recordUnpaidCheckin(ctx, settings, now)
		}
		if err != nil {
			if refused {
				// No retry until the next trigger (§4).
				closeOperatorWindow(windowSeen)
				disarmStartupCheckin()
			}
			return false, err
		}
		plan.done(cfg)
	}
	// Catalog/inventory up-sync rides the same tick, but only when the shop's
	// data actually changed (hash gate) — most ticks send nothing. Replicas
	// skip it entirely: their catalog mirrors the primary (ADR-0011), so the
	// primary's snapshot is the shop's snapshot.
	primaryURL, _, _ := settings.Get(ctx, "sync.primary_url")
	isMainTill := strings.TrimSpace(primaryURL) == ""
	if isMainTill {
		if err := pushSnapshotIfChanged(ctx, cfg, db); err != nil {
			logging.L().Warnf("cloudsync: snapshot push failed (will retry): %v", err)
		}
		// Order-tracking rows ride the same primary-only gate (ADR-0070),
		// but for the CLOUD's storage model, not the shop's data model:
		// the relay replaces a store's whole row set on every push
		// (ADR-0070 decision 2) and every till of a shop shares one
		// marketplace StoreID, so a second pusher would delete the first's
		// rows every tick. Exactly one till per shop may push; the primary
		// is that till. Known consequence, deliberately NOT claimed away:
		// tracking_token and order_status do not travel on the LAN sale
		// journal (data.SaleDetail carries neither), so a self-order sale
		// taken on a REPLICA is not relayed — its /o/{token} keeps working
		// on the shop's LAN, it just gets no off-LAN fallback. Widening
		// that needs a per-till key on the cloud row set — a follow-up
		// card, not something this gate quietly covers.
		if err := pushOrderTrackingIfChanged(ctx, cfg, db); err != nil {
			logging.L().Warnf("cloudsync: order tracking push failed (will retry): %v", err)
		}
		// Daily sales rollups (ADR-0111, ut-docs#2535) ride the same
		// primary-only gate: the primary holds the shop's full sales journal
		// (a replica's sales are journaled onto it, keyed by their till_id),
		// so it alone uploads every till's rollup — a replica pushing too
		// would double-report its own sales. Throttled and self-logging;
		// never fails the tick.
		pushSalesAggregates(ctx, cfg, db)
		// Archived reports (ADR-0147 §2, ut-docs#574): only in retention
		// mode cloud/both, and only from the main till — report_archive is
		// per-till and not synced, so a replica uploading too would need a
		// till key on the cloud row (a follow-up). Throttled and
		// self-logging; never fails the tick.
		pushReportArchives(ctx, cfg, db)
	}
	// Image fetches share one budget per tick: after the first
	// transport-level failure (errImageFetchUnreachable) no further fetch is
	// tried this tick, and each affected directive stays pending (no result
	// post) for the next tick — never 100 × catalogImageFetchTimeout, never
	// a mass "failed" the owner must re-save by hand.
	imageRouteDown, fetchDeferred := false, false
	hooks.fetchCatalogImage = func(ctx context.Context, sum string) ([]byte, error) {
		if imageRouteDown {
			fetchDeferred = true
			return nil, errImageFetchUnreachable
		}
		b, err := fetchCatalogImage(ctx, cfg, sum)
		if errors.Is(err, errImageFetchUnreachable) {
			imageRouteDown, fetchDeferred = true, true
		}
		return b, err
	}
	catalogApplied := false
	for _, d := range dirs {
		if !isMainTill && mainTillOnlyTypes[d.Type] {
			// Contract §3: a satellite till leaves these pending for the
			// main till — no apply, and no result post that would resolve
			// them as failed.
			// Logged once per directive id (review finding 5), not on
			// every tick while it waits for the main till.
			if firstSatelliteSkip(d.ID) {
				logging.L().Infof("cloudsync: directive %s (%s) skipped: main till only", d.ID, d.Type)
			}
			continue
		}
		if reason := deviceTargetSkipReason(d); reason != "" {
			// A device-targeted directive (rename_till ut-docs#3272,
			// print_report ut-docs#2537, kiosk_unlock ut-docs#3466 —
			// deviceTargetedTypes) addressed to another till (or to none)
			// stays pending for its target — no apply, no result post.
			// Logged once per directive id; an own id not known yet is not
			// remembered, so the real reason is still logged once the id
			// is known.
			if ownDeviceID() == "" {
				logging.L().Infof("cloudsync: directive %s (%s) skipped: %s", d.ID, d.Type, reason)
			} else if firstTargetSkip(d.ID) {
				logging.L().Infof("cloudsync: directive %s (%s) skipped: %s", d.ID, d.Type, reason)
			}
			continue
		}
		fetchDeferred = false
		status, msg := apply(ctx, d, hooks)
		if fetchDeferred && status == "failed" {
			logging.L().Warnf("cloudsync: directive %s (%s) left pending: %s", d.ID, d.Type, msg)
			continue
		}
		if status == "applied" && catalogTypes[d.Type] {
			catalogApplied = true
		}
		if err := postResult(ctx, cfg, d.ID, status, msg); err != nil {
			// Leave it pending on the cloud; the next tick re-applies (the
			// hooks are idempotent for the supported types) and re-reports.
			logging.L().Warnf("cloudsync: result for %s not delivered: %v", d.ID, err)
		} else {
			logging.L().Infof("cloudsync: directive %s (%s) %s: %s", d.ID, d.Type, status, msg)
		}
	}
	// Fresh data in the same tick (contract §3.6): a catalog change the
	// cloud just made is reported back now, not a tick later. The hash gate
	// makes this free when nothing actually changed.
	if isMainTill && catalogApplied {
		if err := pushSnapshotIfChanged(ctx, cfg, db); err != nil {
			logging.L().Warnf("cloudsync: snapshot push after directives failed (will retry): %v", err)
		}
	}
	// Issue-reporter status pull (ADR-0022, spec 012, ut-docs#348): the
	// cloud's per-report statuses come down onto the retained
	// issue_reports_sent rows so /my-reports shows what became of each
	// report. Best-effort, same "leave it and retry next tick" spirit as
	// everything above. The upload direction moved to the top of Tick
	// (ut-docs#637) — this pull direction correctly stays gated behind
	// registration/connectivity above: there is nothing to pull without
	// them. A 304 tick skips it too (ut-docs#2827): it is a network read
	// every time, and the check-in's floor POST brings it back within
	// checkinFloor.
	if plan.post {
		pullIssueReportStatuses(ctx, cfg, db)
	}
	return true, nil
}

// maxCategoryLinkIDs bounds update_category's id lists: each id costs a
// lookup + insert inside the till's single write transaction, so an
// unbounded list from the cloud could hold the write lock long enough to
// fail sale-side writes. Matches the cloud's own queue-time cap.
const maxCategoryLinkIDs = 200

// apply routes one directive to its hook. Unknown types and nil hooks fail
// cleanly so the cloud shows WHY nothing happened.
func apply(ctx context.Context, d directive, hooks Hooks) (status, msg string) {
	str := func(k string) string { v, _ := d.Payload[k].(string); return strings.TrimSpace(v) }
	// JSON numbers decode as float64; tolerate string form too.
	num := func(k string) (int64, bool) {
		switch v := d.Payload[k].(type) {
		case float64:
			return int64(v), true
		case string:
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			return n, err == nil
		}
		return 0, false
	}
	// strs decodes a JSON-array-encoded STRING payload field into an ordered
	// []string, trimming whitespace and dropping blank entries. The array
	// rides inside one string field, not as a raw JSON array value — the
	// cloud side's generic queueDirective field check requires
	// payload[field].(string) (see claims.DirectiveTypes' own doc comment
	// on "set_quick_button_layout"), the same "JSON array inside a string
	// field" shape apply_design/set_setting already use on the portal side.
	// A payload field that isn't a string, or doesn't decode as a JSON
	// array (missing, wrong type, malformed) returns nil rather than
	// guessing — the dispatch below then reports "missing barcodes" instead
	// of silently reinterpreting the payload.
	strs := func(k string) []string {
		raw, ok := d.Payload[k].(string)
		if !ok {
			return nil
		}
		var arr []string
		if err := json.Unmarshal([]byte(raw), &arr); err != nil {
			return nil
		}
		out := make([]string, 0, len(arr))
		for _, s := range arr {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	fnum := func(k string) (float64, bool) {
		switch v := d.Payload[k].(type) {
		case float64:
			return v, true
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			return f, err == nil
		}
		return 0, false
	}
	var err error
	switch d.Type {
	case "set_setting":
		if hooks.SetSetting == nil {
			return "failed", "set_setting is not supported on this till"
		}
		key := str("key")
		if key == "" {
			return "failed", "missing setting key"
		}
		msg, err = hooks.SetSetting(ctx, key, str("value"))
	case "set_till_setting":
		if hooks.SetTillSetting == nil {
			return "failed", "set_till_setting is not supported on this till"
		}
		key := str("key")
		if key == "" {
			return "failed", "missing setting key"
		}
		msg, err = hooks.SetTillSetting(ctx, key, str("value"))
	case "install_plugin":
		if hooks.InstallPlugin == nil {
			return "failed", "install_plugin is not supported on this till"
		}
		id := str("listing_id")
		if id == "" {
			return "failed", "missing listing_id"
		}
		msg, err = hooks.InstallPlugin(ctx, id)
	case "remove_plugin":
		if hooks.RemovePlugin == nil {
			return "failed", "remove_plugin is not supported on this till"
		}
		id := str("plugin_id")
		if id == "" {
			return "failed", "missing plugin_id"
		}
		msg, err = hooks.RemovePlugin(ctx, id)
	case "set_price":
		if hooks.SetPrice == nil {
			return "failed", "set_price is not supported on this till"
		}
		id := str("item_id")
		if id == "" {
			return "failed", "missing item_id"
		}
		price, ok := num("price_minor")
		if !ok || price < 0 {
			return "failed", "missing or invalid price_minor"
		}
		msg, err = hooks.SetPrice(ctx, id, price)
	case "adjust_stock":
		if hooks.AdjustStock == nil {
			return "failed", "adjust_stock is not supported on this till"
		}
		id := str("item_id")
		if id == "" {
			return "failed", "missing item_id"
		}
		delta, ok := fnum("qty_delta")
		if !ok || delta == 0 {
			return "failed", "missing or zero qty_delta"
		}
		msg, err = hooks.AdjustStock(ctx, id, delta, str("reason"))
	case "rename_item":
		if hooks.RenameItem == nil {
			return "failed", "rename_item is not supported on this till"
		}
		id := str("item_id")
		name := str("name")
		if id == "" || name == "" {
			return "failed", "missing item_id or name"
		}
		msg, err = hooks.RenameItem(ctx, id, name)
	case "deactivate_item":
		if hooks.DeactivateItem == nil {
			return "failed", "deactivate_item is not supported on this till"
		}
		id := str("item_id")
		if id == "" {
			return "failed", "missing item_id"
		}
		msg, err = hooks.DeactivateItem(ctx, id)
	case "create_item":
		if hooks.CreateItem == nil {
			return "failed", "create_item is not supported on this till"
		}
		name := str("name")
		if name == "" {
			return "failed", "missing name"
		}
		price, ok := num("price_minor")
		if !ok || price < 0 {
			return "failed", "missing or invalid price_minor"
		}
		msg, err = hooks.CreateItem(ctx, name, price, str("barcode"))
	case "add_barcode":
		if hooks.AddBarcode == nil {
			return "failed", "add_barcode is not supported on this till"
		}
		id := str("item_id")
		barcode := str("barcode")
		if id == "" || barcode == "" {
			return "failed", "missing item_id or barcode"
		}
		msg, err = hooks.AddBarcode(ctx, id, barcode)
	case "fiscal_tse_ready":
		if hooks.FiscalTSEReady == nil {
			return "failed", "fiscal_tse_ready is not supported on this till"
		}
		// Deliberately payload-less (ADR-0053 Decision 1): the directive is
		// a non-secret ready signal; no field validation applies.
		msg, err = hooks.FiscalTSEReady(ctx)
	case "diagnostic_mode_revoke":
		if hooks.DiagnosticModeRevoke == nil {
			return "failed", "diagnostic_mode_revoke is not supported on this till"
		}
		id := str("session_id")
		if id == "" {
			return "failed", "missing session_id"
		}
		msg, err = hooks.DiagnosticModeRevoke(ctx, id)
	case "rename_till":
		if hooks.RenameTill == nil {
			return "failed", "rename_till is not supported on this till"
		}
		// device_id was checked in Tick (deviceTargetSkipReason).
		name := str("name")
		if name == "" {
			return "failed", "missing name"
		}
		msg, err = hooks.RenameTill(ctx, name)
	case "print_report":
		if hooks.PrintReport == nil {
			return "failed", "print_report is not supported on this till"
		}
		// device_id was checked in Tick (deviceTargetSkipReason).
		r, bad := decodePrintReport(d)
		if bad != "" {
			return "failed", bad
		}
		msg, err = hooks.PrintReport(ctx, r)
	case "kiosk_unlock":
		if hooks.KioskUnlock == nil {
			return "failed", "kiosk_unlock is not supported on this till"
		}
		// device_id — the payload's only field — was checked in Tick
		// (deviceTargetSkipReason). The id and created_by the till audits
		// come from the directive itself (ADR-0142 D3).
		msg, err = hooks.KioskUnlock(ctx, d.ID, d.CreatedBy)
	case "upsert_category":
		if hooks.UpsertCategory == nil {
			return "failed", "upsert_category is not supported on this till"
		}
		// id is optional (empty = create), color is optional (empty = no
		// colour); only name is required. Palette validation and the
		// create-vs-update decision belong to the hook, not this dispatch.
		name := str("name")
		if name == "" {
			return "failed", "missing name"
		}
		msg, err = hooks.UpsertCategory(ctx, str("id"), name, str("color"))
	case "create_stock_location":
		if hooks.CreateStockLocation == nil {
			return "failed", "create_stock_location is not supported on this till"
		}
		name := str("name")
		if name == "" {
			return "failed", "missing name"
		}
		msg, err = hooks.CreateStockLocation(ctx, name)
	case "rename_stock_location":
		if hooks.RenameStockLocation == nil {
			return "failed", "rename_stock_location is not supported on this till"
		}
		id := str("location_id")
		name := str("name")
		if id == "" || name == "" {
			return "failed", "missing location_id or name"
		}
		msg, err = hooks.RenameStockLocation(ctx, id, name)
	case "set_stock_location_active":
		if hooks.SetStockLocationActive == nil {
			return "failed", "set_stock_location_active is not supported on this till"
		}
		id := str("location_id")
		if id == "" {
			return "failed", "missing location_id"
		}
		// Required, never defaulted: an absent flag must not read as
		// "deactivate".
		active, ok := payload(d.Payload).optBool("active")
		if !ok || active == nil {
			return "failed", "missing or invalid active"
		}
		msg, err = hooks.SetStockLocationActive(ctx, id, *active)
	case "update_category":
		if hooks.UpdateCategory == nil {
			return "failed", "update_category is not supported on this till"
		}
		id := str("id")
		if id == "" {
			return "failed", "missing id"
		}
		// Presence-aware: absent → nil (keep), present → value. A present
		// field of the wrong shape fails rather than being read as absent,
		// which would turn a bad edit into a silent partial one.
		optStr := func(k string) (*string, bool) {
			v, present := d.Payload[k]
			if !present {
				return nil, true
			}
			s, ok := v.(string)
			if !ok {
				return nil, false
			}
			s = strings.TrimSpace(s)
			return &s, true
		}
		optIDs := func(k string) (*[]string, bool) {
			v, present := d.Payload[k]
			if !present {
				return nil, true
			}
			raw, ok := v.(string)
			if !ok {
				return nil, false
			}
			var arr []string
			if err := json.Unmarshal([]byte(raw), &arr); err != nil || arr == nil || len(arr) > maxCategoryLinkIDs {
				return nil, false
			}
			for i := range arr {
				arr[i] = strings.TrimSpace(arr[i])
			}
			return &arr, true
		}
		name, ok := optStr("name")
		if !ok {
			return "failed", "bad name"
		}
		color, ok := optStr("color")
		if !ok {
			return "failed", "bad color"
		}
		groupIDs, ok := optIDs("modifier_group_ids")
		if !ok {
			return "failed", "bad modifier_group_ids"
		}
		stationIDs, ok := optIDs("station_ids")
		if !ok {
			return "failed", "bad station_ids"
		}
		if name == nil && color == nil && groupIDs == nil && stationIDs == nil {
			return "failed", "nothing to update"
		}
		msg, err = hooks.UpdateCategory(ctx, id, name, color, groupIDs, stationIDs)
	case "update_item_details":
		if hooks.UpdateItemDetails == nil {
			return "failed", "update_item_details is not supported on this till"
		}
		id := str("item_id")
		if id == "" {
			return "failed", "missing item_id"
		}
		// Presence-aware readers: str/num/fnum/strs above all return the
		// zero value when the key is absent, which can't distinguish
		// "absent" (leave untouched) from "present but empty/false" — the
		// whole point of this directive's partial-update contract. strp/
		// boolp instead return nil exactly when the key is missing from
		// the payload.
		strp := func(k string) *string {
			v, ok := d.Payload[k]
			if !ok {
				return nil
			}
			s, _ := v.(string)
			s = strings.TrimSpace(s)
			return &s
		}
		boolp := func(k string) *bool {
			v, ok := d.Payload[k]
			if !ok {
				return nil
			}
			switch t := v.(type) {
			case bool:
				return &t
			case string:
				b, err := strconv.ParseBool(strings.TrimSpace(t))
				if err != nil {
					return nil
				}
				return &b
			}
			return nil
		}
		msg, err = hooks.UpdateItemDetails(ctx, id, strp("sku"), strp("description"), strp("unit"), strp("color"), boolp("is_weighed"), boolp("stock_untracked"))
	case "set_quick_button_layout":
		if hooks.SetQuickButtonLayout == nil {
			return "failed", "set_quick_button_layout is not supported on this till"
		}
		barcodes := strs("barcodes")
		if len(barcodes) == 0 {
			return "failed", "missing barcodes"
		}
		msg, err = hooks.SetQuickButtonLayout(ctx, barcodes)
	case "upsert_modifier_group":
		if hooks.UpsertModifierGroup == nil {
			return "failed", "upsert_modifier_group is not supported on this till"
		}
		// item_id is optional (ADR-0101, ut-docs#2399): blank means a
		// shop-wide group with no assignment; the hook validates a
		// non-blank one against the till's own items.
		id := str("item_id")
		name := str("name")
		if name == "" {
			return "failed", "missing name"
		}
		required, _ := d.Payload["required"].(bool)
		minSelect, minOK := num("min_select")
		maxSelect, maxOK := num("max_select")
		if !minOK || !maxOK {
			return "failed", "missing or invalid min_select/max_select"
		}
		options, oerr := modifierGroupOptions(d.Payload["options"])
		if oerr != nil {
			return "failed", oerr.Error()
		}
		msg, err = hooks.UpsertModifierGroup(ctx, id, name, required, int(minSelect), int(maxSelect), options)
	case "save_item":
		if hooks.SaveItem == nil {
			return "failed", "save_item is not supported on this till"
		}
		p, bad := decodeSaveItem(payload(d.Payload))
		if bad != "" {
			return "failed", bad
		}
		msg, err = hooks.SaveItem(ctx, p)
	case "set_net_quantity":
		// ut-docs#3402 (§3.12): the same repository path, audit row
		// (cloud_item_saved) and idempotency as save_item — no own hook.
		if hooks.SaveItem == nil {
			return "failed", "set_net_quantity is not supported on this till"
		}
		p, bad := decodeSetNetQuantity(payload(d.Payload))
		if bad != "" {
			return "failed", bad
		}
		msg, err = hooks.SaveItem(ctx, p)
	case "save_category":
		if hooks.SaveCategory == nil {
			return "failed", "save_category is not supported on this till"
		}
		p, bad := decodeSaveCategory(payload(d.Payload))
		if bad != "" {
			return "failed", bad
		}
		msg, err = hooks.SaveCategory(ctx, p)
	case "set_category_order":
		if hooks.SetCategoryOrder == nil {
			return "failed", "set_category_order is not supported on this till"
		}
		ids, bad := decodeCategoryOrder(payload(d.Payload))
		if bad != "" {
			return "failed", bad
		}
		msg, err = hooks.SetCategoryOrder(ctx, ids)
	case "set_catalog_image":
		if hooks.SetCatalogImage == nil {
			return "failed", "set_catalog_image is not supported on this till"
		}
		img, bad := decodeCatalogImage(payload(d.Payload))
		if bad != "" {
			return "failed", bad
		}
		if !img.Clear {
			fetch, sum := hooks.fetchCatalogImage, img.SHA256
			img.Fetch = func(ctx context.Context) ([]byte, error) {
				if fetch == nil {
					return nil, errors.New("this till cannot download images right now; save the image again to retry")
				}
				return fetch(ctx, sum)
			}
		}
		msg, err = hooks.SetCatalogImage(ctx, img)
	case "delete_category":
		if hooks.DeleteCategory == nil {
			return "failed", "delete_category is not supported on this till"
		}
		pl := payload(d.Payload)
		id := pl.id()
		if id == "" {
			return "failed", "missing id"
		}
		target, ok := pl.optStr("move_items_to")
		if !ok {
			return "failed", "bad move_items_to"
		}
		move := ""
		if target != nil {
			move = *target
		}
		msg, err = hooks.DeleteCategory(ctx, id, move)
	case "delete_item":
		if hooks.DeleteItem == nil {
			return "failed", "delete_item is not supported on this till"
		}
		id := payload(d.Payload).id()
		if id == "" {
			return "failed", "missing id"
		}
		msg, err = hooks.DeleteItem(ctx, id)
	case "save_modifier_group":
		if hooks.SaveModifierGroup == nil {
			return "failed", "save_modifier_group is not supported on this till"
		}
		p, bad := decodeSaveModifierGroup(payload(d.Payload))
		if bad != "" {
			return "failed", bad
		}
		msg, err = hooks.SaveModifierGroup(ctx, p)
	case "delete_modifier_group":
		if hooks.DeleteModifierGroup == nil {
			return "failed", "delete_modifier_group is not supported on this till"
		}
		id := payload(d.Payload).id()
		if id == "" {
			return "failed", "missing id"
		}
		msg, err = hooks.DeleteModifierGroup(ctx, id)
	case "save_option_set":
		if hooks.SaveOptionSet == nil {
			return "failed", "save_option_set is not supported on this till"
		}
		p, bad := decodeSaveOptionSet(payload(d.Payload))
		if bad != "" {
			return "failed", bad
		}
		msg, err = hooks.SaveOptionSet(ctx, p)
	case "delete_option_set":
		if hooks.DeleteOptionSet == nil {
			return "failed", "delete_option_set is not supported on this till"
		}
		id := payload(d.Payload).id()
		if id == "" {
			return "failed", "missing id"
		}
		msg, err = hooks.DeleteOptionSet(ctx, id)
	case "save_item_variant":
		if hooks.SaveItemVariant == nil {
			return "failed", "save_item_variant is not supported on this till"
		}
		p, bad := decodeSaveItemVariant(payload(d.Payload))
		if bad != "" {
			return "failed", bad
		}
		msg, err = hooks.SaveItemVariant(ctx, p)
	case "save_user", "set_user_pin", "deactivate_user":
		hook := map[string]func(context.Context, UserDirective) (string, error){
			"save_user": hooks.SaveUser, "set_user_pin": hooks.SetUserPIN, "deactivate_user": hooks.DeactivateUser,
		}[d.Type]
		if hook == nil {
			return "failed", d.Type + " is not supported on this till"
		}
		u, bad := decodeUserDirective(d)
		if bad != "" {
			return "failed", bad
		}
		msg, err = hook(ctx, u)
	case "save_role", "delete_role":
		hook := hooks.SaveRole
		if d.Type == "delete_role" {
			hook = hooks.DeleteRole
		}
		if hook == nil {
			return "failed", d.Type + " is not supported on this till"
		}
		r, bad := decodeRoleDirective(d)
		if bad != "" {
			return "failed", bad
		}
		msg, err = hook(ctx, r)
	default:
		return "failed", "unknown directive type " + d.Type
	}
	if err != nil {
		return "failed", err.Error()
	}
	if msg == "" {
		msg = "done"
	}
	return "applied", msg
}

// modifierGroupOptions decodes the upsert_modifier_group directive's
// "options" payload field — a JSON-encoded array of {name,
// price_delta_minor} objects riding inside one string field, the same
// "JSON array inside a string field" shape "barcodes" uses (see strs'
// own doc comment above), just decoded into structs rather than strings.
// A missing or blank field decodes as no options at all (nil, nil) rather
// than an error — a group may be created with zero options, mirroring
// data.ModifierRepo.CreateGroup's own rule (see UpsertModifierGroup's own
// doc comment) — but a NON-blank field that fails to decode is a real
// error, surfaced as the directive's failure message. Names are trimmed;
// the cloud side already validated and trimmed them before queuing, but
// this till does not trust that — same "validate all external input" rule
// every other hook here follows.
func modifierGroupOptions(raw any) ([]ModifierGroupOption, error) {
	s, _ := raw.(string)
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var decoded []struct {
		Name            string `json:"name"`
		PriceDeltaMinor int64  `json:"price_delta_minor"`
	}
	if err := json.Unmarshal([]byte(s), &decoded); err != nil {
		return nil, fmt.Errorf("invalid options: %w", err)
	}
	out := make([]ModifierGroupOption, 0, len(decoded))
	for _, o := range decoded {
		out = append(out, ModifierGroupOption{Name: strings.TrimSpace(o.Name), PriceDeltaMinor: o.PriceDeltaMinor})
	}
	return out, nil
}

// LANAddressKey is the device-report key carrying a main till's LAN
// host:port (ut-docs#2774), for a replica's cloud-assisted re-discovery
// (discovery.CloudLookupPath). Contributed through Hooks.DeviceExtra;
// buildSyncRequest keeps it only on a role "primary" report.
const LANAddressKey = "lan_address"

// syncRequest is the /v1/stores/sync body before it is sent: built every
// tick (the check-in hashes its device part), sent only when the check-in
// says so.
type syncRequest struct {
	storeID string
	devices []map[string]any
	hasStop bool // a diagnostics stop report rides the device record
}

// buildSyncRequest assembles this device's report for the sync POST.
func buildSyncRequest(ctx context.Context, cfg *config.Config, settings *data.SettingsRepo, hooks Hooks) syncRequest {
	get := func(k string) string {
		v, _, _ := settings.Get(ctx, k)
		return v
	}
	name := enroll.DeviceName(ctx, settings)
	if name == "" {
		name = "Till"
	}
	isReplica := strings.TrimSpace(get("sync.primary_url")) != ""
	role := "primary"
	if isReplica {
		role = "replica"
	}
	if strings.TrimSpace(get("display.mode")) == "backoffice" {
		role = "backoffice"
	}
	health := map[string]any{
		"uptime_min": int(time.Since(started).Minutes()),
	}
	if fi, err := os.Stat(cfg.DBPath); err == nil {
		health["db_mb"] = fi.Size() / (1 << 20)
	}

	device := map[string]any{
		"device_id": enroll.CurrentStatus().DeviceID,
		"name":      name,
		"version":   buildinfo.Version,
		"platform":  runtime.GOOS + "/" + runtime.GOARCH,
		"role":      role,
		"health":    health,
	}
	// The cloud merges/retires this machine's older device rows by it
	// (ut-docs#2802), and a main till's row needs one too (ut-docs#3307):
	// sync.till_id when joined, else the cloud id kept at promotion, else this
	// machine's own LAN id. That last tier may mint — the same
	// lan_discovery.till_id this machine already advertises, through the same
	// race-safe GetOrCreate in discovery.TillID. Best-effort: a failed read
	// leaves till_id off this heartbeat, never the heartbeat itself.
	if tid, err := discovery.ReportedTillID(ctx, settings); err != nil {
		logging.L().Warnf("cloudsync: resolve till id: %v", err)
	} else if tid = strings.TrimSpace(tid); tid != "" {
		device["till_id"] = tid
	}
	if hooks.DeviceExtra != nil {
		for k, v := range hooks.DeviceExtra(ctx) {
			if _, taken := device[k]; !taken {
				device[k] = v
			}
		}
	}
	// ut-docs#2774: only a main till (no sync.primary_url — ADR-0011's single
	// source of truth, same rule as discovery.RoleCheckFromSettings) tells
	// the cloud where it is on the LAN. Gated on isReplica, not the reported
	// "role" string: display.mode=="backoffice" overwrites role to
	// "backoffice" even for a till that has no primary_url and so IS the
	// actual main till on the LAN (ut-docs#2774 code review) — such a till
	// must still report, or its replicas get no cloud-assisted lookup. A
	// till that is both an actual replica (primary_url set) and in
	// backoffice display mode must still never report (it is not the
	// source of truth, whatever it's labelled for the cloud UI).
	if isReplica {
		delete(device, LANAddressKey)
	}
	// A LOCAL diagnostic-mode stop (ADR-0092 §1, ut-docs#2169) is reported
	// best-effort here — on the device record of the very next heartbeat,
	// no dedicated round trip, no blocking wait — and the marker clears
	// once this push succeeds. Known gap, stated plainly: ut-cloud's sync
	// handler decodes the device record into a typed struct today and has
	// no dedicated stop endpoint (part (b) built activate/batch/revoke
	// only), so the cloud does not yet CONSUME this field; the wire shape
	// is in place for the cloud-side follow-up, and until then a stopped
	// session stays "active" on the cloud until staff revoke it — exactly
	// the bounded lag §1 names, closed for upload purposes by the cloud's
	// own "reject any batch for a non-active session" check.
	stopReport, hasStop := diagnostics.StopReport(ctx, settings)
	if hasStop {
		device["diagnostics"] = stopReport
	}
	return syncRequest{
		storeID: enroll.Effective(cfg).Marketplace.StoreID,
		devices: []map[string]any{device},
		hasStop: hasStop,
	}
}

// pushSync sends the device report and returns the store's pending
// directives.
func pushSync(ctx context.Context, cfg *config.Config, settings *data.SettingsRepo, req syncRequest) ([]directive, error) {
	payload, _ := json.Marshal(map[string]any{
		"store_id": req.storeID,
		"devices":  req.devices,
	})
	body, err := post(ctx, cfg, "/v1/stores/sync", payload)
	if err != nil {
		cachePlanRequired(ctx, settings, err)
		return nil, err
	}
	if req.hasStop {
		if cerr := diagnostics.ClearStopReport(ctx, settings); cerr != nil {
			logging.L().Warnf("cloudsync: diagnostics stop report delivered but marker not cleared: %v", cerr)
		}
	}
	var resp struct {
		Data struct {
			Directives []directive `json:"directives"`
			// Entitlement is ADR-0060 §3's optional current-value block. Kept
			// raw so a malformed block can never fail the decode of the
			// directives riding beside it.
			Entitlement json.RawMessage `json:"entitlement"`
			// DeviceID + DeviceToken: ADR-0116 D4's one-time rotation of a
			// legacy-token till onto its own credential (ut-docs#2769).
			// DeviceToken is a secret: never log it or this struct.
			DeviceID    string `json:"device_id"`
			DeviceToken string `json:"device_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("cloudsync: decode sync response: %w", err)
	}
	cacheEntitlement(ctx, settings, resp.Data.Entitlement, time.Now())
	// Kept only for this till's own device id and only when the environment
	// doesn't pin the token; every cloud caller sends it from the next
	// request (enroll.Effective). Never fails the sync.
	enroll.ApplyRotatedCredential(ctx, settings, resp.Data.DeviceID, resp.Data.DeviceToken)
	return resp.Data.Directives, nil
}

// cacheEntitlement records the sync response's entitlement block in the
// settings KV (ADR-0060 §4, ut-docs#2547), including ADR-0117 §2's
// cloud_link tier/mode carried on the same block (ut-docs#2821) — a block
// that validates writes both entitlement.* and cloud.link_tier/
// cloud.link_mode in the one SetMany transaction below, via Block.Values; a
// block whose cloud_link field is empty/unset (older cloud, or a plan not
// yet on the realtime tier) normalises to "periodic" there, never an error
// (missing = periodic). Best-effort by design: an absent block (older
// cloud) touches nothing at all, cloud_link included; a malformed one is
// ignored whole and the previous cache kept; a write failure is logged.
// None of it ever fails the sync tick or its directive handling.
func cacheEntitlement(ctx context.Context, settings *data.SettingsRepo, raw json.RawMessage, now time.Time) {
	if len(raw) == 0 || string(raw) == "null" {
		return
	}
	var b entitlement.Block
	if err := json.Unmarshal(raw, &b); err != nil {
		logging.L().Warnf("cloudsync: ignoring malformed entitlement block (cache kept): %v", err)
		return
	}
	kv, err := b.Values(now)
	if err != nil {
		logging.L().Warnf("cloudsync: ignoring invalid entitlement block (cache kept): %v", err)
		return
	}
	if err := settings.SetMany(ctx, kv); err != nil {
		logging.L().Warnf("cloudsync: entitlement cache not updated (will retry next tick): %v", err)
	}
}

// planRequiredEntitlement pulls data.entitlement out of a 402
// plan_required body (ut-docs#3624), raw like pushSync keeps a 200's, so
// cacheEntitlement validates it the same way. Bounded like
// parseCloudErrorEnvelope; nil when the body isn't the envelope or carries
// no block (an older cloud's 402, a proxy's page).
func planRequiredEntitlement(body []byte) json.RawMessage {
	if len(body) > cloudErrorMaxBytes {
		body = body[:cloudErrorMaxBytes]
	}
	var env struct {
		Data struct {
			Entitlement json.RawMessage `json:"entitlement"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &env) != nil {
		return nil
	}
	return env.Data.Entitlement
}

// planRequired reports whether err is the cloud's 402 refusal of the
// check-in for a store without an active paid plan (ADR-0148 follow-up,
// ut-docs#3624). errors.As, so it survives wrapping.
func planRequired(err error) (*statusError, bool) {
	var se *statusError
	if errors.As(err, &se) && se.StatusCode == http.StatusPaymentRequired {
		return se, true
	}
	return nil, false
}

// cachePlanRequired caches a 402 plan_required's entitlement block exactly
// as a 200's would be (cacheEntitlement: validated, best-effort), so the
// refusal itself tells this till it is not on a paid plan and ADR-0148's
// gate stops the periodic check-in from the next tick. The tick still
// fails with the 402 — that is what lets the scheduler honour its
// Retry-After (retryAfterHint) — but quietly: logTickError does not report
// it as a problem and authTracker ignores it. Any other error: no-op.
func cachePlanRequired(ctx context.Context, settings *data.SettingsRepo, err error) {
	if se, ok := planRequired(err); ok && settings != nil {
		cacheEntitlement(ctx, settings, se.Entitlement, time.Now())
	}
}

// snapshotSchema is the catalog snapshot's wire version (contract §3.7).
// The cloud's schema-2 ingest (16 MiB cap) must be live before a till
// release that sends it — the old 4 MiB cap would answer 413 forever.
const snapshotSchema = 2

// maxSnapshotItems mirrors the cloud's 20 000-item cap. Items are ordered
// active first, so a truncation drops inactive items first.
const maxSnapshotItems = 20000

type snapshotVariantRow struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	SKU        string   `json:"sku"`
	PriceMinor int64    `json:"price_minor"`
	Active     bool     `json:"active"`
	Barcodes   []string `json:"barcodes"`
}

type snapshotItemRow struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	SKU            string `json:"sku"`
	PriceMinor     int64  `json:"price_minor"`
	CategoryID     string `json:"category_id"`
	Color          string `json:"color"`
	Active         bool   `json:"active"`
	IsWeighed      bool   `json:"is_weighed"`
	StockUntracked bool   `json:"stock_untracked"`
	// AgeRestricted (ut-docs#3340/#3395) lets my.'s catalog views see (and,
	// in a future change, set) the staff-ID-check flag — read-only on this
	// side of the wire for now; the cloud's own ingest/display is separate
	// follow-up work, not this field's concern.
	AgeRestricted bool     `json:"age_restricted"`
	Qty           *float64 `json:"qty,omitempty"`
	// Barcode is the legacy primary barcode, kept for a schema-1 reader.
	Barcode                   string               `json:"barcode"`
	Barcodes                  []string             `json:"barcodes"`
	ModifierGroupIDs          []string             `json:"modifier_group_ids"`
	ModifierOptOutIDs         []string             `json:"modifier_opt_out_ids"`
	EffectiveModifierGroupIDs []string             `json:"effective_modifier_group_ids"`
	Variants                  []snapshotVariantRow `json:"variants"`
	// ImageSHA256 is the hex SHA-256 of the photo the till serves for this
	// item, "" for none or a built-in icon (contract §3.9 rule 5), so my.
	// can tell an image it set from one taken on the till.
	ImageSHA256 string `json:"image_sha256"`
	// Icon (ut-docs#3584) is the icon id the item shows, "" for none, a
	// photo or the generic tile — iconid.EffectiveIcon of its thumbnail
	// path and items.icon, the same read the category report uses
	// (ut-docs#2717), so my. draws and preselects what the till draws.
	Icon string `json:"icon"`
	// EverSold (ut-docs#3317) is true once the item or one of its variants
	// has a sale line here (live or archived). my. offers Delete only while
	// it is false; the cloud keeps it true once any till reported it.
	// Omitted when the till could not work it out, which the cloud reads as
	// "no news". Advisory only: delete_item re-checks on the till.
	EverSold *bool `json:"ever_sold,omitempty"`
	// NetQuantity (ut-docs#3504) is a pre-packed item's net content
	// (items.net_quantity_value/unit, ut-docs#3391), so my. can show and
	// edit it. Omitted when the item has none, or when the stored pair is
	// not a catalogtypes.ValidNetQuantity one — never half a pair.
	NetQuantity *snapshotNetQuantity `json:"net_quantity,omitempty"`
	// SellScreen (ut-docs#3015) is "hidden" or "removed" (from the quick
	// buttons; removed wins when both flags are set) so my. can show it.
	// Omitted when the item is on the sell screen — the same as an older
	// till that never sends it, which the cloud reads as visible.
	SellScreen string `json:"sell_screen,omitempty"`
}

// snapshotNetQuantity is an item's net quantity on the wire (ut-docs#3504):
// a positive whole Value in Unit "g", "ml" or "ea".
type snapshotNetQuantity struct {
	Value int64  `json:"value"`
	Unit  string `json:"unit"`
}

// snapshotStockLocationRow is one stock location in the catalog snapshot's
// additive stock_locations field (ut-docs#3383).
type snapshotStockLocationRow struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	IsActive bool   `json:"is_active"`
}

// pushSnapshotIfChanged uploads the catalog + on-hand stock when it differs
// from what the cloud already has (tracked via a content hash in settings).
// Schema 2 (contract §3.7): every item, inactive included, with its full
// barcode set, modifier links, the till's own resolved groups and its
// variants nested.
func pushSnapshotIfChanged(ctx context.Context, cfg *config.Config, db *sql.DB) error {
	eff := enroll.Effective(cfg)
	m := eff.Marketplace

	items, err := data.NewCatalogRepo(db).CatalogSnapshotItems(ctx)
	if err != nil {
		return err
	}
	qty := map[string]float64{}
	if levels, err := data.NewPOSRepo(db).ListStockLevels(ctx); err == nil {
		for _, l := range levels {
			// ut-docs#2082: ListStockLevels ALSO returns a variant's own row
			// carrying its PARENT item's ItemID. Skip those, or a variant's
			// stock would fold into its parent's cloud qty (ADR-0043
			// Decision 3 forbids exactly this double-counting). Variants
			// carry no qty in the snapshot.
			if l.VariantID != "" {
				continue
			}
			qty[l.ItemID] += l.CurrentQty
		}
	}
	if len(items) > maxSnapshotItems {
		logging.L().Warnf("cloudsync: catalog has %d items; the snapshot carries the first %d (inactive items dropped first)", len(items), maxSnapshotItems)
		items = items[:maxSnapshotItems]
	}
	// Photos (§3.9 rule 5). A read error only costs the image field: the
	// rest of the snapshot is still worth sending.
	thumbs, err := data.NewCatalogRepo(db).ItemThumbnails(ctx)
	if err != nil {
		logging.L().Warnf("cloudsync: snapshot item thumbnails: %v", err)
		thumbs = nil
	}
	// ever_sold (ut-docs#3317). A read error only costs the field, as for
	// photos above: the cloud keeps what it had.
	sold, err := data.NewPOSRepo(db).EverSoldItemIDs(ctx)
	if err != nil {
		logging.L().Warnf("cloudsync: snapshot ever-sold items: %v", err)
		sold = nil
	}
	rows := make([]snapshotItemRow, 0, len(items))
	for _, it := range items {
		row := snapshotItemRow{
			ID: it.ID, Name: it.Name, SKU: it.SKU, PriceMinor: it.PriceMinor,
			CategoryID: it.CategoryID, Color: it.Color, Active: it.Active,
			IsWeighed: it.IsWeighed, StockUntracked: it.StockUntracked,
			AgeRestricted: it.AgeRestricted,
			Barcodes:      it.Barcodes, ModifierGroupIDs: it.ModifierGroupIDs,
			ModifierOptOutIDs: it.ModifierOptOutIDs, EffectiveModifierGroupIDs: it.EffectiveModifierGroupIDs,
			Variants:    make([]snapshotVariantRow, 0, len(it.Variants)),
			ImageSHA256: ServedImageSHA256(thumbs[it.ID]),
			Icon:        iconid.EffectiveIcon(thumbs[it.ID], it.Icon),
			SellScreen:  it.SellScreen,
		}
		if len(it.Barcodes) > 0 {
			row.Barcode = it.Barcodes[0]
		}
		if sold != nil {
			v := sold[it.ID]
			row.EverSold = &v
		}
		if !it.StockUntracked {
			q := qty[it.ID]
			row.Qty = &q
		}
		if it.NetQuantityValue != nil && it.NetQuantityUnit != nil && catalogtypes.ValidNetQuantity(it.NetQuantityValue, it.NetQuantityUnit) {
			row.NetQuantity = &snapshotNetQuantity{Value: *it.NetQuantityValue, Unit: *it.NetQuantityUnit}
		}
		for _, v := range it.Variants {
			row.Variants = append(row.Variants, snapshotVariantRow{
				ID: v.ID, Name: v.Name, SKU: v.SKU, PriceMinor: v.PriceMinor, Active: v.Active, Barcodes: v.Barcodes,
			})
		}
		rows = append(rows, row)
	}
	body := map[string]any{"store_id": m.StoreID, "schema": snapshotSchema, "items": rows}
	// Stock locations (ut-docs#3383): every location, inactive included,
	// for my.'s stock locations node. Part of the hashed payload, so a
	// locations-only change pushes too. A read error leaves the field out
	// — the cloud then keeps what it had — rather than sending an empty
	// list that would read as "this shop has no locations".
	if locs, err := data.NewPOSRepo(db).ListStockLocationsForAdmin(ctx); err != nil {
		logging.L().Warnf("cloudsync: snapshot stock locations: %v", err)
	} else {
		out := make([]snapshotStockLocationRow, 0, len(locs))
		for _, l := range locs {
			out = append(out, snapshotStockLocationRow{ID: l.ID, Name: l.Name, IsActive: l.IsActive})
		}
		body["stock_locations"] = out
	}
	payload, _ := json.Marshal(body)

	sum := sha256.Sum256(payload)
	hash := hex.EncodeToString(sum[:])
	settings := data.NewSettingsRepo(db)
	if prev, _, _ := settings.Get(ctx, "cloudsync.snapshot_hash"); prev == hash {
		return nil // unchanged since the last successful push
	}
	// Size guard (review finding 3): over the cloud's 16 MiB schema-2 cap
	// the post would only be refused, so don't send it. Logged once at
	// warn level, which also puts it in the heartbeat's problems digest.
	if len(payload) > maxSnapshotBytes {
		if snapshotOversize() {
			logging.L().Warnf("cloudsync: catalog snapshot is too large to upload (%d bytes, limit %d); not sent until the catalog shrinks", len(payload), maxSnapshotBytes)
		}
		return nil
	}
	if snapshotInBackoff() {
		return nil // the cloud refused the last attempt; wait it out
	}
	if _, err := post(ctx, cfg, "/v1/stores/catalog-snapshot", payload); err != nil {
		var se *statusError
		if errors.As(err, &se) && rejectedRollup(se.StatusCode) {
			// The cloud refused the body itself (413 from an older cloud's
			// 4 MiB cap, 400/422…): the same bytes get the same answer, so
			// back off exponentially instead of re-uploading the whole
			// catalog every tick, and log each distinct refusal once.
			wait, first := snapshotRefused(fmt.Sprintf("status %d", se.StatusCode))
			if first {
				logging.L().Warnf("cloudsync: catalog snapshot refused by the cloud (%d, %d bytes); retrying with backoff, next in %s", se.StatusCode, len(payload), wait)
			}
			return nil
		}
		return err
	}
	snapshotSucceeded()
	logging.L().Infof("cloudsync: catalog snapshot pushed (schema %d, %d items, %d bytes)", snapshotSchema, len(rows), len(payload))
	return settings.Set(ctx, "cloudsync.snapshot_hash", hash)
}

// pushOrderTrackingIfChanged uploads the shop's currently-visible order
// tracking rows (ADR-0070: token, receipt_no, status, status_updated_at —
// exactly data.TrackedOrder's shape, nothing added) when they differ from
// what the cloud already has, tracked via a content hash in settings like
// pushSnapshotIfChanged. An empty orders ARRAY (never null/absent) is a
// meaningful payload: the cloud replaces-on-push, so it's the delete signal
// that clears aged-out tokens within one tick. The liveness rule is the LAN
// page's own (pos.OrderTrackingVisible), applied via ListLiveTrackedOrders'
// callback.
func pushOrderTrackingIfChanged(ctx context.Context, cfg *config.Config, db *sql.DB) error {
	eff := enroll.Effective(cfg)
	m := eff.Marketplace

	now := time.Now().UTC()
	// ut-docs#1321: bound the SQL side to what could ever pass the callback
	// below — a terminal row older than OrderTrackingExpiry can never be
	// visible, so there's no reason to fetch and marshal it every tick just
	// to filter it back out in Go. Non-terminal rows are deliberately never
	// bounded (data.ListLiveTrackedOrders' own doc comment) — matches
	// OrderTrackingVisible exactly, just pushed into SQL.
	live, err := data.NewPOSRepo(db).ListLiveTrackedOrders(ctx,
		pos.OrderTrackingTerminalStatuses(), now.Add(-pos.OrderTrackingExpiry),
		func(o data.TrackedOrder) bool {
			return pos.OrderTrackingVisible(o.Status, o.StatusUpdatedAt, now)
		})
	if err != nil {
		return err
	}
	orders := make([]map[string]any, 0, len(live))
	for _, o := range live {
		orders = append(orders, map[string]any{
			"token":             o.Token,
			"receipt_no":        o.ReceiptNo,
			"status":            o.Status,
			"status_updated_at": o.StatusUpdatedAt,
		})
	}
	payload, _ := json.Marshal(map[string]any{"store_id": m.StoreID, "orders": orders})

	sum := sha256.Sum256(payload)
	hash := hex.EncodeToString(sum[:])
	settings := data.NewSettingsRepo(db)
	if prev, _, _ := settings.Get(ctx, "cloudsync.order_tracking_hash"); prev == hash {
		return nil // unchanged since the last successful push
	}
	if _, err := post(ctx, cfg, "/v1/stores/order-tracking-snapshot", payload); err != nil {
		return err
	}
	logging.L().Infof("cloudsync: order tracking snapshot pushed (%d orders)", len(orders))
	return settings.Set(ctx, "cloudsync.order_tracking_hash", hash)
}

// postResult reports one directive outcome.
func postResult(ctx context.Context, cfg *config.Config, directiveID, status, msg string) error {
	eff := enroll.Effective(cfg)
	payload, _ := json.Marshal(map[string]string{
		"store_id":     eff.Marketplace.StoreID,
		"directive_id": directiveID,
		"status":       status,
		"message":      msg,
	})
	_, err := post(ctx, cfg, "/v1/stores/directives/result", payload)
	return err
}

func post(ctx context.Context, cfg *config.Config, path string, payload []byte) ([]byte, error) {
	eff := enroll.Effective(cfg)
	m := eff.Marketplace
	url := strings.TrimRight(m.EndpointURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.MerchantToken)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	if resp.StatusCode != http.StatusOK {
		se := &statusError{
			Path:       path,
			StatusCode: resp.StatusCode,
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
		}
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			se.Code, _ = parseCloudErrorEnvelope(buf.Bytes())
		case http.StatusPaymentRequired:
			se.Entitlement = planRequiredEntitlement(buf.Bytes())
		}
		return nil, se
	}
	return buf.Bytes(), nil
}

// drainBody discards up to a bounded amount of an HTTP response body before
// its caller closes it (ut-docs#2588): Go's Transport only returns a
// connection to httpClient's idle pool once the body has been read to EOF
// (or drained far enough) before Close — a caller that inspects only the
// status code and closes right away forces a fresh connection on every
// following request to the same host. Bounded the same as
// decodeCloudError's own read (diagnostics.go): an error body is at most a
// few hundred bytes; never buffer an unbounded one from a misbehaving
// endpoint.
func drainBody(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, cloudErrorMaxBytes))
}

// statusError is post's non-200 failure. Its message is exactly the
// fmt.Errorf text post has always returned, so existing callers see no
// change; a caller that must branch on the code (the sales-aggregate
// upload's 402 subscription_inactive, ut-docs#2535) uses errors.As.
type statusError struct {
	Path       string
	StatusCode int
	// RetryAfter is the response's Retry-After header, parsed by
	// parseRetryAfter (0 when absent/invalid/past) — ut-docs#2588. Captured
	// for every non-200 status; Start's scheduler only ACTS on it for
	// 402/429/503 (see retryAfterHint), so a caller never has to guess whether
	// parsing was worth doing for this particular status.
	RetryAfter time.Duration
	// Code is a 401's machine code from the {error:{code}} envelope
	// (ADR-0116 D6: device_revoked, token_retired or unauthorized), ""
	// when the body isn't the envelope. Decoded for 401 only — every other
	// status leaves it empty, so no existing caller's view changes. Start's
	// scheduler counts consecutive 401s (schedule.go's authTracker).
	Code string
	// Entitlement is a 402 plan_required's ADR-0060 block, raw from the
	// body's data.entitlement (ADR-0148 follow-up, ut-docs#3624): the cloud
	// refuses sync and check-in for a store without an active paid plan
	// and says what the plan is, so the till caches it (cachePlanRequired)
	// and its own ADR-0148 gate takes over. Decoded for 402 only; empty for
	// every other status and when the body carries no block.
	Entitlement json.RawMessage
}

func (e *statusError) Error() string {
	return fmt.Sprintf("cloudsync: %s returned %d", e.Path, e.StatusCode)
}

// parseRetryAfter parses a Retry-After header value (RFC 9110 §10.2.3):
// either a delta-seconds integer or an HTTP-date. now is the reference time
// an HTTP-date is measured against — injectable so tests don't depend on
// the wall clock. Anything that doesn't yield a positive future duration
// (empty, non-numeric garbage, a zero/negative delta, a date at or before
// now) returns 0 — "no hint", never an error: a caller simply falls back to
// its own backoff. Values past retryAfterClamp saturate at it, so a huge
// delta-seconds can't overflow time.Duration into a tiny wait.
func parseRetryAfter(h string, now time.Time) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if strings.Trim(h, "0123456789") == "" {
		// delta-seconds (digits only, per the RFC grammar). Saturate at
		// retryAfterClamp BEFORE multiplying: a huge value would overflow
		// time.Duration and wrap to a tiny or negative wait.
		secs, err := strconv.ParseInt(h, 10, 64)
		if err != nil || secs >= int64(retryAfterClamp/time.Second) {
			return retryAfterClamp
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := t.Sub(now); d > 0 {
			return min(d, retryAfterClamp)
		}
	}
	return 0
}

// logTickError logs a failed tick for Start. A warning lands in the
// problems ring (logging.Recent) that the back-office panel and the
// heartbeat's problems feed show; the cloud's 402 plan_required is not a
// problem but a plan the shop is on (ADR-0148 §5: no warning, no
// sync-error chip), so it is logged at INFO instead. After a 402 an
// unpaid till asks again only at its next trigger (amendment §4,
// ut-docs#3673), never on a timer.
func logTickError(err error) {
	if err == nil {
		return
	}
	if se, ok := planRequired(err); ok {
		next := "at the next start-up or operator action"
		if se.Entitlement == nil {
			// No block to cache: the till still believes it is paid and
			// keeps honouring Retry-After (ADR-0148 amendment §4).
			next = "in " + retryAfterHint(err).String()
		}
		logging.L().Infof("cloudsync: cloud sync is part of the paid plans; this store is not on one (%s answered 402); checking again %s", se.Path, next)
		return
	}
	logging.L().Warnf("cloudsync: tick failed (will retry): %v", err)
}

// Start runs the sync loop: first tick shortly after boot (give enrolment a
// moment), then every few minutes, jittered, with exponential backoff (full
// jitter, optionally raised by a Retry-After hint) after a run of failures
// (ut-docs#2588) — see schedule.go's scheduler for the delay math. Without
// this, a cloud outage or a shop opening (every till powering on within the
// same minute) had every till retrying on the exact same fixed grid,
// hitting the cloud in the same second. Ctx-cancelled with the server. The
// goroutine registers on wg so app.Run's shutdown drain can prove it exited
// before the database closes (same join shape as updates/alerts/enroll).
func Start(ctx context.Context, cfg *config.Config, db *sql.DB, hooks Hooks, wg *sync.WaitGroup) {
	// An unpaid till's start-up check-in (ADR-0148 amendment, ut-docs#3673)
	// is armed here, before the goroutine, for the first hour of this run.
	armStartupCheckin()
	wg.Add(1)
	go func() {
		defer logging.RecoverAndLog("cloudsync.loop")
		defer wg.Done()
		sched := newSchedulerFn()
		first := time.NewTimer(sched.firstWait())
		defer first.Stop()
		select {
		case <-ctx.Done():
			return
		case <-first.C:
		case <-hooks.Kick:
		}
		for {
			// This check-in satisfies any kick already pending — one sent
			// during the backoff, or alongside the timer that just fired —
			// so it must not fire a second, redundant POST right after.
			// A kick that arrives while the check-in runs stays and runs
			// exactly one more (the nudge may postdate what this one read).
			select {
			case <-hooks.Kick:
			default:
			}
			if hooks.BeforeTick != nil {
				hooks.BeforeTick()
			}
			contacted, err := tick(ctx, cfg, db, hooks)
			logTickError(err)
			if hooks.AfterTick != nil {
				hooks.AfterTick(ctx, contacted, err)
			}
			// No kicks while backing off. That includes the flat hourly
			// wait of an ADR-0116 D6 lock-out (schedule.go's authTracker): a
			// credential written while the till waits — a pairing, a
			// re-registration — is first tried on the next hourly tick, and
			// the status chip stays until that tick succeeds. The "Pair with
			// a shop" screen (ut-docs#3523) is where an earlier wake belongs;
			// a cloud nudge can't be it, the link itself is refused on a 401.
			// Exception (ADR-0148, ut-docs#3615): a kick that arrives while an
			// operator check-in window is open is an operator action on this
			// till, and it ends the backoff — otherwise an unpaid till whose
			// window check-in failed once would wait out the backoff, long
			// past its window. A kick outside a window is consumed and the
			// wait goes on; the check-in the wait ends in satisfies it.
			timer := time.NewTimer(sched.next(err))
		wait:
			for {
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
					break wait
				case <-hooks.Kick:
					if err != nil && !operatorWindowOpen(operatorNow()) {
						continue
					}
					timer.Stop()
					break wait
				}
			}
		}
	}()
}
