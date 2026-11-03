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
//     modifier groups, option sets, categories parents-first, items,
//     variants, option-set tombstones, barcodes, then the rest — not the
//     feed's kind/cloud_id order, so references inside the group resolve.
//     A row refused on its first attempt is applied once more at the end
//     of its group, and only then recorded as refused: an option or value
//     moved between groups or sets in one transaction is refused by
//     whichever side applies first (ut-docs#4522).
//   - An option set tombstoned while an active item still uses it is
//     retired — deactivated under a freed name, so a later cloud set of
//     that name applies — and recorded in cloud_retired; every chunk
//     commit retries its delete, so it is deleted once unused
//     (ut-docs#4522).
//   - Every row gets its own savepoint: a row the till refuses (price
//     ceiling, palette, unknown parent, …) rolls back alone, keeps the
//     till's previous state, is upserted into cloud_apply_failures and
//     logged, and the cursor still moves past it, so one bad row cannot
//     wedge the feed. Applied kinds: tax_code, category, item,
//     modifier_group, option_set (ut-docs#4491), with the category/item
//     modifier-group, opt-out and option-set link lists, item_variant and
//     item_barcode (ut-docs#4521; a page's barcodes are resolved before
//     its first transaction, since that reads settings through the pool).
//     Kinds this applier does not apply (layout, staff, setting —
//     ut-docs#4492; any unknown kind) are skipped, cursor advancing.
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
//   - The till's seed rows (ut-docs#4494): a fresh till runs the setup
//     wizard before it can be paired, so it already holds migration 001's
//     tax codes and the wizard's gift voucher item. An unlinked tax_code
//     row named exactly like an unlinked seed tax code
//     (data.SeedTaxCodeIDs) adopts it — applied onto it and linked, audit
//     adopted_seed:true — instead of being refused on the UNIQUE name
//     (any other same-named local row stays refused, ut-docs#4465). A run
//     that applied a row then retires the seeded gift voucher item once
//     the shop's own active one exists, unless it is in a live basket,
//     used or edited (best-effort, after the last commit).
//   - ApplierVersion is stored beside the cursor: a till whose stored
//     applier is older than the code's re-pulls everything from 0, so a
//     release that applies more fields picks them up.
//   - After a run the feed served, the caller reports the till's refusals
//     (ReportFailures, ut-docs#4493): the whole current
//     cloud_apply_failures set, newest first, at most MaxReportedFailures,
//     POSTed to <endpoint>/v1/stores/catalog/apply-failures where it
//     replaces what the cloud held for the store, so a cleared refusal
//     disappears there too. Only a set whose fingerprint differs from the
//     last one the cloud accepted (KeyFailuresReported) is sent; a failed
//     post keeps the fingerprint and the next run retries.
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
// 1: tax codes, categories, items (ut-docs#4463). 2: modifier groups,
// option sets and the link lists naming them (ut-docs#4491). 3: item
// variants (with their option values) and item barcodes (ut-docs#4521).
// 4: every uploadable row's base, which the change upload diffs against
// (ut-docs#4464); the re-pull also forgets every echo version.
const ApplierVersion = 4

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
	// KeyFirstPullDone is set ("1") once a run reached the feed's high_water:
	// the till holds the shop's catalogue as the cloud had it. The sale
	// screen's join flow waits on it (ut-docs#5098).
	KeyFirstPullDone = "cloudsync.catalog.first_pull_done"
	// KeyJoinSince is the unix time the setup wizard finished a pairing with
	// an existing cloud shop; the "Syncing your shop" state shows from then
	// until the first pull is done, or a window passes (ut-docs#5098).
	KeyJoinSince = "cloudsync.join_since"
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
	Echoed  int // rows at the version of this till's own accepted upload
	Pending int // rows left for an unanswered local edit's upload
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
// the next Run continues from the stored cursor. A run that applied at
// least one row then retires the wizard's seeded gift voucher item
// (retireSeededGiftVoucherItem), outside every chunk transaction.
func Run(ctx context.Context, db *sql.DB, fetch Fetcher, opt Options) (Result, error) {
	res, err := run(ctx, db, fetch, opt)
	// Applied > 0 already set Changed, so the sale screen refreshes
	// whether or not the retire removes anything.
	if res.Applied > 0 {
		retireSeededGiftVoucherItem(ctx, db, opt)
	}
	return res, err
}

