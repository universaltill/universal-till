package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#3697: POST /api/sync/plugin-records (main side) and the replica
// push step.

type recordsAsk struct {
	Collection string          `json:"collection"`
	ID         string          `json:"id"`
	FromTill   string          `json:"from_till_id"`
	CreatedAt  string          `json:"created_at"`
	Payload    json.RawMessage `json:"payload"`
}

// recordsPlugin is a fake installed plugin on a main till: manifest on disk,
// DB rows, grants, hook, and a bus handler that answers plugin.records.received.ask.
type recordsPlugin struct {
	id    string
	asks  atomic.Int32
	mu    sync.Mutex
	got   []recordsAsk
	reply func(recordsAsk) string // JSON answer
}

var recordsPluginSeq atomic.Int32

func installRecordsPlugin(t *testing.T, dp *common.Deps, collections []string, opts ...string) *recordsPlugin {
	t.Helper()
	p := &recordsPlugin{id: fmt.Sprintf("com.test.recs%d", recordsPluginSeq.Add(1)),
		reply: func(recordsAsk) string { return `{"ok":true}` }}
	root := t.TempDir()
	prev := paths.DataDir()
	paths.Init(root)
	t.Cleanup(func() { paths.Init(prev) })
	cols, _ := json.Marshal(collections)
	manifest := fmt.Sprintf(`{"id":%q,"name":"t","version":"1.0.0","entrypoint":"./plugin.wasm","runtime":"wasm","permissions":["records:sync","events:receive"],"records_forward":{"collections":%s},"hooks":[{"event":"plugin.records.received.ask","action":"records.apply"}]}`, p.id, cols)
	dir := filepath.Join(root, "plugins", p.id, "1.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	active := 1
	for _, o := range opts {
		if o == "inactive" {
			active = 0
		}
	}
	d := dp.Db
	if _, err := d.Exec(`INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
		VALUES (?, '1.0.0', 't', 'wasm', './plugin.wasm', 'file://x', 'x', '0.0.1', '1.0', datetime('now'))`, p.id); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES (?, 't', '1.0.0', './plugin.wasm', 'wasm', ?)`, p.id, active); err != nil {
		t.Fatal(err)
	}
	repo := data.NewPluginRepo(d)
	perms := []string{"records:sync", "events:receive"}
	if err := repo.InsertPluginPermissions(context.Background(), nil, p.id, perms); err != nil {
		t.Fatal(err)
	}
	for _, perm := range perms {
		if err := repo.SetPermission(context.Background(), p.id, perm, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES (?, ?, 'plugin.records.received.ask', 'records.apply', 1)`, p.id+"-h", p.id); err != nil {
		t.Fatal(err)
	}
	if _, err := plugins.SharedBus(d).SubscribeWithHandler(context.Background(), p.id, []string{plugins.RecordsReceivedEvent},
		func(_ context.Context, ev plugins.Event) (json.RawMessage, error) {
			var a recordsAsk
			_ = json.Unmarshal(ev.Payload, &a)
			p.asks.Add(1)
			p.mu.Lock()
			p.got = append(p.got, a)
			p.mu.Unlock()
			return json.RawMessage(p.reply(a)), nil
		}); err != nil {
		t.Fatal(err)
	}
	return p
}

type recWire struct {
	PluginID   string          `json:"plugin_id"`
	Collection string          `json:"collection"`
	ID         string          `json:"id"`
	CreatedAt  string          `json:"created_at"`
	Payload    json.RawMessage `json:"payload"`
}

func wireRec(pid, col, id string) recWire {
	return recWire{PluginID: pid, Collection: col, ID: id, CreatedAt: time.Now().UTC().Format(time.RFC3339), Payload: json.RawMessage(`{"n":1}`)}
}

type recResp struct {
	Data struct {
		Results []struct {
			PluginID   string `json:"plugin_id"`
			Collection string `json:"collection"`
			ID         string `json:"id"`
			Status     string `json:"status"`
			Reason     string `json:"reason"`
		} `json:"results"`
	} `json:"data"`
	Error any `json:"error"`
}

