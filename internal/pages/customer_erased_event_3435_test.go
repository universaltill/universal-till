package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#3435: a plugin that keeps its own copy of a customer (loyalty, CRM,
// integration) must be told when the till erases that customer, on the
// primary where the erasure happens AND on every replica that learns of it
// through the admin pull — or the plugin's copy outlives the GDPR erasure.

const erasedEventPluginID = "com.example.loyalty-3435"

// subscribeCustomerErased installs a loyalty-style plugin holding
// events:receive in db and subscribes it to customer.erased on the shared
// bus the production call sites publish on.
func subscribeCustomerErased(t *testing.T, db *sql.DB) <-chan plugins.Event {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`INSERT OR IGNORE INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, min_pos_version, api_version, published_at)
		   VALUES ('` + erasedEventPluginID + `', '1.0.0', 'Loyalty', 'desc', 'wasm', './plugin.wasm', 'url', 'sha', 'auth', 'site', '[]', '0.0.0', '1', datetime('now'))`,
		`INSERT OR IGNORE INTO plugins (id, name, version, entrypoint, is_active) VALUES ('` + erasedEventPluginID + `', 'Loyalty', '1.0.0', './plugin.wasm', 1)`,
		`INSERT OR IGNORE INTO plugin_permissions (id, plugin_id, permission, granted) VALUES ('perm-3435', '` + erasedEventPluginID + `', 'events:receive', 1)`,
		`INSERT OR IGNORE INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES ('hook-3435', '` + erasedEventPluginID + `', 'customer.erased', 'loyalty.forget', 1)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("seed plugin: %v", err)
		}
	}
	bus := plugins.SharedBus(db)
	bus.ResetSubscribers()
	t.Cleanup(bus.ResetSubscribers)
	ch, err := bus.Subscribe(ctx, erasedEventPluginID, []string{"customer.erased"})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	return ch
}

// receiveCustomerErased returns the customer ids delivered on ch, decoded
// through the published payload contract — once want have arrived, or when
// the wait window closes.
func receiveCustomerErased(t *testing.T, ch <-chan plugins.Event, want int, wait time.Duration) []string {
	t.Helper()
	var ids []string
	deadline := time.After(wait)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return ids
			}
			if ev.Type != "customer.erased" {
				t.Fatalf("event type = %q", ev.Type)
			}
			var raw map[string]any
			if err := json.Unmarshal(ev.Payload, &raw); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			if len(raw) != 1 {
				t.Fatalf("payload must carry the customer id only, got %s", ev.Payload)
			}
			var p plugins.CustomerErasedEvent
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			ids = append(ids, p.CustomerID)
			if want > 0 && len(ids) >= want {
				return ids
			}
		case <-deadline:
			return ids
		}
	}
}

// (a) Primary: the Settings → Data → Erase a customer handler.
func TestEraseCustomer_PublishesCustomerErasedToPlugins(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO customers(id,name,phone) VALUES('cust1','Jane Doe','555-0100')`); err != nil {
		t.Fatal(err)
	}
	ch := subscribeCustomerErased(t, dp.Db)

	req := httptest.NewRequest(http.MethodPost, "/api/data/customers/erase", strings.NewReader("id=cust1&override_pin="+dataAPITestManagerPIN))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("erase: %d %s", rec.Code, rec.Body.String())
	}

	got := receiveCustomerErased(t, ch, 1, 2*time.Second)
	got = append(got, receiveCustomerErased(t, ch, 0, 200*time.Millisecond)...)
	if len(got) != 1 || got[0] != "cust1" {
		t.Fatalf("plugin received customer.erased for %v, want exactly [cust1]", got)
	}
}

// A refused erasure (unknown customer) tells plugins nothing.
func TestEraseCustomer_UnknownCustomerPublishesNothing(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	ch := subscribeCustomerErased(t, dp.Db)

	req := httptest.NewRequest(http.MethodPost, "/api/data/customers/erase", strings.NewReader("id=nobody&override_pin="+dataAPITestManagerPIN))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("erase unknown: %d %s", rec.Code, rec.Body.String())
	}
	if got := receiveCustomerErased(t, ch, 0, 200*time.Millisecond); len(got) != 0 {
		t.Fatalf("a refused erasure published customer.erased for %v", got)
	}
}

// (b) Replica: the erasure arrives through the admin pull from the primary.
func TestSyncPullTick_PublishesCustomerErasedForPrimaryErasure(t *testing.T) {
	primary := newPullTestPrimary(t)
	ctx := t.Context()
	if _, err := primary.dp.Db.ExecContext(ctx, `INSERT INTO customers(id,name,phone) VALUES('c-pinned','Pinned Person','555'),('c-free','Free Person','556'),('c-kept','Kept Person','557')`); err != nil {
		t.Fatal(err)
	}
	replica := newPullTestReplica(t, primary.server.URL)
	client := &http.Client{Timeout: 5 * time.Second}
	syncPullTick(ctx, replica, client, func(context.Context) {})
	if _, _, ok := data.NewPOSRepo(replica.Db).LookupCustomer(ctx, "c-free"); !ok {
		t.Fatal("first pull did not bring the customers to the replica")
	}
	// The replica's own sale pins one of them (retire-in-place branch).
	if _, err := replica.Db.ExecContext(ctx, `INSERT INTO sales (id, receipt_no, subtotal, total, customer_id) VALUES ('s1', 'R-1', 100, 100, 'c-pinned')`); err != nil {
		t.Fatal(err)
	}
	ch := subscribeCustomerErased(t, replica.Db)

	for _, id := range []string{"c-pinned", "c-free"} {
		if ok, err := data.NewPOSRepo(primary.dp.Db).EraseCustomer(ctx, id, "", ""); err != nil || !ok {
			t.Fatalf("erase %s on primary: ok=%v err=%v", id, ok, err)
		}
	}
	syncPullTick(ctx, replica, client, func(context.Context) {})

	got := receiveCustomerErased(t, ch, 2, 2*time.Second)
	seen := map[string]int{}
	for _, id := range got {
		seen[id]++
	}
	if len(got) != 2 || seen["c-pinned"] != 1 || seen["c-free"] != 1 {
		t.Fatalf("replica plugin received customer.erased for %v, want c-pinned and c-free once each", got)
	}

	// The next pull carries the same erasure state: no repeat delivery.
	if _, err := primary.dp.Db.ExecContext(ctx, `UPDATE customers SET phone='558' WHERE id='c-kept'`); err != nil {
		t.Fatal(err)
	}
	syncPullTick(ctx, replica, client, func(context.Context) {})
	if again := receiveCustomerErased(t, ch, 0, 200*time.Millisecond); len(again) != 0 {
		t.Fatalf("a later pull re-published customer.erased for %v", again)
	}
}
