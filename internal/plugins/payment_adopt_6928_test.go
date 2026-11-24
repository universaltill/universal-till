package plugins

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// ut-docs#6928: a replica claims an ownerless tender row its main says the
// plugin owns, inside the install tx, then the existing checks run unchanged.

const collidesMsg = "collides with an existing built-in or shop-configured tender"

func sumupManifest(id string) *Manifest {
	m := paymentManifest(id, "sumup")
	m.Entries[0].Label = "SumUp"
	return m
}

func seedOwnerless(t *testing.T, id, name string) *db.DB {
	t.Helper()
	d := openRealDB(t)
	mustExecSQL(t, d, `INSERT INTO payment_methods (id, name, type, is_active, sort_order) VALUES (?, ?, 'card', 1, 100)`, id, name)
	return d
}

func pmOwner(t *testing.T, d *db.DB, id string) string {
	t.Helper()
	var o string
	if err := d.DB.QueryRow(`SELECT COALESCE(plugin_id,'') FROM payment_methods WHERE id = ?`, id).Scan(&o); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestPersistManifest_AdoptsOwnerlessHintedTender(t *testing.T) {
	d := seedOwnerless(t, "sumup", "SumUp")
	ctx := context.Background()
	if err := PersistManifest(ctx, d.DB, sumupManifest("com.sumup.pay"), InstallOptions{AdoptPaymentKeys: []string{"sumup"}}); err != nil {
		t.Fatalf("hinted install: %v", err)
	}
	if got := pmOwner(t, d, "sumup"); got != "com.sumup.pay" {
		t.Fatalf("owner = %q, want com.sumup.pay", got)
	}
	if err := data.NewPluginRepo(d.DB).SyncPluginPaymentMethods(ctx); err != nil {
		t.Fatal(err)
	}
	var active int
	_ = d.DB.QueryRow(`SELECT is_active FROM payment_methods WHERE id='sumup'`).Scan(&active)
	if active != 1 || pmOwner(t, d, "sumup") != "com.sumup.pay" {
		t.Fatalf("after sync: active=%d owner=%q", active, pmOwner(t, d, "sumup"))
	}
}

func TestPersistManifest_AdoptedRowRenamedLabelNoNameConflict(t *testing.T) {
	d := seedOwnerless(t, "sumup", "Card (SumUp)")
	mustExecSQL(t, d, `INSERT INTO payment_methods (id, name, type, is_active, sort_order) VALUES ('other', 'Other', 'card', 1, 101)`)
	ctx := context.Background()
	if err := PersistManifest(ctx, d.DB, sumupManifest("com.sumup.pay"), InstallOptions{AdoptPaymentKeys: []string{"sumup"}}); err != nil {
		t.Fatalf("renamed adopted row must not name-conflict: %v", err)
	}
	// Another ownerless row holding the label still conflicts.
	d2 := seedOwnerless(t, "sumup", "SumUp")
	mustExecSQL(t, d2, `INSERT INTO payment_methods (id, name, type, is_active, sort_order) VALUES ('rival', 'Rival', 'card', 1, 101)`)
	m := sumupManifest("com.sumup.pay")
	m.Entries[0].Label = "Rival"
	err := PersistManifest(ctx, d2.DB, m, InstallOptions{AdoptPaymentKeys: []string{"sumup"}})
	if err == nil || !strings.Contains(err.Error(), "label") || !strings.Contains(err.Error(), collidesMsg[:20]) {
		t.Fatalf("want the existing label conflict, got %v", err)
	}
	if got := pmOwner(t, d2, "sumup"); got != "" {
		t.Fatalf("refused install left owner %q", got)
	}
}

func TestPersistManifest_NoAdoptWithoutHint(t *testing.T) {
	for name, opts := range map[string]InstallOptions{
		"manual":     {},
		"empty hint": {AdoptPaymentKeys: []string{}},
		"other key":  {AdoptPaymentKeys: []string{"giro"}},
	} {
		t.Run(name, func(t *testing.T) {
			d := seedOwnerless(t, "sumup", "SumUp")
			err := PersistManifest(context.Background(), d.DB, sumupManifest("com.sumup.pay"), opts)
			if err == nil || !strings.Contains(err.Error(), collidesMsg) {
				t.Fatalf("want the existing collision error, got %v", err)
			}
			if got := pmOwner(t, d, "sumup"); got != "" {
				t.Fatalf("owner = %q, want NULL", got)
			}
		})
	}
}

func TestPersistManifest_AdoptNeverBuiltin(t *testing.T) {
	d := openRealDB(t)
	m := paymentManifest("com.evil.card", "card")
	err := PersistManifest(context.Background(), d.DB, m, InstallOptions{AdoptPaymentKeys: []string{"card"}})
	if err == nil || !strings.Contains(err.Error(), collidesMsg) {
		t.Fatalf("want the existing collision error, got %v", err)
	}
	if got := pmOwner(t, d, "card"); got != "" {
		t.Fatalf("card owner = %q", got)
	}
}

func TestPersistManifest_AdoptNeverShopTender(t *testing.T) {
	d := seedOwnerless(t, "giro", "Giro")
	m := paymentManifest("com.giro.pay", "giro")
	// The main's hint lists other keys, not giro (shop tender: NULL on main).
	err := PersistManifest(context.Background(), d.DB, m, InstallOptions{AdoptPaymentKeys: []string{"sumup"}})
	if err == nil || !strings.Contains(err.Error(), collidesMsg) {
		t.Fatalf("want collision, got %v", err)
	}
	if got := pmOwner(t, d, "giro"); got != "" {
		t.Fatalf("giro owner = %q", got)
	}
}

func TestPersistManifest_AdoptNeverOverridesLocalOwner(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	if err := PersistManifest(ctx, d.DB, sumupManifest("com.x.pay"), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	err := PersistManifest(ctx, d.DB, sumupManifest("com.y.pay"), InstallOptions{AdoptPaymentKeys: []string{"sumup"}})
	if err == nil || !strings.Contains(err.Error(), "already provided by plugin com.x.pay") {
		t.Fatalf("want already-provided error, got %v", err)
	}
	_ = data.NewPluginRepo(d.DB).SyncPluginPaymentMethods(ctx)
	if got := pmOwner(t, d, "sumup"); got != "com.x.pay" {
		t.Fatalf("owner = %q, want com.x.pay", got)
	}
}

func TestPersistManifest_UninstalledOwnerNotReattributed(t *testing.T) {
	d := openRealDB(t)
	mustExecSQL(t, d, `INSERT INTO payment_methods (id, name, type, is_active, sort_order, plugin_id) VALUES ('sumup', 'SumUp', 'card', 0, 100, 'com.gone.pay')`)
	err := PersistManifest(context.Background(), d.DB, sumupManifest("com.y.pay"), InstallOptions{AdoptPaymentKeys: []string{"sumup"}})
	if err == nil || !strings.Contains(err.Error(), "no longer installed") {
		t.Fatalf("want no-longer-installed error, got %v", err)
	}
	if got := pmOwner(t, d, "sumup"); got != "com.gone.pay" {
		t.Fatalf("owner = %q", got)
	}
}

func TestPersistManifest_AdoptRolledBackOnLaterRefusal(t *testing.T) {
	d := seedOwnerless(t, "sumup", "SumUp")
	m := sumupManifest("com.sumup.pay")
	m.Entries = append(m.Entries, ManifestEntry{Type: "payment", Key: "cash", Label: "Cash Two"})
	err := PersistManifest(context.Background(), d.DB, m, InstallOptions{AdoptPaymentKeys: []string{"sumup", "cash"}})
	if err == nil {
		t.Fatal("want refusal for the cash key")
	}
	if got := pmOwner(t, d, "sumup"); got != "" {
		t.Fatalf("adopted row not rolled back, owner = %q", got)
	}
}

func TestAdoptablePaymentKeys(t *testing.T) {
	entries := []ManifestEntry{
		{Type: "payment", Key: "b"}, {Type: "page", Key: "a"}, {Type: "payment", Key: "a"}, {Type: "payment", Key: "b"},
	}
	got := adoptablePaymentKeys(entries, []string{"a", "z", "b", "a", "a"})
	if want := []string{"b", "a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v (manifest order, payment only, deduped, unknown dropped)", got, want)
	}
	if got := adoptablePaymentKeys(entries, nil); len(got) != 0 {
		t.Fatalf("nil hint = %v", got)
	}
}
