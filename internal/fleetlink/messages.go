package fleetlink

import (
	"encoding/json"
	"regexp"
	"slices"
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
	// CloudDeviceID is, on a replica's hello, its OWN cloud device id
	// (enroll.CurrentStatus().DeviceID, ut-docs#2730's per-till identity) —
	// separate from TillID, the LAN pairing id (the main till's tills-table
	// row / this replica's sync.till_id). ut-docs#2897: the main till's
	// status frame to the cloud sends both, so my.'s Tills rows and Live
	// panel (keyed by cloud device id) can name a satellite/replica instead
	// of showing its raw pairing id. Omitempty: an older replica's hello has
	// no such field, and decodes with this simply empty (Go's
	// encoding/json). Display-only on my. — never used for auth.
	CloudDeviceID string `json:"cloud_device_id,omitempty"`
}

// cloudDeviceIDPattern bounds the charset a hello's cloud_device_id may use.
// It is untrusted LAN input, kept only for display on my.'s Tills rows and
// Live panel: a value that isn't this shape (an enroll device id is
// "till-<uuid>", or an operator's explicit UT_MARKETPLACE_DEVICE_ID) is
// dropped rather than risk odd bytes reaching that page.
var cloudDeviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

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
	h.CloudDeviceID = clip(h.CloudDeviceID, maxReportField)
	if !cloudDeviceIDPattern.MatchString(h.CloudDeviceID) {
		h.CloudDeviceID = "" // garbage charset: dropped, not just truncated
	}
}

// SyncPayload is a coalesced nudge: every scope that changed since the last
// frame, once each, in allScopes order.
type SyncPayload struct {
	Scopes []Scope `json:"scopes"`
}

// CloudCheckinPayload is a cloud_checkin frame (main → replica,
// ut-docs#2893): "check in with the cloud now", relayed from the main
// till's cloud-link nudge. It carries only what changed (the nudge's
// scopes) and the cloud's link_version; the replica's own authenticated
// check-in fetches everything else (ADR-0114 §7: frames can do little).
type CloudCheckinPayload struct {
	Scopes      []string `json:"scopes"`
	LinkVersion int64    `json:"link_version"`
}

// maxCheckinScopes bounds a pending cloud_checkin's scope list (the cloud
// defines five today).
const maxCheckinScopes = 8

// merge adds scopes (clipped, deduplicated, at most maxCheckinScopes, in
// arrival order) and keeps the newest link_version.
func (c *CloudCheckinPayload) merge(scopes []string, linkVersion int64) {
	for _, s := range scopes {
		s = clip(s, maxReportField)
		if s == "" || len(c.Scopes) >= maxCheckinScopes || slices.Contains(c.Scopes, s) {
			continue
		}
		c.Scopes = append(c.Scopes, s)
	}
	c.LinkVersion = max(c.LinkVersion, linkVersion)
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
