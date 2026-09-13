package cloudsync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/diagnostics"
)

// withDiagnosticsState isolates the process-wide diagnostics session/ring
// and pending dir for one test.
func withDiagnosticsState(t *testing.T) {
	t.Helper()
	orig := diagnostics.PendingDir
	diagnostics.PendingDir = t.TempDir()
	t.Cleanup(func() {
		diagnostics.PendingDir = orig
		// Leave no session behind for the next test in this package.
		_, _ = diagnostics.Stop(context.Background(), &nopKV{}, diagnostics.EndedStopped)
	})
}

type nopKV struct{}

func (nopKV) Get(context.Context, string) (string, bool, error) { return "", false, nil }
func (nopKV) Set(context.Context, string, string) error         { return nil }

// fakeDiagCloud is the ut-cloud diagnostics contract (PRs #138/#140):
// POST /v1/stores/diagnostics/activate and /batch, bearer-authenticated
// with the store token, error envelope {error:{code,message}}.
type fakeDiagCloud struct {
	mu          sync.Mutex
	activations []map[string]any
	batches     []map[string]any
	activate    func(w http.ResponseWriter, body map[string]any)
	batch       func(w http.ResponseWriter, body map[string]any) // per-call override
	syncBodies  []map[string]any
}

func (f *fakeDiagCloud) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/stores/diagnostics/activate", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-1" {
			writeErr(w, 401, "unauthorized", "invalid store token")
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.activations = append(f.activations, body)
		f.mu.Unlock()
		f.activate(w, body)
	})
	mux.HandleFunc("/v1/stores/diagnostics/batch", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-1" {
			writeErr(w, 401, "unauthorized", "invalid store token")
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.batches = append(f.batches, body)
		f.mu.Unlock()
		if f.batch != nil {
			f.batch(w, body)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"batch_id": "b-1", "stored": true}})
	})
	mux.HandleFunc("/v1/stores/sync", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.syncBodies = append(f.syncBodies, body)
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"directives": []any{}}})
	})
	mux.HandleFunc("/v1/stores/directives/result", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ok": true}})
	})
	mux.HandleFunc("/v1/stores/issue-reports", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ok": true}})
	})
	return mux
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": msg}})
}

func okActivate(w http.ResponseWriter, _ map[string]any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"session_id": "8b4d0d3e-1111-4c5b-9999-0123456789ab"}})
}

// Redemption posts {store_id, code, device_id} with the store's existing
// bearer token — the same credential every other /v1/stores/* call sends —
// and hands back the session id from the data envelope.
func TestActivateDiagnostics_SendsContractAndReturnsSessionID(t *testing.T) {
	cloud := &fakeDiagCloud{activate: okActivate}
	srv := httptest.NewServer(cloud.handler())
	t.Cleanup(srv.Close)

	id, err := ActivateDiagnostics(context.Background(), testCfg(srv.URL), "  abc123  ")
	if err != nil {
		t.Fatalf("ActivateDiagnostics: %v", err)
	}
	if id != "8b4d0d3e-1111-4c5b-9999-0123456789ab" {
		t.Fatalf("session id = %q", id)
	}
	if len(cloud.activations) != 1 {
		t.Fatalf("activations = %d", len(cloud.activations))
	}
	got := cloud.activations[0]
	if got["store_id"] != "store-1" || got["code"] != "abc123" {
		t.Fatalf("body = %v, want store_id store-1 and trimmed code abc123", got)
	}
	if _, ok := got["device_id"].(string); !ok {
		t.Fatalf("body = %v, want a device_id string", got)
	}
}