func postRecords(t *testing.T, mux http.Handler, bearer string, recs ...recWire) (*httptest.ResponseRecorder, recResp) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"records": recs})
	return postRecordsRaw(t, mux, bearer, body)
}

func postRecordsRaw(t *testing.T, mux http.Handler, bearer string, body []byte) (*httptest.ResponseRecorder, recResp) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/plugin-records", bytes.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var resp recResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec, resp
}

func newRecordsMain(t *testing.T) (*http.ServeMux, *common.Deps, string) {
	t.Helper()
	mux, dp := newSyncSalesTestDeps(t)
	registerSyncPluginRecords(mux, dp)
	tillID, err := data.NewTillsRepo(dp.Db).InsertTill(context.Background(), "Replica 1", hashBearer("token-abc"))
	if err != nil {
		t.Fatal(err)
	}
	return mux, dp, tillID
}

func inboxRows(t *testing.T, dp *common.Deps) int {
	t.Helper()
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM plugin_record_inbox`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSyncPluginRecords_WrongBearerIs401AndWritesNothing_3697(t *testing.T) {
	mux, dp, _ := newRecordsMain(t)
	p := installRecordsPlugin(t, dp, []string{"entries"})
	for _, b := range []string{"", "nope"} {
		rec, _ := postRecords(t, mux, b, wireRec(p.id, "entries", "r1"))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("bearer %q: status %d, want 401", b, rec.Code)
		}
	}
	if p.asks.Load() != 0 || inboxRows(t, dp) != 0 {
		t.Fatal("an unauthenticated batch must deliver and store nothing")
	}
}

func TestSyncPluginRecords_AppliesOnceAndPinsSenderToBearer_3697(t *testing.T) {
	mux, dp, tillID := newRecordsMain(t)
	p := installRecordsPlugin(t, dp, []string{"entries"})
	// a spoofed till_id in the body is never read
	body := []byte(fmt.Sprintf(`{"records":[{"plugin_id":%q,"collection":"entries","id":"r1","created_at":"2026-11-27T09:15:00Z","payload":{"n":1},"till_id":"spoofed"}]}`, p.id)) // fixed-date:ok wire fixture, never clock-compared
	rec, resp := postRecordsRaw(t, mux, "token-abc", body)
	if rec.Code != http.StatusOK || len(resp.Data.Results) != 1 || resp.Data.Results[0].Status != "applied" {
		t.Fatalf("first post: %d %s", rec.Code, rec.Body.String())
	}
	if p.asks.Load() != 1 || p.got[0].FromTill != tillID || p.got[0].Collection != "entries" || p.got[0].ID != "r1" || string(p.got[0].Payload) != `{"n":1}` {
		t.Fatalf("ask = %+v (count %d), want from_till_id %q", p.got, p.asks.Load(), tillID)
	}
	if inboxRows(t, dp) != 1 {
		t.Fatal("an applied record must be ledgered")
	}
	// the same id again: duplicate, ONE ask in total
	_, resp = postRecordsRaw(t, mux, "token-abc", body)
	if resp.Data.Results[0].Status != "duplicate" || p.asks.Load() != 1 {
		t.Fatalf("second post = %+v, asks %d, want duplicate and still one ask", resp.Data.Results, p.asks.Load())
	}
	var audits int
	_ = dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='plugin_records_synced'`).Scan(&audits)
	if audits == 0 {
		t.Fatal("a batch must be audited")
	}
}

