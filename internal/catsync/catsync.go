// Package catsync applies catalogue rows an API-source import plugin
// submitted (ADR-0161, ut-docs#4429): it plans every row against the
// catalogue and import_source_links, and — on a commit — applies the plan
// in one transaction. A preview runs the same plan and writes nothing.
//
// Order per row: a row whose (source, external_id) is linked updates that
// item; an unlinked row that matches an item by barcode, then SKU, links
// to it and updates it; anything else is created with the repository
// primitives the file importer uses (EnsureCategoryUnder,
// FindOrCreateTaxCode, CreateItemTx's SKU rule, an opening stock movement).
// A link whose item is gone is recreated and re-pointed. Stock is set
// through a stock movement (reason "import_sync"), never overwritten.
//
// Neutral core: nothing here knows which system the rows came from. The
// source is the importing plugin's id, stamped by the caller.
package catsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// EntityItems is the one entity this package applies today; stock rides
// on item rows (stock_qty) when the run resolved "stock" too.
const EntityItems = "items"

// MaxRows bounds one run (ADR-0161 §2; the host function enforces it on
// submit, this is the same cap again at the apply side).
const MaxRows = 20_000

// MaxSamples bounds the sample rows a result carries.
const MaxSamples = 50

// StockReason is the reason every stock movement this package records
// carries (in the movement's audit entry).
const StockReason = "import_sync"

// Row actions, as reported in samples.
const (
	ActionNew       = "new"
	ActionUpdated   = "updated"
	ActionUnchanged = "unchanged"
	ActionIssue     = "issue"
)

const stockEpsilon = 1e-9

// commitChunk is how many processed rows one transaction carries —
// unchanged ones included: the DSN takes the write lock at BEGIN
// (_txlock=immediate), so planning reads hold it too. A commit
// releases SQLite's single write lock between chunks, so a sale on this
// till never waits behind a big import: one 20k-row transaction would
// outlast the 5 s busy timeout and fail the sale. Chunking is safe because
// every applied row is linked — a run that stops part-way is finished by
// the next run, never duplicated (ADR-0161 §3). A var for tests.
var commitChunk = 100

// afterChunkCommit, when set (tests only), runs after each chunk commits.
var afterChunkCommit func()

// applyFault, when set (tests only), fails a row after its writes, inside
// its savepoint.
var applyFault func(externalID string) error

// Options is one run's context.
type Options struct {
	// Source is the importing plugin's id — core-stamped, never a row
	// field. Required.
	Source string
	// Commit applies the plan; false is a preview (no writes).
	Commit bool
	// LocationID is the stock location stock rows are set at.
	LocationID string
	// ActorID is the signed-in operator, recorded on stock movements.
	ActorID string
	// Stock: the run resolved the "stock" entity (declared and granted
	// stock:write), so stock_qty is applied. Otherwise it is ignored.
	Stock bool
}

// Sample is one row of a result's sample.
type Sample struct {
	Row        int      `json:"row"` // 1-based position in the submitted rows
	ExternalID string   `json:"external_id,omitempty"`
	Name       string   `json:"name,omitempty"`
	Action     string   `json:"action"`
	Changes    []string `json:"changes,omitempty"`
	Issue      string   `json:"issue,omitempty"`
}

// Result counts what a run did (or, for a preview, would do).
type Result struct {
	New       int      `json:"new"`
	Updated   int      `json:"updated"`
	Unchanged int      `json:"unchanged"`
	Issues    int      `json:"issues"`
	Samples   []Sample `json:"samples"`
}

func (r *Result) add(s Sample) {
	switch s.Action {
	case ActionNew:
		r.New++
	case ActionUpdated:
		r.Updated++
	case ActionUnchanged:
		r.Unchanged++
	default:
		r.Issues++
	}
	if len(r.Samples) < MaxSamples {
		r.Samples = append(r.Samples, s)
	}
}

// plan is what one row will do.
type plan struct {
	action  string
	target  string // existing item to update; "" for a new one
	relink  bool   // the link must be written (new, matched, or re-pointed)
	changes []string
	issue   string
	state   data.SyncItemState
	addBC   []catalogtypes.BarcodeInput
	delta   float64 // stock movement quantity, when "stock" is a change
}

