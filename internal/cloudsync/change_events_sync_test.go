package cloudsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
)

// ADR-0200 (ut-docs#6559): change events ride the sync POST. They are
// excluded from the state hash, force a POST past a 304, and are deleted only
// when the cloud answered 2xx or 402.

// reportingTill makes the till one that may report: a release version, set
// up, notice shown.
func reportingTill(t *testing.T, db *sql.DB) *data.ChangeEventsRepo {
	t.Helper()
	pinVersion(t, "0.30.20")
	if err := data.NewSettingsRepo(db).SetMany(context.Background(), map[string]string{
		"setup.completed":       "true",
		enroll.KeyNoticeShownAt: time.Now().UTC().Format(time.RFC3339),
		enroll.KeyNoticeVersion: enroll.NoticeVersion,
	}); err != nil {
		t.Fatal(err)
	}
	return data.NewChangeEventsRepo(db)
}

func addEvents(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := db.ExecContext(context.Background(), `INSERT INTO till_change_events (id, kind, plugin_id, action, from_version, to_version, at) VALUES (?, 'plugin', ?, 'install', '', '1.0.0', ?)`,
			fmt.Sprintf("%032x", i+1), fmt.Sprintf("ut-plugin-p%d", i), time.Now().UTC().Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
}

func syncDevice(t *testing.T, cloud *fakeCloud, i int) map[string]any {
	t.Helper()
	cloud.mu.Lock()
	defer cloud.mu.Unlock()
	if len(cloud.syncBodies) <= i {
		t.Fatalf("sync POSTs = %d, want more than %d", len(cloud.syncBodies), i)
	}
	devs, _ := cloud.syncBodies[i]["devices"].([]any)
	if len(devs) != 1 {
		t.Fatalf("devices = %v", cloud.syncBodies[i]["devices"])
	}
	return devs[0].(map[string]any)
}

func TestChangeEvents_RideThePostOldestFirstAndAckOnSuccess(t *testing.T) {
	fakeClock(t)
	cloud, srv := newCheckinCloud(3)
	defer srv.Close()
	db := testDB(t)
	repo := reportingTill(t, db)
	addEvents(t, db, 60)

	mustTick(t, srv.URL, db, Hooks{})

	evs, _ := syncDevice(t, cloud.fakeCloud, 0)["events"].([]any)
	if len(evs) != data.ChangeEventBatch {
		t.Fatalf("events in POST = %d, want %d", len(evs), data.ChangeEventBatch)
	}
	first, _ := evs[0].(map[string]any)
	if first["plugin_id"] != "ut-plugin-p0" || first["kind"] != "plugin" || first["action"] != "install" || first["to"] != "1.0.0" {
		t.Fatalf("first event = %v", first)
	}
	if id, _ := first["id"].(string); len(id) != 32 {
		t.Errorf("event id %q is not 32 hex chars", id)
	}
	for k := range first {
		switch k {
		case "id", "kind", "plugin_id", "action", "from", "to", "at":
		default:
			t.Errorf("event carries key %q outside the wire contract", k)
		}
	}
	if n, _ := pendingCount(repo); n != 10 {
		t.Fatalf("outbox after ack = %d, want the 10 not yet sent", n)
	}
}

func TestChangeEvents_NonEmptyOutboxForcesThePostPast304(t *testing.T) {
	fakeClock(t)
	cloud, srv := newCheckinCloud(3)
	defer srv.Close()
	db := testDB(t)
	reportingTill(t, db)

	mustTick(t, srv.URL, db, Hooks{}) // 200 -> POST
	mustTick(t, srv.URL, db, Hooks{}) // 304 -> no POST
	if cloud.posts() != 1 {
		t.Fatalf("posts = %d before any event, want 1 (the 304 skipped the second)", cloud.posts())
	}
	addEvents(t, db, 1)
	mustTick(t, srv.URL, db, Hooks{})
	if cloud.posts() != 2 {
		t.Fatalf("posts = %d, want 2: a waiting event must force a POST past a 304", cloud.posts())
	}
	if evs, _ := syncDevice(t, cloud.fakeCloud, 1)["events"].([]any); len(evs) != 1 {
		t.Fatalf("forced POST carried %v", evs)
	}
	mustTick(t, srv.URL, db, Hooks{})
	if cloud.posts() != 2 {
		t.Fatalf("posts = %d, want 2: after the ack the 304 skips again", cloud.posts())
	}
}

func TestChangeEvents_StateHashIgnoresEvents(t *testing.T) {
	base := map[string]any{"device_id": "d", "version": "1", "health": map[string]any{"db_mb": 1}}
	withEvents := map[string]any{"device_id": "d", "version": "1", "health": map[string]any{"db_mb": 1},
		"events": []map[string]any{{"id": "x", "kind": "version"}}}
	a, err := stateHash([]map[string]any{base})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := stateHash([]map[string]any{withEvents})
	if a != b {
		t.Fatal("events changed the state hash: a 304 would then never skip, or never see them")
	}
}

func TestChangeEvents_AckedOn402ButKeptOnOtherFailures(t *testing.T) {
	useRealSyncGate(t)
	resetTillAuth(t)
	pinOperatorNow(t, startupDay)
	for _, tc := range []struct {
		name     string
		status   int
		wantLeft int
	}{
		{"402 plan required acks", http.StatusPaymentRequired, 0},
		{"500 keeps", http.StatusInternalServerError, 3},
		{"429 keeps", http.StatusTooManyRequests, 3},
		{"503 keeps", http.StatusServiceUnavailable, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cloud := &startupCloud{syncStatus: tc.status}
			srv := httptest.NewServer(cloud.handler())
			t.Cleanup(srv.Close)
			db := testDB(t)
			repo := reportingTill(t, db)
			addEvents(t, db, 3)
			armStartup()
			_, _ = tick(context.Background(), testCfg(srv.URL), db, Hooks{})
			if cloud.count(reqSyncPost) != 1 {
				t.Fatalf("sync POSTs = %d, want 1", cloud.count(reqSyncPost))
			}
			if n, _ := pendingCount(repo); n != tc.wantLeft {
				t.Fatalf("outbox = %d, want %d", n, tc.wantLeft)
			}
		})
	}
}