// Every failure class the endpoint documents comes back typed so the
// Settings handler can pick a localized message: the cloud's own
// {error:{code}} for 4xx, errNotRegistered for an unenrolled till, and a
// plain error for an unreachable host.
func TestActivateDiagnostics_ErrorClasses(t *testing.T) {
	cases := []struct {
		name   string
		status int
		code   string
	}{
		{"invalid code", 403, "invalid_code"},
		{"consumed/expired", 409, "code_unavailable"},
		{"bad request", 400, "invalid_request"},
		{"bad token", 401, "unauthorized"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cloud := &fakeDiagCloud{activate: func(w http.ResponseWriter, _ map[string]any) {
				writeErr(w, tc.status, tc.code, "nope")
			}}
			srv := httptest.NewServer(cloud.handler())
			t.Cleanup(srv.Close)
			_, err := ActivateDiagnostics(context.Background(), testCfg(srv.URL), "x")
			var ce *CloudError
			if !errors.As(err, &ce) {
				t.Fatalf("err = %v, want *CloudError", err)
			}
			if ce.Status != tc.status || ce.Code != tc.code {
				t.Fatalf("CloudError = %+v, want status %d code %q", ce, tc.status, tc.code)
			}
		})
	}

	if _, err := ActivateDiagnostics(context.Background(), &config.Config{}, "x"); !errors.Is(err, errNotRegistered) {
		t.Fatalf("unregistered: err = %v, want errNotRegistered", err)
	}
	var ce *CloudError
	if _, err := ActivateDiagnostics(context.Background(), testCfg("http://127.0.0.1:1"), "x"); err == nil {
		t.Fatal("unreachable cloud: err = nil")
	} else if errors.As(err, &ce) {
		t.Fatalf("unreachable cloud must not be a CloudError: %v", err)
	}
	if _, err := ActivateDiagnostics(context.Background(), testCfg("http://127.0.0.1:1"), "   "); err == nil {
		t.Fatal("blank code accepted")
	}
}

func activateLocally(t *testing.T, db diagnostics.KV) {
	t.Helper()
	if err := diagnostics.Activate(context.Background(), db, "8b4d0d3e-1111-4c5b-9999-0123456789ab", time.Now()); err != nil {
		t.Fatalf("Activate: %v", err)
	}
}

// The tick flushes the ring, uploads every pending batch FIFO with the
// contract body {store_id, session_id, seq, events}, and deletes a batch
// locally ONLY once the cloud acknowledged it.
func TestTickUploadsDiagnosticBatchesAndDeletesOnAck(t *testing.T) {
	withDiagnosticsState(t)
	withTempPendingDir(t)
	cloud := &fakeDiagCloud{activate: okActivate}
	srv := httptest.NewServer(cloud.handler())
	t.Cleanup(srv.Close)
	db := testDB(t)
	kv := data.NewSettingsRepo(db)
	activateLocally(t, kv)
	diagnostics.Emit(diagnostics.Gap{DroppedCount: 3})

	if err := Tick(context.Background(), testCfg(srv.URL), db, Hooks{}); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(cloud.batches) != 1 {
		t.Fatalf("batches uploaded = %d, want 1", len(cloud.batches))
	}
	b := cloud.batches[0]
	if b["store_id"] != "store-1" || b["session_id"] != "8b4d0d3e-1111-4c5b-9999-0123456789ab" || b["seq"] != float64(0) {
		t.Fatalf("batch body = %v", b)
	}
	evs, _ := b["events"].([]any)
	if len(evs) != 1 {
		t.Fatalf("events = %v, want the one gap event", b["events"])
	}
	if pending, _ := diagnostics.Pending(); len(pending) != 0 {
		t.Fatalf("acked batch still on disk: %+v", pending)
	}
	if !diagnostics.Active() {
		t.Fatal("a successful upload must leave the session active")
	}
}

