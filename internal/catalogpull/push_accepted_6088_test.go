package catalogpull

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#6088: a till that knows more upload fields than its cloud accepts
// must keep uploading to it. Today uploadFields == fields100, so these tests
// make a "newer" field by narrowing fields100 for the test: description is a
// real column with a real local value, so every path runs end to end without
// the fields of ut-docs#6082.

// withFields100 narrows kind's frozen 1.0.0 list for the test, so the dropped
// fields stand in for fields a newer contract minor added.
func withFields100(t *testing.T, kind string, without ...string) {
	t.Helper()
	old := fields100[kind]
	var kept []string
	for _, f := range old {
		if !slices.Contains(without, f) {
			kept = append(kept, f)
		}
	}
	fields100[kind] = kept
	t.Cleanup(func() { fields100[kind] = old })
}

// cloudLists are the lists a cloud advertises and enforces: the (narrowed)
// 1.0.0 lists plus extra item fields.
func cloudLists(extraItem ...string) map[string][]string {
	out := map[string][]string{}
	for k, v := range fields100 {
		out[k] = slices.Clone(v)
	}
	out[kindItem] = append(out[kindItem], extraItem...)
	return out
}

// advertise is cloudLists as the cached AcceptedFields value.
func advertise(extraItem ...string) string {
	raw, _ := json.Marshal(cloudLists(extraItem...))
	return string(raw)
}

// strictCloud makes the fake cloud enforce cloudLists(extraItem...) and the
// till learn it (extraItem empty = a 1.0.0 cloud, which advertises nothing).
func (h *pushHarness) strictCloud(advertised bool, extraItem ...string) {
	h.cloud.mu.Lock()
	h.cloud.accept = cloudLists(extraItem...)
	h.cloud.mu.Unlock()
	h.opt.AcceptedFields = ""
	if advertised {
		h.opt.AcceptedFields = advertise(extraItem...)
	}
}

func (h *pushHarness) refusedCalls() int {
	h.cloud.mu.Lock()
	defer h.cloud.mu.Unlock()
	return h.cloud.refused
}

func (h *pushHarness) heldAt(kind, id string) (string, bool) {
	h.t.Helper()
	var at string
	err := h.db.QueryRow(`SELECT created_at FROM cloud_outbox_held WHERE entity = ? AND local_id = ?`, kind, id).Scan(&at)
	return at, err == nil
}

func (h *pushHarness) baseOf(kind, cloudID string) values {
	h.t.Helper()
	st, ok, err := data.NewCloudLinkRepo().State(context.Background(), h.db, kind, cloudID)
	if err != nil || !ok {
		h.t.Fatalf("link %s %s: %v %v", kind, cloudID, ok, err)
	}
	return parseBase(st.Base)
}

// mentions reports whether any sent change names field.
func mentions(chs []Change, field string) bool {
	for _, ch := range chs {
		if slices.Contains(ch.ChangedFields, field) {
			return true
		}
		if _, ok := ch.Before[field]; ok {
			return true
		}
		if _, ok := ch.After[field]; ok {
			return true
		}
	}
	return false
}

func TestFields100Frozen(t *testing.T) {
	want := map[string][]string{
		kindItem:     {"name", "description", "sku", "price_minor", "category_cloud_id", "tax_code_cloud_id", "active", "barcodes"},
		kindCategory: {"name", "parent_cloud_id", "sort_order", "active"},
		kindTaxCode:  {"name", "rate_basis_points", "takeaway_rate_basis_points", "active"},
	}
	if len(fields100) != len(want) {
		t.Fatalf("fields100 has %d kinds, want %d", len(fields100), len(want))
	}
	for kind, w := range want {
		if !slices.Equal(fields100[kind], w) {
			t.Errorf("fields100[%s] = %v, want the frozen 1.0.0 list %v", kind, fields100[kind], w)
		}
		for _, f := range fields100[kind] {
			if !slices.Contains(uploadFields[kind], f) {
				t.Errorf("fields100[%s] names %q, which uploadFields lacks: a 1.0.0 field must always upload", kind, f)
			}
		}
	}
}

