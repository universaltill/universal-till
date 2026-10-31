package agentaction

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

func settingsDB(t *testing.T) *db.DB {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "agent-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	d, err := db.Open(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Exec(`INSERT INTO users(id, username, display_name, pin_hash, role) VALUES ('u','u','u','x','manager')`); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSettingsSet_Registered_4129(t *testing.T) {
	a, ok := Lookup("settings.set.v1")
	if !ok || a.Area != "settings" || a.RolePermission != "settings" {
		t.Fatalf("settings.set.v1 = %+v", a)
	}
}

// Only the catalogue's writable keys parse; anything fiscal, tax, payment,
// secret, user, update, pairing or AI is refused, as is a bad value.
func TestSettingsSet_ParseAllowList_4129(t *testing.T) {
	ok := `{"changes":[{"key":"pos.allow_negative_inventory","value":"on"}]}`
	if _, err := parseSettingsSet([]byte(ok)); err != nil {
		t.Fatalf("allow-listed key refused: %v", err)
	}
	bad := []string{
		`{"changes":[]}`,
		`{"changes":[{"key":"pos.allow_negative_inventory","value":"maybe"}]}`,
		`{"changes":[{"key":"pos.allow_negative_inventory"}]}`,
		`{"changes":[{"key":"pos.allow_negative_inventory","value":"on"},{"key":"pos.allow_negative_inventory","value":"off"}]}`,
		`{"changes":[{"key":"unknown.key","value":"on"}]}`,
		`{"changes":[{"key":"store.currency","value":"USD"}]}`,     // explain-only
		`{"changes":[{"key":"invoice.seller_name","value":"x"}]}`,  // explain-only
		`{"changes":[{"key":"tax.default_rate","value":"0"}]}`,     // never
		`{"changes":[{"key":"fiscal.tse_enabled","value":"off"}]}`, // never
		`{"changes":[{"key":"sync.bearer","value":"x"}]}`,          // secret
		`{"changes":[{"key":"marketplace.api_key","value":"x"}]}`,  // secret
		`{"changes":[{"key":"update.channel","value":"beta"}]}`,    // update channel
		`{"changes":[{"key":"ai.provider","value":"claude"}]}`,     // AI settings
		`{"changes":[{"key":"pos.allow_negative_inventory","value":"on","extra":1}]}`,
	}
	for _, b := range bad {
		if _, err := parseSettingsSet([]byte(b)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", b, err)
		}
	}
}

func TestSettingsSet_PlanApplyAudit_4129(t *testing.T) {
	d := settingsDB(t)
	ctx := context.Background()
	a, _ := Lookup("settings.set.v1")
	env := Env{DB: d.DB}
	args, rows, state, err := a.Prepare(ctx, env, []byte(`{"changes":[{"key":"pos.allow_negative_inventory","value":"on"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ItemKey != "settings.stock_tracking.label" ||
		rows[0].Before.Kind != "key" || rows[0].Before.Text != "agent.value.off" || rows[0].After.Text != "agent.value.on" {
		t.Fatalf("rows = %+v", rows)
	}
	res, err := a.Apply(ctx, env, args, state, "u", nil)
	if err != nil || res.Changes != 1 {
		t.Fatalf("apply = %+v, %v", res, err)
	}
	raw, found, _ := data.AgentSettingRaw(ctx, d.DB, "pos.allow_negative_inventory")
	if !found || raw != "true" {
		t.Fatalf("stored = %q (found %v), want true", raw, found)
	}
	if len(res.Settings) != 1 || res.Settings[0].Key != "pos.allow_negative_inventory" {
		t.Fatalf("result settings = %+v", res.Settings)
	}
	var n int
	var payload string
	if err := d.QueryRow(`SELECT COUNT(*), COALESCE(MAX(data_json),'') FROM audit_log WHERE action = 'allow_negative_inventory_changed' AND actor_id = 'u' AND entity_type = 'settings' AND entity_id = 'pos.allow_negative_inventory'`).Scan(&n, &payload); err != nil || n != 1 {
		t.Fatalf("audit rows = %d (%v), want 1", n, err)
	}
	if !strings.Contains(payload, `"enabled":true`) {
		t.Fatalf("audit payload = %s, want the manual screen's {enabled:true}", payload)
	}
	// Turning it on again is a no-change proposal: refused at propose time.
	if _, _, _, err := a.Prepare(ctx, env, []byte(`{"changes":[{"key":"pos.allow_negative_inventory","value":"on"}]}`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no-op proposal: err = %v, want ErrInvalid", err)
	}
}

func TestSettingsSet_StaleRefused_4129(t *testing.T) {
	d := settingsDB(t)
	ctx := context.Background()
	a, _ := Lookup("settings.set.v1")
	env := Env{DB: d.DB}
	args, _, state, err := a.Prepare(ctx, env, []byte(`{"changes":[{"key":"catalog_pre_pack_unit_price_enabled","value":"on"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	// Someone changes it on the screen before the operator confirms.
	if _, err := d.Exec(`INSERT INTO settings(key, value, updated_at) VALUES ('catalog_pre_pack_unit_price_enabled','1',CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Apply(ctx, env, args, state, "u", nil); !errors.Is(err, ErrStale) {
		t.Fatalf("apply after a manual change: err = %v, want ErrStale", err)
	}
}
