package catalogpull

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// The upload of a cloud-master main till's own catalogue edits (ADR-0162
// §5, ut-docs#4464; contract ut-docs reference/contracts/
// cloud-catalog-changes.md). Migration 084's triggers leave an outbox mark
// in the same transaction as every local write to the uploadable set
// (items with their item-level barcodes, categories, tax codes); Push turns
// marks into changes and uploads them, 100 per call, BEFORE each pull.
//
//   - Sealing: an open mark becomes a change — the fields whose local value
//     differs from the row's base, with the base as before (the cloud's
//     conflict test compares it) — and is stored on the mark with a fresh
//     change_id. A sealed mark is re-sent unchanged until answered, so the
//     upload is idempotent on change_id. A mark with nothing to send (the
//     pull's own write, an edit reverted) is dropped. A row the cloud never
//     had uploads as a create under a cloud_id the till mints and links at
//     once (so a retry, and a row referencing it, use the same id); a
//     referenced row with no cloud_id is sealed (sent) first. A linked row
//     with no base (pulled by an applier-3 build) has one seeded from its
//     local values before the first upload or re-pull (seedBases); one
//     still without (its local row was gone) waits for the pull to set it,
//     and a seeded item's sku change waits for the re-pull to settle the
//     seeded sku (seededSKUField). A linked category or tax code deleted
//     locally uploads as an update of active to false: the till never
//     deletes either kind (ut-docs#4623).
//   - In flight: an open mark is never sealed, nor dropped, while a sealed
//     mark of the same row is unanswered. Its change is computed against
//     the base that answer leaves (sealed against the old base it would
//     conflict with the first change, and the cloud's answer would revert
//     the newer edit; a delete dropped while the create is in flight would
//     let the create's answer resurrect the row), so it seals once the
//     answer has applied, in the same Push or the next.
//   - Age is the only bound (no depth cap; an ADR-0161 import of 20 000
//     rows uploads 100 at a time): a mark first recorded more than
//     StaleAfter ago uploads stale, and the cloud logs it without applying.
//   - Answers apply in one transaction whose own writes are not marked
//     (Suppress): accepted → the row's current state is applied and becomes
//     its base, the mark is cleared, and the version is remembered as the
//     echo the feed will bring; conflict → the cloud's current row
//     overwrites the local edit (the cloud wins; the cloud logged the
//     till's change), the mark is cleared; no current row → the cloud has
//     none, so the local row goes through the tombstone rules (an item
//     ever sold is deactivated, not deleted); rejected (cloud_id
//     unavailable) → the create is re-minted under a new cloud_id; retry →
//     sent again in the next call. An accepted or conflict answer for an
//     item the till deleted after sealing the change (a create, an update)
//     does not bring it back: the link takes the cloud's row as its base and
//     version, and the open delete mark uploads a delete for that cloud_id.
//   - Stale is decided each time a change is sent, from its created_at, so
//     a change that waits in flight past the bound goes stale too.
//   - Known limit: an edit made to a row between its seal and the answer of
//     a conflict is replaced with the cloud's row too (the cloud wins); one
//     answered accepted keeps it (it is diffed against the new base).
//
// Nothing here runs on the sale path: internal/cloudsync calls Push from
// its own goroutine, before Run.

// StaleAfter is the outbox age bound (ADR-0162 §5): older marks upload
// stale.
const StaleAfter = 30 * 24 * time.Hour

// MaxChangesPerCall is the most changes one upload call carries (§5).
const MaxChangesPerCall = 100

var (
	// maxCallsPerRun bounds one Push; the rest continues on the next
	// trigger. 250 calls is 25 000 changes, a whole ADR-0161 import. A var
	// for tests.
	maxCallsPerRun = 250
	// pushNow is the clock (tests).
	pushNow = func() time.Time { return time.Now().UTC() }
)

// Change is one uploaded change (contract "Body").
type Change struct {
	ChangeID      string                     `json:"change_id"`
	Kind          string                     `json:"kind"`
	CloudID       string                     `json:"cloud_id"`
	Op            string                     `json:"op"`
	ChangedFields []string                   `json:"changed_fields"`
	Before        map[string]json.RawMessage `json:"before,omitempty"`
	After         map[string]json.RawMessage `json:"after,omitempty"`
	CreatedAt     string                     `json:"created_at"`
	StaffID       string                     `json:"staff_id"`
	Stale         bool                       `json:"stale"`
}