func TestAcceptedFrom(t *testing.T) {
	withFields100(t, kindItem, "description")
	has := func(a acceptedSet, f string) bool { return a.accepts(kindItem, f) }
	for _, tc := range []struct {
		name, raw string
		desc      bool
	}{
		{"empty is 1.0.0", "", false},
		{"garbled is 1.0.0", `{"item": [`, false},
		{"wrong shape is 1.0.0", `["item"]`, false},
		{"kind of the wrong type is 1.0.0", `{"item":"description"}`, false},
		{"unknown field is ignored", `{"item":["zz_unknown"]}`, false},
		{"missing kind gets 1.0.0", `{"category":["name"]}`, false},
		{"advertised field is accepted", `{"item":["description"]}`, true},
		{"1.0.0 fields are never dropped", `{"item":["description"]}`, true},
	} {
		a := acceptedFrom(tc.raw)
		if got := has(a, "description"); got != tc.desc {
			t.Errorf("%s: description accepted = %v, want %v", tc.name, got, tc.desc)
		}
		for _, f := range fields100[kindItem] {
			if !has(a, f) {
				t.Errorf("%s: 1.0.0 field %q dropped", tc.name, f)
			}
		}
		if (len(a.withheldOf(kindItem)) > 0) != !tc.desc {
			t.Errorf("%s: withheld = %v", tc.name, a.withheldOf(kindItem))
		}
	}
	// An advertisement that omits 1.0.0 fields does not drop them either.
	if a := acceptedFrom(`{"item":[]}`); !has(a, "name") || !has(a, "price_minor") {
		t.Error("an empty advertised list dropped 1.0.0 fields")
	}
	if !acceptedFrom("").anyWithheld() {
		t.Error("anyWithheld = false with description narrowed out")
	}
	// The fingerprint ignores order and tells different sets apart.
	a, b := acceptedFrom(`{"item":["description","name"],"category":["name"]}`), acceptedFrom(`{"category":["name"],"item":["name","description"]}`)
	if a.fp != b.fp || a.fp == "" {
		t.Errorf("fingerprint depends on order: %q vs %q", a.fp, b.fp)
	}
	if a.fp == acceptedFrom("").fp {
		t.Error("a widened set has the narrow set's fingerprint")
	}
}

func TestAcceptedFromKeepsPairsWhole(t *testing.T) {
	old := uploadFields[kindItem]
	uploadFields[kindItem] = append(slices.Clone(old), "net_quantity_value", "net_quantity_unit")
	t.Cleanup(func() { uploadFields[kindItem] = old })
	for _, tc := range []struct {
		raw        string
		val, unit  bool
		withheldOf int
	}{
		{"", false, false, 2},
		{`{"item":["net_quantity_value"]}`, false, false, 2},
		{`{"item":["net_quantity_unit"]}`, false, false, 2},
		{`{"item":["net_quantity_value","net_quantity_unit"]}`, true, true, 0},
	} {
		a := acceptedFrom(tc.raw)
		if a.accepts(kindItem, "net_quantity_value") != tc.val || a.accepts(kindItem, "net_quantity_unit") != tc.unit || len(a.withheldOf(kindItem)) != tc.withheldOf {
			t.Errorf("%s: value=%v unit=%v withheld=%v", tc.raw, a.accepts(kindItem, "net_quantity_value"), a.accepts(kindItem, "net_quantity_unit"), a.withheldOf(kindItem))
		}
	}
}

// AC 4: a till that knows a field its cloud does not (no advertisement =
// 1.0.0) uploads an update, a create and a delete without a 400.
func TestPushNewerTillOlderCloudNoStall(t *testing.T) {
	withFields100(t, kindItem, "description")
	h := newPushHarness(t)
	h.strictCloud(false)
	edited := h.seedPulled(1, "Latte", 250)
	gone := h.seedPulled(2, "Tea", 200)
	h.saveItem(data.ItemPatch{ID: edited, PriceMinor: ptrTo(int64(280)), Description: ptrTo("Oat milk")})
	h.saveItem(data.ItemPatch{ID: "till-made-1", Create: true, Name: ptrTo("Scone"), PriceMinor: ptrTo(int64(300)), Description: ptrTo("Warm")})
	if _, err := data.NewPOSRepo(h.db).DeleteUnusedItem(context.Background(), gone); err != nil {
		t.Fatal(err)
	}
	p, err := Push(context.Background(), h.db, h.post, h.opt)
	if err != nil {
		t.Fatalf("Push against a 1.0.0 cloud: %v (a 400 would stall every later change)", err)
	}
	if p.Accepted != 3 || h.refusedCalls() != 0 {
		t.Fatalf("push = %+v, refused calls = %d", p, h.refusedCalls())
	}
	chs := h.sentChanges()
	if len(chs) != 3 || mentions(chs, "description") {
		t.Fatalf("sent %d changes, description named: %+v", len(chs), chs)
	}
	if it, _ := h.item("till-made-1"); it.Desc != "Warm" {
		t.Fatalf("local description = %q", it.Desc)
	}
}

