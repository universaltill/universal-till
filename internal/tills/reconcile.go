// Package tills holds the pure logic behind the one "Tills & devices" page
// (ut-docs#7057): the merge of the two lists that used to be shown as if
// they were one. It does no I/O, never deletes and never re-registers
// anything (ADR-0116 tombstones); an unavailable account list never marks
// a till stale (ADR-0003, offline-first).
package tills

import (
	"sort"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
)

// Roles as the page names them. The local roster only ever holds
// additional and satellite tills; the main till is the one serving it.
const (
	RoleMain       = "main"
	RoleAdditional = data.TillRoleAdditional
	RoleSatellite  = data.TillRoleSatellite
)

// MaxRows bounds the merged list: one row per local till, the cloud list
// capped the same way as the fleet it came from.
const MaxRows = 500

// State is where a till stands between the shop network and the account.
type State string

const (
	// StateOK: in both lists, or on this network while the account list is
	// unavailable (never stale then).
	StateOK State = "ok"
	// StateLocalOnly: enrolled here, absent from the account list (stale).
	StateLocalOnly State = "local_only"
	// StateCloudOnly: in the account, never added on this network.
	StateCloudOnly State = "cloud_only"
	// StateRoleMismatch: both lists know it, with different roles.
	StateRoleMismatch State = "role_mismatch"
	// StateReplaced: this till (the old main) was replaced.
	StateReplaced State = "replaced"
)

// Action is something a manager can do to a row from the list.
type Action string

// ActionRemove revokes the local enrolment (POST /api/sync/tills/{id}/revoke).
const ActionRemove Action = "remove"

// SelfInfo is this till.
type SelfInfo struct{ ID, Name, Role string }

// Source is everything Merge needs, already read by the caller.
type Source struct {
	Cloud    []enroll.Device
	CloudOK  bool // the account list was fetched; false means unknown, not empty
	Local    []data.TillRow
	Self     SelfInfo
	Replaced *enroll.Replaced
}

// Row is one till in the merged list.
type Row struct {
	ID, Name, Role, CloudRole, LastSeen string
	IsSelf                              bool
	InCloud, InLocal                    bool
	State                               State
	// LongUnseen: a stale row not seen within the stale-after window; the
	// page then says "safe to remove", otherwise "check before removing".
	LongUnseen bool
	// SameNameOtherID: another row carries the same name under another id.
	SameNameOtherID bool
	ReplacedBy      string
	Actions         []Action
}

// NormRole maps the account's role words (primary/replica) and the local
// ones (main/additional/satellite) onto the page's three.
func NormRole(r string) string {
	switch strings.ToLower(strings.TrimSpace(r)) {
	case "primary", "main":
		return RoleMain
	case "replica", "additional":
		return RoleAdditional
	case "satellite":
		return RoleSatellite
	}
	return strings.ToLower(strings.TrimSpace(r))
}

// Merge joins the lists on the till id (the account's till_id field, not
// its device id: the two differ). A cloud row without a till_id cannot be
// matched to anything, so while one exists no local till is marked stale
// (the list is ambiguous, not proof of absence). Names are only used to
// flag a same-name different-id pair.
// Order is deterministic: this till, the main till, then by name.
func Merge(s Source, now time.Time, staleAfter time.Duration) []Row {
	byID := map[string]*Row{}
	var order []string
	add := func(r *Row) {
		if _, dup := byID[r.ID]; dup || len(byID) >= MaxRows {
			return
		}
		byID[r.ID] = r
		order = append(order, r.ID)
	}
	cloudByID := map[string]enroll.Device{}
	cloudUnmatchable := false // a cloud row we cannot join to a local till
	for _, c := range s.Cloud {
		if c.TillID == "" {
			cloudUnmatchable = true
			continue
		}
		if _, dup := cloudByID[c.TillID]; !dup {
			c.Role = NormRole(c.Role)
			cloudByID[c.TillID] = c
		}
	}
	if s.Self.ID != "" {
		r := &Row{ID: s.Self.ID, Name: s.Self.Name, Role: s.Self.Role, IsSelf: true, InLocal: true, State: StateOK}
		if c, ok := cloudByID[s.Self.ID]; ok {
			r.InCloud, r.CloudRole = true, c.Role
		}
		if s.Replaced != nil {
			r.State, r.ReplacedBy = StateReplaced, s.Replaced.By
		}
		add(r)
	}
	for _, l := range s.Local {
		if l.ID == s.Self.ID {
			continue
		}
		r := &Row{ID: l.ID, Name: l.Name, Role: l.Role, LastSeen: l.LastSeenAt, InLocal: true, State: StateOK}
		if c, ok := cloudByID[l.ID]; ok {
			r.InCloud, r.CloudRole = true, c.Role
			if c.Role != "" && c.Role != NormRole(l.Role) {
				r.State = StateRoleMismatch
			}
		} else if s.CloudOK && !cloudUnmatchable {
			r.State = StateLocalOnly
			// A till that never connected counts from when it was enrolled, so a
			// tablet added minutes ago is not "long unseen".
			seen := l.LastSeenAt
			if seen == "" {
				seen = l.EnrolledAt
			}
			r.LongUnseen = unseenFor(seen, now) >= staleAfter
			r.Actions = []Action{ActionRemove}
		}
		add(r)
	}
	if s.CloudOK {
		for _, c := range s.Cloud {
			if c.TillID == "" {
				continue
			}
			if _, have := byID[c.TillID]; have {
				continue
			}
			c.Role = NormRole(c.Role)
			add(&Row{ID: c.TillID, Name: c.Name, Role: c.Role, CloudRole: c.Role, LastSeen: c.LastSeen,
				InCloud: true, State: StateCloudOnly})
		}
	}
	rows := make([]Row, 0, len(order))
	names := map[string][]string{}
	for _, id := range order {
		names[strings.ToLower(strings.TrimSpace(byID[id].Name))] = append(names[strings.ToLower(strings.TrimSpace(byID[id].Name))], id)
	}
	for _, ids := range names {
		if len(ids) > 1 {
			for _, id := range ids {
				byID[id].SameNameOtherID = true
			}
		}
	}
	for _, id := range order {
		rows = append(rows, *byID[id])
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.IsSelf != b.IsSelf {
			return a.IsSelf
		}
		if (a.Role == RoleMain) != (b.Role == RoleMain) {
			return a.Role == RoleMain
		}
		an, bn := strings.ToLower(a.Name), strings.ToLower(b.Name)
		if an != bn {
			return an < bn
		}
		return a.ID < b.ID
	})
	return rows
}

// unseenFor is how long ago a till was last seen; a till never seen counts
// as unseen for a very long time.
func unseenFor(lastSeen string, now time.Time) time.Duration {
	t, err := time.Parse(time.RFC3339, lastSeen)
	if err != nil {
		// SQLite's datetime('now') default, which enrolled_at carries (UTC).
		t, err = time.Parse("2006-01-02 15:04:05", lastSeen)
	}
	if err != nil {
		return 1<<63 - 1
	}
	return now.Sub(t)
}
