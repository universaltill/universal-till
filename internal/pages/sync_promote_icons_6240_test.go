package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#6240: POST /api/sync/promote queues the locally held icons the
// cloud never received.

func seedPromoteIcons(t *testing.T, dp *common.Deps, mode string) {
	t.Helper()
	ctx := context.Background()
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatal(err)
	}
	if mode != "" {
		if err := dp.Settings.Set(ctx, "entitlement.catalog_master", mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`INSERT INTO items (id, sku, name, base_price, icon) VALUES ('i1', 'S6240', 'Latte', 100, 'lucide:beer')`,
		`INSERT INTO categories (id, name, icon) VALUES ('c1', 'Drinks', 'lucide:coffee')`,
		`INSERT INTO cloud_links (entity, cloud_id, local_id, version, updated_at, base) VALUES ('item', 'ci1', 'i1', 1, 'x', '{"icon":""}')`,
		`INSERT INTO cloud_links (entity, cloud_id, local_id, version, updated_at, base) VALUES ('category', 'cc1', 'c1', 1, 'x', '{"icon":""}')`,
		`DELETE FROM cloud_outbox`,
	} {
		if _, err := dp.Db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func postPromote(mux *http.ServeMux) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/sync/promote", strings.NewReader("confirm=PROMOTE"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func promoteCount6240(t *testing.T, dp *common.Deps, q string) int {
	t.Helper()
	var n int
	if err := dp.Db.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSyncPromote_MarksUnsentIcons_6240(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	seedPromoteIcons(t, dp, "cloud")
	if rec := postPromote(mux); rec.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}
	for _, e := range []string{"item", "category"} {
		if n := promoteCount6240(t, dp, `SELECT COUNT(*) FROM cloud_outbox WHERE sealed_at IS NULL AND entity = '`+e+`'`); n != 1 {
			t.Fatalf("open %s marks = %d, want 1", e, n)
		}
	}
}

func TestSyncPromote_TillMasterOpensNoMarks_6240(t *testing.T) {
	for _, mode := range []string{"till", ""} {
		t.Run("mode="+mode, func(t *testing.T) {
			t.Setenv("UT_AUTH", "off")
			mux, dp := newSyncAPITestDeps(t)
			seedPromoteIcons(t, dp, mode)
			rec := postPromote(mux)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), httpx.T("en", "tills.promoted")) {
				t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
			}
			if n := promoteCount6240(t, dp, `SELECT COUNT(*) FROM cloud_outbox`); n != 0 {
				t.Fatalf("marks = %d, want 0", n)
			}
			if n := promoteCount6240(t, dp, `SELECT COUNT(*) FROM audit_log WHERE action = 'till_promoted'`); n != 1 {
				t.Fatalf("till_promoted audit rows = %d, want 1", n)
			}
		})
	}
}

func TestSyncPromote_MarkFailureIsLocalized_6240(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	seedPromoteIcons(t, dp, "cloud")
	if _, err := dp.Db.Exec(`CREATE TRIGGER t6240 BEFORE INSERT ON cloud_outbox BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	rec := postPromote(mux)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, httpx.T("en", "sync.error.server")) || strings.Contains(body, "boom") {
		t.Fatalf("body = %q, want the localized error without raw SQL", body)
	}
	if dp.SyncPrimaryURL(context.Background()) == "" {
		t.Fatal("a failed promotion must leave the till a replica")
	}
}
