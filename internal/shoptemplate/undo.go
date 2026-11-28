package shoptemplate

import (
	"encoding/json"
	"time"
)

// UndoKey is the shop-wide setting holding the one undo record for the last
// shop-type change (a single row, so it is bounded by construction).
const UndoKey = "shop.template_undo"

// UndoWindow is how long a type change can be undone.
const UndoWindow = 7 * 24 * time.Hour

// Undo is what a confirmed type change needs to be reversed: the previous
// type and ledger, and for each value the change wrote, what it wrote and
// what was there before. Undo only restores a key whose current value still
// equals Applied[key] (an owner edit since then wins).
type Undo struct {
	ToType     string            `json:"to_type"`
	PrevType   string            `json:"prev_type"`
	PrevLedger string            `json:"prev_ledger"`
	Applied    map[string]string `json:"applied"`
	Prev       map[string]string `json:"prev"`
	ExpiresAt  string            `json:"expires_at"`
	// Dismissed hides the banner only; the record, and so Undo (reachable
	// from the Shop type card), stays until it expires (ut-docs#7193).
	Dismissed bool `json:"dismissed,omitempty"`
}

// NewUndo builds the record for a change made at `at`.
func NewUndo(toType, prevType, prevLedger string, applied, prev map[string]string, at time.Time) Undo {
	return Undo{
		ToType: toType, PrevType: prevType, PrevLedger: prevLedger,
		Applied: applied, Prev: prev,
		ExpiresAt: at.UTC().Add(UndoWindow).Format(time.RFC3339),
	}
}

// Encode is the stored JSON.
func (u Undo) Encode() string {
	b, _ := json.Marshal(u)
	return string(b)
}

// ParseUndo reads the stored record; ok is false for no record, garbage or
// an expired one (expiry is checked at read, there is no timer).
func ParseUndo(raw string, now time.Time) (Undo, bool) {
	if raw == "" {
		return Undo{}, false
	}
	var u Undo
	if err := json.Unmarshal([]byte(raw), &u); err != nil || u.ToType == "" {
		return Undo{}, false
	}
	exp, err := time.Parse(time.RFC3339, u.ExpiresAt)
	if err != nil || !now.Before(exp) {
		return Undo{}, false
	}
	return u, true
}
