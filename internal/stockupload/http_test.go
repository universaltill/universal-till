package stockupload_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/stockupload"
)

func serve(t *testing.T, h http.HandlerFunc) (string, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization")+" "+string(b))
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/api/", &seen
}

func TestHTTPPoster_SendsTheContractedBodyWithTheDeviceCredential(t *testing.T) {
	base, seen := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"results": []map[string]any{
			{"seq": 7, "status": "applied"}, {"seq": 8, "status": "stale", "stored_seq": 9},
		}}, "error": nil})
	})
	post := stockupload.NewHTTPPoster(http.DefaultClient, base, "store-1", "tok-1")
	ans, err := post(context.Background(), stockupload.Request{StreamID: "s-1", StreamStartedAt: time.Date(2026, 10, 24, 9, 30, 0, 5000, time.UTC), TillVersion: "0.34.18", Rows: []stockupload.Row{
		{ItemID: cloudA, LocationID: "loc", OnHandMilli: 12500, Seq: 7},
		{ItemID: cloudA, VariantID: cloudB, LocationID: "loc", OnHandMilli: -1, Seq: 8},
	}})
	if err != nil || len(ans) != 2 || ans[1].StoredSeq != 9 {
		t.Fatalf("ans = %+v, %v", ans, err)
	}
	want := `POST /api/v1/stores/stock/levels Bearer tok-1 `
	if len(*seen) != 1 || !strings.HasPrefix((*seen)[0], want) {
		t.Fatalf("request = %v", *seen)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix((*seen)[0], want)), &body); err != nil {
		t.Fatal(err)
	}
	if body["store_id"] != "store-1" || body["stream_id"] != "s-1" || body["till_version"] != "0.34.18" || body["stream_started_at"] != "2026-10-24T09:30:00.000005Z" {
		t.Fatalf("body = %v", body)
	}
	rows := body["rows"].([]any)
	r0 := rows[0].(map[string]any)
	if r0["on_hand_milli"] != float64(12500) || r0["item_id"] != cloudA || r0["seq"] != float64(7) {
		t.Fatalf("row 0 = %v", r0)
	}
	if _, has := r0["variant_id"]; has {
		t.Fatal("an item row carries a variant_id")
	}
	if strings.Contains((*seen)[0], "12500.") || strings.Contains((*seen)[0], "e+") {
		t.Fatalf("a quantity left the till as a float: %s", (*seen)[0])
	}
}

func TestHTTPPoster_RefusalsAreStopsAndOthersAreTransient(t *testing.T) {
	for status, wantStop := range map[int]bool{
		400: true, 401: true, 402: true, 403: true, 404: true, 405: true, 409: true,
		429: false, 500: false, 502: false, 503: false,
	} {
		base, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": map[string]any{"code": "not_main_till", "message": "x"}})
		})
		_, err := stockupload.NewHTTPPoster(http.DefaultClient, base, "s", "t")(context.Background(), stockupload.Request{Rows: []stockupload.Row{{Seq: 1}}})
		var stop *stockupload.StopError
		var st *stockupload.StatusError
		switch {
		case wantStop && errors.As(err, &stop) && stop.Status == status && stop.Code == "not_main_till":
		case !wantStop && errors.As(err, &st) && st.Status == status:
		default:
			t.Errorf("status %d: err = %#v", status, err)
		}
	}
}

func TestHTTPPoster_TokenNeverAppearsInAnError(t *testing.T) {
	base, _ := serve(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	_, err := stockupload.NewHTTPPoster(http.DefaultClient, base, "s", "super-secret-token")(context.Background(), stockupload.Request{})
	if err == nil || strings.Contains(err.Error(), "super-secret-token") {
		t.Fatalf("err = %v", err)
	}
	_, err = stockupload.NewHTTPPoster(http.DefaultClient, "http://127.0.0.1:1/", "s", "super-secret-token")(context.Background(), stockupload.Request{})
	if err == nil || strings.Contains(err.Error(), "super-secret-token") {
		t.Fatalf("transport err = %v", err)
	}
}

func TestHTTPPoster_BadAnswerBodiesAreErrors(t *testing.T) {
	for name, body := range map[string]string{"not json": "{", "no data": `{"data":null}`} {
		base, _ := serve(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) })
		if _, err := stockupload.NewHTTPPoster(http.DefaultClient, base, "s", "t")(context.Background(), stockupload.Request{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
