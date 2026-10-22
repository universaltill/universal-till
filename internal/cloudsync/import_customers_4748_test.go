package cloudsync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/logging"
)

// import_customers routing (ADR-0168 §5, ut-docs#4748). The sealed payload is
// opened by the hook; this package only moves it and reports the outcome.

type resultCapture struct {
	mu   sync.Mutex
	body []map[string]any
}

func (c *resultCapture) server(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/stores/directives/result", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		c.mu.Lock()
		c.body = append(c.body, b)
		c.mu.Unlock()
		_, _ = w.Write([]byte(`{"data":{},"error":null}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestImportCustomersIsMainTillOnly(t *testing.T) {
	if !mainTillOnlyTypes["import_customers"] {
		t.Fatal("import_customers must stay pending for the main till on a satellite")
	}
}

func TestHandleImportCustomers_PostsTheResultData(t *testing.T) {
	var got ImportCustomersDirective
	hooks := Hooks{ImportCustomers: func(_ context.Context, d ImportCustomersDirective) (ImportCustomersResult, error) {
		got = d
		return ImportCustomersResult{Message: "2 created", Data: map[string]any{"counts": map[string]any{"created": 2}}}, nil
	}}
	cap := &resultCapture{}
	srv := cap.server(t)
	cfg, _ := bootEnrolledTill(t, srv.URL)
	d := directive{ID: "dir-7", Type: "import_customers", Payload: map[string]any{"job_id": "job-3", "chunk_index": 4.0, "sealed": "v1.x.y.z"}}
	handleImportCustomers(context.Background(), cfg, d, hooks)
	if got.DirectiveID != "dir-7" || got.JobID != "job-3" || got.ChunkIndex != 4 || got.Sealed != "v1.x.y.z" {
		t.Fatalf("hook got %+v", got)
	}
	if len(cap.body) != 1 || cap.body[0]["status"] != "applied" || cap.body[0]["directive_id"] != "dir-7" {
		t.Fatalf("posted %v", cap.body)
	}
	data, _ := cap.body[0]["data"].(map[string]any)
	if counts, _ := data["counts"].(map[string]any); counts["created"] != 2.0 {
		t.Fatalf("result data = %v", data)
	}
}

func TestHandleImportCustomers_RefusalsFailWithoutLeaking(t *testing.T) {
	cap := &resultCapture{}
	srv := cap.server(t)
	cfg, _ := bootEnrolledTill(t, srv.URL)
	var logs strings.Builder
	var mu sync.Mutex
	restore := logging.CaptureForTest(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return logs.Write(p)
	}))
	defer restore()

	for name, c := range map[string]struct {
		payload map[string]any
		hooks   Hooks
		wantMsg string
	}{
		"no hook":      {map[string]any{"job_id": "j", "chunk_index": 0.0, "sealed": "s"}, Hooks{}, "not supported on this till"},
		"no sealed":    {map[string]any{"job_id": "j", "chunk_index": 0.0}, Hooks{ImportCustomers: failHook(nil)}, "missing sealed"},
		"no job":       {map[string]any{"chunk_index": 0.0, "sealed": "s"}, Hooks{ImportCustomers: failHook(nil)}, "missing job_id"},
		"bad chunk":    {map[string]any{"job_id": "j", "chunk_index": -1.0, "sealed": "s"}, Hooks{ImportCustomers: failHook(nil)}, "invalid chunk_index"},
		"hook refuses": {map[string]any{"job_id": "j", "chunk_index": 0.0, "sealed": "SEALED-SECRET"}, Hooks{ImportCustomers: failHook(errors.New("refused for a reason"))}, "refused for a reason"},
	} {
		cap.body = nil
		handleImportCustomers(context.Background(), cfg, directive{ID: "d", Type: "import_customers", Payload: c.payload}, c.hooks)
		if len(cap.body) != 1 || cap.body[0]["status"] != "failed" || !strings.Contains(cap.body[0]["message"].(string), c.wantMsg) {
			t.Errorf("%s: posted %v, want failed containing %q", name, cap.body, c.wantMsg)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(logs.String(), "SEALED-SECRET") {
		t.Fatal("the sealed value reached the log")
	}
}

func failHook(err error) func(context.Context, ImportCustomersDirective) (ImportCustomersResult, error) {
	return func(context.Context, ImportCustomersDirective) (ImportCustomersResult, error) {
		return ImportCustomersResult{}, err
	}
}
