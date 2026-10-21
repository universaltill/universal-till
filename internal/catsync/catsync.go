// Package catsync applies catalogue rows an API-source import plugin
// submitted (ADR-0161, ut-docs#4429): it plans every row against the
// catalogue and import_source_links, and — on a commit — applies the plan
// in one transaction. A preview runs the same plan and writes nothing.
//
// Order per row: a row whose (source, external_id) is linked updates that
// item; an unlinked row that matches an item by barcode, then SKU, links
// to it and updates it; an unlinked row with neither barcode nor SKU, after
// those found nothing, matches by exact name — but only an item a sibling
// entry of the same plugin linked, and only when exactly one such item has
// that name (ADR-0161 Amendment 2, ut-docs#4734); anything else is created with the repository
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
	// Source is the link source of the importing entry ("<plugin id>#<entry
	// key>", ut-docs#4727) — core-stamped, never a row field. Required.
	Source string
	// LegacySource, when non-empty and different from Source, is a link
	// source older runs stamped (the bare plugin id, before links were per
	// entry — ut-docs#4727). It also counts as a sibling for the exact-name
	// fallback (ut-docs#4734). A row with no link under Source but one under
	// LegacySource is treated as linked, and its link is written under
	// Source on commit. It is never consulted for the linked-elsewhere
	// refusal, and old rows are left in place.
	LegacySource string
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
	Issue      string   `json:"issue,omitempty"` // English text
	// IssueCode is the stable code of the issue and IssueArgs the values its
	// text names (ut-docs#4485): the till translates them, a client may too.
	IssueCode string   `json:"issue_code,omitempty"`
	IssueArgs []string `json:"issue_args,omitempty"`
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
	issue   rowIssue
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

	// Exact-name fallback (ut-docs#4734), snapshotted before any row is
	// applied so a preview and a commit decide alike: sibling-linked items
	// by name, items this run's rows adopt through LegacySource (never a
	// name candidate), and how many code-less rows carry each name.
	sibByName     map[string][]string
	reserved      map[string]bool
	codelessNames map[string]int
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
	issues := make([]rowIssue, len(rows))
	exts := make([]string, len(rows))
	seenExt := map[string]bool{}
	for i, raw := range rows {
		r, ext, issue := parseRow(raw)
		exts[i] = ext
		if issue.none() && seenExt[ext] {
			issue = newIssue(IssueExternalIDDuplicate)
		}
		if issue.none() {
			issue = normalizeBarcodes(ctx, rn.cat, &r)
		}
		if ext != "" {
			seenExt[ext] = true
		}
		parsed[i], issues[i] = r, issue
	}
	if err := rn.snapshotNameFallback(ctx, db, parsed, issues); err != nil {
		return Result{}, err
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
		if !issues[i].none() {
			s.Action = ActionIssue
			s.setIssue(issues[i])
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
				p = issue(IssueApplyFailed)
			}
		}
		s.Action, s.Changes = p.action, p.changes
		s.setIssue(p.issue)
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

func issue(code string, args ...string) plan {
	return plan{action: ActionIssue, issue: newIssue(code, args...)}
}

func (s *Sample) setIssue(i rowIssue) {
	if i.none() {
		return
	}
	s.Issue, s.IssueCode, s.IssueArgs = i.Text(), i.Code, i.Args
}

