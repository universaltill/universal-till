package data

import (
	"context"
	"testing"
)

// ut-docs#7214: icon is a compared field too, so a future promoted field
// other than icon (skip = that field) still blocks a mark when icon differs.
func TestOtherFieldsMatchBase_IconComparedWhenSkipIsAnotherField_7214(t *testing.T) {
	d := openMigratedDB(t, "mode-entry-icon-7214.db")
	db := d.DB
	linkItem(t, db, "i1", "food:pizza", fullItemBase(100)) // base icon "", local icon valid
	linkCategory(t, db, "c1", "food:pizza", `{"name":"C","parent_cloud_id":null,"sort_order":0,"active":true,"icon":""}`)
	ctx := context.Background()
	for _, tc := range []struct{ entity, id, base string }{
		{"item", "i1", fullItemBase(100)},
		{"category", "c1", `{"name":"C","parent_cloud_id":null,"sort_order":0,"active":true,"icon":""}`},
	} {
		if ok, err := otherFieldsMatchBase(ctx, db, tc.entity, tc.id, tc.base, "name"); err != nil || ok {
			t.Fatalf("%s skip=name: ok=%v err=%v, want false (icon differs)", tc.entity, ok, err)
		}
		if ok, err := otherFieldsMatchBase(ctx, db, tc.entity, tc.id, tc.base, "icon"); err != nil || !ok {
			t.Fatalf("%s skip=icon: ok=%v err=%v, want true", tc.entity, ok, err)
		}
	}
}

// A stored icon id that fails iconid.ValidFormat uploads as "" (catalogpull
// iconOut), so against a base icon "" it is no difference (review L1).
func TestOtherFieldsMatchBase_InvalidIconCountsAsNone_7214(t *testing.T) {
	d := openMigratedDB(t, "mode-entry-icon-invalid-7214.db")
	db := d.DB
	catBase := `{"name":"C","parent_cloud_id":null,"sort_order":0,"active":true,"icon":""}`
	linkItem(t, db, "i1", "Not A Valid Id!", fullItemBase(100))
	linkCategory(t, db, "c1", "Not A Valid Id!", catBase)
	ctx := context.Background()
	for _, tc := range []struct{ entity, id, base string }{
		{"item", "i1", fullItemBase(100)},
		{"category", "c1", catBase},
	} {
		if ok, err := otherFieldsMatchBase(ctx, db, tc.entity, tc.id, tc.base, "name"); err != nil || !ok {
			t.Fatalf("%s skip=name with invalid local icon: ok=%v err=%v, want true", tc.entity, ok, err)
		}
	}
}

// A listed field with no local value fails closed with an error rather
// than being left uncompared (ut-docs#7214).
func TestOtherFieldsMatchBase_UnknownListedFieldErrors_7214(t *testing.T) {
	d := openMigratedDB(t, "mode-entry-unknown-field-7214.db")
	db := d.DB
	catBase := `{"name":"C","parent_cloud_id":null,"sort_order":0,"active":true,"icon":"","color":"red"}`
	linkCategory(t, db, "c1", "", catBase)
	orig := ModeEntryCompareFields["category"]
	t.Cleanup(func() { ModeEntryCompareFields["category"] = orig })
	ModeEntryCompareFields["category"] = append(append([]string(nil), orig...), "color")
	ok, err := otherFieldsMatchBase(context.Background(), db, "category", "c1", catBase, "icon")
	if err == nil || ok {
		t.Fatalf("unlisted local value: ok=%v err=%v, want false and an error", ok, err)
	}
}
