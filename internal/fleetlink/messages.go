package fleetlink

import (
	"encoding/json"
	"strings"
	"time"
)

// SyncProtocolLevel is this build's LAN-sync journal contract level, sent in
// hello (ADR-0114 §2). #2726/§5 compare it to hold a newer replica's push
// against an older main till; bump it only with a journal wire change.
const SyncProtocolLevel = 1

// Scope names one area a `sync` nudge says has changed (ADR-0114 §2). Each
// maps to an existing idempotent HTTP pull; the link never carries the data.
type Scope string

const (
	// ScopeAdmin: catalogue, settings, users, roles and PINs (#2731) — the
	// GET /api/sync/admin bundle.
	ScopeAdmin     Scope = "admin"
	ScopePlugins   Scope = "plugins"
	ScopeStock     Scope = "stock"
	ScopeHeldSales Scope = "held_sales"
	ScopeTables    Scope = "tables"
	ScopeOrders    Scope = "orders"
)

// allScopes fixes both the bit each scope owns in a peer's dirty mask and
// the order scopes are listed in a sync frame.
var allScopes = []Scope{ScopeAdmin, ScopePlugins, ScopeStock, ScopeHeldSales, ScopeTables, ScopeOrders}

func scopeBit(s Scope) uint32 {
	for i, v := range allScopes {
		if v == s {
			return 1 << uint(i)
		}
	}
	return 0
}

func scopesOf(mask uint32) []Scope {
	out := make([]Scope, 0, len(allScopes))
	for i, s := range allScopes {
		if mask&(1<<uint(i)) != 0 {
			out = append(out, s)
		}
	}
	return out
}

// Cursors are the pull fingerprints a replica compares against its own
// sync.*_version settings to decide whether to pull after (re)connect.
type Cursors struct {
	Admin   string `json:"admin"`
	Plugins string `json:"plugins"`
	Stock   string `json:"stock_version"`
}

// Hello is the first frame each way (ADR-0114 §2). Update policy, fleet
// target and TLS pin state are placeholders here: #2726 (§5) and #2736
// (§7) fill them; null means "not offered by this build".
type Hello struct {
	TillID       string  `json:"till_id"`
	Role         string  `json:"role"` // "main" | "replica" | "satellite"
	Version      string  `json:"version"`
	Platform     string  `json:"platform"`
	SyncProtocol int     `json:"sync_protocol"`
	Cursors      Cursors `json:"cursors"`
	UpdatePolicy *string `json:"update_policy"`
	FleetTarget  *string `json:"fleet_target"`
	TLSPin       *string `json:"tls_pin"`
	// PeerTillID is, on the main till's hello, the id this link is
	// authenticated as — lets the dialler confirm who the main till thinks
	// it is.
	PeerTillID string `json:"peer_till_id,omitempty"`
}

// clipStrings bounds every string a peer's hello can carry before it is
// kept for the link's lifetime.
func (h *Hello) clipStrings() {
	for _, s := range []*string{&h.TillID, &h.Role, &h.Version, &h.Platform, &h.PeerTillID,
		&h.Cursors.Admin, &h.Cursors.Plugins, &h.Cursors.Stock} {
		*s = clip(*s, maxReportField)
	}
	for _, p := range []**string{&h.UpdatePolicy, &h.FleetTarget, &h.TLSPin} {
		if *p != nil {
			v := clip(**p, maxReportField)
			*p = &v
		}
	}
}

// SyncPayload is a coalesced nudge: every scope that changed since the last
// frame, once each, in allScopes order.
type SyncPayload struct {
	Scopes []Scope `json:"scopes"`
}

// ByePayload says why a side is closing (ADR-0114 §2).
type ByePayload struct {
	Reason string `json:"reason"` // shutdown | updating | restarting
}

// Bye reasons.
const (
	ByeShutdown   = "shutdown"
	ByeUpdating   = "updating"
	ByeRestarting = "restarting"
)

// Report is a peer's periodic state (ADR-0114 §2 → main). Kept in memory
// only, latest per till, for the Tills page (#… §10) and the fleet-update
// cards; strings are clipped so a peer can't grow the store.
type Report struct {
	Version        string    `json:"version"`
	UpdateState    string    `json:"update_state"`
	PushQueueDepth int       `json:"push_queue_depth"`
	TLSPinned      bool      `json:"tls_pinned"`
	ReceivedAt     time.Time `json:"-"`
}

const maxReportField = 64

func decodeReport(raw json.RawMessage) (Report, bool) {
	var r Report
	if len(raw) == 0 || json.Unmarshal(raw, &r) != nil {
		return Report{}, false
	}
	r.Version = clip(r.Version, maxReportField)
	r.UpdateState = clip(r.UpdateState, maxReportField)
	if r.PushQueueDepth < 0 {
		r.PushQueueDepth = 0
	}
	return r, true
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}
