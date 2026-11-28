// Package registeraudit classifies registers for the ghost audit (ut-docs#7098,
// epic #7097). Pure: no I/O, no SQL (the facts come from
// data.AuditRegisters). The Registers page, the optional auto-retire sweep and
// cmd/register-audit all call Classify, so they cannot disagree.
package registeraudit

import (
	"fmt"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// States. The audit prints old_entry and superseded together as
// ghost_candidate (see GhostCandidate); the page shows them as they are.
const (
	StateSelf       = "self"       // this till's own register
	StateLive       = "live"       // its till is enrolled and not an old entry
	StateOldEntry   = "old_entry"  // its till is gone or no longer in the account
	StateSuperseded = "superseded" // a newer device took the till's place
	StateUnknown    = "unknown"    // no link and no till: labelled, never offered "safe"
	StateRetired    = "retired"    // already inactive
)

// StateGhostCandidate is the audit's combined word for old_entry and superseded.
const StateGhostCandidate = "ghost_candidate"

// DefaultQuietDays is how long a register must have had no sales before the
// audit calls its retire safe (the same as tills.OldEntryAfter, 30 days).
const DefaultQuietDays = 30

// Context is what Classify needs besides the row.
type Context struct {
	Now         time.Time
	QuietDays   int // 0 = DefaultQuietDays
	ActiveCount int // active registers in total
	// TillGone: the account says the till is gone or it is an old entry
	// (tills.Merge StateOldEntry). False when the cloud list is unknown:
	// unknown is never treated as gone.
	TillGone bool
}

// Result is the verdict for one register.
type Result struct {
	State string
	// RetireSafe: a manager may retire it with one tap without losing
	// anything: not self, not the last active, no unsent sales, no open
	// shift, and superseded or quiet for QuietDays.
	RetireSafe bool
	// AutoRetireOK: RetireSafe AND the device id is known. Only this may be
	// retired by the optional sweep.
	AutoRetireOK bool
	Evidence     string
}

// GhostCandidate reports whether the register is an old entry or superseded.
func (r Result) GhostCandidate() bool {
	return r.State == StateOldEntry || r.State == StateSuperseded
}

// AuditState is the state word the audit prints.
func (r Result) AuditState() string {
	if r.GhostCandidate() {
		return StateGhostCandidate
	}
	return r.State
}

// Classify decides one register's state and whether retiring it is safe.
func Classify(row data.RegisterAuditRow, c Context) Result {
	days := c.QuietDays
	if days <= 0 {
		days = DefaultQuietDays
	}
	switch {
	case row.IsSelf:
		return Result{State: StateSelf, Evidence: "this till's own register"}
	case !row.IsActive:
		ev := "already retired"
		if row.RetiredReason != "" {
			ev += " (" + row.RetiredReason + ")"
		}
		return Result{State: StateRetired, Evidence: ev}
	}
	var res Result
	switch {
	case row.TillExists && !c.TillGone:
		return Result{State: StateLive, Evidence: "its till is enrolled and in the account"}
	case !row.HasLink && row.TillID == "", row.DeviceID == "" && row.TillID == "":
		return Result{State: StateUnknown, Evidence: "no link to a till or device"}
	case row.SupersededBy != "":
		res.State = StateSuperseded
		res.Evidence = "replaced by device " + row.SupersededBy
	case !row.TillExists:
		res.State = StateOldEntry
		res.Evidence = "its till was removed"
	default:
		res.State = StateOldEntry
		res.Evidence = "its till is no longer in the account"
	}

	// Why it may not be safe, strongest reason first.
	switch {
	case c.ActiveCount <= 1:
		res.Evidence += "; last active register"
		return res
	case row.UnsentSales > 0:
		res.Evidence += fmt.Sprintf("; %d unsent sales", row.UnsentSales)
		return res
	case row.OpenShift:
		res.Evidence += "; open shift"
		return res
	}
	if res.State != StateSuperseded {
		ref := row.LastSale
		what := "last sale"
		if ref.IsZero() {
			ref, what = row.TillEnrolledAt, "enrolment"
		}
		if !ref.IsZero() && c.Now.Sub(ref) <= time.Duration(days)*24*time.Hour {
			res.Evidence += fmt.Sprintf("; %s %d days ago, under %d", what, int(c.Now.Sub(ref).Hours()/24), days)
			return res
		}
	}
	res.RetireSafe = true
	res.AutoRetireOK = row.DeviceID != ""
	if !res.AutoRetireOK {
		res.Evidence += "; device id unknown, never auto-retired"
	}
	return res
}
