package channelactions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serve(t *testing.T, h http.HandlerFunc) (string, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization")+" "+r.Header.Get("Content-Type")+" "+string(b))
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/api/", &seen
}

func TestHTTPPoster_SendsTheContractedRequest(t *testing.T) {
	base, seen := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"results": []map[string]any{
			{"action_id": "a-1", "status": "applied", "revision": 5, "writeback": "queued", "current": map[string]any{"revision": 5}},
			{"action_id": "a-2", "status": "refused", "code": "order_changed", "revision": 2},
		}}, "error": nil})
	})
	post := NewHTTPPoster(http.DefaultClient, base, "store-1", "tok-secret")
	res, err := post(context.Background(), []Action{
		{ActionID: "a-1", Action: "accept", BaseRevision: 1},
		{ActionID: "a-2", Action: "cancel", BaseRevision: 1, Cancel: &Cancel{Reason: "other"}},
	})
	if err != nil || len(res) != 2 || res[0].Status != "applied" || res[0].Revision != 5 || res[0].Writeback != "queued" ||
		res[0].Current["revision"] != float64(5) || res[1].Code != "order_changed" {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	want := "POST /api/v1/stores/channel/orders/actions Bearer tok-secret application/json "
	if len(*seen) != 1 || !strings.HasPrefix((*seen)[0], want) {
		t.Fatalf("request = %v", *seen)
	}
	var body struct {
		StoreID string           `json:"store_id"`
		Actions []map[string]any `json:"actions"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix((*seen)[0], want)), &body); err != nil || body.StoreID != "store-1" || len(body.Actions) != 2 {
		t.Fatalf("body = %+v %v", body, err)
	}
}

func TestHTTPPoster_StatusMapping(t *testing.T) {
	cases := []struct {
		status int
		code   string
		kind   string
	}{
		{403, "main_till_required", "stop"},
		{403, "store_mismatch", "stop"},
		{403, "device_credential_required", "stop"},
		{403, "something_new", "status"},
		{403, "", "status"},
		{400, "invalid_request", "bad"},
		{401, "unauthorized", "status"},
		{402, "subscription_read_only", "status"},
		{404, "", "status"},
		{405, "method_not_allowed", "status"},
		{409, "channel_not_enabled", "status"},
		{500, "", "status"},
		{503, "actions_unavailable", "status"},
	}
	for _, c := range cases {
		base, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			if c.code != "" {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": map[string]any{"code": c.code, "message": "tok-secret leaked?"}})
			}
		})
		_, err := NewHTTPPoster(http.DefaultClient, base, "store-1", "tok-secret")(context.Background(), []Action{{ActionID: "a"}})
		var stop *StopError
		var bad *BadRequestError
		var st *StatusError
		switch c.kind {
		case "stop":
			if !errors.As(err, &stop) || stop.Code != c.code || stop.Status != c.status {
				t.Errorf("%d %s: err = %v", c.status, c.code, err)
			}
		case "bad":
			if !errors.As(err, &bad) || bad.Code != c.code {
				t.Errorf("%d %s: err = %v", c.status, c.code, err)
			}
		default:
			if !errors.As(err, &st) || st.Status != c.status || st.Code != c.code {
				t.Errorf("%d %s: err = %v", c.status, c.code, err)
			}
		}
		if err != nil && strings.Contains(err.Error(), "tok-secret") {
			t.Errorf("%d %s: the token is in the error: %v", c.status, c.code, err)
		}
	}
}

func TestHTTPPoster_TransportErrorNeverCarriesTheToken(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL + "/api"
	srv.Close() // connection refused
	_, err := NewHTTPPoster(http.DefaultClient, url, "store-1", "tok-secret")(context.Background(), []Action{{ActionID: "a"}})
	if err == nil || strings.Contains(err.Error(), "tok-secret") {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPPoster_HostileCodeIsNotEchoed(t *testing.T) {
	base, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"error":{"code":"<b>tok</b>"}}`))
	})
	_, err := NewHTTPPoster(http.DefaultClient, base, "s", "t")(context.Background(), []Action{{ActionID: "a"}})
	var st *StatusError
	if !errors.As(err, &st) || st.Code != "" || strings.Contains(err.Error(), "<b>") {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPPoster_AnswerBodyIsCappedAt4MiB(t *testing.T) {
	base, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"results":[{"action_id":"a","status":"applied","current":{"x":"`))
		chunk := strings.Repeat("a", 1<<20)
		for i := 0; i < 5; i++ {
			_, _ = w.Write([]byte(chunk))
		}
		_, _ = w.Write([]byte(`"}}]}}`))
	})
	_, err := NewHTTPPoster(http.DefaultClient, base, "s", "t")(context.Background(), []Action{{ActionID: "a"}})
	if err == nil {
		t.Fatal("an answer over 4 MiB must be refused")
	}
}

func TestHTTPPoster_AnswerWithoutDataIsAnError(t *testing.T) {
	base, _ := serve(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"data":null,"error":null}`)) })
	if _, err := NewHTTPPoster(http.DefaultClient, base, "s", "t")(context.Background(), []Action{{ActionID: "a"}}); err == nil {
		t.Fatal("want an error")
	}
}