// runner carries one run's repositories, executor and per-run caches.
type runner struct {
	opt   Options
	cat   *data.CatalogRepo
	inv   *data.POSRepo
	links *data.ImportSourceLinkRepo
	q     data.SyncQuerier
	tx    *sql.Tx // nil on a preview

	catCache map[string]string // category path -> id (found or ensured)
	taxCache map[int]string    // rate -> tax code id
	// In-run claims, so two rows can never land on one item or code.
	claimedItem    map[string]string
	claimedBarcode map[string]string
	claimedSKU     map[string]string
}

// Run plans rows and, when opt.Commit, applies them in transactions of at
// most commitChunk applied rows. A malformed or conflicting row is a
// per-row issue. An error return means the run stopped: the Result then
// counts only the rows of chunks that were already committed (they stay,
// linked, and the next run continues from them).
func Run(ctx context.Context, db *sql.DB, rows []json.RawMessage, opt Options) (Result, error) {
	if opt.Source == "" {
		return Result{}, errors.New("catsync: source required")
	}
	if len(rows) > MaxRows {
		return Result{}, fmt.Errorf("catsync: %d rows exceeds the %d-row cap", len(rows), MaxRows)
	}
	if opt.Stock && opt.LocationID == "" {
		return Result{}, errors.New("catsync: stock location required")
	}
	rn := &runner{
		opt: opt, cat: data.NewCatalogRepo(db), inv: data.NewPOSRepo(db),
		links: data.NewImportSourceLinkRepo(), q: db,
		catCache: map[string]string{}, taxCache: map[int]string{},
		claimedItem: map[string]string{}, claimedBarcode: map[string]string{}, claimedSKU: map[string]string{},
	}

	// Parse, validate and normalize first, through the pool: barcode
	// normalization reads settings and must not run inside the tx.
	parsed := make([]row, len(rows))
	issues := make([]string, len(rows))
	exts := make([]string, len(rows))
	seenExt := map[string]bool{}
	for i, raw := range rows {
		r, ext, issue := parseRow(raw)
		exts[i] = ext
		if issue == "" && seenExt[ext] {
			issue = "external_id appears on more than one row"
		}
		if issue == "" {
			issue = normalizeBarcodes(ctx, rn.cat, &r)
		}
		if ext != "" {
			seenExt[ext] = true
		}
		parsed[i], issues[i] = r, issue
	}

	// committed is res as of the last chunk commit: what an error return
	// reports, since rows of the open chunk roll back with it.
	var res, committed Result
	processed := 0
	begin := func() error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("catsync: begin: %w", err)
		}
		rn.tx, rn.q = tx, tx
		return nil
	}
	commitOpen := func() error {
		if err := rn.tx.Commit(); err != nil {
			return fmt.Errorf("catsync: commit: %w", err)
		}
		rn.tx, rn.q = nil, db
		committed, processed = res, 0
		if afterChunkCommit != nil {
			afterChunkCommit()
		}
		return nil
	}
	defer func() {
		if rn.tx != nil {
			_ = rn.tx.Rollback()
		}
	}()
	fail := func(err error) (Result, error) {
		if !opt.Commit {
			return Result{}, err
		}
		return committed, err
	}

	for i := range parsed {
		if opt.Commit && rn.tx == nil {
			if err := ctx.Err(); err != nil {
				return fail(fmt.Errorf("catsync: %w", err))
			}
			if err := begin(); err != nil {
				return fail(err)
			}
		}
		s := Sample{Row: i + 1, ExternalID: exts[i]}
		if issues[i] != "" {
			s.Action, s.Issue = ActionIssue, issues[i]
			res.add(s)
			continue
		}
		r := &parsed[i]
		s.Name = r.name
		p, err := rn.plan(ctx, r)
		if err != nil {
			return fail(err)
		}
		if opt.Commit && (p.action == ActionNew || p.action == ActionUpdated) {
			err := data.WithSavepoint(ctx, rn.tx, "catsync_row", func() error {
				if err := rn.apply(ctx, r, &p); err != nil {
					return err
				}
				if applyFault != nil {
					return applyFault(r.ext)
				}
				return nil
			})
			switch {
			case errors.Is(err, data.ErrSavepoint) || ctx.Err() != nil:
				return fail(fmt.Errorf("catsync: %w", errors.Join(err, ctx.Err())))
			case err != nil:
				// Ids only — never row contents — in the log: repository
				// errors can embed a barcode, so only their type is logged.
				logging.L().Warnf("[catsync] source %s row %d (external_id %q): apply failed (%T)", opt.Source, i+1, r.ext, err)
				// Nothing of this row was written: give back what plan()
				// claimed, or a later valid row is a false conflict.
				rn.releaseClaims(r, &p)
				// The savepoint rolled back any category or tax code this
				// row created; cached ids may now point at nothing.
				clear(rn.catCache)
				clear(rn.taxCache)
				p = plan{action: ActionIssue, issue: "could not apply this row"}
			}
		}
		s.Action, s.Changes, s.Issue = p.action, p.changes, p.issue
		res.add(s)
		if opt.Commit {
			if processed++; processed >= commitChunk {
				if err := commitOpen(); err != nil {
					return fail(err)
				}
			}
		}
	}
	if rn.tx != nil {
		if err := commitOpen(); err != nil {
			return fail(err)
		}
	}
	return res, nil
}