// Answer is the cloud's answer to one change (contract "Response").
type Answer struct {
	ChangeID string `json:"change_id"`
	Status   string `json:"status"`
	Conflict string `json:"conflict"`
	Code     string `json:"code"`
	Version  int64  `json:"version"`
	Current  []Row  `json:"current"`
}

// Answer statuses.
const (
	statusAccepted = "accepted"
	statusConflict = "conflict"
	statusRejected = "rejected"
)

// Change ops.
const (
	opCreate = "create"
	opUpdate = "update"
	opDelete = "delete"
)

// Poster uploads one call's changes and returns the cloud's answers.
type Poster func(ctx context.Context, changes []Change) ([]Answer, error)

// PushResult reports one Push.
type PushResult struct {
	Calls     int
	Sent      int // changes sent (a re-sent one counts again)
	Accepted  int
	Conflicts int // answered conflict: the cloud's row replaced the edit
	Rejected  int // re-minted creates
	Dropped   int // marks with nothing to upload
	// Changed is true when an answer changed the local catalogue.
	Changed bool
	// Pending is the marks still waiting (open and sealed) after the run.
	Pending int
	// Stopped / StopStatus: an answer that stops the upload with no error
	// (409 catalog_master_till / catalog_adopting, 402, 401/403).
	Stopped    string
	StopStatus int
}

// Push uploads the outbox (package doc above). A non-nil error means the
// run stopped early; what was answered stays answered, and the next Push
// re-sends the rest.
func Push(ctx context.Context, db *sql.DB, post Poster, opt Options) (res PushResult, err error) {
	r := newRunner(db, opt)
	if stale, err := r.outbox.ClearStaleSuppress(ctx, db); err != nil {
		return res, err
	} else if stale {
		logging.L().Warnf("catalogpull: removed a committed outbox suppression flag; till edits since were not marked for upload")
	}
	if err := seedBasesIfUpgrading(ctx, db, r.settings); err != nil {
		return res, err
	}
	st, err := loadGen(ctx, r.settings)
	if err != nil {
		return res, err
	}
	defer func() {
		if open, sealed, cerr := r.outbox.Counts(context.WithoutCancel(ctx), db); cerr == nil {
			res.Pending = open + sealed
		}
	}()
	for calls := 0; calls < maxCallsPerRun; calls++ {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		batch, err := r.nextBatch(ctx, st, &res)
		if err != nil {
			return res, err
		}
		if len(batch) == 0 {
			return res, nil
		}
		changes := make([]Change, len(batch))
		for i, m := range batch {
			if err := json.Unmarshal([]byte(m.Body), &changes[i]); err != nil {
				return res, fmt.Errorf("catalogpull: sealed change %d unreadable: %w", m.ID, err)
			}
			// The age bound holds at send time, not only at seal time: a
			// change re-sent after 30 days in flight goes stale.
			changes[i].Stale = changes[i].Stale || isStale(changes[i].CreatedAt)
		}
		res.Calls++
		res.Sent += len(changes)
		answers, err := post(ctx, changes)
		var stop *StopError
		switch {
		case errors.As(err, &stop):
			res.Stopped, res.StopStatus = stop.Code, stop.Status
			return res, nil
		case err != nil:
			return res, err
		}
		progress, err := r.applyAnswers(ctx, batch, answers, st, &res)
		if err != nil {
			return res, err
		}
		if !progress {
			// Every change answered retry (or not at all): try again on the
			// next trigger rather than spin.
			return res, nil
		}
	}
	return res, nil
}

func newRunner(db *sql.DB, opt Options) *runner {
	return &runner{
		db: db, settings: data.NewSettingsRepo(db), opt: opt,
		cat: data.NewCatalogRepo(db), pos: data.NewPOSRepo(db), links: data.NewCloudLinkRepo(),
		mods: data.NewModifierRepo(db), sets: data.NewOptionSetRepo(db), outbox: data.NewCloudOutboxRepo(),
	}
}

