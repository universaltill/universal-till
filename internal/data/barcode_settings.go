package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/barcode"
)

// BarcodeEnabledSymbologiesKey is the settings-table key holding the shop's
// enabled barcode symbology ids (ADR-0059 Decision §2) as a JSON array of
// internal/barcode registry ids, e.g. ["EAN13","CODE128"]. Stored in the
// existing generic settings table rather than a dedicated table — the ADR
// explicitly leaves the storage shape open ("a JSON column on the existing
// shop-settings row — Dev's call"), and a key/value row needs no migration,
// which is exactly what "defaulting to the set above when absent" wants:
// a shop that never opens the (ut-docs#935) settings checklist has no row
// and behaves exactly as before ADR-0059.
//
// ut-docs#1361 review note: this key's value is cached in-process (see
// barcodeSymbologyCache below) and the cache is invalidated only by
// SetEnabledBarcodeSymbologies/SetBarcodeSymbologyEnabled. A future write
// to this key through a generic path instead — SettingsRepo.Delete,
// SetMany, or a raw Set(BarcodeEnabledSymbologiesKey, ...) — would bypass
// that invalidation and leave a stale cached value in place; route any new
// write through one of the two methods above, or call
// invalidateBarcodeSymbologyCache explicitly.
const BarcodeEnabledSymbologiesKey = "barcode_enabled_symbologies"

// CatalogImportBarcodeFromSKUDefaultKey is the settings-table key holding
// whether this shop wants ut-docs#1224's "derive a barcode from each item's
// own number" catalog-import checkbox pre-ticked by default the first time
// a barcode-less import is previewed (ut-docs#1356's bulk backfill card
// shares this same default). "1" = pre-tick, "0"/absent = don't (the
// unchanged #1224 default) — this key is purely a UI starting point: it
// only ever changes what a fresh checkbox shows before the operator
// touches it, never the actual import/backfill behaviour, which is always
// whatever the operator explicitly submits. No GetOrCreate/seed like
// BarcodeEnabledSymbologiesKey above — absent reads as off with no row
// ever written, so a shop that never opens this toggle behaves exactly as
// before this card.
const CatalogImportBarcodeFromSKUDefaultKey = "catalog_import_barcode_from_sku_default"

// ErrEmptyBarcodeSymbologySet is returned by SetBarcodeSymbologyEnabled when
// disabling the given id would leave the shop with zero enabled
// symbologies — every scan and every untyped AddBarcode call would then
// fail to match anything, silently and with no indication why (ut-docs#935
// review finding MAJOR 3: nothing previously stopped a manager unticking
// every box mid-shift). The caller must recover from this — reject the
// change with a message, never let it through.
var ErrEmptyBarcodeSymbologySet = errors.New("at least one barcode symbology must stay enabled")

// barcodeSymbologyCache is EnabledBarcodeSymbologies' in-process cache
// (ut-docs#1361, split from the 2026-08-30 perf audit finding 5): the
// enabled-symbology set is manager-toggled and changes essentially never,
// but was being re-fetched from SQLite and re-JSON-parsed on every single
// scan. Keyed by *sql.DB rather than a single flat value, and rather than
// a field on *SettingsRepo, for two reasons: (1) SettingsRepo is
// constructed fresh on nearly every call site (data.NewSettingsRepo(db)),
// so an instance field would never actually be hit twice; (2) tests open
// many independent (usually in-memory) *sql.DB instances within one test
// binary — keying by db pointer gives each its own cache slot for free,
// the same isolation a fresh instance would give if instance-scoped
// caching worked here, with no test-only reset hook required (contrast
// internal/pages/setup_language_catalog.go's resetSetupLanguageCatalog,
// needed there only because that cache is a single flat package value).
// Neither map ever removes an entry for a *sql.DB that gets closed —
// harmless in production (one long-lived *sql.DB per process) and cheap
// even across a test binary's many short-lived DBs (small string slices
// plus a uint64), so this is accepted rather than adding cleanup machinery
// for a value nothing here actually leaks memory over.
//
// barcodeSymbologyGen guards against a TOCTOU race between an invalidating
// writer and a reader that started fetching before the write landed (review
// finding on ut-docs#1361): the read path's DB fetch runs UNLOCKED (see
// below — only the final cache-store is), so a writer's commit +
// invalidateBarcodeSymbologyCache can land while a reader is mid-fetch of
// the now-stale pre-write value. Without this counter, that reader would
// then store its stale result right after the invalidate ran, silently
// undoing it — pinning the wrong value until the NEXT write, which for a
// setting that "changes essentially never" could be months. Each
// invalidate bumps db's generation; a reader snapshots the generation
// before its unlocked fetch and only commits the fetched value to the
// cache if the generation is still the one it started with — otherwise a
// write raced it, and it leaves the cache alone (the next call retries and
// observes the new value normally) rather than clobber the fresher state.
var (
	barcodeSymbologyCacheMu sync.RWMutex
	barcodeSymbologyCache   = map[*sql.DB][]string{}
	barcodeSymbologyGen     = map[*sql.DB]uint64{}
)