func issue(msg string) plan { return plan{action: ActionIssue, issue: msg} }

// plan decides what one valid row does. Reads only (through rn.q).
func (rn *runner) plan(ctx context.Context, r *row) (plan, error) {
	for _, b := range r.barcodes {
		if other, ok := rn.claimedBarcode[b.Barcode]; ok && other != r.ext {
			return issue("barcode " + b.Barcode + " appears on more than one row"), nil
		}
	}
	if other, ok := rn.claimedSKU[r.sku]; r.sku != "" && ok && other != r.ext {
		return issue("sku appears on more than one row"), nil
	}

	var p plan
	linked, isLinked, err := rn.links.LocalID(ctx, rn.q, rn.opt.Source, EntityItems, r.ext)
	if err != nil {
		return plan{}, err
	}
	if isLinked {
		st, ok, err := rn.cat.SyncItemState(ctx, rn.q, linked)
		if err != nil {
			return plan{}, err
		}
		if ok {
			p.target, p.state = linked, st
		} else {
			p.relink = true // the local item is gone: recreate and re-point
		}
	} else {
		cand, err := rn.match(ctx, r)
		if err != nil {
			return plan{}, err
		}
		if cand != "" {
			if ext, ok, err := rn.links.ExternalIDFor(ctx, rn.q, rn.opt.Source, EntityItems, cand); err != nil {
				return plan{}, err
			} else if ok && ext != r.ext {
				return issue("the matching item is already linked to source record " + ext), nil
			}
			st, ok, err := rn.cat.SyncItemState(ctx, rn.q, cand)
			if err != nil {
				return plan{}, err
			}
			if ok {
				p.target, p.state, p.relink = cand, st, true
			}
		} else {
			p.relink = true
		}
	}
	if p.target != "" {
		if other, ok := rn.claimedItem[p.target]; ok && other != r.ext {
			return issue("another row in this run already matched the same item"), nil
		}
	}

	if p.target == "" {
		if q, err := rn.planNew(ctx, r, &p); err != nil || q.action == ActionIssue {
			return q, err
		}
	} else if q, err := rn.planUpdate(ctx, r, &p); err != nil || q.action == ActionIssue {
		return q, err
	}

	for _, b := range r.barcodes {
		rn.claimedBarcode[b.Barcode] = r.ext
	}
	if r.sku != "" {
		rn.claimedSKU[r.sku] = r.ext
	}
	if p.target != "" {
		rn.claimedItem[p.target] = r.ext
	}
	return p, nil
}

// releaseClaims undoes plan()'s in-run claims for a row whose apply was
// rolled back (only claims this row still holds).
func (rn *runner) releaseClaims(r *row, p *plan) {
	for _, b := range r.barcodes {
		if rn.claimedBarcode[b.Barcode] == r.ext {
			delete(rn.claimedBarcode, b.Barcode)
		}
	}
	if r.sku != "" && rn.claimedSKU[r.sku] == r.ext {
		delete(rn.claimedSKU, r.sku)
	}
	if p.target != "" && rn.claimedItem[p.target] == r.ext {
		delete(rn.claimedItem, p.target)
	}
}

// match finds an existing item for an unlinked row: barcode first, then SKU.
func (rn *runner) match(ctx context.Context, r *row) (string, error) {
	for _, b := range r.barcodes {
		id, ok, err := rn.cat.SyncBarcodeOwner(ctx, rn.q, b.Barcode)
		if err != nil || ok {
			return id, err
		}
	}
	if r.sku != "" {
		id, ok, err := rn.cat.SyncSKUOwner(ctx, rn.q, r.sku)
		if err != nil || ok {
			return id, err
		}
	}
	return "", nil
}

