// Package catalogpull is the main till's pull of a cloud-master store's
// catalogue (ADR-0162 §4, ut-docs#4463; contract ut-docs
// reference/contracts/cloud-catalog-feed.md): it pages the cloud's delta
// feed from its stored cursor and applies each row through the till's own
// repositories — the validation and write paths the till's screens and the
// manage-shop directives use — so a pulled row is re-validated exactly like
// a local edit.
//
// Shape of one Run:
//
//   - The cursor (KeyCursor) is the highest feed version fully applied. A
//     page is applied in transactions of at most commitChunk rows (SQLite's
//     one write lock must never outlast a sale's 5 s busy timeout,
//     ADR-0161's reason). The cursor is written in the same transaction as
//     the chunk it covers, and only at a version boundary: a chunk that ends
//     inside one version's rows stores the previous version, so a crash
//     re-applies that version whole. Re-applying is idempotent — a
//     cloud-created row's local id is its cloud_id, and a row's write and
//     its cloud_links row share a savepoint.
//   - Within one version group (one cloud transaction, which always
//     arrives whole) rows are applied in dependency order — tax codes,
//     categories parents-first, items, then the rest — not the feed's
//     kind/cloud_id order, so references inside the group resolve.
//   - Every row gets its own savepoint: a row the till refuses (price
//     ceiling, palette, unknown parent, …) rolls back alone, keeps the
//     till's previous state, is upserted into cloud_apply_failures and
//     logged, and the cursor still moves past it, so one bad row cannot
//     wedge the feed. Kinds this slice does not apply (variants, barcodes,
//     modifier groups, option sets, layouts, staff, settings — follow-ups
//     ut-docs#4491/#4492 — and any unknown kind) are skipped, cursor
//     advancing.
//   - An error that is not the till refusing a row (context ended,
//     database failure, a hook failing) aborts the chunk with the cursor
//     unchanged; it is never recorded as a refusal.
//   - 409 (catalog_master_till / catalog_adopting) and 401/403 (the
//     till's credential is not accepted) stop the run, cursor unchanged,
//     no error. 410 resync starts a resync generation: the cursor goes to
//     0, every row the full pull applies or sees stamps its link with the
//     generation, and when the pull reaches high_water the till fetches
//     once more at since=high_water; only an empty answer at the same
//     high_water lets every link with an older generation go through the
//     tombstone delete rules (anything else is applied and the prune
//     waits for a later run). The generation in progress is persisted
//     (KeyResyncGen), so a crash mid-resync still prunes once the resumed
//     pull finishes. Any other failure keeps the cursor; the caller logs
//     and retries on the next trigger.
//   - ApplierVersion is stored beside the cursor: a till whose stored
//     applier is older than the code's re-pulls everything from 0, so a
//     release that applies more fields picks them up.
//
// Nothing here runs on the sale path: internal/cloudsync calls Run from its
// own goroutine after a successful check-in. An open basket keeps the
// prices on its lines; a pulled price applies to the next line rung up.
package catalogpull

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// ApplierVersion is the version of this package's row applier. Bump it
// when a release applies a field or kind it skipped before: every till
// re-pulls from 0 once (cursor reset) and so picks up what it ignored.
const ApplierVersion = 1

// Settings keys of the pull state. "cloudsync." is a per-till settings
// family (data.PerTillSettingPrefixes): never LAN-synced, so the main
// till's cursor never moves a replica's admin fingerprint.
const (
	KeyCursor         = "cloudsync.catalog.cursor"
	KeyApplierVersion = "cloudsync.catalog.applier_version"
	// KeyGen is the current link generation every applied row stamps.
	KeyGen = "cloudsync.catalog.gen"
	// KeyResyncGen is the generation of a resync in progress ("" = none).
	KeyResyncGen = "cloudsync.catalog.resync_gen"
)

// PageLimit is the page size the pull asks for (the contract's maximum).
const PageLimit = 500

var (
	// commitChunk is the most rows one transaction carries (ADR-0162 §4).
	// A var for tests.
	commitChunk = 100
	// maxPagesPerRun bounds one Run, so a misbehaving cloud cannot keep
	// the pull spinning; a longer catalogue continues on the next trigger.
	// 40 pages is 20 000 rows. A var for tests.
	maxPagesPerRun = 40
	// afterChunkCommit, when set (tests only), runs after each commit.
	afterChunkCommit func()
	// beforeChunkCommit, when set (tests only), runs just before each
	// commit; an error aborts the run as a crash would (the chunk rolls
	// back).
	beforeChunkCommit func() error
)

