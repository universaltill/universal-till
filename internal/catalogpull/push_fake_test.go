package catalogpull

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/data"
)

// fakeCloud is an httptest stand-in for ut-cloud's POST
// /api/v1/stores/catalog/changes (contract cloud-catalog-changes.md
// 1.0.0) over the SAME rows the fake feed serves, so an accepted change is
// a new feed version (the echo) and a conflict mints none. It implements
// the contract's value-based test (internal/catalogchanges is the real one):
// every changed field at before or already at after → write the ones at
// before; any third value → conflict value; an update of a missing row →
// conflict delete; idempotent on change_id.
type fakeCloud struct {
	mu   sync.Mutex
	feed *fakeFeed

	// status/code: every call answers this error.
	status int
	code   string
	// failAfterApply answers the next call 503 AFTER applying it (a lost
	// answer): the till re-sends the same change_ids.
	failAfterApply bool
	// reject answers these cloud_ids rejected cloud_id_unavailable.
	reject map[string]bool
	// retry answers these cloud_ids retry.
	retry map[string]bool
	// duplicateNames answers a create of an item with one of these names
	// conflict duplicate (barcode_taken).
	duplicateNames map[string]bool
	// rejectNextCreate answers the next create rejected
	// cloud_id_unavailable.
	rejectNextCreate bool

	// index of feed.rows, kept current incrementally (rows only grow).
	indexed int
	last    map[string]int             // kind:cloud_id → index of its latest row
	byItem  map[string]map[string]bool // item cloud_id → its barcode cloud_ids

	receipts map[string]Answer
	calls    [][]Change
}

func newFakeCloud(feed *fakeFeed) *fakeCloud {
	return &fakeCloud{feed: feed, receipts: map[string]Answer{}, reject: map[string]bool{}, retry: map[string]bool{}, duplicateNames: map[string]bool{}}
}