// invalidateBarcodeSymbologyCache drops db's cached entry, if any, and
// bumps its generation (see barcodeSymbologyGen above). Called from both
// write paths below after a successful write, so the very next read
// observes the new value rather than a stale cached one.
func invalidateBarcodeSymbologyCache(db *sql.DB) {
	barcodeSymbologyCacheMu.Lock()
	defer barcodeSymbologyCacheMu.Unlock()
	delete(barcodeSymbologyCache, db)
	barcodeSymbologyGen[db]++
}

// DefaultEnabledBarcodeSymbologyIDs returns ADR-0059 §2's default-enabled
// set: every registry entry EXCEPT the two embedded-data ones
// (EAN13_WEIGHT_PREFIX2X / EAN13_PRICE_PREFIX02), which are opt-in — a shop
// that never scanned a scale label must not risk an ordinary EAN-13 being
// misread as a weight/price label. The two excluded ids are fixed registry
// ids (ADR-0059 §1's table); a new embedded-data entry added to the
// registry later must be added here too so it also defaults off.
func DefaultEnabledBarcodeSymbologyIDs() []string {
	all := barcode.Default().IDs()
	out := make([]string, 0, len(all))
	for _, id := range all {
		if id == "EAN13_WEIGHT_PREFIX2X" || id == "EAN13_PRICE_PREFIX02" {
			continue
		}
		out = append(out, id)
	}
	return out
}