// Row is one feed row. Data is the kind's payload, absent on a tombstone.
type Row struct {
	Kind    string          `json:"kind"`
	CloudID string          `json:"cloud_id"`
	Version int64           `json:"version"`
	Deleted bool            `json:"deleted"`
	Data    json.RawMessage `json:"data"`
}

// Page is one feed page.
type Page struct {
	Rows      []Row `json:"rows"`
	Next      int64 `json:"next"`
	HighWater int64 `json:"high_water"`
}

// Fetcher reads one page of the feed with since < version.
type Fetcher func(ctx context.Context, since int64, limit int) (Page, error)

// Options are the till-side rules the data layer cannot see.
type Options struct {
	// ItemInLiveBasket reports whether the cashier or kiosk basket (memory
	// only) holds the item: a tombstoned item that is in one is
	// deactivated, never deleted (delete_item's rule, ut-docs#3317). Nil =
	// no live baskets.
	ItemInLiveBasket func(ctx context.Context, itemID string) (bool, error)
	// TaxRateRefused is the shop country's tax-rate rule (the tax-code
	// screen's taxCodeRatesRefusedForCountry). Nil = no restriction.
	TaxRateRefused func(rateBP int, takeawayBP *int) bool
	// ItemPhotoReplaced is told, after the chunk commits, that a pulled
	// icon replaced an item's thumbnail (ut-docs#3584's one-picture rule),
	// so the caller can remove a superseded upload's file. Nil = keep.
	ItemPhotoReplaced func(itemID, clearedPath string)
}

// Result reports one Run.
type Result struct {
	Pages   int
	Applied int // rows written (saves and tombstones)
	Refused int // rows the till's validation refused (recorded)
	Skipped int // rows of kinds this applier does not handle
	Pruned  int // links removed by a resync's prune
	// Changed is true when the catalogue changed (the snapshot push and
	// LAN replicas should notice).
	Changed bool
	// Done is true when the pull reached the feed's high_water.
	Done bool
	// Stopped is the error code of an answer that stops the pull without
	// an error: a 409 (the cloud does not serve the feed now), or a 401/403
	// (this till's credential is not accepted; it will not fix itself by
	// the next tick). StopStatus is its HTTP status.
	Stopped    string
	StopStatus int
}

// state is the pull's persisted position.
type state struct {
	cursor, gen, resyncGen int64
}

// Run pulls and applies the feed from the stored cursor. A non-nil error
// means the run stopped early; everything committed before it stays, and
// the next Run continues from the stored cursor.
func Run(ctx context.Context, db *sql.DB, fetch Fetcher, opt Options) (Result, error) {
	var res Result
	settings := data.NewSettingsRepo(db)
	st, err := loadState(ctx, settings)
	if err != nil {
		return res, err
	}
	r := &runner{
		db: db, settings: settings, opt: opt,
		cat: data.NewCatalogRepo(db), pos: data.NewPOSRepo(db), links: data.NewCloudLinkRepo(),
	}
	resynced := false
	for pages := 0; pages < maxPagesPerRun; pages++ {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		p, err := fetch(ctx, st.cursor, PageLimit)
		var stop *StopError
		switch {
		case errors.As(err, &stop):
			res.Stopped, res.StopStatus = stop.Code, stop.Status
			return res, nil
		case errors.Is(err, ErrResync):
			if resynced || st.cursor == 0 {
				return res, fmt.Errorf("catalogpull: resync asked again at since=%d", st.cursor)
			}
			resynced = true
			st.gen++
			st.cursor, st.resyncGen = 0, st.gen
			if err := settings.SetMany(ctx, map[string]string{
				KeyCursor: "0", KeyGen: itoa(st.gen), KeyResyncGen: itoa(st.resyncGen),
			}); err != nil {
				return res, fmt.Errorf("catalogpull: start resync: %w", err)
			}
			logging.L().Infof("catalogpull: the cloud asked for a resync; pulling the whole catalogue again (generation %d)", st.gen)
			continue
		case err != nil:
			return res, err
		}
		if err := r.takePage(ctx, p, &st, &res); err != nil {
			return res, err
		}
		if p.Next >= p.HighWater {
			if st.resyncGen != 0 {
				return r.confirmAndPrune(ctx, fetch, p.HighWater, &st, res)
			}
			res.Done = true
			return res, nil
		}
	}
	return res, nil
}