// AC 5: a mark mixing accepted and withheld fields seals the accepted ones.
func TestPushMixedUpdateSealsAcceptedOnly(t *testing.T) {
	withFields100(t, kindItem, "description")
	h := newPushHarness(t)
	h.strictCloud(false)
	id := h.seedPulled(1, "Latte", 250)
	h.saveItem(data.ItemPatch{ID: id, PriceMinor: ptrTo(int64(280)), Description: ptrTo("Oat milk")})
	p := h.push()
	if p.Accepted != 1 {
		t.Fatalf("push = %+v", p)
	}
	ch := onlyChange(t, h.sentChanges())
	if ch.Op != opUpdate || !slices.Equal(ch.ChangedFields, []string{"price_minor"}) || len(ch.Before) != 1 || len(ch.After) != 1 {
		t.Fatalf("change = %+v, want price_minor only", ch)
	}
}

// AC 5: a mark whose only changes are withheld is neither sealed nor
// dropped: it leaves the outbox for the held table with its created_at.
func TestPushWithheldOnlyUpdateIsHeldNotDropped(t *testing.T) {
	withFields100(t, kindItem, "description")
	h := newPushHarness(t)
	h.strictCloud(false)
	id := h.seedPulled(1, "Latte", 250)
	h.saveItem(data.ItemPatch{ID: id, Description: ptrTo("Oat milk")})
	var created string
	if err := h.db.QueryRow(`SELECT created_at FROM cloud_outbox WHERE local_id = ?`, id).Scan(&created); err != nil {
		t.Fatal(err)
	}
	p := h.push()
	if p.Calls != 0 || p.Sent != 0 || p.Dropped != 0 || p.Held != 1 || p.Pending != 1 {
		t.Fatalf("push = %+v, want no call, nothing dropped, 1 held, 1 pending", p)
	}
	if at, ok := h.heldAt(kindItem, id); !ok || at != created {
		t.Fatalf("held = %q %v, want the mark's created_at %q", at, ok, created)
	}
	if h.marks() != 0 {
		t.Fatalf("outbox marks = %d, want 0 (held rows are not in cloud_outbox)", h.marks())
	}
	// A second tick is quiet.
	if p := h.push(); p.Calls != 0 || p.Held != 1 {
		t.Fatalf("second push = %+v", p)
	}
}

// AC 6: a create sends the accepted fields, then the withheld one stays a
// pending edit on the till.
func TestPushCreateSendsAcceptedThenHolds(t *testing.T) {
	withFields100(t, kindItem, "description")
	h := newPushHarness(t)
	h.strictCloud(false)
	h.saveItem(data.ItemPatch{ID: "till-made-1", Create: true, Name: ptrTo("Scone"), PriceMinor: ptrTo(int64(300)), Description: ptrTo("Warm")})
	p, _ := h.tick()
	if p.Accepted != 1 || p.Held != 1 || p.Pending != 1 {
		t.Fatalf("push = %+v", p)
	}
	ch := onlyChange(t, h.sentChanges())
	if ch.Op != opCreate || mentions([]Change{ch}, "description") || ch.After["name"] == nil {
		t.Fatalf("create = %+v", ch)
	}
	if _, ok := h.heldAt(kindItem, "till-made-1"); !ok {
		t.Fatal("the created row's description edit is not held")
	}
	if it, _ := h.item("till-made-1"); it.Desc != "Warm" {
		t.Fatalf("local description after the answer and pull = %q, want Warm", it.Desc)
	}
	if b := h.baseOf(kindItem, ch.CloudID); string(b["description"]) != `""` {
		t.Fatalf("base description = %s, want the cloud's \"\"", b["description"])
	}
}