func TestSyncPluginRecords_Refusals_3697(t *testing.T) {
	mux, dp, _ := newRecordsMain(t)
	p := installRecordsPlugin(t, dp, []string{"entries"})
	off := installRecordsPlugin(t, dp, []string{"entries"}, "inactive")
	tooBig := wireRec(p.id, "entries", "big")
	tooBig.Payload = json.RawMessage(`{"x":"` + strings.Repeat("y", 64<<10) + `"}`)
	notObj := wireRec(p.id, "entries", "arr")
	notObj.Payload = json.RawMessage(`[1]`)
	cases := []struct {
		name   string
		rec    recWire
		status string
		reason string
	}{
		{"unknown plugin", wireRec("com.test.nobody", "entries", "a"), "rejected", "plugin_unknown"},
		{"inactive plugin", wireRec(off.id, "entries", "a"), "rejected", "plugin_inactive"},
		{"undeclared collection", wireRec(p.id, "other", "a"), "rejected", "collection_refused"},
		{"bad id", wireRec(p.id, "entries", "a b"), "rejected", "invalid"},
		{"bad collection", wireRec(p.id, "Entries", "a"), "rejected", "invalid"},
		{"payload not an object", notObj, "rejected", "invalid"},
		{"payload over 64 KiB", tooBig, "rejected", "too_large"},
	}
	for _, c := range cases {
		_, resp := postRecords(t, mux, "token-abc", c.rec)
		if len(resp.Data.Results) != 1 || resp.Data.Results[0].Status != c.status || resp.Data.Results[0].Reason != c.reason {
			t.Errorf("%s: %+v, want %s/%s", c.name, resp.Data.Results, c.status, c.reason)
		}
	}
	if p.asks.Load() != 0 || off.asks.Load() != 0 || inboxRows(t, dp) != 0 {
		t.Fatal("a refused record must never reach a plugin or the ledger")
	}
}

func TestSyncPluginRecords_NeverDeliversToAPluginWithoutThePermission_3697(t *testing.T) {
	mux, dp, _ := newRecordsMain(t)
	p := installRecordsPlugin(t, dp, []string{"entries"})
	if err := data.NewPluginRepo(dp.Db).SetPermission(context.Background(), p.id, "records:sync", false); err != nil {
		t.Fatal(err)
	}
	_, resp := postRecords(t, mux, "token-abc", wireRec(p.id, "entries", "a"))
	if resp.Data.Results[0].Status != "rejected" || resp.Data.Results[0].Reason != "refused" || p.asks.Load() != 0 {
		t.Fatalf("revoked permission: %+v asks %d", resp.Data.Results, p.asks.Load())
	}
}

func TestSyncPluginRecords_RetryThenRedelivered_3697(t *testing.T) {
	mux, dp, _ := newRecordsMain(t)
	p := installRecordsPlugin(t, dp, []string{"entries"})
	p.reply = func(recordsAsk) string { return `{"ok":false,"retry":true,"reason":"busy"}` }
	_, resp := postRecords(t, mux, "token-abc", wireRec(p.id, "entries", "a"))
	if resp.Data.Results[0].Status != "retry" || inboxRows(t, dp) != 0 {
		t.Fatalf("retry:true = %+v inbox %d", resp.Data.Results, inboxRows(t, dp))
	}
	p.reply = func(recordsAsk) string { return `{"ok":true}` }
	_, resp = postRecords(t, mux, "token-abc", wireRec(p.id, "entries", "a"))
	if resp.Data.Results[0].Status != "applied" || p.asks.Load() != 2 {
		t.Fatalf("redelivery = %+v asks %d, want applied after 2 asks", resp.Data.Results, p.asks.Load())
	}
}

func TestSyncPluginRecords_PluginSaysNoRetryIsRejectedRefused_3697(t *testing.T) {
	mux, dp, _ := newRecordsMain(t)
	p := installRecordsPlugin(t, dp, []string{"entries"})
	p.reply = func(recordsAsk) string { return `{"ok":false,"retry":false,"reason":"invalid"}` }
	_, resp := postRecords(t, mux, "token-abc", wireRec(p.id, "entries", "a"))
	if r := resp.Data.Results[0]; r.Status != "rejected" || r.Reason != "refused" {
		t.Fatalf("= %+v", r)
	}
	if inboxRows(t, dp) != 0 {
		t.Fatal("a refused record is not ledgered")
	}
	// an unreadable answer is transient, never a silent ack
	p.reply = func(recordsAsk) string { return `not json` }
	_, resp = postRecords(t, mux, "token-abc", wireRec(p.id, "entries", "b"))
	if resp.Data.Results[0].Status != "retry" {
		t.Fatalf("garbage answer = %+v, want retry", resp.Data.Results)
	}
}