// takePage checks one fetched page against the contract and applies it.
func (r *runner) takePage(ctx context.Context, p Page, st *state, res *Result) error {
	res.Pages++
	if err := validatePage(p, st.cursor, PageLimit); err != nil {
		return err
	}
	if len(p.Rows) == 0 && p.Next == st.cursor && p.Next < p.HighWater {
		return fmt.Errorf("catalogpull: the feed did not advance past %d (high_water %d)", st.cursor, p.HighWater)
	}
	return r.applyPage(ctx, p, st, res)
}

// confirmAndPrune ends a resync whose full pull reached high. A row the
// cloud wrote after the page that reached high would otherwise be pruned
// as "not returned" by a till that never asked for it, so the till asks
// once more at since=high: only an empty answer at the same high_water
// confirms the full pull saw everything, and the prune runs. Anything
// else is applied as an ordinary page and the prune waits for a later run
// (KeyResyncGen stays set, so the resumed pull prunes when it confirms).
// On a feed written without pause the prune keeps waiting for a quiet
// run; meanwhile stale links only linger, nothing is deleted.
func (r *runner) confirmAndPrune(ctx context.Context, fetch Fetcher, high int64, st *state, res Result) (Result, error) {
	c, err := fetch(ctx, st.cursor, PageLimit)
	var stop *StopError
	switch {
	case errors.As(err, &stop):
		res.Stopped, res.StopStatus = stop.Code, stop.Status
		return res, nil
	case errors.Is(err, ErrResync):
		// The cloud asks for another resync: the next run starts it from
		// its own 410, without a warning for this one.
		return res, nil
	case err != nil:
		return res, fmt.Errorf("catalogpull: confirm the resync's end: %w", err)
	}
	if len(c.Rows) == 0 && c.Next == st.cursor && c.HighWater == high {
		res.Pages++
		if err := r.prune(ctx, st, &res); err != nil {
			return res, err
		}
		res.Done = true
		return res, nil
	}
	if err := r.takePage(ctx, c, st, &res); err != nil {
		return res, err
	}
	logging.L().Infof("catalogpull: the catalogue changed while the resync finished (high_water %d → %d); the prune waits for the next run", high, c.HighWater)
	return res, nil
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// loadState reads the pull state and applies the applier-version rule.
func loadState(ctx context.Context, settings *data.SettingsRepo) (state, error) {
	var st state
	read := func(key string) (int64, error) {
		v, ok, err := settings.Get(ctx, key)
		if err != nil {
			return 0, fmt.Errorf("catalogpull: read %s: %w", key, err)
		}
		if !ok || v == "" {
			return 0, nil
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			// A damaged value: start over from 0 rather than guess.
			return 0, nil
		}
		return n, nil
	}
	var err error
	if st.cursor, err = read(KeyCursor); err != nil {
		return st, err
	}
	if st.gen, err = read(KeyGen); err != nil {
		return st, err
	}
	if st.resyncGen, err = read(KeyResyncGen); err != nil {
		return st, err
	}
	applier, err := read(KeyApplierVersion)
	if err != nil {
		return st, err
	}
	if applier < ApplierVersion {
		if st.cursor > 0 {
			logging.L().Infof("catalogpull: applier version %d → %d; pulling the whole catalogue again", applier, ApplierVersion)
		}
		st.cursor = 0
		if err := settings.SetMany(ctx, map[string]string{KeyCursor: "0", KeyApplierVersion: itoa(ApplierVersion)}); err != nil {
			return st, fmt.Errorf("catalogpull: store applier version: %w", err)
		}
	}
	return st, nil
}

// validatePage refuses a page that is not the contract's: next and
// high_water out of order, a row outside (since, next] or out of version
// order, or more rows than the limit asked for — unless every row shares
// one version (one version too big for a page, which the cloud sends
// whole: the contract's only oversized page). Nothing of it is applied
// and the cursor stays.
func validatePage(p Page, since int64, limit int) error {
	if p.Next < since || p.HighWater < p.Next {
		return fmt.Errorf("catalogpull: malformed page: since %d, next %d, high_water %d", since, p.Next, p.HighWater)
	}
	if len(p.Rows) > limit && p.Rows[0].Version != p.Rows[len(p.Rows)-1].Version {
		return fmt.Errorf("catalogpull: malformed page: %d rows over the limit %d across versions %d..%d", len(p.Rows), limit, p.Rows[0].Version, p.Rows[len(p.Rows)-1].Version)
	}
	prev := since
	for _, row := range p.Rows {
		if row.Version <= since || row.Version > p.Next || row.Version < prev {
			return fmt.Errorf("catalogpull: malformed page: row version %d outside (%d, %d] or out of order", row.Version, since, p.Next)
		}
		prev = row.Version
	}
	return nil
}

// runner applies rows; the repositories are stateless over db or the
// chunk's tx.
type runner struct {
	db       *sql.DB
	settings *data.SettingsRepo
	cat      *data.CatalogRepo
	pos      *data.POSRepo
	links    *data.CloudLinkRepo
	opt      Options
	// photos are thumbnails the open chunk's icons replaced, reported to
	// opt.ItemPhotoReplaced once the chunk commits.
	photos []replacedPhoto
}

type replacedPhoto struct{ itemID, path string }

// applyPage applies one page's rows, each version group in dependency
// order (dependencyOrder), in chunks of at most commitChunk rows.
// The cursor is written with each chunk, at the last version whose rows
// are all applied; the page's last chunk writes page.Next.
func (r *runner) applyPage(ctx context.Context, p Page, st *state, res *Result) error {
	var tx *sql.Tx
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
		r.photos = nil
	}()
	begin := func() error {
		var err error
		if tx, err = r.db.BeginTx(ctx, nil); err != nil {
			return fmt.Errorf("catalogpull: begin: %w", err)
		}
		return nil
	}
	commit := func(cursor int64) error {
		if err := r.settings.SetTx(ctx, tx, KeyCursor, itoa(cursor)); err != nil {
			return fmt.Errorf("catalogpull: store cursor: %w", err)
		}
		if beforeChunkCommit != nil {
			if err := beforeChunkCommit(); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("catalogpull: commit: %w", err)
		}
		tx = nil
		st.cursor = cursor
		if r.opt.ItemPhotoReplaced != nil {
			for _, ph := range r.photos {
				r.opt.ItemPhotoReplaced(ph.itemID, ph.path)
			}
		}
		r.photos = nil
		if afterChunkCommit != nil {
			afterChunkCommit()
		}
		return nil
	}
	rows := dependencyOrder(p.Rows)
	done, n := st.cursor, 0
	for i, row := range rows {
		if tx == nil {
			if err := begin(); err != nil {
				return err
			}
		}
		if err := r.applyRow(ctx, tx, row, st.gen, res); err != nil {
			return err
		}
		n++
		last := i == len(rows)-1
		if last || rows[i+1].Version != row.Version {
			done = row.Version
		}
		if n >= commitChunk && !last {
			if err := commit(done); err != nil {
				return err
			}
			n = 0
		}
	}
	if tx == nil {
		if err := begin(); err != nil {
			return err
		}
	}
	return commit(p.Next)
}