// EnabledBarcodeSymbologies returns the shop's enabled symbology ids,
// seeding the setting with the ADR-0059 §2 default set on first read (the
// upgrade path for an existing shop with no row: GetOrCreate writes the
// compatibility-preserving default, so nothing that resolved before stops
// resolving). The default set is ALSO returned alongside any error, so a
// caller that must never block on a settings read (the scan path,
// ADR-0003 offline-first) can use the returned ids regardless — under the
// defaults, behaviour is exactly the pre-ADR-0059 one.
//
// This is the single shared accessor for AddBarcode's inference path, the
// scan-path lookup (POSRepo), the ut-docs#935 settings checklist, and the
// ut-docs#936 catalog-import wiring — do not inline a private copy.
func (r *SettingsRepo) EnabledBarcodeSymbologies(ctx context.Context) ([]string, error) {
	barcodeSymbologyCacheMu.RLock()
	cached, ok := barcodeSymbologyCache[r.db]
	gen := barcodeSymbologyGen[r.db]
	barcodeSymbologyCacheMu.RUnlock()
	if ok {
		return cached, nil
	}

	defaults := DefaultEnabledBarcodeSymbologyIDs()
	val, found, err := r.Get(ctx, BarcodeEnabledSymbologiesKey)
	if err != nil {
		return defaults, err
	}
	if !found {
		seed, err := json.Marshal(defaults)
		if err != nil {
			return defaults, fmt.Errorf("marshal default symbologies: %w", err)
		}
		val, err = r.GetOrCreate(ctx, BarcodeEnabledSymbologiesKey, string(seed))
		if err != nil {
			return defaults, err
		}
	}
	var ids []string
	if err := json.Unmarshal([]byte(val), &ids); err != nil {
		// A corrupt row must not brick scanning/barcode entry: fall back to
		// the defaults, surfacing the parse failure to callers that care.
		return defaults, fmt.Errorf("parse setting %s: %w", BarcodeEnabledSymbologiesKey, err)
	}
	// A stored [""] (one blank-string element) is functionally just as
	// all-disabling as null/[] — an empty/whitespace-only id can never
	// match a real registry id (ut-docs#959's "related" note) — but has
	// len(ids) == 1, so it would slip past a bare length check below.
	// Filter first so this hits the same fallback as null/[].
	ids = nonBlankSymbologyIDs(ids)
	if len(ids) == 0 {
		// A stored "null" or "[]" unmarshals cleanly to an empty/nil slice
		// with no error, but an all-disabling enabled set has no legitimate
		// use (SetBarcodeSymbologyEnabled itself refuses to ever write one,
		// per ErrEmptyBarcodeSymbologySet) and is indistinguishable from
		// corruption — nothing writes this value directly today, but once
		// any API can persist the setting, a bad write must not silently
		// break every scan and every untyped AddBarcode call on the
		// offline-first checkout hot path (ut-docs#955). Treat it exactly
		// like the parse-error case: fall back to the default set.
		return r.cacheEnabledSymbologies(gen, defaults), nil
	}
	return r.cacheEnabledSymbologies(gen, ids), nil
}

// cacheEnabledSymbologies stores ids as db's cached EnabledBarcodeSymbologies
// result and returns it unchanged — called only from the clean, no-error
// return paths above; an error path deliberately returns the uncached
// defaults directly so the next call retries rather than pinning a
// read/parse failure in the cache. gen is the generation this call
// observed before its (unlocked) DB fetch; if a write has bumped it since
// (barcodeSymbologyGen above), ids is stale relative to that write, so it
// is returned to THIS caller but deliberately not stored — storing it
// would silently undo the write's own invalidation.
func (r *SettingsRepo) cacheEnabledSymbologies(gen uint64, ids []string) []string {
	barcodeSymbologyCacheMu.Lock()
	if barcodeSymbologyGen[r.db] == gen {
		barcodeSymbologyCache[r.db] = ids
	}
	barcodeSymbologyCacheMu.Unlock()
	return ids
}

// SetEnabledBarcodeSymbologies persists the shop's enabled symbology ids
// verbatim (a full-list replace). Kept for callers that already hold a
// complete, validated list to write; the ut-docs#935 settings checklist
// itself goes through SetBarcodeSymbologyEnabled below instead, which
// read-modify-writes a single id atomically rather than racing a
// stale full-list write against a concurrent toggle.
func (r *SettingsRepo) SetEnabledBarcodeSymbologies(ctx context.Context, ids []string) error {
	b, err := json.Marshal(ids)
	if err != nil {
		return fmt.Errorf("marshal enabled symbologies: %w", err)
	}
	if err := r.Set(ctx, BarcodeEnabledSymbologiesKey, string(b)); err != nil {
		return err
	}
	invalidateBarcodeSymbologyCache(r.db)
	return nil
}