// A 5xx / network failure is retryable: the batch stays on disk untouched
// and the next tick re-sends it with the SAME seq (the cloud dedups).
func TestTickRetriesDiagnosticBatchOnTransientFailure(t *testing.T) {
	withDiagnosticsState(t)
	withTempPendingDir(t)
	fail := true
	cloud := &fakeDiagCloud{activate: okActivate}
	cloud.batch = func(w http.ResponseWriter, _ map[string]any) {
		if fail {
			writeErr(w, 500, "save_failed", "db down")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"batch_id": "b", "stored": true}})
	}
	srv := httptest.NewServer(cloud.handler())
	t.Cleanup(srv.Close)
	db := testDB(t)
	activateLocally(t, data.NewSettingsRepo(db))
	diagnostics.Emit(diagnostics.Gap{DroppedCount: 1})

	_ = Tick(context.Background(), testCfg(srv.URL), db, Hooks{})
	if pending, _ := diagnostics.Pending(); len(pending) != 1 {
		t.Fatalf("after a 500 the batch must stay pending, got %d", len(pending))
	}
	if !diagnostics.Active() {
		t.Fatal("a transient failure must not deactivate the session")
	}
	fail = false
	_ = Tick(context.Background(), testCfg(srv.URL), db, Hooks{})
	if len(cloud.batches) != 2 || cloud.batches[1]["seq"] != float64(0) {
		t.Fatalf("retry: %d uploads, second seq = %v, want 2 uploads both seq 0", len(cloud.batches), cloud.batches[len(cloud.batches)-1]["seq"])
	}
	if pending, _ := diagnostics.Pending(); len(pending) != 0 {
		t.Fatalf("acked retry still on disk: %+v", pending)
	}
}

// 409 session_not_active is TERMINAL: that batch is discarded, not
// retried, the rest of the session's queue is drained in one step, and the
// local flag flips off — the same disposition as a revoke.
func TestTick409DrainsSessionAndDeactivates(t *testing.T) {
	withDiagnosticsState(t)
	withTempPendingDir(t)
	cloud := &fakeDiagCloud{activate: okActivate}
	cloud.batch = func(w http.ResponseWriter, _ map[string]any) {
		writeErr(w, 409, "session_not_active", "diagnostic session is revoked — stop uploading")
	}
	srv := httptest.NewServer(cloud.handler())
	t.Cleanup(srv.Close)
	db := testDB(t)
	kv := data.NewSettingsRepo(db)
	activateLocally(t, kv)
	// Two batches on disk before the tick.
	diagnostics.Emit(diagnostics.Gap{DroppedCount: 1})
	if err := diagnostics.Flush(context.Background(), kv); err != nil {
		t.Fatal(err)
	}
	diagnostics.Emit(diagnostics.Gap{DroppedCount: 2})
	if err := diagnostics.Flush(context.Background(), kv); err != nil {
		t.Fatal(err)
	}

	_ = Tick(context.Background(), testCfg(srv.URL), db, Hooks{})
	if len(cloud.batches) != 1 {
		t.Fatalf("uploads = %d, want exactly 1 (the second must never be attempted after a 409)", len(cloud.batches))
	}
	if pending, _ := diagnostics.Pending(); len(pending) != 0 {
		t.Fatalf("queue not drained after 409: %+v", pending)
	}
	if diagnostics.Active() {
		t.Fatal("still active after a terminal 409")
	}
	if v, _, _ := kv.Get(context.Background(), "diagnostics.ended_reason"); v != diagnostics.EndedRejected {
		t.Fatalf("ended_reason = %q, want %q", v, diagnostics.EndedRejected)
	}
	if v, _, _ := kv.Get(context.Background(), "diagnostics.stop_report"); v != "" {
		t.Fatalf("a 409 must not queue a stop report (the cloud already knows), got %q", v)
	}
}

// 404 session_not_found is TERMINAL for the whole session too, same as 409
// — the session row itself is gone server-side (a GDPR crypto-shred, a
// merchant delete, an env reset), so retrying is exactly as pointless as
// retrying against a revoked one (review finding, ut-docs#2169: the
// original code only special-cased 409 and treated 404 as a plain
// retryable failure, which would retry forever against a session that can
// never come back).
func TestTick404DrainsSessionAndDeactivates(t *testing.T) {
	withDiagnosticsState(t)
	withTempPendingDir(t)
	cloud := &fakeDiagCloud{activate: okActivate}
	cloud.batch = func(w http.ResponseWriter, _ map[string]any) {
		writeErr(w, 404, "session_not_found", "no such diagnostic session for this store")
	}
	srv := httptest.NewServer(cloud.handler())
	t.Cleanup(srv.Close)
	db := testDB(t)
	kv := data.NewSettingsRepo(db)
	activateLocally(t, kv)
	diagnostics.Emit(diagnostics.Gap{DroppedCount: 1})
	if err := diagnostics.Flush(context.Background(), kv); err != nil {
		t.Fatal(err)
	}
	diagnostics.Emit(diagnostics.Gap{DroppedCount: 2})
	if err := diagnostics.Flush(context.Background(), kv); err != nil {
		t.Fatal(err)
	}

	_ = Tick(context.Background(), testCfg(srv.URL), db, Hooks{})
	if len(cloud.batches) != 1 {
		t.Fatalf("uploads = %d, want exactly 1 (the second must never be attempted after a 404)", len(cloud.batches))
	}
	if pending, _ := diagnostics.Pending(); len(pending) != 0 {
		t.Fatalf("queue not drained after 404: %+v", pending)
	}
	if diagnostics.Active() {
		t.Fatal("still active after a terminal 404")
	}
}