func (c *fakeCloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if r.Header.Get("Authorization") != "Bearer tok-1" {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		StoreID string   `json:"store_id"`
		Changes []Change `json:"changes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.StoreID != "store-1" || len(req.Changes) == 0 || len(req.Changes) > MaxChangesPerCall {
		writeErr(w, http.StatusBadRequest, "invalid_request")
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, req.Changes)
	if c.status != 0 {
		writeErr(w, c.status, c.code)
		return
	}
	c.feed.mu.Lock()
	defer c.feed.mu.Unlock()
	out := make([]Answer, 0, len(req.Changes))
	for _, ch := range req.Changes {
		a, seen := c.receipts[ch.ChangeID]
		if !seen {
			a = c.decide(ch)
			if a.Status == statusAccepted || a.Status == statusConflict {
				c.receipts[ch.ChangeID] = a
			}
		}
		if a.Status == statusAccepted || a.Status == statusConflict {
			a.Current = c.current(ch.Kind, ch.CloudID)
		}
		out = append(out, a)
	}
	if c.failAfterApply {
		c.failAfterApply = false
		writeErr(w, http.StatusServiceUnavailable, "changes_unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"results": out}, "error": nil})
}

// reindex brings the row index up to date (rows are only appended while
// a test uploads; a shrink rebuilds it).
func (c *fakeCloud) reindex() {
	if c.last == nil || len(c.feed.rows) < c.indexed {
		c.last, c.byItem, c.indexed = map[string]int{}, map[string]map[string]bool{}, 0
	}
	for i := c.indexed; i < len(c.feed.rows); i++ {
		r := c.feed.rows[i]
		c.last[r.Kind+":"+r.CloudID] = i
		if r.Kind == "item_barcode" && !r.Deleted {
			if item, ok := r.Data["item_cloud_id"].(string); ok {
				if c.byItem[item] == nil {
					c.byItem[item] = map[string]bool{}
				}
				c.byItem[item][r.CloudID] = true
			}
		}
	}
	c.indexed = len(c.feed.rows)
}

// latest returns the latest row of (kind, id), or nil.
func (c *fakeCloud) latest(kind, id string) *feedRow {
	c.reindex()
	i, ok := c.last[kind+":"+id]
	if !ok {
		return nil
	}
	return &c.feed.rows[i]
}

// itemBarcodes returns an item's live item-level barcode rows, primary
// first.
func (c *fakeCloud) itemBarcodes(item string) []feedRow {
	c.reindex()
	var ids []string
	for id := range c.byItem[item] {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []feedRow
	for _, id := range ids {
		r := c.feed.rows[c.last["item_barcode:"+id]]
		if r.Deleted || r.Data["item_cloud_id"] != item || r.Data["variant_cloud_id"] != nil {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi, _ := out[i].Data["is_primary"].(bool)
		pj, _ := out[j].Data["is_primary"].(bool)
		return pi && !pj
	})
	return out
}

func (c *fakeCloud) current(kind, id string) []Row {
	r := c.latest(kind, id)
	if r == nil || r.Deleted {
		return []Row{}
	}
	rows := []feedRow{*r}
	if kind == "item" {
		rows = append(rows, c.itemBarcodes(id)...)
	}
	out := make([]Row, len(rows))
	for i, fr := range rows {
		raw, _ := json.Marshal(fr.Data)
		out[i] = Row{Kind: fr.Kind, CloudID: fr.CloudID, Version: fr.Version, Data: raw}
	}
	return out
}

// value is the cloud's current value of field, normalised for comparison.
func (c *fakeCloud) value(kind, id, field string) string {
	if field == "barcodes" {
		var codes []string
		for _, b := range c.itemBarcodes(id) {
			codes = append(codes, b.Data["barcode"].(string))
		}
		return codesKey(codes)
	}
	r := c.latest(kind, id)
	if r == nil {
		return "null"
	}
	return norm(rawJSON(r.Data[field]))
}

func codesKey(codes []string) string {
	if len(codes) == 0 {
		return "[]"
	}
	rest := append([]string(nil), codes[1:]...)
	sort.Strings(rest)
	return strings.Join(append([]string{codes[0]}, rest...), ",")
}

func changeValue(field string, raw json.RawMessage) string {
	if field != "barcodes" {
		return norm(raw)
	}
	var list []struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(raw, &list)
	codes := make([]string, len(list))
	for i, b := range list {
		codes[i] = b.Code
	}
	return codesKey(codes)
}

func (c *fakeCloud) decide(ch Change) Answer {
	a := Answer{ChangeID: ch.ChangeID}
	switch {
	case c.reject[ch.CloudID]:
		a.Status, a.Code = statusRejected, "cloud_id_unavailable"
		return a
	case c.retry[ch.CloudID]:
		a.Status = "retry"
		return a
	}
	row := c.latest(ch.Kind, ch.CloudID)
	live := row != nil && !row.Deleted
	conflict := func(kind string) Answer {
		a.Status, a.Conflict = statusConflict, kind
		if live {
			a.Version = row.Version
		}
		return a
	}
	if ch.Stale {
		return conflict("stale")
	}
	switch ch.Op {
	case opCreate:
		if live {
			// As the cloud (evaluate.go): idempotent when the live row
			// holds the till's values, else a value conflict.
			for _, f := range ch.ChangedFields {
				if c.value(ch.Kind, ch.CloudID, f) != changeValue(f, ch.After[f]) {
					return conflict("value")
				}
			}
			a.Status, a.Version = statusAccepted, row.Version
			return a
		}
		var name string
		_ = json.Unmarshal(ch.After["name"], &name)
		if c.duplicateNames[name] {
			a.Status, a.Conflict, a.Code = statusConflict, "duplicate", "barcode_taken"
			return a
		}
		if c.rejectNextCreate {
			c.rejectNextCreate = false
			a.Status, a.Code = statusRejected, "cloud_id_unavailable"
			return a
		}
		return c.write(ch, ch.ChangedFields)
	case opDelete:
		if !live {
			a.Status = statusAccepted
			return a
		}
		for f, b := range ch.Before {
			if c.value(ch.Kind, ch.CloudID, f) != changeValue(f, b) {
				return conflict("delete")
			}
		}
		v := c.feed.high + 1
		// As the cloud (catalogwrite DeleteItem): the item's barcodes go
		// with it.
		for _, b := range c.itemBarcodes(ch.CloudID) {
			c.feed.rows = append(c.feed.rows, feedRow{Kind: "item_barcode", CloudID: b.CloudID, Version: v, Deleted: true})
		}
		c.feed.rows = append(c.feed.rows, feedRow{Kind: ch.Kind, CloudID: ch.CloudID, Version: v, Deleted: true})
		c.feed.high = v
		a.Status, a.Version = statusAccepted, v
		return a
	}
	if !live {
		return conflict("delete")
	}
	var apply []string
	for _, f := range ch.ChangedFields {
		cur := c.value(ch.Kind, ch.CloudID, f)
		switch cur {
		case changeValue(f, ch.After[f]):
		case changeValue(f, ch.Before[f]):
			apply = append(apply, f)
		default:
			return conflict("value")
		}
	}
	if len(apply) == 0 {
		a.Status, a.Version = statusAccepted, row.Version
		return a
	}
	return c.write(ch, apply)
}

// write applies fields of ch at a new version.
func (c *fakeCloud) write(ch Change, fields []string) Answer {
	v := c.feed.high + 1
	var data map[string]any
	if old := c.latest(ch.Kind, ch.CloudID); old != nil && !old.Deleted {
		data = map[string]any{}
		for k, x := range old.Data {
			data[k] = x
		}
	} else {
		switch ch.Kind {
		case "item":
			data = itemRowData("", 0)
			data["sku"] = "GEN-" + ch.CloudID[:4]
		case "category":
			data = catRow(ch.CloudID, 0, "", nil).Data
		default:
			data = map[string]any{"name": "", "rate_basis_points": 0, "takeaway_rate_basis_points": nil, "active": true}
		}
	}
	var barcodes []string
	setBarcodes := false
	for _, f := range fields {
		if f == "barcodes" {
			setBarcodes = true
			var list []struct {
				Code string `json:"code"`
			}
			_ = json.Unmarshal(ch.After[f], &list)
			for _, b := range list {
				barcodes = append(barcodes, b.Code)
			}
			continue
		}
		var x any
		_ = json.Unmarshal(ch.After[f], &x)
		data[f] = x
	}
	c.feed.rows = append(c.feed.rows, feedRow{Kind: ch.Kind, CloudID: ch.CloudID, Version: v, Data: data})
	if setBarcodes {
		have := map[string]feedRow{}
		for _, b := range c.itemBarcodes(ch.CloudID) {
			have[b.Data["barcode"].(string)] = b
		}
		want := map[string]bool{}
		for i, code := range barcodes {
			want[code] = true
			if old, ok := have[code]; ok {
				if p, _ := old.Data["is_primary"].(bool); p == (i == 0) {
					continue
				}
				d := map[string]any{}
				for k, x := range old.Data {
					d[k] = x
				}
				d["is_primary"] = i == 0
				c.feed.rows = append(c.feed.rows, feedRow{Kind: "item_barcode", CloudID: old.CloudID, Version: v, Data: d})
				continue
			}
			c.feed.rows = append(c.feed.rows, feedRow{Kind: "item_barcode", CloudID: uuid.NewString(), Version: v, Data: map[string]any{
				"barcode": code, "item_cloud_id": ch.CloudID, "variant_cloud_id": nil, "barcode_type": "CODE128", "is_primary": i == 0,
			}})
		}
		for code, old := range have {
			if !want[code] {
				c.feed.rows = append(c.feed.rows, feedRow{Kind: "item_barcode", CloudID: old.CloudID, Version: v, Deleted: true})
			}
		}
	}
	c.feed.high = v
	return Answer{ChangeID: ch.ChangeID, Status: statusAccepted, Version: v}
}

// pushHarness is the pull harness with the change endpoint mounted on the
// same fake cloud, on a cloud-master main till.
type pushHarness struct {
	*harness
	cloud *fakeCloud
	post  Poster
}

func newPushHarness(t *testing.T) *pushHarness {
	t.Helper()
	h := newHarness(t)
	h.feed.latestOnly = true
	cloud := newFakeCloud(h.feed)
	mux := http.NewServeMux()
	mux.Handle("/v1/stores/catalog", h.feed)
	mux.Handle("/v1/stores/catalog/changes", cloud)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	h.srv = srv
	h.fetch = NewHTTPFetcher(srv.Client(), srv.URL, "store-1", "tok-1")
	ph := &pushHarness{harness: h, cloud: cloud, post: NewHTTPPoster(srv.Client(), srv.URL, "store-1", "tok-1")}
	ph.exec(`INSERT INTO settings (key, value) VALUES ('entitlement.catalog_master', 'cloud')`)
	return ph
}

func (h *pushHarness) exec(q string, args ...any) {
	h.t.Helper()
	if _, err := h.db.Exec(q, args...); err != nil {
		h.t.Fatalf("%s: %v", q, err)
	}
}

func (h *pushHarness) push() PushResult {
	h.t.Helper()
	res, err := Push(context.Background(), h.db, h.post, h.opt)
	if err != nil {
		h.t.Fatalf("Push: %v", err)
	}
	return res
}

// tick is one cloudsync tick: upload first, then pull (ADR-0162 §4).
func (h *pushHarness) tick() (PushResult, Result) {
	h.t.Helper()
	p := h.push()
	return p, h.mustRun()
}

func (h *pushHarness) marks() int {
	h.t.Helper()
	open, sealed, err := data.NewCloudOutboxRepo().Counts(context.Background(), h.db)
	if err != nil {
		h.t.Fatal(err)
	}
	return open + sealed
}

// cloudRow is the cloud's latest row of (kind, id).
func (h *pushHarness) cloudRow(kind, id string) *feedRow {
	h.cloud.mu.Lock()
	defer h.cloud.mu.Unlock()
	h.feed.mu.Lock()
	defer h.feed.mu.Unlock()
	r := h.cloud.latest(kind, id)
	if r == nil {
		return nil
	}
	cp := *r
	return &cp
}

// sentChanges flattens every call's changes.
func (h *pushHarness) sentChanges() []Change {
	h.cloud.mu.Lock()
	defer h.cloud.mu.Unlock()
	var out []Change
	for _, c := range h.cloud.calls {
		out = append(out, c...)
	}
	return out
}

// myEdit is an edit made in my. (a new cloud version of the row).
func (h *pushHarness) myEdit(kind, id string, set map[string]any) int64 {
	h.t.Helper()
	h.feed.mu.Lock()
	defer h.feed.mu.Unlock()
	var old *feedRow
	for i := range h.feed.rows {
		if h.feed.rows[i].Kind == kind && h.feed.rows[i].CloudID == id {
			old = &h.feed.rows[i]
		}
	}
	d := map[string]any{}
	for k, x := range old.Data {
		d[k] = x
	}
	for k, x := range set {
		d[k] = x
	}
	v := h.feed.high + 1
	h.feed.rows = append(h.feed.rows, feedRow{Kind: kind, CloudID: id, Version: v, Data: d})
	h.feed.high = v
	return v
}
