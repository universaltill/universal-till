package catalogpull

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// Edits a cloud cannot take yet (ut-docs#6088, contract cloud-catalog-changes
// 1.2.0 "Accepted fields"). A field the till knows but the cloud does not
// advertise is withheld: left out of the change, kept on the till as a
// pending edit. A pending edit is a field whose local value differs from its
// base on a row with an outbox mark - and a withheld one has no mark once its
// change is answered, so the row goes to cloud_outbox_held instead, which
// pendingFields reads like a mark. The table is not cloud_outbox on purpose:
// sealOpen rescans every open mark each Push, and a 20 000-row import held
// against an old cloud must cost nothing per tick.
//
// A hold is written when the change is sealed (a part uploads, or nothing
// does), settled by the answer (holdWithheld: kept while a withheld field
// still differs from its base, removed when none does) and released back
// into the outbox as an ordinary open mark when the accepted set changes
// (releaseHeldIfChanged), where it seals as an update with the base as before.

// withheldInfof is the Info logger of the once-per-set withheld notice
// (tests replace it).
var withheldInfof = func(format string, args ...any) { logging.L().Infof(format, args...) }

// withheldLogged is the accepted-set fingerprint the notice was last logged
// for in this process.
var withheldLogged atomic.Value

// noteWithheld logs once per process and accepted set which fields wait for a
// newer cloud: kinds, field names and the row count, never a value.
func (r *runner) noteWithheld(held int) {
	if held == 0 {
		return
	}
	if v, _ := withheldLogged.Load().(string); v == r.accepted.fp {
		return
	}
	var kinds []string
	for kind, w := range r.accepted.withheld {
		if len(w) > 0 {
			kinds = append(kinds, kind)
		}
	}
	if len(kinds) == 0 {
		return // held rows from a wider set: nothing is withheld now
	}
	sort.Strings(kinds)
	parts := make([]string, len(kinds))
	for i, kind := range kinds {
		parts[i] = kind + " fields " + strings.Join(r.accepted.withheld[kind], ", ")
	}
	withheldLogged.Store(r.accepted.fp)
	withheldInfof("catalogpull: this cloud does not accept %s yet; %d catalogue rows keep those edits on the till until it does", strings.Join(parts, "; "), held)
}

// releaseHeldIfChanged hands every held row back to the outbox once when the
// accepted set differs from the one the held rows were last released for (a
// cloud upgrade, or a rollback that holds some of them again). An unchanged
// set costs one settings read per Push.
func (r *runner) releaseHeldIfChanged(ctx context.Context) error {
	v, _, err := r.settings.Get(ctx, KeyHeldAccepted)
	if err != nil {
		return fmt.Errorf("catalogpull: read %s: %w", KeyHeldAccepted, err)
	}
	if v == r.accepted.fp {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("catalogpull: release held: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	n, err := r.outbox.ReleaseHeld(ctx, tx)
	if err != nil {
		return err
	}
	if err := r.settings.SetTx(ctx, tx, KeyHeldAccepted, r.accepted.fp); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("catalogpull: release held: commit: %w", err)
	}
	if n > 0 {
		logging.L().Infof("catalogpull: the cloud's accepted catalogue fields changed; %d held rows are checked for upload again", n)
	}
	return nil
}

// resealOutrun re-seals sealed, unanswered changes that name a field outside
// the accepted set (sealed before a cloud rollback, or by a build that sent
// ahead): the withheld keys are stripped and the change gets a new change_id,
// keeping seal_seq (reference order) and created_at. An update with nothing
// left moves to held. Safe: a 400 recorded nothing on the cloud, and an
// answered change_id is never reused. It reports whether it changed any.
func (r *runner) resealOutrun(ctx context.Context, sealed []data.OutboxMark) (bool, error) {
	if !r.accepted.anyWithheld() {
		return false, nil
	}
	type fix struct {
		m  data.OutboxMark
		ch Change
	}
	var fixes []fix
	for _, m := range sealed {
		var ch Change
		if json.Unmarshal([]byte(m.Body), &ch) != nil {
			continue // Push reports an unreadable body
		}
		if r.accepted.strip(&ch) {
			fixes = append(fixes, fix{m, ch})
		}
	}
	if len(fixes) == 0 {
		return false, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("catalogpull: reseal: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, f := range fixes {
		if f.ch.Op == opUpdate && len(f.ch.ChangedFields) == 0 {
			if err := r.outbox.DeleteMark(ctx, tx, f.m.ID); err != nil {
				return false, err
			}
			if err := r.outbox.HoldMark(ctx, tx, f.m.Entity, f.m.LocalID, f.m.CreatedAt); err != nil {
				return false, err
			}
			continue
		}
		f.ch.ChangeID = uuid.NewString()
		body, err := json.Marshal(f.ch)
		if err != nil {
			return false, fmt.Errorf("catalogpull: encode change: %w", err)
		}
		if _, err := r.outbox.Reseal(ctx, tx, f.m.ID, f.ch.ChangeID, string(body)); err != nil {
			return false, err
		}
		if f.ch.Op != opDelete {
			// The stripped fields' edits stay on the till until the answer settles them.
			if err := r.outbox.HoldMark(ctx, tx, f.m.Entity, f.m.LocalID, f.m.CreatedAt); err != nil {
				return false, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("catalogpull: reseal: commit: %w", err)
	}
	return true, nil
}

// holdWithheld settles a row's withheld fields after an accepted or conflict
// answer to ch, before the answer's rows apply: a withheld field the base
// lacks (a create's new link) takes the answer row's value as its base, and
// the row stays held (keeping the mark's created_at) while a withheld field's
// local value differs from its base, so neither the pull nor the answer's own
// rows overwrite it. A row with none left is no longer held.
func (r *runner) holdWithheld(ctx context.Context, tx *sql.Tx, m data.OutboxMark, ch Change, a Answer) error {
	wh := r.accepted.withheldOf(ch.Kind)
	if len(wh) == 0 || ch.Op == opDelete {
		return nil
	}
	st, ok, err := r.links.State(ctx, tx, ch.Kind, ch.CloudID)
	if err != nil || !ok {
		return err
	}
	base := parseBase(st.Base)
	if base == nil {
		base = values{}
	}
	filled := false
	for i := range a.Current {
		row := a.Current[i]
		if row.Kind != ch.Kind || row.Deleted {
			continue
		}
		if id, err := canonicalID(row.CloudID); err != nil || id != ch.CloudID {
			continue
		}
		f, err := decodeFields(row.Data)
		if err != nil {
			continue
		}
		fill := baseFromRow(ch.Kind, f, nil)
		for _, name := range wh {
			if _, has := base[name]; has || name == "barcodes" {
				continue
			}
			if v, present := fill[name]; present {
				base[name] = v
				filled = true
			}
		}
	}
	if filled {
		if err := r.links.SetBase(ctx, tx, ch.Kind, ch.CloudID, base.encode()); err != nil {
			return err
		}
	}
	l, err := r.readLocal(ctx, tx, ch.Kind, m.LocalID, base)
	if err != nil {
		return err
	}
	if l.exists {
		if _, held := r.accepted.split(ch.Kind, changedFields(ch.Kind, l, base)); len(held) > 0 {
			return r.outbox.HoldMark(ctx, tx, ch.Kind, m.LocalID, m.CreatedAt)
		}
	}
	return r.outbox.DeleteHeld(ctx, tx, ch.Kind, m.LocalID)
}