// AC 7: a delete's before holds only accepted fields.
func TestPushDeleteBeforeHasAcceptedOnly(t *testing.T) {
	withFields100(t, kindItem, "description")
	h := newPushHarness(t)
	h.strictCloud(false)
	gone := h.seedPulled(1, "Tea", 200)
	if _, err := data.NewPOSRepo(h.db).DeleteUnusedItem(context.Background(), gone); err != nil {
		t.Fatal(err)
	}
	p := h.push()
	if p.Accepted != 1 {
		t.Fatalf("push = %+v", p)
	}
	ch := onlyChange(t, h.sentChanges())
	if ch.Op != opDelete || mentions([]Change{ch}, "description") || ch.Before["name"] == nil {
		t.Fatalf("delete = %+v", ch)
	}
}

// heldDescriptionEdit is the AC 8 setup on a 1.0.0 cloud: price and
// description edited, the price accepted, the description held.
func heldDescriptionEdit(t *testing.T) (*pushHarness, string) {
	t.Helper()
	withFields100(t, kindItem, "description")
	h := newPushHarness(t)
	h.strictCloud(false)
	id := h.seedPulled(1, "Latte", 250)
	h.saveItem(data.ItemPatch{ID: id, PriceMinor: ptrTo(int64(280)), Description: ptrTo("Local note")})
	p, _ := h.tick()
	if p.Accepted != 1 || p.Held != 1 {
		t.Fatalf("setup push = %+v", p)
	}
	return h, id
}

// AC 8: after the accepted answer the withheld edit is still on the till,
// and a pull serving the row with another value leaves it and its base alone.
func TestPushWithheldEditSurvivesPull(t *testing.T) {
	h, id := heldDescriptionEdit(t)
	if h.marks() != 0 {
		t.Fatalf("marks = %d", h.marks())
	}
	h.myEdit("item", id, map[string]any{"description": "Cloud text"})
	h.mustRun()
	if it, _ := h.item(id); it.Desc != "Local note" || it.Price != 280 {
		t.Fatalf("local item = %q %d, want the till's description and the accepted price", it.Desc, it.Price)
	}
	if b := h.baseOf(kindItem, id); string(b["description"]) != `""` {
		t.Fatalf("base description = %s, want it untouched", b["description"])
	}
	if _, ok := h.heldAt(kindItem, id); !ok {
		t.Fatal("hold lost")
	}
}

// AC 8 / D6: a conflict on the price applies the cloud's price, and the
// withheld description (never shown to the cloud) stays.
func TestPushWithheldEditSurvivesConflict(t *testing.T) {
	withFields100(t, kindItem, "description")
	h := newPushHarness(t)
	h.strictCloud(false)
	id := h.seedPulled(1, "Latte", 250)
	h.saveItem(data.ItemPatch{ID: id, PriceMinor: ptrTo(int64(280)), Description: ptrTo("Local note")})
	h.myEdit("item", id, map[string]any{"price_minor": 999})
	p := h.push()
	if p.Conflicts != 1 {
		t.Fatalf("push = %+v, want the price to conflict", p)
	}
	if it, _ := h.item(id); it.Price != 999 || it.Desc != "Local note" {
		t.Fatalf("local item = %d %q, want the cloud's price and the till's description", it.Price, it.Desc)
	}
	if _, ok := h.heldAt(kindItem, id); !ok {
		t.Fatal("the description edit is no longer held")
	}
}

