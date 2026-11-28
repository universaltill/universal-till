package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
)

type fakeHandover struct {
	ids  []string
	evs  []Event
	resp string
	err  error
}

func (f *fakeHandover) HandleEvent(_ context.Context, id string, ev Event) (json.RawMessage, error) {
	f.ids = append(f.ids, id)
	f.evs = append(f.evs, ev)
	return json.RawMessage(f.resp), f.err
}

func handoverPlugin(t *testing.T, id, event, runtime string) *db.DB {
	t.Helper()
	d := openRealDB(t)
	m := providesManifest(id, nil, nil)
	m.Hooks = []ManifestHook{{Event: event, Action: "run"}}
	if err := PersistManifest(context.Background(), d.DB, m, InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	mustExecSQL(t, d, `UPDATE plugins SET runtime=? WHERE id=?`, runtime, id)
	return d
}

func TestHandoverEventNameIsCoreNamespaced(t *testing.T) {
	root := HandoverEvent[:len("plugin")]
	if root != "plugin" || !coreEventRoots[root] {
		t.Fatalf("%q must sit under a core root", HandoverEvent)
	}
	if HandoverEvent != "plugin.data.handover.ask" {
		t.Fatalf("event name = %q", HandoverEvent)
	}
	// A plugin schedule/event under that name is refused: ids may not start with a core root.
	if err := validatePluginID("plugin.data"); err == nil {
		t.Fatal("a plugin id inside the plugin.* root must be refused")
	}
}

func TestHandoverPassAsksOnceAndRecordsDone(t *testing.T) {
	d := handoverPlugin(t, "com.ho.one", HandoverEvent, "wasm")
	f := &fakeHandover{resp: `{"done":true,"extra":"ignored"}`}
	now := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	ctx := context.Background()

	if n := runHandoverPass(ctx, d.DB, f, now); n != 1 {
		t.Fatalf("asked %d, want 1", n)
	}
	if len(f.ids) != 1 || f.ids[0] != "com.ho.one" || f.evs[0].Type != HandoverEvent {
		t.Fatalf("calls: %v %v", f.ids, f.evs)
	}
	var p map[string]any
	if err := json.Unmarshal(f.evs[0].Payload, &p); err != nil || p["plugin"] != "com.ho.one" || p["to_version"] != "1.0.0" {
		t.Fatalf("payload = %s (%v)", f.evs[0].Payload, err)
	}
	// Idempotent: a later pass (even a restart) never asks again.
	if n := runHandoverPass(ctx, d.DB, f, now.Add(time.Hour)); n != 0 || len(f.ids) != 1 {
		t.Fatalf("second pass asked again: n=%d calls=%d", n, len(f.ids))
	}
	done, err := handoverDone(ctx, d.DB, "com.ho.one")
	if err != nil || !done {
		t.Fatalf("done = %v, %v", done, err)
	}
}

func TestHandoverNotDoneRetriesWithBackoffThenStopsAtFive(t *testing.T) {
	d := handoverPlugin(t, "com.ho.retry", HandoverEvent, "wasm")
	f := &fakeHandover{resp: `{"done":false}`}
	ctx := context.Background()
	now := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)

	if n := runHandoverPass(ctx, d.DB, f, now); n != 1 {
		t.Fatalf("first = %d", n)
	}
	if n := runHandoverPass(ctx, d.DB, f, now.Add(10*time.Second)); n != 0 {
		t.Fatalf("retry inside the backoff window ran: %d", n)
	}
	for i := 1; i < 5; i++ {
		now = now.Add(time.Duration(1<<uint(i)) * time.Hour)
		if n := runHandoverPass(ctx, d.DB, f, now); n != 1 {
			t.Fatalf("attempt %d = %d", i+1, n)
		}
	}
	if n := runHandoverPass(ctx, d.DB, f, now.Add(1000*time.Hour)); n != 0 || len(f.ids) != 5 {
		t.Fatalf("a sixth attempt ran: n=%d calls=%d", n, len(f.ids))
	}
	if done, _ := handoverDone(ctx, d.DB, "com.ho.retry"); done {
		t.Fatal("never-done plugin marked done")
	}
}