// SetBarcodeSymbologyEnabled adds or removes id from the shop's enabled
// symbology set, reading the current set and writing the updated one back
// inside a single transaction (ut-docs#935 review finding MAJOR 4: the
// settings checklist has ten independent checkboxes with no hx-sync
// between them, so two toggles issued close together — plausible with a
// ten-item list inviting rapid multi-clicking — must not silently drop one
// another via a read-modify-write race that spans two separate calls).
// Returns the resulting set on success. Returns ErrEmptyBarcodeSymbologySet
// (writing nothing) if this toggle would leave the set empty — never
// silently allowed, since an empty set makes every scan and every untyped
// AddBarcode call fail to match anything.
func (r *SettingsRepo) SetBarcodeSymbologyEnabled(ctx context.Context, id string, enabled bool) ([]string, error) {
	var err error
	done := settingsObs.trace("set_barcode_symbology_enabled")
	defer func() { done(err) }()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("set barcode symbology %s: %w", id, err)
	}
	defer tx.Rollback()

	defaults := DefaultEnabledBarcodeSymbologyIDs()
	ids := defaults
	var val string
	scanErr := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, BarcodeEnabledSymbologiesKey).Scan(&val)
	switch {
	case errors.Is(scanErr, sql.ErrNoRows):
		// No row yet: this toggle is the shop's first-ever change, applied
		// on top of the ADR-0059 §2 defaults — same starting point
		// EnabledBarcodeSymbologies would return.
	case scanErr != nil:
		err = fmt.Errorf("set barcode symbology %s: %w", id, scanErr)
		return nil, err
	default:
		// A corrupt row must not brick this write: fall back to the
		// defaults, same posture as EnabledBarcodeSymbologies. Unmarshal
		// into a fresh variable rather than &ids directly — a stored
		// "null"/"[]" parses with NO error and would silently overwrite
		// the "ids := defaults" starting point above with a nil/empty
		// slice (ut-docs#959), contradicting this comment's own stated
		// intent. Only adopt the parsed value when it actually contains
		// at least one non-blank id; otherwise ids keeps the defaults.
		var parsed []string
		if json.Unmarshal([]byte(val), &parsed) == nil {
			parsed = nonBlankSymbologyIDs(parsed)
			if len(parsed) > 0 {
				ids = parsed
			}
		}
	}

	newIDs := toggleSymbologyID(ids, id, enabled)
	if len(newIDs) == 0 {
		return nil, ErrEmptyBarcodeSymbologySet
	}

	b, marshalErr := json.Marshal(newIDs)
	if marshalErr != nil {
		err = fmt.Errorf("marshal enabled symbologies: %w", marshalErr)
		return nil, err
	}
	if _, execErr := tx.ExecContext(ctx, `
INSERT INTO settings (key, value, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET
	value = excluded.value,
	updated_at = excluded.updated_at
`, BarcodeEnabledSymbologiesKey, string(b), time.Now().UTC()); execErr != nil {
		err = fmt.Errorf("set barcode symbology %s: %w", id, execErr)
		return nil, err
	}
	if commitErr := tx.Commit(); commitErr != nil {
		err = fmt.Errorf("set barcode symbology %s: %w", id, commitErr)
		return nil, err
	}
	invalidateBarcodeSymbologyCache(r.db)
	return newIDs, nil
}

// nonBlankSymbologyIDs filters out empty/whitespace-only entries from a
// parsed id list. A stored `[""]` is functionally just as all-disabling as
// `null`/`[]` — an empty string can never match a real registry id
// (ut-docs#959's "related" note) — so it must route through the same
// defaults fallback as those in both EnabledBarcodeSymbologies (where it
// would otherwise survive length checks and silently break every scan and
// every untyped AddBarcode call on the offline-first checkout hot path)
// and SetBarcodeSymbologyEnabled (where it would otherwise survive as a
// spurious "one enabled symbology" that passes the len(newIDs) > 0
// empty-set guard undetected).
func nonBlankSymbologyIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			continue
		}
		out = append(out, id)
	}
	return out
}

// toggleSymbologyID returns ids with id added (enabled=true) or removed
// (enabled=false). Does NOT deduplicate pre-existing duplicates in ids —
// only relevant if ids already contained id more than once, which nothing
// in this package's write path can produce (SetBarcodeSymbologyEnabled is
// the only mutator, and it always starts from a list built the same way).
func toggleSymbologyID(ids []string, id string, enabled bool) []string {
	out := make([]string, 0, len(ids)+1)
	seen := false
	for _, existing := range ids {
		if existing == id {
			seen = true
			if !enabled {
				continue
			}
		}
		out = append(out, existing)
	}
	if enabled && !seen {
		out = append(out, id)
	}
	return out
}