// plan decides what one valid row does. Reads only (through rn.q).
func (rn *runner) plan(ctx context.Context, r *row) (plan, error) {
	for _, b := range r.barcodes {
		if other, ok := rn.claimedBarcode[b.Barcode]; ok && other != r.ext {
			return issue(IssueBarcodeDuplicate, b.Barcode), nil
		}
	}
	if other, ok := rn.claimedSKU[r.sku]; r.sku != "" && ok && other != r.ext {
		return issue(IssueSKUDuplicate), nil
	}

	var p plan
	linked, isLinked, err := rn.links.LocalID(ctx, rn.q, rn.opt.Source, EntityItems, r.ext)
	if err != nil {
		return plan{}, err
	}
	adopted := false
	if !isLinked && rn.opt.LegacySource != "" && rn.opt.LegacySource != rn.opt.Source {
		linked, isLinked, err = rn.links.LocalID(ctx, rn.q, rn.opt.LegacySource, EntityItems, r.ext)
		if err != nil {
			return plan{}, err
		}
		adopted = isLinked
	}
	if isLinked {
		p.relink = adopted // a legacy link is re-stamped under Source
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
			// An earlier row of this run already claimed cand: report that
			// before the link check, which on a commit would see the link
			// that row just wrote and say "linked elsewhere" where a
			// preview says "already matched" (ut-docs#4740).
			if other, ok := rn.claimedItem[cand]; ok && other != r.ext {
				return issue(IssueItemAlreadyMatched), nil
			}
			if ext, ok, err := rn.links.ExternalIDFor(ctx, rn.q, rn.opt.Source, EntityItems, cand); err != nil {
				return plan{}, err
			} else if ok && ext != r.ext {
				return issue(IssueItemLinkedElsewhere, ext), nil
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
			return issue(IssueItemAlreadyMatched), nil
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
// A row with neither falls back to an exact name match (ut-docs#4734,
// ADR-0161 Amendment 2): among items linked by a sibling entry of the same
// plugin (source "<plugin id>#<other entry>", or LegacySource), not yet
// linked under Source, and only when exactly one item has that name. A row
// that carries a barcode or SKU never uses its name.
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
	if len(r.barcodes) > 0 || r.sku != "" || rn.codelessNames[r.name] > 1 {
		return "", nil
	}
	var cand []string
	for _, id := range rn.sibByName[r.name] {
		if rn.reserved[id] {
			continue
		}
		if other, ok := rn.claimedItem[id]; ok && other != r.ext {
			continue
		}
		cand = append(cand, id)
	}
	if len(cand) != 1 {
		return "", nil
	}
	return cand[0], nil
}

// snapshotNameFallback loads match()'s name-fallback state once per run,
// through the pool and before the first transaction, and only when a valid
// row has neither barcode nor SKU.
func (rn *runner) snapshotNameFallback(ctx context.Context, db *sql.DB, parsed []row, issues []rowIssue) error {
	rn.codelessNames = map[string]int{}
	for i := range parsed {
		if issues[i].none() && len(parsed[i].barcodes) == 0 && parsed[i].sku == "" {
			rn.codelessNames[parsed[i].name]++
		}
	}
	if len(rn.codelessNames) == 0 {
		return nil
	}
	plugin := ""
	if i := strings.IndexByte(rn.opt.Source, '#'); i >= 0 {
		plugin = rn.opt.Source[:i]
	}
	legacy := rn.opt.LegacySource
	if legacy == rn.opt.Source {
		legacy = ""
	}
	if plugin == "" && legacy == "" {
		return nil
	}
	byName, err := rn.links.SiblingItemsByName(ctx, db, plugin, rn.opt.Source, legacy)
	if err != nil {
		return err
	}
	rn.sibByName, rn.reserved = byName, map[string]bool{}
	if len(byName) == 0 {
		return nil
	}
	// A row that names an item by barcode or SKU keeps it: a code-less row
	// listed before it never takes that item by name.
	for i := range parsed {
		if !issues[i].none() || (len(parsed[i].barcodes) == 0 && parsed[i].sku == "") {
			continue
		}
		id, err := rn.match(ctx, &parsed[i]) // codes only: never reaches the name step
		if err != nil {
			return err
		}
		if id != "" {
			rn.reserved[id] = true
		}
	}
	if legacy == "" {
		return nil
	}
	for i := range parsed {
		if !issues[i].none() {
			continue
		}
		if _, own, err := rn.links.LocalID(ctx, db, rn.opt.Source, EntityItems, parsed[i].ext); err != nil {
			return err
		} else if own {
			continue // linked under Source: the legacy link is never read
		}
		id, ok, err := rn.links.LocalID(ctx, db, legacy, EntityItems, parsed[i].ext)
		if err != nil {
			return err
		}
		if ok {
			rn.reserved[id] = true
		}
	}
	return nil
}

func (rn *runner) planNew(ctx context.Context, r *row, p *plan) (plan, error) {
	// Reachable only for a re-pointed link (an unlinked row with a code
	// match updates that item instead): a code now on another item is a
	// conflict, never a silent re-assignment.
	for _, b := range r.barcodes {
		if _, ok, err := rn.cat.SyncBarcodeOwner(ctx, rn.q, b.Barcode); err != nil {
			return plan{}, err
		} else if ok {
			return issue(IssueBarcodeTaken, b.Barcode), nil
		}
	}
	if r.sku != "" {
		if _, ok, err := rn.cat.SyncSKUOwner(ctx, rn.q, r.sku); err != nil {
			return plan{}, err
		} else if ok {
			return issue(IssueSKUTaken), nil
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
	// Absent = leave as is; a present field that differs is the source's.
	if r.desc != "" && st.Description != r.desc {
		p.changes = append(p.changes, "description")
	}
	if r.age != nil && st.AgeRestricted != *r.age {
		p.changes = append(p.changes, "age_restricted")
	}
	if r.nqValue != nil && !sameNetQuantity(st, r) {
		p.changes = append(p.changes, "net_quantity")
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
			return issue(IssueBarcodeTaken, b.Barcode), nil
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

// sameNetQuantity reports whether the item already stores r's pair (r
// carries one).
func sameNetQuantity(st data.SyncItemState, r *row) bool {
	return st.NetQuantityValue != nil && st.NetQuantityUnit != nil &&
		*st.NetQuantityValue == *r.nqValue && *st.NetQuantityUnit == *r.nqUnit
}

// setOptionalFields copies the fields a row may leave out (description,
// age check, net quantity) onto in, only where the row carries them.
func setOptionalFields(in *catalogtypes.ItemInput, r *row) {
	if r.desc != "" {
		in.Description = r.desc
	}
	if r.age != nil {
		in.AgeRestricted = *r.age
	}
	if r.nqValue != nil {
		in.NetQuantityValue, in.NetQuantityUnit = r.nqValue, r.nqUnit
	}
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
		setOptionalFields(&in, r)
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
	fields := changed["name"] || changed["price"] || changed["category"] || changed["vat"] || changed["active"] ||
		changed["description"] || changed["age_restricted"] || changed["net_quantity"]
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
		setOptionalFields(&it, r)
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