func (rn *runner) planNew(ctx context.Context, r *row, p *plan) (plan, error) {
	// Reachable only for a re-pointed link (an unlinked row with a code
	// match updates that item instead): a code now on another item is a
	// conflict, never a silent re-assignment.
	for _, b := range r.barcodes {
		if _, ok, err := rn.cat.SyncBarcodeOwner(ctx, rn.q, b.Barcode); err != nil {
			return plan{}, err
		} else if ok {
			return issue("barcode " + b.Barcode + " belongs to another item"), nil
		}
	}
	if r.sku != "" {
		if _, ok, err := rn.cat.SyncSKUOwner(ctx, rn.q, r.sku); err != nil {
			return plan{}, err
		} else if ok {
			return issue("sku belongs to another item"), nil
		}
	}
	p.action = ActionNew
	p.addBC = r.barcodes
	if rn.opt.Stock && r.hasStock && math.Abs(r.stock) > stockEpsilon {
		p.delta = r.stock
	}
	return *p, nil
}

func (rn *runner) planUpdate(ctx context.Context, r *row, p *plan) (plan, error) {
	st := p.state
	if st.Name != r.name {
		p.changes = append(p.changes, "name")
	}
	if st.BasePrice != r.price.Minor() {
		p.changes = append(p.changes, "price")
	}
	if r.catPath != nil {
		id, found, err := rn.findCategory(ctx, r.catPath)
		if err != nil {
			return plan{}, err
		}
		if !found || id != st.CategoryID {
			p.changes = append(p.changes, "category")
		}
	}
	if r.hasVAT && (!st.HasTax || st.TaxRateBP != int64(r.vat)) {
		p.changes = append(p.changes, "vat")
	}
	if st.IsActive != r.active {
		p.changes = append(p.changes, "active")
	}
	have := map[string]bool{}
	for _, b := range st.Barcodes {
		have[b] = true
	}
	for _, b := range r.barcodes {
		if have[b.Barcode] {
			continue
		}
		owner, ok, err := rn.cat.SyncBarcodeOwner(ctx, rn.q, b.Barcode)
		if err != nil {
			return plan{}, err
		}
		if ok && owner != p.target {
			return issue("barcode " + b.Barcode + " belongs to another item"), nil
		}
		if !ok {
			// The item keeps its own primary barcode; one only becomes
			// primary when the item has none yet.
			b.IsPrimary = len(st.Barcodes) == 0 && len(p.addBC) == 0
			p.addBC = append(p.addBC, b)
		}
	}
	if len(p.addBC) > 0 {
		p.changes = append(p.changes, "barcode")
	}
	if rn.opt.Stock && r.hasStock && !st.StockUntracked {
		cur, _, err := rn.inv.CurrentQty(ctx, rn.tx, rn.opt.LocationID, p.target, "")
		if err != nil {
			return plan{}, err
		}
		if d := r.stock - cur; math.Abs(d) > stockEpsilon {
			p.delta = d
			p.changes = append(p.changes, "stock")
		}
	}
	switch {
	case len(p.changes) > 0:
		p.action = ActionUpdated
	case p.relink:
		p.action, p.changes = ActionUpdated, []string{"link"}
	default:
		p.action = ActionUnchanged
	}
	return *p, nil
}

func pathKey(path []string) string { return strings.Join(path, "\x00") }

// findCategory resolves a category path read-only: found=false when a
// level does not exist yet (a commit would create it).
func (rn *runner) findCategory(ctx context.Context, path []string) (string, bool, error) {
	if len(path) == 0 {
		return "", true, nil
	}
	if id, ok := rn.catCache[pathKey(path)]; ok {
		return id, true, nil
	}
	parent := ""
	for _, name := range path {
		id, ok, err := rn.cat.FindCategoryUnder(ctx, rn.q, name, parent)
		if err != nil || !ok {
			return "", false, err
		}
		parent = id
	}
	rn.catCache[pathKey(path)] = parent
	return parent, true, nil
}

// ensureCategory resolves a category path, creating missing levels.
func (rn *runner) ensureCategory(ctx context.Context, path []string) (*string, error) {
	if len(path) == 0 {
		return nil, nil
	}
	if id, ok := rn.catCache[pathKey(path)]; ok {
		return &id, nil
	}
	parent := ""
	for _, name := range path {
		id, err := rn.cat.EnsureCategoryUnderTx(ctx, rn.tx, name, parent)
		if err != nil {
			return nil, err
		}
		parent = id
	}
	rn.catCache[pathKey(path)] = parent
	return &parent, nil
}

