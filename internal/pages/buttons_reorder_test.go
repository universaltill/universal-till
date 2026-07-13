package pages

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/ui"
)

// The Designer's drag&drop posts FormData — a multipart body. The handler
// must parse it (plain ParseForm ignores multipart, which silently dropped
// every reorder).
func TestReorderAcceptsMultipartFormData(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	for _, s := range []string{
		`CREATE TABLE shortcut_buttons (barcode TEXT PRIMARY KEY, label TEXT, item_id TEXT, image_path TEXT, sort_order INTEGER NOT NULL DEFAULT 0)`,
		`INSERT INTO shortcut_buttons(barcode,label) VALUES ('b1','Alpha'),('b2','Beta'),('b3','Gamma')`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	d := &common.Deps{Db: db, BtnStore: ui.NewButtonStore(db)}
	mux := http.NewServeMux()
	registerButtonsAPI(mux, d)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, code := range []string{"b3", "b1", "b2"} { // new display order
		_ = mw.WriteField("codes", code)
	}
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/buttons/reorder", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("multipart reorder = %d (%s), want 204", rec.Code, rec.Body.String())
	}

	rows, err := db.Query(`SELECT barcode FROM shortcut_buttons ORDER BY sort_order`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var b string
		_ = rows.Scan(&b)
		got = append(got, b)
	}
	want := []string{"b3", "b1", "b2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("persisted order = %v, want %v", got, want)
		}
	}

	// URL-encoded posts keep working.
	req2 := httptest.NewRequest(http.MethodPost, "/api/buttons/reorder",
		bytes.NewBufferString("codes=b1&codes=b2&codes=b3"))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNoContent {
		t.Fatalf("urlencoded reorder = %d, want 204", rec2.Code)
	}
}