// dependencyOrder returns rows with each version group (the rows of one
// cloud transaction, which arrive whole and contiguous) reordered so a
// row's references within the group are applied before it: tax codes,
// then categories parents-first, then items, then every other kind. The
// server orders a group by kind then cloud_id (ut-cloud
// internal/catalogfeed/kinds.go), so an item written with its tax code,
// or a child category whose cloud_id sorts before its new parent's, would
// otherwise be refused as "has not reached this till". The order is
// settled over the whole group before chunking, so when an oversized
// group spans transactions a parent still lands in an earlier-or-same
// chunk. Versions stay contiguous, so the cursor rule is unchanged.
func dependencyOrder(rows []Row) []Row {
	out := make([]Row, 0, len(rows))
	for i := 0; i < len(rows); {
		j := i
		for j < len(rows) && rows[j].Version == rows[i].Version {
			j++
		}
		out = append(out, orderGroup(rows[i:j])...)
		i = j
	}
	return out
}

func orderGroup(group []Row) []Row {
	if len(group) < 2 {
		return group
	}
	var tax, cats, items, other []Row
	for _, r := range group {
		switch r.Kind {
		case kindTaxCode:
			tax = append(tax, r)
		case kindCategory:
			cats = append(cats, r)
		case kindItem:
			items = append(items, r)
		default:
			other = append(other, r)
		}
	}
	out := make([]Row, 0, len(group))
	out = append(out, tax...)
	out = append(out, parentsFirst(cats)...)
	out = append(out, items...)
	return append(out, other...)
}