// retireSeededGiftVoucherItem removes the gift voucher item the setup
// wizard seeded (ut-docs#4259) once another active gift voucher item
// exists — normally the shop's own, brought by the pull (ADR-0162 §4,
// ut-docs#4494) — import_page.go's
// retireSeededGiftVoucherItem for a cloud-master store, which is fed by
// the pull instead of an import. data.POSRepo.RetireSeededGiftVoucherItem
// acts only while the wizard's marker is set, another active gift voucher
// item exists and the seeded item is untouched and unused. The live
// baskets are memory-only, so they are asked first; an item in one, or a
// failing check, keeps it and the marker, and the next run that applies a
// row retries. Best-effort: the applied rows are committed, so a failure
// is logged and never fails the run. It reports whether the item was
// removed. It runs after the last chunk
// committed: its own write transaction must not nest in one (SQLite's
// single writer).
func retireSeededGiftVoucherItem(ctx context.Context, db *sql.DB, opt Options) bool {
	if ctx.Err() != nil {
		return false
	}
	id, ok, err := data.NewCatalogRepo(db).SeededGiftVoucherItemID(ctx)
	if err != nil {
		logging.L().Warnf("catalogpull: seeded gift voucher item: %v", err)
		return false
	}
	if !ok {
		return false
	}
	if opt.ItemInLiveBasket != nil {
		in, err := opt.ItemInLiveBasket(ctx, id)
		if err != nil {
			logging.L().Warnf("catalogpull: seeded gift voucher item: basket check: %v", err)
			return false
		}
		if in {
			return false
		}
	}
	res, err := data.NewPOSRepo(db).RetireSeededGiftVoucherItem(ctx)
	if err != nil {
		logging.L().Warnf("catalogpull: seeded gift voucher item: %v", err)
		return false
	}
	if res.Removed {
		logging.L().Infof("catalogpull: removed the seeded gift voucher item %q (%s): the cloud catalogue brought the shop's own", res.Name, res.ID)
	}
	return res.Removed
}

func run(ctx context.Context, db *sql.DB, fetch Fetcher, opt Options) (Result, error) {
	r := newRunner(db, opt)
	res, err := r.pull(ctx, db, fetch)
	if ferr := r.retryDeferred(ctx, db, &res); err == nil {
		err = ferr
	}
	return res, err
}

// retryDeferred applies, after the whole run, the rows that named a row
// which had not arrived when they were first applied (ut-docs#5098). The
// feed serves a row at its latest version, so a barcode written at version
// 9 can name an item edited at version 12; the barcode used to be refused
// for good and stuck until it changed. Now it applies here, once the item
// is linked, or is recorded and logged as the refusal it then really is.
// The cursor is not touched: it already moved past these rows.
func (r *runner) retryDeferred(ctx context.Context, db *sql.DB, res *Result) error {
	var rows []Row
	for _, d := range r.deferred {
		rows = append(rows, d.row)
	}
	r.deferred = nil
	if len(rows) == 0 {
		return nil
	}
	// A run cut short (deadline, shutdown) leaves the rows as the recorded
	// refusals applyRow wrote when it deferred them: visible and reported,
	// and a later edit or resync applies them.
	if ctx.Err() != nil {
		return nil
	}
	defer func() { r.lastPass, r.barcodes, r.photos = false, nil, nil }()
	// The resolved barcodes of the last page are gone; resolve them again.
	r.barcodes = r.resolveBarcodes(ctx, rows)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("catalogpull: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := r.outbox.Suppress(ctx, tx); err != nil {
		return err
	}
	st, err := loadState(ctx, db, r.settings)
	if err != nil {
		return err
	}
	// One group in dependency order (kind order, categories parents-first),
	// then repeated passes while any row still applies: a chain such as
	// barcode -> item -> category resolves from the bottom up whatever the
	// versions. A pass that settles nothing ends with the final one, which
	// refuses what is left.
	cur := orderGroup(rows)
	for {
		r.deferred = nil
		for _, row := range cur {
			if _, err := r.applyRow(ctx, tx, row, st.gen, res, true); err != nil {
				return err
			}
		}
		if len(r.deferred) == 0 {
			break
		}
		next := make([]Row, 0, len(r.deferred))
		for _, d := range r.deferred {
			next = append(next, d.row)
		}
		if len(next) == len(cur) {
			r.lastPass = true
			r.deferred = nil
			for _, row := range next {
				if _, err := r.applyRow(ctx, tx, row, st.gen, res, true); err != nil {
					return err
				}
			}
			break
		}
		cur = orderGroup(next)
	}
	r.deferred = nil
	if err := r.outbox.Unsuppress(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("catalogpull: commit: %w", err)
	}
	if r.opt.ItemPhotoReplaced != nil {
		for _, ph := range r.photos {
			r.opt.ItemPhotoReplaced(ph.itemID, ph.path)
		}
	}
	return nil
}

