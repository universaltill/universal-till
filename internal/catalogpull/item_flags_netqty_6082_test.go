package catalogpull

import (
	"os"
	"reflect"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#6082/#6202 (contract cloud-catalog-changes.md 1.1.0): an item's
// is_weighed, age_restricted and net quantity (value + unit) upload like
// the other mastered fields. The net quantity's two feed fields always
// travel together: one half alone is a 400 at the cloud, and a pull that
// strips only one half fails the whole row with ErrInvalidNetQuantity.

func pairLocal(v *int64, u *string) local {
	return local{vals: values{
		"name": rawJSON("x"), "is_weighed": rawJSON(false), "age_restricted": rawJSON(false),
		"net_quantity_value": rawJSON(v), "net_quantity_unit": rawJSON(u),
	}}
}

func TestChangedFieldsCouplesNetQuantityPair(t *testing.T) {
	base := values{
		"name": rawJSON("x"), "is_weighed": rawJSON(false), "age_restricted": rawJSON(false),
		"net_quantity_value": rawJSON(330), "net_quantity_unit": rawJSON("ml"),
	}
	cases := []struct {
		name string
		l    local
		base values
		want []string
	}{
		{"value only", pairLocal(ptrTo(int64(500)), ptrTo("ml")), base, []string{"net_quantity_value", "net_quantity_unit"}},
		{"unit only", pairLocal(ptrTo(int64(330)), ptrTo("g")), base, []string{"net_quantity_value", "net_quantity_unit"}},
		{"cleared", pairLocal(nil, nil), base, []string{"net_quantity_value", "net_quantity_unit"}},
		{"unchanged", pairLocal(ptrTo(int64(330)), ptrTo("ml")), base, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := changedFields(kindItem, tc.l, tc.base)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("changedFields = %v, want %v", got, tc.want)
			}
		})
	}
	// A base lacking either half of the pair: neither half is compared.
	for _, drop := range []string{"net_quantity_value", "net_quantity_unit"} {
		b := values{}
		for k, v := range base {
			if k != drop {
				b[k] = v
			}
		}
		got := changedFields(kindItem, pairLocal(ptrTo(int64(500)), ptrTo("g")), b)
		if len(got) != 0 {
			t.Fatalf("base without %s: changedFields = %v, want none of the pair", drop, got)
		}
	}
	// The flags are compared on their own, in upload order.
	l := pairLocal(ptrTo(int64(330)), ptrTo("ml"))
	l.vals["is_weighed"] = rawJSON(true)
	l.vals["age_restricted"] = rawJSON(true)
	if got := changedFields(kindItem, l, base); !reflect.DeepEqual(got, []string{"is_weighed", "age_restricted"}) {
		t.Fatalf("flags: changedFields = %v", got)
	}
}

// seedWithItem gives the till item 1 as the cloud has it, with mutate
// applied to the feed row's data first.
func (h *pushHarness) seedWithItem(mutate func(map[string]any)) string {
	h.t.Helper()
	row := itemFeedRow(1, h.feed.high+1, "Cola", 250)
	if mutate != nil {
		mutate(row.Data)
	}
	h.feed.add(row)
	h.mustRun()
	if h.marks() != 0 {
		h.t.Fatalf("the pull left %d outbox marks", h.marks())
	}
	return itemID(1)
}