func TestSyncPluginRecords_MalformedBatches_3697(t *testing.T) {
	mux, dp, _ := newRecordsMain(t)
	p := installRecordsPlugin(t, dp, []string{"entries"})
	var many []recWire
	for i := 0; i < data.RecordWireBatchMax+1; i++ {
		many = append(many, wireRec(p.id, "entries", fmt.Sprintf("r%d", i)))
	}
	if rec, _ := postRecords(t, mux, "token-abc", many...); rec.Code != http.StatusBadRequest {
		t.Fatalf("21 records: %d, want 400", rec.Code)
	}
	if rec, _ := postRecordsRaw(t, mux, "token-abc", []byte(`{nope`)); rec.Code != http.StatusBadRequest {
		t.Fatalf("garbage body: %d, want 400", rec.Code)
	}
	if rec, _ := postRecordsRaw(t, mux, "token-abc", bytes.Repeat([]byte(" "), pluginRecordsMaxBody+1)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body: %d, want 413", rec.Code)
	}
	if p.asks.Load() != 0 {
		t.Fatal("a malformed batch delivers nothing")
	}
}

func TestSyncPluginRecords_ReplicaAnswers403NotMain_3697(t *testing.T) {
	mux, dp, _ := newRecordsMain(t)
	if _, err := dp.Db.Exec(`INSERT OR REPLACE INTO settings (key, value, updated_at) VALUES ('sync.primary_url','http://main', ?)`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	rec, _ := postRecords(t, mux, "token-abc", wireRec("com.x", "entries", "a"))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "not_main") {
		t.Fatalf("a replica receiving records: %d %s, want 403 not_main", rec.Code, rec.Body.String())
	}
}

func TestSyncPluginRecords_BatchDeadlineLeavesTheRestRetry_3697(t *testing.T) {
	prev := pluginRecordsBatchDeadline
	pluginRecordsBatchDeadline = 150 * time.Millisecond
	t.Cleanup(func() { pluginRecordsBatchDeadline = prev })
	mux, dp, _ := newRecordsMain(t)
	p := installRecordsPlugin(t, dp, []string{"entries"})
	p.reply = func(recordsAsk) string { time.Sleep(100 * time.Millisecond); return `{"ok":true}` }
	_, resp := postRecords(t, mux, "token-abc", wireRec(p.id, "entries", "a"), wireRec(p.id, "entries", "b"), wireRec(p.id, "entries", "c"))
	retried := 0
	for _, r := range resp.Data.Results {
		if r.Status == "retry" {
			retried++
		}
	}
	if len(resp.Data.Results) != 3 || retried == 0 {
		t.Fatalf("results = %+v, want the rest reported retry past the batch deadline", resp.Data.Results)
	}
}

// --- replica push step ---

// newRecordsReplicaDeps is a till DB with no primary yet; recordsPointAt
// makes it a replica of srvURL. Build it BEFORE installRecordsPlugin: it
// runs plugins.Init, which resets the process-wide bus subscriptions.
func newRecordsReplicaDeps(t *testing.T) *common.Deps {
	t.Helper()
	_, rdp := newSyncSalesTestDeps(t)
	return rdp
}

func recordsPointAt(t *testing.T, rdp *common.Deps, srvURL string) *common.Deps {
	t.Helper()
	for k, v := range map[string]string{"sync.primary_url": srvURL, "sync.bearer": "token-abc"} {
		if err := rdp.Settings.Set(context.Background(), k, v); err != nil {
			t.Fatal(err)
		}
	}
	return rdp
}

func recordsReplica(t *testing.T, srvURL string) *common.Deps {
	t.Helper()
	return recordsPointAt(t, newRecordsReplicaDeps(t), srvURL)
}