// parentsFirst orders one group's category rows so a parent named in the
// group precedes its children (feed order otherwise). A cycle, or a row
// whose parent never becomes ready, falls back to feed order; the normal
// validation then refuses what cannot apply.
func parentsFirst(cats []Row) []Row {
	if len(cats) < 2 {
		return cats
	}
	ids := make([]string, len(cats))
	parents := make([]string, len(cats))
	pending := map[string]int{} // cloud_id → rows of it not yet emitted
	for i, c := range cats {
		ids[i], _ = canonicalID(c.CloudID)
		parents[i] = parentCloudID(c)
		pending[ids[i]]++
	}
	out := make([]Row, 0, len(cats))
	done := make([]bool, len(cats))
	for len(out) < len(cats) {
		progress := false
		for i, c := range cats {
			if done[i] {
				continue
			}
			p := parents[i]
			if p == "" || p == ids[i] || pending[p] == 0 {
				out = append(out, c)
				done[i] = true
				pending[ids[i]]--
				progress = true
			}
		}
		if !progress {
			for i, c := range cats {
				if !done[i] {
					out = append(out, c)
				}
			}
			break
		}
	}
	return out
}

// parentCloudID is a category row's canonical parent_cloud_id, "" when
// none, unreadable or a tombstone.
func parentCloudID(r Row) string {
	if r.Deleted {
		return ""
	}
	var d struct {
		Parent *string `json:"parent_cloud_id"`
	}
	if json.Unmarshal(r.Data, &d) != nil || d.Parent == nil {
		return ""
	}
	id, err := canonicalID(*d.Parent)
	if err != nil {
		return ""
	}
	return id
}

// prune ends a resync: every link the full pull did not stamp with the
// resync's generation goes through the tombstone delete rules, in chunks.
// A link whose delete rule fails is recorded and dropped, so the prune
// always ends.
func (r *runner) prune(ctx context.Context, st *state, res *Result) error {
	for {
		stale, err := r.links.Stale(ctx, r.db, st.resyncGen, commitChunk)
		if err != nil {
			return fmt.Errorf("catalogpull: prune: %w", err)
		}
		if len(stale) == 0 {
			break
		}
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("catalogpull: prune: begin: %w", err)
		}
		for _, l := range stale {
			err := data.WithSavepoint(ctx, tx, "cloud_pull_prune", func() error {
				return r.tombstone(ctx, tx, l.Entity, l.CloudID, l.LocalID, l.Version)
			})
			switch {
			case isInfra(ctx, err):
				_ = tx.Rollback()
				return fmt.Errorf("catalogpull: prune: %w", errors.Join(err, ctx.Err()))
			case err != nil:
				logging.L().Warnf("catalogpull: prune %s %s (local %s) refused: %v", l.Entity, l.CloudID, l.LocalID, err)
				if ferr := r.recordRefusal(ctx, tx, l.Entity, l.CloudID, l.Version, err); ferr != nil {
					_ = tx.Rollback()
					return ferr
				}
				if derr := r.links.Delete(ctx, tx, l.Entity, l.CloudID); derr != nil {
					_ = tx.Rollback()
					return fmt.Errorf("catalogpull: prune: %w", derr)
				}
			default:
				res.Pruned++
				res.Changed = true
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("catalogpull: prune: commit: %w", err)
		}
	}
	// TODO(ut-docs#4464): links with a pending outbox create are exempt
	// from the prune (ADR-0162 §4) — there is no outbox yet.
	if err := r.settings.Delete(ctx, KeyResyncGen); err != nil {
		return fmt.Errorf("catalogpull: end resync: %w", err)
	}
	st.resyncGen = 0
	logging.L().Infof("catalogpull: resync finished; %d rows the cloud no longer has were removed", res.Pruned)
	return nil
}