func (rn *runner) ensureTax(ctx context.Context, rateBP int) (*string, error) {
	if id, ok := rn.taxCache[rateBP]; ok {
		return &id, nil
	}
	id, _, err := rn.cat.FindOrCreateTaxCodeTx(ctx, rn.tx, rateBP, nil)
	if err != nil {
		return nil, err
	}
	rn.taxCache[rateBP] = id
	return &id, nil
}

// apply writes one planned row inside the run's transaction (called under
// a savepoint, so an error discards only this row's writes).
func (rn *runner) apply(ctx context.Context, r *row, p *plan) error {
	itemID := p.target
	if p.action == ActionNew {
		in := catalogtypes.ItemInput{
			Name: r.name, SKU: r.sku, BasePrice: r.price.Minor(), Unit: "each",
			IsWeighed: r.weighed, IsActive: true,
		}
		var err error
		if in.CategoryID, err = rn.ensureCategory(ctx, r.catPath); err != nil {
			return err
		}
		if r.hasVAT {
			if in.TaxCodeID, err = rn.ensureTax(ctx, r.vat); err != nil {
				return err
			}
		}
		if itemID, err = rn.cat.CreateItemTx(ctx, rn.tx, in); err != nil {
			return err
		}
		// Barcodes attach to an active item; a row that is inactive at the
		// source is switched off after.
		if err := rn.addBarcodes(ctx, itemID, p.addBC); err != nil {
			return err
		}
		if !r.active {
			if err := rn.setActive(ctx, itemID, false); err != nil {
				return err
			}
		}
	} else if err := rn.applyUpdate(ctx, r, p); err != nil {
		return err
	}
	if p.delta != 0 {
		typ := "adjust"
		if p.action == ActionNew && p.delta > 0 {
			typ = "receive" // opening stock, like the file importer's
		}
		if _, err := rn.inv.RecordStockMovement(ctx, rn.tx, data.StockMovementInput{
			ItemID: itemID, LocationID: rn.opt.LocationID, Type: typ,
			Quantity: p.delta, Reason: StockReason, ActorID: rn.opt.ActorID,
		}); err != nil {
			return fmt.Errorf("stock movement: %w", err)
		}
	}
	return rn.links.Upsert(ctx, rn.tx, rn.opt.Source, EntityItems, r.ext, itemID)
}

func (rn *runner) applyUpdate(ctx context.Context, r *row, p *plan) error {
	changed := map[string]bool{}
	for _, c := range p.changes {
		changed[c] = true
	}
	fields := changed["name"] || changed["price"] || changed["category"] || changed["vat"] || changed["active"]
	// Barcodes attach to an active item: switch an inactive one on for
	// the attach, then to the row's state.
	tempActive := len(p.addBC) > 0 && !(p.state.IsActive && r.active)
	if fields || tempActive {
		it, ok, err := rn.cat.GetItemTx(ctx, rn.tx, p.target)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("item %s vanished", p.target)
		}
		it.Name, it.BasePrice, it.IsActive = r.name, r.price.Minor(), r.active || tempActive
		if changed["category"] {
			if it.CategoryID, err = rn.ensureCategory(ctx, r.catPath); err != nil {
				return err
			}
		}
		if changed["vat"] {
			if it.TaxCodeID, err = rn.ensureTax(ctx, r.vat); err != nil {
				return err
			}
		}
		if err := rn.cat.UpdateItemTx(ctx, rn.tx, it); err != nil {
			return err
		}
	}
	if err := rn.addBarcodes(ctx, p.target, p.addBC); err != nil {
		return err
	}
	if tempActive && !r.active {
		return rn.setActive(ctx, p.target, false)
	}
	return nil
}

func (rn *runner) addBarcodes(ctx context.Context, itemID string, codes []catalogtypes.BarcodeInput) error {
	for _, b := range codes {
		b.ItemID = itemID
		if err := rn.cat.AddNormalizedBarcodeTx(ctx, rn.tx, b); err != nil {
			return fmt.Errorf("barcode: %w", err)
		}
	}
	return nil
}

func (rn *runner) setActive(ctx context.Context, itemID string, active bool) error {
	it, ok, err := rn.cat.GetItemTx(ctx, rn.tx, itemID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("item %s vanished", itemID)
	}
	it.IsActive = active
	return rn.cat.UpdateItemTx(ctx, rn.tx, it)
}