func queueRecord(t *testing.T, dp *common.Deps, plugin, id string) {
	t.Helper()
	if _, err := data.NewPluginRecordsRepo(dp.Db).Enqueue(context.Background(), plugin, "entries", id, []byte(`{"n":1}`), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func outboxState(t *testing.T, dp *common.Deps, id string) (status, reason string, attempts int, next string) {
	t.Helper()
	err := dp.Db.QueryRow(`SELECT status, reason, attempts, next_attempt_at FROM plugin_record_outbox WHERE record_id = ?`, id).Scan(&status, &reason, &attempts, &next)
	if err != nil {
		return "gone", "", 0, ""
	}
	return
}

func TestSyncPushPluginRecords_DeliversAndDeletesTheRow_3697(t *testing.T) {
	rdp := newRecordsReplicaDeps(t)
	mux, mdp, _ := newRecordsMain(t)
	p := installRecordsPlugin(t, mdp, []string{"entries"})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	recordsPointAt(t, rdp, srv.URL)
	queueRecord(t, rdp, p.id, "r1")

	syncPushTick(context.Background(), rdp, &http.Client{Timeout: 5 * time.Second})
	if st, _, _, _ := outboxState(t, rdp, "r1"); st != "gone" {
		t.Fatalf("delivered row state = %q, want deleted", st)
	}
	if p.asks.Load() != 1 || inboxRows(t, mdp) != 1 {
		t.Fatalf("main: asks %d inbox %d, want 1 and 1", p.asks.Load(), inboxRows(t, mdp))
	}
	syncPushTick(context.Background(), rdp, &http.Client{Timeout: 5 * time.Second})
	if p.asks.Load() != 1 {
		t.Fatal("a second tick must not redeliver")
	}
	if v, _, _ := rdp.Settings.Get(context.Background(), "sync.last_push_at"); v == "" {
		t.Fatal("a delivered batch is contact with the main till")
	}
}

// recordsStubMain answers /api/sync/plugin-records with a fixed status and body and counts hits.
func recordsStubMain(t *testing.T, status int, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sync/plugin-records" {
			w.WriteHeader(http.StatusOK)
			return
		}
		hits.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func recordsResultsBody(status, reason string, ids ...string) string {
	var rs []string
	for _, id := range ids {
		rs = append(rs, fmt.Sprintf(`{"plugin_id":"p.a","collection":"entries","id":%q,"status":%q,"reason":%q}`, id, status, reason))
	}
	return `{"data":{"results":[` + strings.Join(rs, ",") + `]},"error":null}`
}

func TestSyncPushPluginRecords_PerRecordAnswers_3697(t *testing.T) {
	srv, _ := recordsStubMain(t, 200, `{"data":{"results":[
	 {"plugin_id":"p.a","collection":"entries","id":"ok","status":"applied","reason":""},
	 {"plugin_id":"p.a","collection":"entries","id":"dup","status":"duplicate","reason":""},
	 {"plugin_id":"p.a","collection":"entries","id":"bad","status":"rejected","reason":"invalid"},
	 {"plugin_id":"p.a","collection":"entries","id":"later","status":"retry","reason":"busy"}]},"error":null}`)
	rdp := recordsReplica(t, srv.URL)
	for _, id := range []string{"ok", "dup", "bad", "later", "unanswered"} {
		queueRecord(t, rdp, "p.a", id)
	}
	syncPushTick(context.Background(), rdp, &http.Client{Timeout: 5 * time.Second})
	for id, want := range map[string]string{"ok": "gone", "dup": "gone", "bad": "rejected", "later": "pending", "unanswered": "pending"} {
		if st, _, _, _ := outboxState(t, rdp, id); st != want {
			t.Errorf("%s = %q, want %q", id, st, want)
		}
	}
	if _, reason, _, _ := outboxState(t, rdp, "bad"); reason != "invalid" {
		t.Errorf("rejected reason = %q, want invalid", reason)
	}
	if _, _, att, next := outboxState(t, rdp, "later"); att != 1 || next <= time.Now().UTC().Format(time.RFC3339) {
		t.Errorf("retry row: attempts %d next %s, want 1 and in the future", att, next)
	}
}

func TestSyncPushPluginRecords_BackoffHoldsTheRowBack_3697(t *testing.T) {
	srv, hits := recordsStubMain(t, 500, `boom`)
	rdp := recordsReplica(t, srv.URL)
	queueRecord(t, rdp, "p.a", "r1")
	c := &http.Client{Timeout: 5 * time.Second}
	syncPushTick(context.Background(), rdp, c)
	syncPushTick(context.Background(), rdp, c)
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1: the second tick is inside the backoff", hits.Load())
	}
	if st, _, att, _ := outboxState(t, rdp, "r1"); st != "pending" || att != 1 {
		t.Fatalf("5xx row = %s/%d, want pending/1", st, att)
	}
}

func TestSyncPushPluginRecords_LinkConditionsNeverDestroyData_3697(t *testing.T) {
	for _, code := range []int{401, 403, 404, 409, 429, 502} {
		srv, _ := recordsStubMain(t, code, `{"data":null,"error":"x"}`)
		rdp := recordsReplica(t, srv.URL)
		queueRecord(t, rdp, "p.a", "r1")
		syncPushTick(context.Background(), rdp, &http.Client{Timeout: 5 * time.Second})
		if st, _, _, _ := outboxState(t, rdp, "r1"); st != "pending" {
			t.Errorf("status %d: row = %q, want kept pending", code, st)
		}
	}
}

func TestSyncPushPluginRecords_MalformedBatchRejectsRows_3697(t *testing.T) {
	for _, code := range []int{400, 413, 422} {
		srv, _ := recordsStubMain(t, code, `{"data":null,"error":"bad"}`)
		rdp := recordsReplica(t, srv.URL)
		queueRecord(t, rdp, "p.a", "r1")
		syncPushTick(context.Background(), rdp, &http.Client{Timeout: 5 * time.Second})
		if st, reason, _, _ := outboxState(t, rdp, "r1"); st != "rejected" || reason != data.RecordReasonBadBatch {
			t.Errorf("status %d: row = %s/%s, want rejected/bad_batch", code, st, reason)
		}
	}
}

func TestSyncPushPluginRecords_UnreachableMainKeepsRows_3697(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens any more
	rdp := recordsReplica(t, url)
	queueRecord(t, rdp, "p.a", "r1")
	start := time.Now()
	syncPushTick(context.Background(), rdp, &http.Client{Timeout: 2 * time.Second})
	if time.Since(start) > 3*time.Second {
		t.Fatal("an unreachable main must fail fast")
	}
	if st, _, att, _ := outboxState(t, rdp, "r1"); st != "pending" || att != 1 {
		t.Fatalf("row = %s/%d, want pending/1", st, att)
	}
}

func TestSyncPushPluginRecords_RejectedRowRaisesAKeyedProblemThatResolves_3697(t *testing.T) {
	srv, _ := recordsStubMain(t, 200, recordsResultsBody("rejected", "collection_refused", "r1"))
	rdp := recordsReplica(t, srv.URL)
	queueRecord(t, rdp, "p.a", "r1")
	syncPushTick(context.Background(), rdp, &http.Client{Timeout: 5 * time.Second})
	key := plugins.RecordsProblemKey("p.a")
	if key != "plugin_records.rejected.p.a" {
		t.Fatalf("problem key = %q", key)
	}
	if !logging.HasOpenProblem(key) {
		t.Fatal("a rejected row must raise the keyed Problem")
	}
	var audits int
	_ = rdp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='plugin_record_rejected'`).Scan(&audits)
	if audits != 1 {
		t.Fatalf("rejected audit rows = %d, want 1", audits)
	}
	if _, err := rdp.Db.Exec(`DELETE FROM plugin_record_outbox`); err != nil {
		t.Fatal(err)
	}
	syncPushTick(context.Background(), rdp, &http.Client{Timeout: 5 * time.Second})
	if logging.HasOpenProblem(key) {
		t.Fatal("the Problem must resolve once no rejected row remains")
	}
}

func TestSyncPushPluginRecords_BatchBounds_3697(t *testing.T) {
	var sizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sync/plugin-records" {
			return
		}
		var req struct {
			Records []recWire `json:"records"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		sizes = append(sizes, len(req.Records))
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	rdp := recordsReplica(t, srv.URL)
	for i := 0; i < 30; i++ {
		queueRecord(t, rdp, "p.a", fmt.Sprintf("r%02d", i))
	}
	syncPushTick(context.Background(), rdp, &http.Client{Timeout: 5 * time.Second})
	if len(sizes) == 0 || sizes[0] != data.RecordWireBatchMax {
		t.Fatalf("batch sizes = %v, want the first batch capped at %d", sizes, data.RecordWireBatchMax)
	}
}

func TestSyncPushPluginRecords_InertWithoutRows_3697(t *testing.T) {
	srv, hits := recordsStubMain(t, 200, `{}`)
	rdp := recordsReplica(t, srv.URL)
	syncPushTick(context.Background(), rdp, &http.Client{Timeout: 5 * time.Second})
	if hits.Load() != 0 {
		t.Fatal("no queued record = no request to the main till")
	}
}

func TestSyncPushPluginRecords_SweepsExpiredRows_3697(t *testing.T) {
	srv, hits := recordsStubMain(t, 200, `{}`)
	rdp := recordsReplica(t, srv.URL)
	old := time.Now().UTC().Add(-31 * 24 * time.Hour)
	if _, err := data.NewPluginRecordsRepo(rdp.Db).Enqueue(context.Background(), "p.a", "entries", "old", []byte(`{}`), old); err != nil {
		t.Fatal(err)
	}
	syncPushTick(context.Background(), rdp, &http.Client{Timeout: 5 * time.Second})
	if st, reason, _, _ := outboxState(t, rdp, "old"); st != "rejected" || reason != data.RecordReasonExpired {
		t.Fatalf("old row = %s/%s, want rejected/expired", st, reason)
	}
	if hits.Load() != 0 {
		t.Fatal("an expired row is never sent")
	}
}

// A sale never waits for the plugin-records channel: with an unreachable
// main and a full outbox, completing a sale touches neither the outbox nor
// the network (ut-docs#3697 offline-first AC).
func TestSaleCommitUnaffectedByUnreachableMainAndFullOutbox_3697(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	rdp := recordsReplica(t, url)
	// 2,000 queued rows: the per-plugin cap
	tx, err := rdp.Db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for i := 0; i < data.RecordMaxRowsPlugin; i++ {
		if _, err := tx.Exec(`INSERT INTO plugin_record_outbox (plugin_id, collection, record_id, payload, bytes, next_attempt_at, created_at) VALUES ('p.a','entries',?, '{}', 2, ?, ?)`,
			fmt.Sprintf("r%d", i), now, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := rdp.Db.Exec(`INSERT OR IGNORE INTO items (id, sku, name, base_price, is_active) VALUES ('itm-rec', 'SKU-rec', 'Item', 500, 1)`); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	start := time.Now()
	if _, err := pos.CompleteSale(context.Background(), rdp.Db, pos.SaleInput{
		SaleType: "sale", Currency: "GBP", AllowNegativeInventory: true,
		Lines:    []pos.SaleLineInput{{ItemID: "itm-rec", Name: "Item", Qty: 1, UnitPrice: money.FromMinor(500), LocationID: "loc_main"}},
		Payments: []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(500)}},
	}); err != nil {
		t.Fatalf("CompleteSale: %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("sale took %v with the main till unreachable and a full outbox", d)
	}
	var pending int
	_ = rdp.Db.QueryRow(`SELECT COUNT(*) FROM plugin_record_outbox WHERE status='pending'`).Scan(&pending)
	if pending != data.RecordMaxRowsPlugin {
		t.Fatalf("pending = %d: the sale path must not touch the outbox", pending)
	}
}

// Review (low): the main's reason string is stored and audited on the
// replica, so its length is capped there whatever the main sends.
func TestSyncPushPluginRecords_RejectReasonIsCapped_3697(t *testing.T) {
	long := strings.Repeat("é", 5000)
	srv, _ := recordsStubMain(t, 200, recordsResultsBody("rejected", long, "r1"))
	rdp := recordsReplica(t, srv.URL)
	queueRecord(t, rdp, "p.a", "r1")
	syncPushTick(context.Background(), rdp, &http.Client{Timeout: 5 * time.Second})
	st, reason, _, _ := outboxState(t, rdp, "r1")
	if st != "rejected" || len(reason) == 0 || len(reason) > pluginRecordsReasonMax || !utf8.ValidString(reason) {
		t.Fatalf("row = %s, reason len %d (valid utf8 %v), want rejected with at most %d bytes", st, len(reason), utf8.ValidString(reason), pluginRecordsReasonMax)
	}
	var audit string
	if err := rdp.Db.QueryRow(`SELECT data_json FROM audit_log WHERE action='plugin_record_rejected'`).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if len(audit) > 512 {
		t.Fatalf("audit row carries the uncapped reason (%d bytes)", len(audit))
	}
}