// 400 invalid_request/invalid_event is terminal for that ONE BATCH only,
// not the whole session — the cloud's own second-pass revalidation
// rejected this specific payload, and resending identical bytes can never
// succeed, but a LATER batch in the same (or a different) session may
// still be perfectly valid. Review finding (ut-docs#2169): the original
// code treated every non-409 status, including 400, as a transient
// failure and `return`ed on it — so a single permanently-rejected batch
// silently head-of-line-blocked every batch queued behind it, for every
// session, until eviction (up to maxPendingBatches later) finally dropped
// it. This test proves the fix: the bad batch is discarded without a
// retry, and the good batch right behind it in the SAME session still
// uploads and gets acked in the SAME tick.
func TestTick400DiscardsOnlyThatBatchAndContinuesQueue(t *testing.T) {
	withDiagnosticsState(t)
	withTempPendingDir(t)
	cloud := &fakeDiagCloud{activate: okActivate}
	cloud.batch = func(w http.ResponseWriter, body map[string]any) {
		if body["seq"] == float64(0) {
			writeErr(w, 400, "invalid_event", "event failed validation")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"batch_id": "b-1", "stored": true}})
	}
	srv := httptest.NewServer(cloud.handler())
	t.Cleanup(srv.Close)
	db := testDB(t)
	kv := data.NewSettingsRepo(db)
	activateLocally(t, kv)
	// seq 0 (rejected) and seq 1 (valid) both queued before the tick.
	diagnostics.Emit(diagnostics.Gap{DroppedCount: 1})
	if err := diagnostics.Flush(context.Background(), kv); err != nil {
		t.Fatal(err)
	}
	diagnostics.Emit(diagnostics.Gap{DroppedCount: 2})
	if err := diagnostics.Flush(context.Background(), kv); err != nil {
		t.Fatal(err)
	}

	_ = Tick(context.Background(), testCfg(srv.URL), db, Hooks{})
	if len(cloud.batches) != 2 {
		t.Fatalf("uploads = %d, want 2 (the 400 must not block the batch behind it)", len(cloud.batches))
	}
	if pending, _ := diagnostics.Pending(); len(pending) != 0 {
		t.Fatalf("queue not fully drained (rejected batch discarded, good batch acked): %+v", pending)
	}
	if !diagnostics.Active() {
		t.Fatal("a batch-level 400 must not deactivate the session — only the session-level 409/404 do")
	}
}

// diagnostic_mode_revoke rides apply() like every other directive: nil
// hook fails cleanly, a blank session_id is rejected before the hook runs,
// and the hook's message/error map to applied/failed.
func TestApplyDiagnosticModeRevoke(t *testing.T) {
	status, msg := apply(context.Background(), directive{Type: "diagnostic_mode_revoke", Payload: map[string]any{"session_id": "s1"}}, Hooks{})
	if status != "failed" || msg == "" {
		t.Fatalf("nil hook: %q %q", status, msg)
	}
	var got string
	hooks := Hooks{DiagnosticModeRevoke: func(_ context.Context, id string) (string, error) {
		got = id
		return "revoked", nil
	}}
	status, _ = apply(context.Background(), directive{Type: "diagnostic_mode_revoke", Payload: map[string]any{}}, hooks)
	if status != "failed" || got != "" {
		t.Fatalf("blank session_id: status=%q hook called with %q", status, got)
	}
	status, msg = apply(context.Background(), directive{Type: "diagnostic_mode_revoke", Payload: map[string]any{"session_id": " s1 "}}, hooks)
	if status != "applied" || msg != "revoked" || got != "s1" {
		t.Fatalf("revoke: status=%q msg=%q hook got %q", status, msg, got)
	}
	hooks.DiagnosticModeRevoke = func(context.Context, string) (string, error) { return "", errors.New("boom") }
	if status, _ = apply(context.Background(), directive{Type: "diagnostic_mode_revoke", Payload: map[string]any{"session_id": "s1"}}, hooks); status != "failed" {
		t.Fatalf("hook error: status=%q", status)
	}
}

