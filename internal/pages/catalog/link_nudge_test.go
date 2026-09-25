package catalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ADR-0114 §2 (ut-docs#2735 review): an item photo is a file, not a row —
// it rides /api/sync/assets on the replica's pull, and no admin-table
// trigger moves for it. While linked that pull runs every 5 min, so the
// upload itself must nudge `admin`, or a replaced photo takes up to 5 min to
// reach the other tills (30 s before the link).

// linkedTill opens one link to dp's hub as "till-2" and consumes the hello,
// so the next frame it reads is the first nudge the handler under test sends.
func linkedTill(t *testing.T, dp *common.Deps) *websocket.Conn {
	t.Helper()
	cfg := fleetlink.DefaultConfig()
	cfg.PingInterval = 50 * time.Millisecond
	hub := fleetlink.NewHub(fleetlink.HubOptions{Config: cfg})
	dp.Link = hub
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.Serve(w, r, "till-2")
	}))
	t.Cleanup(func() { hub.Close(); srv.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	if e := nextLinkFrame(t, c); e.Type != fleetlink.TypeHello {
		t.Fatalf("first frame = %s, want hello", e.Type)
	}
	return c
}

// nextLinkFrame reads the next non-ping envelope, or fails after 2 s.
func nextLinkFrame(t *testing.T, c *websocket.Conn) fleetlink.Envelope {
	t.Helper()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, b, err := c.Read(ctx)
		cancel()
		if err != nil {
			t.Fatalf("no frame from the main till: %v", err)
		}
		e, err := fleetlink.Decode(b)
		if err != nil {
			t.Fatalf("undecodable frame %s: %v", b, err)
		}
		if e.Type != fleetlink.TypePing {
			return e
		}
	}
}

func TestItemPhotoUpload_NudgesLinkedTills(t *testing.T) {
	chdirToRepoRoot(t)
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })
	db := testsupport.NewCatalogTestDB(t)
	testsupport.SeedTaxCode(t, db, "tax_std", "Standard", 2000)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "SKU1", Name: "Latte", BasePrice: 250, TaxCodeID: "tax_std", IsActive: true})
	dp := &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}}
	mux := http.NewServeMux()
	Register(mux, dp)
	c := linkedTill(t, dp)

	body, ct := multipartUpload(t, map[string]string{"item_id": "itm1"}, "photo.png", validPNG(t))
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/item/image", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload = %d: %s", rec.Code, rec.Body.String())
	}

	e := nextLinkFrame(t, c)
	var p fleetlink.SyncPayload
	_ = json.Unmarshal(e.Payload, &p)
	if e.Type != fleetlink.TypeSync || len(p.Scopes) != 1 || p.Scopes[0] != fleetlink.ScopeAdmin {
		t.Fatalf("after a photo upload got %s %s, want sync {admin}", e.Type, e.Payload)
	}
}