// AC 9: once the cloud advertises the field, the held edit uploads as its own
// update with its base as before, with no new operator edit.
func TestPushCatchUpAfterCloudUpgrade(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		h, id := heldDescriptionEdit(t)
		h.strictCloud(true, "description")
		p := h.push()
		if p.Accepted != 1 || p.Held != 0 || p.Pending != 0 {
			t.Fatalf("push = %+v, want the edit accepted and nothing left", p)
		}
		chs := h.sentChanges()
		ch := chs[len(chs)-1]
		if ch.Op != opUpdate || !slices.Equal(ch.ChangedFields, []string{"description"}) ||
			jsonOf(t, ch.Before["description"]) != "" || jsonOf(t, ch.After["description"]) != "Local note" {
			t.Fatalf("catch-up change = %+v", ch)
		}
		if got := h.cloudRow("item", id); got.Data["description"] != "Local note" {
			t.Fatalf("cloud description = %v", got.Data["description"])
		}
		if _, ok := h.heldAt(kindItem, id); ok {
			t.Fatal("hold not cleared")
		}
		if p := h.push(); p.Sent != 0 {
			t.Fatalf("an answered edit re-sent: %+v", p)
		}
	})
	t.Run("the cloud changed the field meanwhile", func(t *testing.T) {
		h, id := heldDescriptionEdit(t)
		h.myEdit("item", id, map[string]any{"description": "Cloud text"})
		h.strictCloud(true, "description")
		p := h.push()
		if p.Conflicts != 1 || p.Held != 0 || p.Pending != 0 {
			t.Fatalf("push = %+v, want a conflict the cloud wins", p)
		}
		if it, _ := h.item(id); it.Desc != "Cloud text" {
			t.Fatalf("local description = %q, want the cloud's", it.Desc)
		}
	})
}

// AC 10: a sealed, unanswered change that names a field the accepted set no
// longer holds is re-sent without it under a new change_id, in its place.
func TestPushResealsChangeOutrunningAcceptedSet(t *testing.T) {
	withFields100(t, kindItem, "description")
	h := newPushHarness(t)
	h.strictCloud(true, "description")
	a := h.seedPulled(1, "Latte", 250)
	b := h.seedPulled(2, "Tea", 200)
	h.saveItem(data.ItemPatch{ID: a, PriceMinor: ptrTo(int64(280)), Description: ptrTo("Local note")})
	h.saveItem(data.ItemPatch{ID: b, PriceMinor: ptrTo(int64(210))})
	h.failUpload(false) // sealed with description, never answered
	first := h.sentChanges()
	if len(first) != 2 || !mentions(first, "description") {
		t.Fatalf("setup: %+v", first)
	}
	h.strictCloud(false) // the cloud rolled back
	p := h.push()
	if p.Accepted != 2 || h.refusedCalls() != 0 {
		t.Fatalf("push = %+v, refused = %d", p, h.refusedCalls())
	}
	all := h.sentChanges()
	resent := all[2:]
	if len(resent) != 2 || mentions(resent, "description") {
		t.Fatalf("resent = %+v", resent)
	}
	for i, ch := range resent {
		if ch.CloudID != first[i].CloudID {
			t.Fatalf("resent[%d] is row %s, want %s: the seal order must hold", i, ch.CloudID, first[i].CloudID)
		}
	}
	// Only the change that named description gets a new change_id; the other
	// is re-sent unchanged.
	if resent[0].ChangeID == first[0].ChangeID || resent[1].ChangeID != first[1].ChangeID {
		t.Fatalf("change_ids: %s -> %s, %s -> %s", first[0].ChangeID, resent[0].ChangeID, first[1].ChangeID, resent[1].ChangeID)
	}
	if _, ok := h.heldAt(kindItem, a); !ok {
		t.Fatal("the stripped description edit is not held")
	}
	if it, _ := h.item(a); it.Desc != "Local note" {
		t.Fatalf("local description = %q", it.Desc)
	}
}

// AC 11: 100 sealed changes of which one names a withheld field all answer.
func TestPushHundredSealedOneOutrunNoStall(t *testing.T) {
	withFields100(t, kindItem, "description")
	h := newPushHarness(t)
	h.strictCloud(true, "description")
	for n := 1; n <= MaxChangesPerCall; n++ {
		h.feed.add(itemFeedRow(n, h.feed.high+1, fmt.Sprintf("Item %d", n), 100))
	}
	h.mustRun()
	for n := 1; n <= MaxChangesPerCall; n++ {
		patch := data.ItemPatch{ID: itemID(n), PriceMinor: ptrTo(int64(200 + n))}
		if n == 50 {
			patch.Description = ptrTo("odd one out")
		}
		h.saveItem(patch)
	}
	h.failUpload(false)
	h.strictCloud(false)
	p, err := Push(context.Background(), h.db, h.post, h.opt)
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if p.Accepted != MaxChangesPerCall || p.Calls != 1 || h.refusedCalls() != 0 {
		t.Fatalf("push = %+v, refused = %d, want all %d answered in one call", p, h.refusedCalls(), MaxChangesPerCall)
	}
}