// loadGen reads the link generation a new link is stamped with.
func loadGen(ctx context.Context, settings *data.SettingsRepo) (int64, error) {
	v, ok, err := settings.Get(ctx, KeyGen)
	if err != nil {
		return 0, fmt.Errorf("catalogpull: read %s: %w", KeyGen, err)
	}
	if !ok || v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, nil
	}
	return n, nil
}

// nextBatch returns up to MaxChangesPerCall sealed marks: the ones sealed
// before (never answered), then newly sealed open marks.
func (r *runner) nextBatch(ctx context.Context, gen int64, res *PushResult) ([]data.OutboxMark, error) {
	sealed, err := r.outbox.SealedMarks(ctx, r.db, MaxChangesPerCall)
	if err != nil {
		return nil, err
	}
	if len(sealed) < MaxChangesPerCall {
		if err := r.sealOpen(ctx, gen, MaxChangesPerCall-len(sealed), res); err != nil {
			return nil, err
		}
		if sealed, err = r.outbox.SealedMarks(ctx, r.db, MaxChangesPerCall); err != nil {
			return nil, err
		}
	}
	return sealed, nil
}

// sealOpen seals up to want open marks, oldest first, in one transaction.
func (r *runner) sealOpen(ctx context.Context, gen int64, want int, res *PushResult) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("catalogpull: seal: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	sealed, after := 0, int64(0)
	for sealed < want {
		marks, err := r.outbox.OpenMarks(ctx, tx, after, want)
		if err != nil {
			return err
		}
		if len(marks) == 0 {
			break
		}
		for _, m := range marks {
			after = m.ID
			n, err := r.sealMark(ctx, tx, m, gen, 0, res)
			if err != nil {
				return err
			}
			sealed += n
			if sealed >= want {
				break
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("catalogpull: seal: commit: %w", err)
	}
	return nil
}

// maxRefDepth bounds sealing referenced rows first (a category's parents).
const maxRefDepth = 8

// sealMark turns one open mark into a sealed change, dropping it when
// there is nothing to upload, and returns how many marks it sealed
// (referenced rows sealed first included).
func (r *runner) sealMark(ctx context.Context, tx *sql.Tx, m data.OutboxMark, gen int64, depth int, res *PushResult) (int, error) {
	if !uploadable(m.Entity) {
		res.Dropped++
		return 0, r.outbox.DeleteMark(ctx, tx, m.ID)
	}
	// The in-flight rule (package doc): wait for the earlier change's answer.
	if inFlight, err := r.outbox.HasSealedMark(ctx, tx, m.Entity, m.LocalID); err != nil || inFlight {
		return 0, err
	}
	link, linked, err := r.links.StateByLocal(ctx, tx, m.Entity, m.LocalID)
	if err != nil {
		return 0, err
	}
	base := parseBase(link.Base)
	l, err := r.readLocal(ctx, tx, m.Entity, m.LocalID, base)
	if err != nil {
		return 0, err
	}
	sealedRefs := 0
	if len(l.unlinked) > 0 {
		if depth >= maxRefDepth {
			// A reference chain this deep (or a cycle): leave the mark open.
			return 0, nil
		}
		for _, rf := range l.unlinked {
			if err := r.outbox.EnsureOpenMark(ctx, tx, rf.kind, rf.localID, m.CreatedAt); err != nil {
				return 0, err
			}
			refMarks, err := r.openMarkOf(ctx, tx, rf.kind, rf.localID)
			if err != nil {
				return 0, err
			}
			for _, rm := range refMarks {
				n, err := r.sealMark(ctx, tx, rm, gen, depth+1, res)
				if err != nil {
					return 0, err
				}
				sealedRefs += n
			}
		}
		if l, err = r.readLocal(ctx, tx, m.Entity, m.LocalID, base); err != nil {
			return 0, err
		}
		if len(l.unlinked) > 0 {
			return sealedRefs, nil // still unresolved: wait
		}
	}
	ch := Change{Kind: m.Entity, CreatedAt: m.CreatedAt, ChangedFields: []string{}}
	switch {
	case !l.exists && !linked:
		// Created and removed before it ever uploaded.
		res.Dropped++
		return sealedRefs, r.outbox.DeleteMark(ctx, tx, m.ID)
	case !l.exists && m.Entity != kindItem && link.Version != 0 && base != nil:
		// A category or tax code hard-deleted on the till (the demo-data
		// removal): the till never deletes either kind, it deactivates
		// one, so the cloud hears an update of active (contract "op",
		// ut-docs#4623). Already inactive in the base (or a base without
		// active, which no applied row leaves): nothing to send.
		if a, ok := base["active"]; !ok || norm(a) == "false" {
			res.Dropped++
			return sealedRefs, r.outbox.DeleteMark(ctx, tx, m.ID)
		}
		ch.Op, ch.CloudID, ch.ChangedFields = opUpdate, link.CloudID, []string{"active"}
		ch.Before = map[string]json.RawMessage{"active": beforeValue("active", base["active"])}
		ch.After = map[string]json.RawMessage{"active": rawJSON(false)}
	case !l.exists:
		if link.Version == 0 || base == nil || m.Entity != kindItem {
			// Never accepted by the cloud (a create that did not reach it),
			// or a row with no base yet: nothing to tell the cloud.
			res.Dropped++
			if link.Version == 0 {
				if err := r.links.Delete(ctx, tx, m.Entity, link.CloudID); err != nil {
					return 0, err
				}
			}
			return sealedRefs, r.outbox.DeleteMark(ctx, tx, m.ID)
		}
		ch.Op, ch.CloudID, ch.Before = opDelete, link.CloudID, map[string]json.RawMessage{}
		_, seeded := base[seededSKUField]
		for _, f := range uploadFields[m.Entity] {
			if f == "sku" && seeded {
				// An unsettled seeded sku may be the till's own SKU for a
				// blank cloud one, and a deleted row cannot wait for the
				// re-pull (it skips a marked row the till deleted): the
				// cloud compares the fields it is sent (ut-docs#4623).
				continue
			}
			if v, ok := base[f]; ok {
				ch.Before[f] = beforeValue(f, v)
			}
		}
	case !linked || (link.Version == 0 && base == nil):
		ch.Op, ch.After = opCreate, map[string]json.RawMessage{}
		ch.CloudID = link.CloudID
		if !linked {
			ch.CloudID = uuid.NewString()
			if err := r.links.Upsert(ctx, tx, data.CloudLink{Entity: m.Entity, CloudID: ch.CloudID, LocalID: m.LocalID, Version: 0, Gen: gen}); err != nil {
				return 0, err
			}
		}
		for _, f := range uploadFields[m.Entity] {
			if f == "barcodes" && len(l.keys) == 0 {
				continue
			}
			ch.After[f] = l.vals[f]
			ch.ChangedFields = append(ch.ChangedFields, f)
		}
	case base == nil:
		// Linked by a pull that kept no base (an older applier): the next
		// pull re-applies it and sets one; the mark waits.
		return sealedRefs, nil
	default:
		fields := changedFields(m.Entity, l, base)
		if len(fields) == 0 {
			res.Dropped++
			return sealedRefs, r.outbox.DeleteMark(ctx, tx, m.ID)
		}
		if _, seeded := base[seededSKUField]; seeded && slices.Contains(fields, "sku") {
			// A seeded sku may be the till's own SKU for a blank cloud
			// one, not the cloud's value: sent as before it would be a
			// false conflict. The re-pull settles it; the mark waits
			// (ut-docs#4623).
			return sealedRefs, nil
		}
		ch.Op, ch.CloudID = opUpdate, link.CloudID
		ch.Before, ch.After = map[string]json.RawMessage{}, map[string]json.RawMessage{}
		for _, f := range fields {
			ch.Before[f] = beforeValue(f, base[f])
			ch.After[f] = l.vals[f]
		}
		ch.ChangedFields = fields
	}
	ch.Stale = isStale(m.CreatedAt)
	ch.ChangeID = uuid.NewString()
	body, err := json.Marshal(ch)
	if err != nil {
		return 0, fmt.Errorf("catalogpull: encode change: %w", err)
	}
	ok, err := r.outbox.Seal(ctx, tx, m.ID, ch.ChangeID, string(body), pushNow().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	if !ok {
		return sealedRefs, nil
	}
	return sealedRefs + 1, nil
}

// isStale reports whether a change first recorded at createdAt is past the
// outbox age bound now.
func isStale(createdAt string) bool {
	at, err := time.Parse(time.RFC3339, createdAt)
	return err == nil && pushNow().Sub(at) > StaleAfter
}

func (r *runner) openMarkOf(ctx context.Context, tx *sql.Tx, entity, localID string) ([]data.OutboxMark, error) {
	m, ok, err := r.outbox.OpenMarkFor(ctx, tx, entity, localID)
	if err != nil || !ok {
		return nil, err
	}
	return []data.OutboxMark{m}, nil
}

// beforeValue is a base value as the upload sends it: a barcodes base
// carries the cloud's codes.
func beforeValue(field string, v json.RawMessage) json.RawMessage {
	if field != "barcodes" {
		return v
	}
	type wireBarcode struct {
		Code string `json:"code"`
		Type string `json:"type"`
	}
	list := barcodesOf(v)
	out := make([]wireBarcode, len(list))
	for i, b := range list {
		out[i] = wireBarcode{Code: b.Code}
	}
	return rawJSON(out)
}

// applyAnswers applies one call's answers in one transaction whose writes
// are never marked; progress is false when no change was answered.
func (r *runner) applyAnswers(ctx context.Context, batch []data.OutboxMark, answers []Answer, gen int64, res *PushResult) (bool, error) {
	byID := make(map[string]Answer, len(answers))
	var current []Row
	for _, a := range answers {
		byID[a.ChangeID] = a
		current = append(current, a.Current...)
	}
	// Barcode resolution reads settings through the pool: before the
	// transaction (resolveBarcodes' rule).
	r.barcodes = r.resolveBarcodes(ctx, current)
	defer func() { r.barcodes, r.photos = nil, nil }()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("catalogpull: answers: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := r.outbox.Suppress(ctx, tx); err != nil {
		return false, err
	}
	progress := false
	for _, m := range batch {
		a, ok := byID[m.ChangeID]
		if !ok {
			continue // not answered: sent again
		}
		var ch Change
		if err := json.Unmarshal([]byte(m.Body), &ch); err != nil {
			return false, fmt.Errorf("catalogpull: sealed change %d unreadable: %w", m.ID, err)
		}
		switch a.Status {
		case statusAccepted, statusConflict:
			if err := r.applyAnswer(ctx, tx, m, ch, a, gen, res); err != nil {
				return false, err
			}
			if a.Status == statusAccepted {
				res.Accepted++
			} else {
				res.Conflicts++
				logging.L().Infof("catalogpull: the cloud kept its own %s %s over this till's change (%s %s); the till's change is in the cloud's conflict log", ch.Kind, ch.CloudID, a.Conflict, a.Code)
			}
		case statusRejected:
			if ch.Op != opCreate {
				continue // only a create is ever rejected; treat as retry
			}
			// The cloud_id cannot be used: mint another on the next seal.
			if err := r.links.Delete(ctx, tx, ch.Kind, ch.CloudID); err != nil {
				return false, err
			}
			if err := r.outbox.DeleteMark(ctx, tx, m.ID); err != nil {
				return false, err
			}
			if err := r.outbox.EnsureOpenMark(ctx, tx, m.Entity, m.LocalID, m.CreatedAt); err != nil {
				return false, err
			}
			res.Rejected++
		default:
			continue // retry, or a status this build does not know
		}
		progress = true
	}
	if err := r.outbox.Unsuppress(ctx, tx); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("catalogpull: answers: commit: %w", err)
	}
	if r.opt.ItemPhotoReplaced != nil {
		for _, ph := range r.photos {
			r.opt.ItemPhotoReplaced(ph.itemID, ph.path)
		}
	}
	return progress, nil
}

// applyAnswer applies one accepted or conflict answer and clears its mark.
func (r *runner) applyAnswer(ctx context.Context, tx *sql.Tx, m data.OutboxMark, ch Change, a Answer, gen int64, res *PushResult) error {
	if err := r.outbox.DeleteMark(ctx, tx, m.ID); err != nil {
		return err
	}
	if len(a.Current) == 0 {
		// The cloud holds no live row (deleted, never created, or the
		// create lost): the local row goes through the tombstone rules.
		if err := r.tombstone(ctx, tx, ch.Kind, ch.CloudID, m.LocalID, 0); err != nil {
			if isInfra(ctx, err) {
				return fmt.Errorf("catalogpull: %w", errors.Join(err, ctx.Err()))
			}
			logging.L().Warnf("catalogpull: %s %s: the cloud has no such row, but the till could not remove it: %v", ch.Kind, ch.CloudID, err)
			if err := r.recordRefusal(ctx, tx, ch.Kind, ch.CloudID, 0, err); err != nil {
				return err
			}
			if err := r.links.Delete(ctx, tx, ch.Kind, ch.CloudID); err != nil {
				return err
			}
		}
		if ch.Kind == kindItem {
			if err := r.dropItemBarcodeLinks(ctx, tx, m.LocalID, nil); err != nil {
				return err
			}
		}
		res.Changed = true
		return nil
	}
	if ch.Kind == kindItem && ch.Op != opDelete {
		if gone, err := r.adoptForLocalDelete(ctx, tx, m, ch, a, gen); err != nil || gone {
			return err
		}
	}
	forced := a.Status == statusConflict
	if !forced {
		// The accepted values are the cloud's now: the base moves to them
		// before the answer applies, so an edit made since the seal stays a
		// pending local edit.
		if st, ok, err := r.links.State(ctx, tx, ch.Kind, ch.CloudID); err != nil {
			return err
		} else if ok {
			base := parseBase(st.Base)
			if base == nil {
				base = values{}
			}
			for f, v := range ch.After {
				if f == "barcodes" {
					continue // set from the answer's barcode rows below
				}
				base[f] = v
			}
			if err := r.links.SetBase(ctx, tx, ch.Kind, ch.CloudID, base.encode()); err != nil {
				return err
			}
		}
	}
	r.forced, r.answering = forced, true
	defer func() { r.forced, r.answering = false, false }()
	var applyRes Result
	live := map[string]bool{}
	for _, row := range dependencyOrder(a.Current) {
		// final: an answer's rows are applied once (ut-docs#4522's retry is
		// for option sets moved within one feed version group; an answer
		// carries only the changed row and its item-level barcodes).
		if _, err := r.applyRow(ctx, tx, row, gen, &applyRes, true); err != nil {
			return err
		}
		id, err := canonicalID(row.CloudID)
		if err != nil {
			continue
		}
		live[row.Kind+":"+id] = true
		if a.Status == statusAccepted && a.Version > 0 && row.Version == a.Version {
			if err := r.links.SetEcho(ctx, tx, row.Kind, id, a.Version); err != nil {
				return err
			}
		}
	}
	if applyRes.Refused > 0 {
		logging.L().Warnf("catalogpull: the cloud's answer to %s %s was refused by this till (%d rows); recorded as apply failures", ch.Kind, ch.CloudID, applyRes.Refused)
	}
	if ch.Kind == kindItem {
		if err := r.dropItemBarcodeLinks(ctx, tx, m.LocalID, live); err != nil {
			return err
		}
		if forced {
			if err := r.dropLocalBarcodesNotIn(ctx, tx, m.LocalID, a.Current); err != nil {
				return err
			}
		}
	}
	res.Changed = res.Changed || applyRes.Applied > 0 || forced
	return nil
}

// adoptForLocalDelete handles an answer (accepted or conflict, the cloud
// holding a live row) to a create or update of an item the till deleted
// after sealing it: the cloud's row is not applied (it would bring the
// item back); the link takes its version and values as the base, and the
// item's delete mark (kept open by the in-flight rule) uploads a delete
// against them. gone is false when the item still exists locally.
func (r *runner) adoptForLocalDelete(ctx context.Context, tx *sql.Tx, m data.OutboxMark, ch Change, a Answer, gen int64) (gone bool, err error) {
	_, _, exists, err := r.outbox.OutboxItemValues(ctx, tx, m.LocalID)
	if err != nil || exists {
		return false, err
	}
	var item *Row
	var codes []baseBarcode
	for i := range a.Current {
		row := a.Current[i]
		id, err := canonicalID(row.CloudID)
		if err != nil || row.Deleted {
			continue
		}
		switch row.Kind {
		case kindItem:
			if id == ch.CloudID {
				item = &a.Current[i]
			}
		case kindItemBarcode:
			f, err := decodeFields(row.Data)
			if err != nil {
				continue
			}
			if v, _, null, _ := f.str("variant_cloud_id"); v != "" && !null {
				continue // a variant's barcode: not in the item's set
			}
			code, err := f.requiredStr("barcode")
			if err != nil || code == nil {
				continue
			}
			entry := baseBarcode{Code: *code, Key: *code}
			if p, _ := f.boolPtr("is_primary"); p != nil && *p {
				codes = append([]baseBarcode{entry}, codes...)
			} else {
				codes = append(codes, entry)
			}
		}
	}
	if item == nil {
		return false, nil // no live item row: the tombstone rules apply
	}
	f, err := decodeFields(item.Data)
	if err != nil {
		return false, nil
	}
	if err := r.links.Upsert(ctx, tx, data.CloudLink{Entity: kindItem, CloudID: ch.CloudID, LocalID: m.LocalID, Version: item.Version, Gen: gen}); err != nil {
		return false, err
	}
	base := baseFromRow(kindItem, f, nil)
	if codes == nil {
		codes = []baseBarcode{}
	}
	base["barcodes"] = rawJSON(codes)
	if err := r.links.SetBase(ctx, tx, kindItem, ch.CloudID, base.encode()); err != nil {
		return false, err
	}
	if err := r.dropItemBarcodeLinks(ctx, tx, m.LocalID, nil); err != nil {
		return false, err
	}
	if err := r.outbox.EnsureOpenMark(ctx, tx, kindItem, m.LocalID, pushNow().Format(time.RFC3339)); err != nil {
		return false, err
	}
	logging.L().Infof("catalogpull: item %s was deleted on this till while its change was in flight; uploading the delete", ch.CloudID)
	return true, nil
}

// dropItemBarcodeLinks removes item_barcode links of itemID's item-level
// barcodes whose cloud row is not in live (nil = all): the cloud no longer
// holds them.
func (r *runner) dropItemBarcodeLinks(ctx context.Context, tx *sql.Tx, itemID string, live map[string]bool) error {
	links, err := r.links.ItemBarcodeLinks(ctx, tx, kindItemBarcode, itemID)
	if err != nil {
		return err
	}
	for _, l := range links {
		if live[kindItemBarcode+":"+l.CloudID] {
			continue
		}
		if err := r.links.Delete(ctx, tx, kindItemBarcode, l.CloudID); err != nil {
			return err
		}
	}
	return nil
}

// dropLocalBarcodesNotIn removes the item's item-level barcodes the
// cloud's current rows do not hold (a conflict: the cloud's set wins).
func (r *runner) dropLocalBarcodesNotIn(ctx context.Context, tx *sql.Tx, itemID string, current []Row) error {
	keep := map[string]bool{}
	for _, row := range current {
		if row.Kind != kindItemBarcode || row.Deleted {
			continue
		}
		f, err := decodeFields(row.Data)
		if err != nil {
			continue
		}
		code, err := f.requiredStr("barcode")
		if err != nil || code == nil {
			continue
		}
		if res, ok := r.barcodes[*code]; ok && res.err == nil {
			keep[res.res.Key] = true
		}
	}
	_, bcs, ok, err := r.outbox.OutboxItemValues(ctx, tx, itemID)
	if err != nil || !ok {
		return err
	}
	for _, b := range bcs {
		if keep[b.Key] {
			continue
		}
		if _, err := r.cat.DeleteOwnedBarcodeTx(ctx, tx, b.Key, itemID, ""); err != nil {
			return err
		}
		if err := r.dropBaseBarcode(ctx, tx, itemID, b.Key); err != nil {
			return err
		}
	}
	return nil
}