func TestHandoverErrorCountsButNotLoadedDoesNot(t *testing.T) {
	d := handoverPlugin(t, "com.ho.err", HandoverEvent, "wasm")
	ctx := context.Background()
	now := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)

	f := &fakeHandover{err: errors.New("module not loaded: com.ho.err")}
	runHandoverPass(ctx, d.DB, f, now)
	runHandoverPass(ctx, d.DB, f, now.Add(time.Second))
	if len(f.ids) != 2 {
		t.Fatalf("a not-yet-loaded runtime must be retried every pass, calls=%d", len(f.ids))
	}
	f = &fakeHandover{err: errors.New("boom")}
	runHandoverPass(ctx, d.DB, f, now)
	if n := runHandoverPass(ctx, d.DB, f, now.Add(time.Second)); n != 0 {
		t.Fatal("a real failure must back off")
	}
}

func TestHandoverSkipsPluginsWithoutTheHookOrNotWasmOrInactive(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	for name, mod := range map[string]func(*testing.T) *db.DB{
		"other event": func(t *testing.T) *db.DB { return handoverPlugin(t, "com.ho.a", "sale.completed", "wasm") },
		"not wasm":    func(t *testing.T) *db.DB { return handoverPlugin(t, "com.ho.b", HandoverEvent, "none") },
		"inactive": func(t *testing.T) *db.DB {
			d := handoverPlugin(t, "com.ho.c", HandoverEvent, "wasm")
			mustExecSQL(t, d, `UPDATE plugins SET is_active=0`)
			return d
		},
	} {
		d := mod(t)
		f := &fakeHandover{resp: `{"done":true}`}
		if n := runHandoverPass(ctx, d.DB, f, now); n != 0 || len(f.ids) != 0 {
			t.Fatalf("%s: asked %d", name, n)
		}
	}
}

func TestHandoverRefusesGarbageAnswer(t *testing.T) {
	d := handoverPlugin(t, "com.ho.junk", HandoverEvent, "wasm")
	f := &fakeHandover{resp: `not json`}
	runHandoverPass(context.Background(), d.DB, f, time.Now())
	if done, _ := handoverDone(context.Background(), d.DB, "com.ho.junk"); done {
		t.Fatal("garbage answer marked done")
	}
}

// handoverDone reports whether plugin_handover has a done_at for the plugin.
func handoverDone(ctx context.Context, db *sql.DB, id string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugin_handover WHERE plugin_id = ? AND done_at IS NOT NULL`, id).Scan(&n)
	return n > 0, err
}

// A plugin that answers {"done":false,"progress":true} is working through a
// big table: it is asked again after a minute, forever, and the attempt
// counter does not move. Mutation: count progress as an attempt -> red.
func TestHandoverProgressAnswersDoNotBurnTheFiveAttempts(t *testing.T) {
	d := handoverPlugin(t, "com.ho.big", HandoverEvent, "wasm")
	f := &fakeHandover{resp: `{"done":false,"progress":true}`}
	ctx := context.Background()
	now := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 12; i++ {
		if n := runHandoverPass(ctx, d.DB, f, now); n != 1 {
			t.Fatalf("pass %d asked %d, want 1", i, n)
		}
		if n := runHandoverPass(ctx, d.DB, f, now.Add(10*time.Second)); n != 0 {
			t.Fatalf("pass %d asked again inside the wait", i)
		}
		now = now.Add(handoverProgressWait + time.Second)
	}
	var attempts int
	if err := d.DB.QueryRow(`SELECT attempts FROM plugin_handover WHERE plugin_id='com.ho.big'`).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("attempts = %d, %v; progress must not count", attempts, err)
	}
	f.resp = `{"done":true}`
	runHandoverPass(ctx, d.DB, f, now)
	if done, _ := handoverDone(ctx, d.DB, "com.ho.big"); !done {
		t.Fatal("final done answer not recorded")
	}
}