// Cost: held rows live outside cloud_outbox, so the uploader never rescans
// them: while the one open mark uploads, the outbox holds that mark alone.
func TestPushHeldRowsNotRescanned(t *testing.T) {
	withFields100(t, kindItem, "description")
	h := newPushHarness(t)
	h.strictCloud(false)
	id := h.seedPulled(1, "Latte", 250)
	h.exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 2000)
INSERT INTO cloud_outbox_held (entity, local_id, created_at) SELECT 'item', 'ghost-' || i, '2026-01-01T00:00:00Z' FROM n`)
	h.exec(`INSERT INTO settings (key, value) VALUES (?, ?)`, KeyHeldAccepted, acceptedFrom("").fp)
	h.saveItem(data.ItemPatch{ID: id, PriceMinor: ptrTo(int64(280))})
	var inOutbox int
	post := func(ctx context.Context, changes []Change) ([]Answer, error) {
		_ = h.db.QueryRow(`SELECT COUNT(*) FROM cloud_outbox`).Scan(&inOutbox)
		return h.post(ctx, changes)
	}
	p, err := Push(context.Background(), h.db, post, h.opt)
	if err != nil {
		t.Fatal(err)
	}
	if p.Sent != 1 || inOutbox != 1 {
		t.Fatalf("push = %+v, outbox rows during the call = %d, want 1 and 1", p, inOutbox)
	}
	if p.Held != 2000 {
		t.Fatalf("held = %d, want 2000 untouched", p.Held)
	}
}

// AC 12: a signal that disappears, or one that is garbled or names only
// fields the till does not know, falls back to 1.0.0.
func TestPushSignalDisappearsFallsBackTo100(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"disappeared", ""},
		{"garbled", `{"item": [`},
		{"unknown fields only", `{"item":["zz_unknown"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withFields100(t, kindItem, "description")
			h := newPushHarness(t)
			h.strictCloud(true, "description")
			id := h.seedPulled(1, "Latte", 250)
			h.saveItem(data.ItemPatch{ID: id, PriceMinor: ptrTo(int64(280)), Description: ptrTo("one")})
			if p := h.push(); p.Accepted != 1 || !mentions(h.sentChanges(), "description") {
				t.Fatalf("with the signal: %+v", p)
			}
			h.strictCloud(false)
			h.opt.AcceptedFields = tc.raw
			h.saveItem(data.ItemPatch{ID: id, PriceMinor: ptrTo(int64(290)), Description: ptrTo("two")})
			p, err := Push(context.Background(), h.db, h.post, h.opt)
			if err != nil || p.Accepted != 1 || h.refusedCalls() != 0 || p.Held != 1 {
				t.Fatalf("without the signal: %+v, %v, refused %d", p, err, h.refusedCalls())
			}
			if it, _ := h.item(id); it.Desc != "two" {
				t.Fatalf("local description = %q", it.Desc)
			}
		})
	}
}

// AC 13: PushResult.Held counts the rows that wait, and the Info line names
// the fields once per process (per accepted set), never a value.
func TestPushHeldCountAndLogOnce(t *testing.T) {
	withFields100(t, kindItem, "description")
	var mu sync.Mutex
	var lines []string
	oldLog := withheldInfof
	withheldInfof = func(f string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(f, a...))
	}
	withheldLogged.Store("")
	t.Cleanup(func() { withheldInfof = oldLog; withheldLogged.Store("") })
	h := newPushHarness(t)
	h.strictCloud(false)
	id := h.seedPulled(1, "Latte", 250)
	h.saveItem(data.ItemPatch{ID: id, Description: ptrTo("Secret recipe")})
	for i := 0; i < 3; i++ {
		if p := h.push(); p.Held != 1 {
			t.Fatalf("push %d = %+v", i, p)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 1 || !strings.Contains(lines[0], "item fields description") || !strings.Contains(lines[0], "1 catalogue rows") || strings.Contains(lines[0], "Secret") {
		t.Fatalf("log lines = %q, want one line naming item fields description and 1 row, no value", lines)
	}
}
