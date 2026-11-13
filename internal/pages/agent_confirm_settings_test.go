package pages

import (
	"net/http"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/agentaction"
	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/settings"
)

// settings.set.v1 on the confirm route (ADR-0153 Decision 3, ut-docs#4129).

const settingsNegArgs = `{"changes":[{"key":"pos.allow_negative_inventory","value":"on"}]}`

func settingsHarness(t *testing.T) *agentHarness {
	t.Helper()
	h := newAgentHarness(t)
	if _, err := h.d.Db.Exec(`INSERT INTO plugin_permissions(id,plugin_id,permission,granted) VALUES(?,?,?,1)`, agentPluginID+"settings", agentPluginID, "ai.tools.write:settings"); err != nil {
		t.Fatal(err)
	}
	h.d.Settings = settings.NewStore(h.d.Db)
	h.d.Cfg = &config.Config{}
	return h
}

func TestAgentConfirm_SettingsAppliesAndRefreshesLiveState_4129(t *testing.T) {
	h := settingsHarness(t)
	id := h.propose("mgr-1", "manager", "sess", "settings.set.v1", settingsNegArgs)
	if rec := h.post(id, "confirm", "mgr-1", "manager", "sess"); rec.Code != http.StatusOK {
		t.Fatalf("confirm = %d %s", rec.Code, rec.Body.String())
	}
	v, _, _ := h.d.Settings.Get(t.Context(), "pos.allow_negative_inventory")
	if v != "true" {
		t.Fatalf("stored = %q", v)
	}
	if !h.d.CurrentState().AllowNegativeInventory {
		t.Fatal("the live runtime state still has the stock guard on")
	}
	if n := h.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'allow_negative_inventory_changed' AND actor_id = 'mgr-1'`); n != 1 {
		t.Fatalf("settings audit rows = %d, want 1", n)
	}
}

// A cashier holds no `settings` permission (cashier is sale-only, #3079):
// refused at confirm, nothing changes.
func TestAgentConfirm_SettingsCashierRefused_4129(t *testing.T) {
	h := settingsHarness(t)
	id := h.propose("csh-1", "cashier", "sess", "settings.set.v1", settingsNegArgs)
	if rec := h.post(id, "confirm", "csh-1", "cashier", "sess"); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier confirm = %d %s", rec.Code, rec.Body.String())
	}
	if _, found, _ := h.d.Settings.Get(t.Context(), "pos.allow_negative_inventory"); found {
		t.Fatal("a cashier changed a setting")
	}
}

// A shop-wide setting changes on the main till: a till following one refuses
// the proposal at confirm (and propose) time.
func TestAgentConfirm_SettingsSatelliteRefused_4129(t *testing.T) {
	h := settingsHarness(t)
	id := h.propose("mgr-1", "manager", "sess", "settings.set.v1", settingsNegArgs)
	if err := h.d.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatal(err)
	}
	if rec := h.post(id, "confirm", "mgr-1", "manager", "sess"); rec.Code != http.StatusForbidden {
		t.Fatalf("satellite confirm = %d %s", rec.Code, rec.Body.String())
	}
	if _, found, _ := h.d.Settings.Get(t.Context(), "pos.allow_negative_inventory"); found {
		t.Fatal("a satellite changed a shop-wide setting")
	}
}

func TestAgentConfirm_SettingsCardShowsTranslatedRows_4129(t *testing.T) {
	h := settingsHarness(t)
	id := h.propose("mgr-1", "manager", "sess", "settings.set.v1", settingsNegArgs)
	p := agentaction.Proposals.Peek(t.Context(), h.d.Db, id, "mgr-1", auth.SessionKey(auth.WithSessionKey(t.Context(), "sess")))
	if p == nil {
		t.Fatal("no proposal")
	}
	c := agentConfirmCard(p, "en")
	if len(c.Rows) != 1 {
		t.Fatalf("rows = %+v", c.Rows)
	}
	r := c.Rows[0]
	if r.Item != "Sell items without tracking stock" || r.Before != "Off" || r.After != "On" || !strings.Contains(c.Title, "setting") {
		t.Fatalf("card = %+v", c)
	}
}