func TestChangeEvents_NotSentWhenAGuardSaysNo(t *testing.T) {
	cases := map[string]func(t *testing.T, db *sql.DB){
		"stopped": func(t *testing.T, db *sql.DB) {
			_ = data.NewSettingsRepo(db).Set(context.Background(), enroll.KeyStopped, "true")
		},
		"notice not shown": func(t *testing.T, db *sql.DB) {
			_ = data.NewSettingsRepo(db).Set(context.Background(), enroll.KeyNoticeShownAt, "")
		},
		"dev build": func(t *testing.T, db *sql.DB) { pinVersion(t, "dev") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			fakeClock(t)
			cloud, srv := newCheckinCloud(3)
			defer srv.Close()
			db := testDB(t)
			repo := reportingTill(t, db)
			addEvents(t, db, 2)
			mutate(t, db)
			mustTick(t, srv.URL, db, Hooks{})
			if _, has := syncDevice(t, cloud.fakeCloud, 0)["events"]; has {
				t.Fatal("events were sent although the guards say no")
			}
			if n, _ := pendingCount(repo); n != 2 {
				t.Fatalf("outbox = %d, want the 2 events kept", n)
			}
		})
	}
}

func TestChangeEvents_OldCloudIgnoringTheFieldStillWorks(t *testing.T) {
	// An old cloud decodes the device into a struct without "events": the POST
	// still answers 200 and the till acks (bounded loss, ADR-0200 consequences).
	fakeClock(t)
	var mu sync.Mutex
	var bodies []map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/stores/sync", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"directives": []any{}}})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	db := testDB(t)
	repo := reportingTill(t, db)
	addEvents(t, db, 1)
	mustTick(t, srv.URL, db, Hooks{})
	if n, _ := pendingCount(repo); n != 0 {
		t.Fatalf("outbox = %d after a 200", n)
	}
}

func TestChangeEvents_LastCheckinRecordedOnAnswer(t *testing.T) {
	fakeClock(t)
	_, srv := newCheckinCloud(3)
	defer srv.Close()
	db := testDB(t)
	reportingTill(t, db)
	mustTick(t, srv.URL, db, Hooks{})
	v, ok, _ := data.NewSettingsRepo(db).Get(context.Background(), enroll.KeyLastCheckinAt)
	if !ok {
		t.Fatal("last check-in not recorded after the cloud answered")
	}
	if _, err := time.Parse(time.RFC3339, v); err != nil {
		t.Fatalf("last check-in %q is not RFC 3339", v)
	}
}

// "Stop sending" halts the start-up refresh of an unpaid till (ADR-0200).
func TestStoppedTillMakesNoStartupCheckin(t *testing.T) {
	cloud, srv, db, _ := startupFixture(t, 0)
	_ = data.NewSettingsRepo(db).Set(context.Background(), enroll.KeyStopped, "true")
	armStartup()
	contacted, err := tick(context.Background(), testCfg(srv.URL), db, Hooks{})
	if contacted || err != nil {
		t.Fatalf("tick = (%v, %v), want a quiet gated tick", contacted, err)
	}
	if got := cloud.requests(); len(got) != 0 {
		t.Fatalf("a stopped till made requests %v", got)
	}
}

// pendingCount is how many events still wait in the outbox.
func pendingCount(repo *data.ChangeEventsRepo) (int, error) {
	evs, err := repo.ListPending(context.Background(), data.ChangeEventCap)
	return len(evs), err
}