// End to end through Tick: the cloud's revoke directive stops capture and
// upload on the next sync, drains the queue, and is acked as applied.
func TestTickAppliesRevokeDirective(t *testing.T) {
	withDiagnosticsState(t)
	withTempPendingDir(t)
	fc := &fakeCloud{directives: []map[string]any{
		{"id": "d9", "type": "diagnostic_mode_revoke", "payload": map[string]any{"session_id": "8b4d0d3e-1111-4c5b-9999-0123456789ab"}},
	}}
	srv := httptest.NewServer(fc.handler())
	t.Cleanup(srv.Close)
	db := testDB(t)
	kv := data.NewSettingsRepo(db)
	activateLocally(t, kv)
	diagnostics.Emit(diagnostics.Gap{DroppedCount: 1})
	if err := diagnostics.Flush(context.Background(), kv); err != nil {
		t.Fatal(err)
	}
	hooks := Hooks{DiagnosticModeRevoke: func(ctx context.Context, id string) (string, error) {
		return diagnostics.Revoke(ctx, kv, id)
	}}
	// The batch upload happens BEFORE directives on the same tick; the
	// fakeCloud has no /batch route, so that upload fails (retryable) and
	// the batch is still on disk when the revoke arrives — exactly the
	// "drain in one step" case.
	if err := Tick(context.Background(), testCfg(srv.URL), db, hooks); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if diagnostics.Active() {
		t.Fatal("still active after the revoke directive")
	}
	if pending, _ := diagnostics.Pending(); len(pending) != 0 {
		t.Fatalf("queue not drained by revoke: %+v", pending)
	}
	if len(fc.results) != 1 || fc.results[0]["status"] != "applied" || fc.results[0]["directive_id"] != "d9" {
		t.Fatalf("results = %v, want d9 applied", fc.results)
	}
}

// A local stop is reported best-effort on the next heartbeat's device
// record and the marker is cleared once that push succeeded.
func TestTickReportsLocalStopOnHeartbeatOnce(t *testing.T) {
	withDiagnosticsState(t)
	withTempPendingDir(t)
	cloud := &fakeDiagCloud{activate: okActivate}
	srv := httptest.NewServer(cloud.handler())
	t.Cleanup(srv.Close)
	db := testDB(t)
	kv := data.NewSettingsRepo(db)
	activateLocally(t, kv)
	if _, err := diagnostics.Stop(context.Background(), kv, diagnostics.EndedStopped); err != nil {
		t.Fatal(err)
	}

	if err := Tick(context.Background(), testCfg(srv.URL), db, Hooks{}); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(cloud.syncBodies) != 1 {
		t.Fatalf("sync pushes = %d", len(cloud.syncBodies))
	}
	devices := cloud.syncBodies[0]["devices"].([]any)
	dev := devices[0].(map[string]any)
	rep, ok := dev["diagnostics"].(map[string]any)
	if !ok || rep["session_id"] != "8b4d0d3e-1111-4c5b-9999-0123456789ab" || rep["status"] != "stopped" {
		t.Fatalf("device diagnostics report = %v", dev["diagnostics"])
	}
	// Delivered once: the second heartbeat carries nothing.
	if err := Tick(context.Background(), testCfg(srv.URL), db, Hooks{}); err != nil {
		t.Fatalf("Tick 2: %v", err)
	}
	dev = cloud.syncBodies[1]["devices"].([]any)[0].(map[string]any)
	if _, still := dev["diagnostics"]; still {
		t.Fatalf("stop report repeated after delivery: %v", dev["diagnostics"])
	}
}