func (r *runner) pull(ctx context.Context, db *sql.DB, fetch Fetcher) (Result, error) {
	var res Result
	settings := data.NewSettingsRepo(db)
	st, err := loadState(ctx, db, settings)
	if err != nil {
		return res, err
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
			// A restored cloud may serve a different row at the version
			// the till remembers as its own echo: the full pull applies
			// every row again. Cleared before the resync is recorded, so
			// a crash in between only re-applies rows.
			if err := r.links.ClearEchoes(ctx, db); err != nil {
				return res, fmt.Errorf("catalogpull: start resync: %w", err)
			}
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
	r.resyncing = st.resyncGen != 0
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
func loadState(ctx context.Context, db *sql.DB, settings *data.SettingsRepo) (state, error) {
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
		// A re-pull after a bump applies every row again: no row is an
		// echo. (A first run — no stored applier — has none to forget.)
		if applier > 0 {
			if err := data.NewCloudLinkRepo().ClearEchoes(ctx, db); err != nil {
				return st, fmt.Errorf("catalogpull: %w", err)
			}
			// Rows an older applier linked get a base before the re-pull
			// reaches them (seedBases), so a till edit made meanwhile is a
			// pending field, not overwritten.
			if err := seedBases(ctx, db); err != nil {
				return st, err
			}
		}
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
	mods     *data.ModifierRepo
	sets     *data.OptionSetRepo
	links    *data.CloudLinkRepo
	outbox   *data.CloudOutboxRepo
	opt      Options
	// answering is set while an upload answer's current rows apply (no
	// echo skip); forced additionally overwrites pending local edits (a
	// conflict: the cloud wins).
	answering, forced bool
	// resyncing is set while a resync's full pull runs (a restored cloud
	// may serve older versions than the links hold).
	resyncing bool
	// photos are thumbnails the open chunk's icons replaced, reported to
	// opt.ItemPhotoReplaced once the chunk commits.
	photos []replacedPhoto
	// barcodes are the open page's item_barcode codes, resolved before its
	// first chunk began (resolveBarcodes).
	barcodes map[string]barcodeResolution
	// deferred are rows refused only because a row they name had not
	// arrived yet; lastPass is set while retryDeferred applies them for
	// good (ut-docs#5098).
	deferred []deferredRow
	lastPass bool
}

type deferredRow struct {
	row Row
	err error
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
		r.barcodes = nil
	}()
	r.barcodes = r.resolveBarcodes(ctx, p.Rows)
	begin := func() error {
		var err error
		if tx, err = r.db.BeginTx(ctx, nil); err != nil {
			return fmt.Errorf("catalogpull: begin: %w", err)
		}
		// The cloud's rows are not till edits: no outbox mark (084).
		return r.outbox.Suppress(ctx, tx)
	}
	commit := func(cursor int64) error {
		if err := r.retryRetired(ctx, tx); err != nil {
			return fmt.Errorf("catalogpull: retired: %w", err)
		}
		if err := r.settings.SetTx(ctx, tx, KeyCursor, itoa(cursor)); err != nil {
			return fmt.Errorf("catalogpull: store cursor: %w", err)
		}
		if err := r.outbox.Unsuppress(ctx, tx); err != nil {
			return err
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
	// step applies one row; then, when the row ends its version group
	// (no retry left), the cursor may move to that version, and a full
	// chunk commits — unless the row ends the page (the final commit below).
	step := func(row Row, final bool) (bool, error) {
		if tx == nil {
			if err := begin(); err != nil {
				return false, err
			}
		}
		retry, err := r.applyRow(ctx, tx, row, st.gen, res, final)
		if err != nil {
			return false, err
		}
		n++
		return retry, nil
	}
	settle := func(groupEnd, pageEnd bool, version int64) error {
		if groupEnd {
			done = version
		}
		if n >= commitChunk && !pageEnd {
			if err := commit(done); err != nil {
				return err
			}
			n = 0
		}
		return nil
	}
	for i := 0; i < len(rows); {
		j := i
		for j < len(rows) && rows[j].Version == rows[i].Version {
			j++
		}
		// A row refused on its first attempt is applied once more after the
		// rest of its version group (ut-docs#4522): an option or value
		// moved between groups or sets in one cloud transaction is refused
		// by whichever side comes first until the other side releases it.
		var retry []Row
		for k := i; k < j; k++ {
			again, err := step(rows[k], false)
			if err != nil {
				return err
			}
			if again {
				retry = append(retry, rows[k])
			}
			end := k == j-1 && len(retry) == 0
			if err := settle(end, end && j == len(rows), rows[k].Version); err != nil {
				return err
			}
		}
		for k, row := range retry {
			if _, err := step(row, true); err != nil {
				return err
			}
			end := k == len(retry)-1
			if err := settle(end, end && j == len(rows), row.Version); err != nil {
				return err
			}
		}
		i = j
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
// then modifier groups, then option sets, then categories parents-first,
// then items, then item variants, then option-set tombstones, then item
// barcodes, then every other kind. An option-set tombstone follows the
// items so a set deleted together with its last use is unused by then and
// deleted, not retired (ut-docs#4522). The server orders a group by kind
// then cloud_id (ut-cloud internal/catalogfeed/kinds.go), so an item
// written with its tax code, or with a modifier group or option set it
// names, or a child category whose cloud_id sorts before its new
// parent's, would otherwise be refused as "has not reached this till". The order is
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
	var tax, groups, sets, cats, items, variants, setTombs, barcodes, other []Row
	for _, r := range group {
		switch r.Kind {
		case kindTaxCode:
			tax = append(tax, r)
		case kindModifierGroup:
			groups = append(groups, r)
		case kindOptionSet:
			if r.Deleted {
				setTombs = append(setTombs, r)
			} else {
				sets = append(sets, r)
			}
		case kindCategory:
			cats = append(cats, r)
		case kindItem:
			items = append(items, r)
		case kindItemVariant:
			variants = append(variants, r)
		case kindItemBarcode:
			barcodes = append(barcodes, r)
		default:
			other = append(other, r)
		}
	}
	out := make([]Row, 0, len(group))
	out = append(out, tax...)
	out = append(out, groups...)
	out = append(out, sets...)
	out = append(out, parentsFirst(cats)...)
	out = append(out, items...)
	out = append(out, variants...)
	out = append(out, setTombs...)
	out = append(out, barcodes...)
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
		if err := r.outbox.Suppress(ctx, tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("catalogpull: prune: %w", err)
		}
		for _, l := range stale {
			if exempt, err := r.pruneExempt(ctx, tx, l); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("catalogpull: prune: %w", err)
			} else if exempt {
				// Uploads first (ADR-0162 §4): re-stamped so the prune moves
				// past it.
				if err := r.links.Touch(ctx, tx, l.Entity, l.CloudID, st.resyncGen); err != nil {
					_ = tx.Rollback()
					return fmt.Errorf("catalogpull: prune: %w", err)
				}
				continue
			}
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
		if err := r.outbox.Unsuppress(ctx, tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("catalogpull: prune: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("catalogpull: prune: commit: %w", err)
		}
	}
	if err := r.settings.Delete(ctx, KeyResyncGen); err != nil {
		return fmt.Errorf("catalogpull: end resync: %w", err)
	}
	st.resyncGen = 0
	logging.L().Infof("catalogpull: resync finished; %d rows the cloud no longer has were removed", res.Pruned)
	return nil
}
