package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/lantls"
)

// ut-docs#4423 (ADR-0114 §7's on-demand trigger): the main till's Tills
// page starts a LAN TLS key rotation now.

func postRotateTLSKey(t *testing.T, mux *http.ServeMux, userRole string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/tills/rotate-tls-key", nil)
	req = auth.WithUser(req, auth.User{ID: "u-" + userRole, Role: userRole})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func servingCert(t *testing.T) *lantls.Cert {
	t.Helper()
	c, err := lantls.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lantls.SetServing(c))
	return c
}

func TestTillRotateTLSKey_Refusals(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	initTillRoleI18n(t)
	f := newSyncLinkFixture(t)
	registerTillRotateTLSKey(f.mux, f.dp)
	t.Cleanup(lantls.SetServing(nil))

	if rec := postRotateTLSKey(t, f.mux, "cashier"); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier: %d, want 403", rec.Code)
	}
	rec := postRotateTLSKey(t, f.mux, "manager")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "secure connection") ||
		rec.Header().Get("X-UT-Response") != "refused" {
		t.Fatalf("TLS off: %d %q (X-UT-Response %q), want a localized 409 refusal", rec.Code, rec.Body, rec.Header().Get("X-UT-Response"))
	}

	c := servingCert(t)
	if err := f.dp.Settings.Set(t.Context(), "sync.primary_url", "http://192.0.2.1:8080"); err != nil {
		t.Fatal(err)
	}
	if rec := postRotateTLSKey(t, f.mux, "manager"); rec.Code != http.StatusConflict {
		t.Fatalf("on a joined till: %d, want 409", rec.Code)
	}
	if c.NextPin() != "" {
		t.Fatal("a refused request started a rotation")
	}
	if got := auditRows(t, f.dp, "lan_tls_rotation_started"); len(got) != 0 {
		t.Fatalf("refused requests were audited: %v", got)
	}
}

// End to end: the press creates the next key, is audited, and a linked
// till gets a fresh hello carrying next_pin over its live link.
func TestTillRotateTLSKey_StartsRotationAndTellsLinkedTills(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	initTillRoleI18n(t)
	f := newSyncLinkFixture(t)
	registerTillRotateTLSKey(f.mux, f.dp)
	registerTillsRoster(f.mux, f.dp)
	c := servingCert(t)
	tillID := f.enrol(t, "Counter", "bearer-t2")
	if _, err := f.dp.Db.Exec(`INSERT INTO users(id, username, display_name, pin_hash, role) VALUES ('u-manager','mgr','Manager','x','manager')`); err != nil {
		t.Fatal(err)
	}
	ws, _, err := f.dial(t, "bearer-t2")
	if err != nil {
		t.Fatal(err)
	}
	if e, err := nextLinkFrame(t, ws); err != nil || e.Type != fleetlink.TypeHello {
		t.Fatalf("first frame = %v, %v; want hello", e.Type, err)
	}
	msg, _ := json.Marshal(fleetlink.Envelope{V: fleetlink.Version, ID: "h-1", Type: fleetlink.TypeHello,
		Payload: json.RawMessage(`{"till_id":"` + tillID + `","role":"replica"}`)})
	if err := ws.Write(t.Context(), websocket.MessageText, msg); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 3*time.Second, func() bool {
		p := f.dp.Link.Peer(tillID)
		if p == nil {
			return false
		}
		_, ok := p.Hello()
		return ok
	}) {
		t.Fatal("link never established")
	}

	if body := getRoster(t, f.mux, "manager").Body.String(); !strings.Contains(body, `hx-post="/api/tills/rotate-tls-key"`) ||
		!strings.Contains(body, `data-rotation="idle"`) {
		t.Fatalf("roster lacks the network key action: %s", body)
	}

	rec := postRotateTLSKey(t, f.mux, "manager")
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate: %d %s, want 200", rec.Code, rec.Body)
	}
	var resp struct {
		Data struct {
			NextPin string `json:"next_pin"`
			Started bool   `json:"started"`
			Linked  int    `json:"linked_tills_told"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.NextPin == "" || resp.Data.NextPin != c.NextPin() || resp.Data.NextPin == c.Pin() || !resp.Data.Started ||
		resp.Data.Linked != 1 || resp.Error != nil {
		t.Fatalf("response %+v (served %s, next %s)", resp, c.Pin(), c.NextPin())
	}
	for {
		e, err := nextLinkFrame(t, ws)
		if err != nil {
			t.Fatalf("no hello with the next pin reached the linked till: %v", err)
		}
		if e.Type != fleetlink.TypeHello {
			continue
		}
		var h fleetlink.Hello
		if err := json.Unmarshal(e.Payload, &h); err != nil {
			t.Fatal(err)
		}
		if h.NextPin == resp.Data.NextPin {
			break
		}
	}
	mainID, _ := discovery.TillID(context.Background(), data.NewSettingsRepo(f.dp.Db))
	got := auditRows(t, f.dp, "lan_tls_rotation_started")
	if len(got) != 1 || got[0] != `till|`+mainID+`|u-manager|{"next_pin":"`+resp.Data.NextPin+`"}` {
		t.Fatalf("audit = %v", got)
	}

	// A second press keeps the pending rotation and is not audited again.
	rec = postRotateTLSKey(t, f.mux, "manager")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), resp.Data.NextPin) || !strings.Contains(rec.Body.String(), `"started":false`) {
		t.Fatalf("second press: %d %s", rec.Code, rec.Body)
	}
	if got := auditRows(t, f.dp, "lan_tls_rotation_started"); len(got) != 1 {
		t.Fatalf("second press audited again: %v", got)
	}
	body := getRoster(t, f.mux, "manager").Body.String()
	if !strings.Contains(body, `data-rotation="pending"`) || strings.Contains(body, `hx-post="/api/tills/rotate-tls-key"`) {
		t.Fatalf("roster while pending: %s", body)
	}
}
