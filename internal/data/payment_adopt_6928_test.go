package data

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// ut-docs#6928: the replica claims an ownerless tender row its main says a
// plugin owns. These tests pin the repo half (AdoptOwnerlessPaymentMethods)
// and the wire half (PluginSyncRow.OwnedPaymentMethodIDs).

func TestAdoptOwnerlessPaymentMethods_AdoptsNullRowOnly(t *testing.T) {
	d := openMigratedDB(t, "adopt.db")
	ctx := context.Background()
	pgPlugin(t, d, "com.x.pay", 1)
	pgPlugin(t, d, "com.y.pay", 1)
	mustExec(t, d, `INSERT INTO payment_methods (id, name, type, is_active, sort_order) VALUES ('sumup', 'SumUp', 'card', 1, 100)`)
	mustExec(t, d, `INSERT INTO payment_methods (id, name, type, is_active, sort_order, plugin_id) VALUES ('owned', 'Owned', 'card', 1, 101, 'com.x.pay')`)
	mustExec(t, d, `INSERT INTO payment_methods (id, name, type, is_active, sort_order, plugin_id) VALUES ('blank', 'Blank', 'card', 1, 102, '')`)

	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	got, err := NewPluginRepo(d.DB).AdoptOwnerlessPaymentMethods(ctx, tx, "com.y.pay",
		[]string{"sumup", "owned", "blank", "missing", "cash", "card", "gift", "voucher"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "sumup" {
		t.Fatalf("adopted = %v, want [sumup]", got)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"sumup": "com.y.pay", "owned": "com.x.pay", "blank": "", "cash": "", "card": "", "gift": "", "voucher": ""} {
		s, ok := pgMethod(t, d, id)
		if id != "missing" && !ok {
			t.Fatalf("row %s missing", id)
		}
		if s.PluginID != want {
			t.Errorf("%s plugin_id = %q, want %q", id, s.PluginID, want)
		}
	}
}

func TestAdoptOwnerlessPaymentMethods_Guards(t *testing.T) {
	d := openMigratedDB(t, "adopt2.db")
	ctx := context.Background()
	repo := NewPluginRepo(d.DB)
	if _, err := repo.AdoptOwnerlessPaymentMethods(ctx, nil, "p", []string{"a"}); err == nil {
		t.Fatal("nil tx must error")
	}
	tx, _ := d.DB.BeginTx(ctx, nil)
	defer tx.Rollback()
	if _, err := repo.AdoptOwnerlessPaymentMethods(ctx, tx, "", []string{"a"}); err == nil {
		t.Fatal("empty plugin id must error")
	}
	if got, err := repo.AdoptOwnerlessPaymentMethods(ctx, tx, "p", nil); err != nil || got != nil {
		t.Fatalf("empty keys = %v, %v; want nil, nil", got, err)
	}
}

func TestSyncPluginPaymentMethods_BuiltinInvariantUsesSharedList(t *testing.T) {
	want := []string{"cash", "card", "gift", "voucher", "card_external"}
	if len(builtinPaymentMethodIDs) != len(want) {
		t.Fatalf("builtinPaymentMethodIDs = %v", builtinPaymentMethodIDs)
	}
	for i := range want {
		if builtinPaymentMethodIDs[i] != want[i] {
			t.Fatalf("builtinPaymentMethodIDs = %v, want %v", builtinPaymentMethodIDs, want)
		}
	}
}

func TestDumpActivePlugins_OwnedPaymentMethodIDs(t *testing.T) {
	d := openMigratedDB(t, "dump.db")
	ctx := context.Background()
	for _, r := range []struct {
		listing, id string
		active      int
	}{{"lst-a", "com.test.a", 1}, {"lst-b", "com.test.b", 0}, {"lst-c", "com.test.c", 1}} {
		pgPlugin(t, d, r.id, r.active)
		mustExec(t, d, `INSERT INTO plugin_install_status (listing_id, plugin_id, plugin_name, current_version, state, updated_at) VALUES (?, ?, ?, '1.0.0', 'active', datetime('now'))`, r.listing, r.id, r.id)
	}
	mustExec(t, d, `INSERT INTO payment_methods (id, name, type, is_active, sort_order, plugin_id) VALUES ('a2', 'A2', 'card', 1, 101, 'com.test.a')`)
	mustExec(t, d, `INSERT INTO payment_methods (id, name, type, is_active, sort_order, plugin_id) VALUES ('a1', 'A1', 'card', 0, 100, 'com.test.a')`)
	mustExec(t, d, `INSERT INTO payment_methods (id, name, type, is_active, sort_order, plugin_id) VALUES ('b1', 'B1', 'card', 1, 102, 'com.test.b')`)
	mustExec(t, d, `INSERT INTO payment_methods (id, name, type, is_active, sort_order) VALUES ('shop', 'Shop', 'card', 1, 103)`)
	mustExec(t, d, `INSERT INTO payment_methods (id, name, type, is_active, sort_order, plugin_id) VALUES ('stranger', 'Stranger', 'card', 1, 104, 'com.gone.pay')`)

	b, err := NewSyncPluginsRepo(d.DB).DumpActivePlugins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]PluginSyncRow{}
	for _, r := range b.Plugins {
		by[r.PluginID] = r
	}
	if p := by["com.test.a"].OwnedPaymentMethodIDs; p == nil || strings.Join(*p, ",") != "a1,a2" {
		t.Fatalf("a owned = %v, want [a1 a2] (active and inactive, ordered)", p)
	}
	if p := by["com.test.b"].OwnedPaymentMethodIDs; p == nil || strings.Join(*p, ",") != "b1" {
		t.Fatalf("b owned = %v, want [b1]", p)
	}
	pc := by["com.test.c"].OwnedPaymentMethodIDs
	if pc == nil || len(*pc) != 0 {
		t.Fatalf("c owned = %v, want non-nil empty", pc)
	}
	raw, _ := json.Marshal(by["com.test.c"])
	if !strings.Contains(string(raw), `"owned_payment_method_ids":[]`) {
		t.Fatalf("empty list must serialise as []: %s", raw)
	}
	raw, _ = json.Marshal(PluginSyncRow{ListingID: "l"})
	if strings.Contains(string(raw), "owned_payment_method_ids") {
		t.Fatalf("nil must be omitted: %s", raw)
	}
	var old PluginSyncRow
	if err := json.Unmarshal([]byte(`{"listing_id":"l","plugin_id":"p"}`), &old); err != nil || old.OwnedPaymentMethodIDs != nil {
		t.Fatalf("old primary row must decode nil: %v %v", old.OwnedPaymentMethodIDs, err)
	}
	// Bundled rows stay nil.
	mustExec(t, d, `INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at) VALUES ('com.sys', '1.0.0', 'sys', 'wasm', 'm.wasm', 'x', 'x', '0.0.1', '1', datetime('now'))`)
	mustExec(t, d, `INSERT INTO plugins (id, name, version, install_state, runtime, entrypoint, is_active, trust_level) VALUES ('com.sys', 'sys', '1.0.0', 'installed', 'wasm', 'm.wasm', 1, 'system')`)
	sb, err := NewSyncPluginsRepo(d.DB).DumpActivePluginsWithSystem(ctx, []string{"com.sys"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range sb.Plugins {
		if r.PluginID == "com.sys" && r.OwnedPaymentMethodIDs != nil {
			t.Fatalf("bundled row must leave the hint nil, got %v", *r.OwnedPaymentMethodIDs)
		}
	}
}

func TestApplyAdmin_PaymentMethodsPluginIDNeverWritten(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "p.db")
	replica := openReplicaDB(t, "r.db")
	pgPlugin(t, primary, "com.x.pay", 1)
	mustExec(t, primary, `INSERT INTO payment_methods (id, name, type, is_active, sort_order, plugin_id) VALUES ('sumup', 'SumUp', 'card', 1, 100, 'com.x.pay')`)
	mustExec(t, primary, `INSERT INTO payment_methods (id, name, type, is_active, sort_order, plugin_id) VALUES ('kept', 'Kept', 'card', 1, 101, 'com.x.pay')`)
	pgPlugin(t, replica, "com.mine.pay", 1)
	mustExec(t, replica, `INSERT INTO payment_methods (id, name, type, is_active, sort_order, plugin_id) VALUES ('kept', 'Kept', 'card', 1, 101, 'com.mine.pay')`)

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The dump already strips plugin_id; plant it on every payment_methods
	// row as a hostile or buggy main would send it, so this pins the
	// APPLY-side skip too (Tester gap G1).
	trip := wireTrip(t, bundle)
	planted := 0
	for _, row := range trip.Tables["payment_methods"] {
		row["plugin_id"] = "com.hostile.pay"
		planted++
	}
	if planted == 0 {
		t.Fatal("bundle has no payment_methods rows to plant plugin_id on")
	}
	if _, err := NewSyncAdminRepo(replica.DB).ApplyAdminWithResult(ctx, trip); err != nil {
		t.Fatal(err)
	}
	if s, ok := pgMethod(t, replica, "sumup"); !ok || s.PluginID != "" {
		t.Fatalf("new row: ok=%v plugin_id=%q, want present and NULL", ok, s.PluginID)
	}
	if s, _ := pgMethod(t, replica, "kept"); s.PluginID != "com.mine.pay" {
		t.Fatalf("existing row plugin_id = %q, want com.mine.pay kept", s.PluginID)
	}
}