func TestTillEditOfFlagsAndNetQtySurvivesCloudPriceEdit_6082(t *testing.T) {
	pair := []string{"net_quantity_value", "net_quantity_unit"}
	has330ml := func(d map[string]any) { d["net_quantity_value"], d["net_quantity_unit"] = 330, "ml" }
	cases := []struct {
		name   string
		seed   func(map[string]any)
		patch  data.ItemPatch
		want   []string
		check  func(t *testing.T, it itemRow)
		cloud  map[string]any // the cloud's value of the edited field afterwards
		before map[string]any // the change's before for the edited fields
	}{
		{"is_weighed", nil, data.ItemPatch{IsWeighed: ptrTo(true)}, []string{"is_weighed"},
			func(t *testing.T, it itemRow) {
				if !it.Weighed {
					t.Fatal("is_weighed lost")
				}
			}, map[string]any{"is_weighed": true}, map[string]any{"is_weighed": false}},
		{"age_restricted", nil, data.ItemPatch{AgeRestricted: ptrTo(true)}, []string{"age_restricted"},
			func(t *testing.T, it itemRow) {
				if !it.AgeRestricted {
					t.Fatal("age_restricted lost")
				}
			}, map[string]any{"age_restricted": true}, map[string]any{"age_restricted": false}},
		{"net quantity set", nil, data.ItemPatch{NetQuantityValue: ptrTo(int64(330)), NetQuantityUnit: ptrTo("ml")}, pair,
			func(t *testing.T, it itemRow) {
				if it.NQValue.Int64 != 330 || it.NQUnit.String != "ml" {
					t.Fatalf("net quantity = %+v %+v", it.NQValue, it.NQUnit)
				}
			}, map[string]any{"net_quantity_value": 330, "net_quantity_unit": "ml"},
			map[string]any{"net_quantity_value": nil, "net_quantity_unit": nil}},
		{"net quantity changed", has330ml, data.ItemPatch{NetQuantityValue: ptrTo(int64(500)), NetQuantityUnit: ptrTo("g")}, pair,
			func(t *testing.T, it itemRow) {
				if it.NQValue.Int64 != 500 || it.NQUnit.String != "g" {
					t.Fatalf("net quantity = %+v %+v", it.NQValue, it.NQUnit)
				}
			}, map[string]any{"net_quantity_value": 500, "net_quantity_unit": "g"},
			map[string]any{"net_quantity_value": 330, "net_quantity_unit": "ml"}},
		{"net quantity value only differs", has330ml, data.ItemPatch{NetQuantityValue: ptrTo(int64(500)), NetQuantityUnit: ptrTo("ml")}, pair,
			func(t *testing.T, it itemRow) {
				if it.NQValue.Int64 != 500 || it.NQUnit.String != "ml" {
					t.Fatalf("net quantity = %+v %+v", it.NQValue, it.NQUnit)
				}
			}, map[string]any{"net_quantity_value": 500, "net_quantity_unit": "ml"},
			map[string]any{"net_quantity_value": 330, "net_quantity_unit": "ml"}},
		{"net quantity cleared", has330ml, data.ItemPatch{ClearNetQuantity: true}, pair,
			func(t *testing.T, it itemRow) {
				if it.NQValue.Valid || it.NQUnit.Valid {
					t.Fatalf("net quantity = %+v %+v, want none", it.NQValue, it.NQUnit)
				}
			}, map[string]any{"net_quantity_value": nil, "net_quantity_unit": nil},
			map[string]any{"net_quantity_value": 330, "net_quantity_unit": "ml"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newPushHarness(t)
			id := h.seedWithItem(tc.seed)
			tc.patch.ID = id
			h.saveItem(tc.patch)
			if p, _ := h.tick(); p.Accepted != 1 {
				t.Fatalf("push = %+v, want the edit accepted", p)
			}
			ch := onlyChange(t, h.sentChanges())
			if ch.Op != opUpdate || !reflect.DeepEqual(ch.ChangedFields, tc.want) {
				t.Fatalf("change = %s %v, want update of exactly %v", ch.Op, ch.ChangedFields, tc.want)
			}
			for f, v := range tc.before {
				if got := norm(ch.Before[f]); got != norm(rawJSON(v)) {
					t.Fatalf("before[%s] = %s, want %v", f, got, v)
				}
			}
			row := h.cloudRow("item", id)
			for f, v := range tc.cloud {
				if got := norm(rawJSON(row.Data[f])); got != norm(rawJSON(v)) {
					t.Fatalf("cloud %s = %s, want %v", f, got, v)
				}
			}
			// The cloud then changes only the price; the pull applies it
			// and the till keeps its edit.
			h.myEdit("item", id, map[string]any{"price_minor": 300})
			h.mustRun()
			it, _ := h.item(id)
			if it.Price != 300 {
				t.Fatalf("price = %d, want the cloud's 300", it.Price)
			}
			tc.check(t, it)
			if p, _ := h.tick(); p.Sent != 0 {
				t.Fatalf("a settled tick sent %d changes", p.Sent)
			}
		})
	}
}

// A marked unit-only edit with a feed row that changes the cloud's pair:
// the pull must strip both halves, never one (a lone half is an invalid
// net quantity that would refuse the whole row).
func TestPullLeavesAPendingNetQuantityPairAlone_6082(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedWithItem(func(d map[string]any) { d["net_quantity_value"], d["net_quantity_unit"] = 330, "ml" })
	h.saveItem(data.ItemPatch{ID: id, NetQuantityValue: ptrTo(int64(330)), NetQuantityUnit: ptrTo("g")}) // unit only
	h.myEdit("item", id, map[string]any{"net_quantity_value": 500, "net_quantity_unit": "ml", "price_minor": 300})
	res := h.mustRun() // a pull with no upload before it
	if res.Refused != 0 {
		t.Fatalf("pull refused %d rows, want none (a lone net-quantity half)", res.Refused)
	}
	it, _ := h.item(id)
	if it.NQValue.Int64 != 330 || it.NQUnit.String != "g" {
		t.Fatalf("pending pair = %v %v, want the till's 330 g kept", it.NQValue, it.NQUnit)
	}
	if it.Price != 300 {
		t.Fatalf("price = %d, want the cloud's 300 applied", it.Price)
	}
}

// A conflict hands the cloud's whole row to the till, flags included, and
// clears the mark; a cloud price edit and a till flag edit made apart both
// stay.
func TestPushAgeRestrictedConflictAppliesCloudValue_6082(t *testing.T) {
	t.Run("conflict", func(t *testing.T) {
		h := newPushHarness(t)
		id := h.seedWithItem(nil)
		h.myEdit("item", id, map[string]any{"price_minor": 300})
		h.saveItem(data.ItemPatch{ID: id, PriceMinor: ptrTo(int64(280)), AgeRestricted: ptrTo(true)})
		p := h.push()
		if p.Conflicts != 1 {
			t.Fatalf("push = %+v, want one conflict", p)
		}
		it, _ := h.item(id)
		if it.Price != 300 || it.AgeRestricted {
			t.Fatalf("item = price %d age_restricted %v, want the cloud's 300 / false", it.Price, it.AgeRestricted)
		}
		if h.marks() != 0 {
			t.Fatalf("%d marks left after the conflict", h.marks())
		}
		if row := h.cloudRow("item", id); row.Data["age_restricted"] != false {
			t.Fatalf("cloud age_restricted = %v, want unchanged false", row.Data["age_restricted"])
		}
	})
	t.Run("a cloud price edit and a till is_weighed edit both stay", func(t *testing.T) {
		h := newPushHarness(t)
		id := h.seedWithItem(nil)
		h.myEdit("item", id, map[string]any{"price_minor": 300})
		h.saveItem(data.ItemPatch{ID: id, IsWeighed: ptrTo(true)})
		if p, _ := h.tick(); p.Accepted != 1 {
			t.Fatalf("push = %+v", p)
		}
		it, _ := h.item(id)
		if it.Price != 300 || !it.Weighed {
			t.Fatalf("item = price %d weighed %v, want 300 / true", it.Price, it.Weighed)
		}
		if row := h.cloudRow("item", id); row.Data["is_weighed"] != true || num(row.Data["price_minor"]) != 300 {
			t.Fatalf("cloud row = %+v", row.Data)
		}
	})
}

// The migration (102) fills the four fields into an applier-4 base from the
// local row, so a flag edit made before the first applier-5 tick uploads
// (its base field exists) and the re-pull leaves nothing the till disagrees
// with (AC7).
func TestBackfilledBaseUploadsAFlagEditBeforeTheRepull_6082(t *testing.T) {
	h := newPushHarness(t)
	id := h.seedWithItem(func(d map[string]any) { d["net_quantity_value"], d["net_quantity_unit"] = 330, "ml" })
	// The till as an applier-4 build left it: bases without the four.
	for _, f := range []string{"is_weighed", "age_restricted", "net_quantity_value", "net_quantity_unit"} {
		h.exec(`UPDATE cloud_links SET base = json_remove(base, '$.` + f + `') WHERE entity = 'item'`)
	}
	h.exec(`UPDATE settings SET value = '4' WHERE key = ?`, KeyApplierVersion)
	raw, err := os.ReadFile("../db/migrations/102_cloud_outbox_item_flags.sql")
	if err != nil {
		t.Fatal(err)
	}
	h.exec(string(raw)) // the migration, as upgrading runs it (idempotent)
	var st string
	if err := h.db.QueryRow(`SELECT base FROM cloud_links WHERE entity = 'item'`).Scan(&st); err != nil {
		t.Fatal(err)
	}
	base := parseBase(st)
	for f, want := range map[string]string{"is_weighed": "false", "age_restricted": "false", "net_quantity_value": "330", "net_quantity_unit": `"ml"`} {
		if got := norm(base[f]); got != want {
			t.Fatalf("backfilled base[%s] = %s, want %s (base %s)", f, got, want, st)
		}
	}
	h.saveItem(data.ItemPatch{ID: id, IsWeighed: ptrTo(true)})
	p := h.push()
	if p.Accepted != 1 {
		t.Fatalf("push = %+v, want the edit uploaded before the re-pull", p)
	}
	ch := onlyChange(t, h.sentChanges())
	if !reflect.DeepEqual(ch.ChangedFields, []string{"is_weighed"}) {
		t.Fatalf("changed_fields = %v", ch.ChangedFields)
	}
	h.mustRun() // the applier-5 re-pull from 0
	it, _ := h.item(id)
	if !it.Weighed || it.NQValue.Int64 != 330 || it.NQUnit.String != "ml" {
		t.Fatalf("after the re-pull: %+v", it)
	}
	if err := h.db.QueryRow(`SELECT base FROM cloud_links WHERE entity = 'item'`).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if got := norm(parseBase(st)["is_weighed"]); got != "true" {
		t.Fatalf("base is_weighed = %s after the accepted upload", got)
	}
	if p, _ := h.tick(); p.Sent != 0 {
		t.Fatalf("settled tick sent %d", p.Sent)
	}
}
