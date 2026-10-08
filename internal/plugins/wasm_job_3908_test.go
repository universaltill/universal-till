package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// Plugin jobs in the runtime (ADR-0121 §3/§8, ut-docs#3908): an event run
// under WithJob gets the job's deadline, never the reserved sale-path slot,
// the ui.* answer cap, and a job_progress sink.

// jobGuestRuntime loads the hostfn guest for pluginID with a short default
// deadline, so a long call fails without a job.
func jobGuestRuntime(t *testing.T, pluginID string) *WasmRuntime {
	t.Helper()
	guest := buildHostfnGuest(t)
	d := hostfnTestDB(t)
	seedPlugin(t, d, pluginID)
	w := NewWasmRuntime(t.TempDir())
	t.Cleanup(func() { _ = w.rt.Close(context.Background()) })
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load: %v", err)
	}
	w.mu.Lock()
	w.db = d
	w.timeout = 200 * time.Millisecond
	w.mu.Unlock()
	return w
}

func jobEvent(t *testing.T, typ string, payload any) Event {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return Event{ID: "ev-job", Type: typ, Timestamp: time.Now(), Payload: body}
}

func TestWasmJob_UsesJobDeadline_3908(t *testing.T) {
	const pluginID = "com.test.jobs"
	w := jobGuestRuntime(t, pluginID)
	ev := jobEvent(t, pluginID+".identify", map[string]any{"mode": "sleep", "sleep_ms": 600})

	// Without a job the 200 ms event deadline ends the long call.
	if _, err := w.HandleEvent(context.Background(), pluginID, ev); err == nil {
		t.Fatal("a 600 ms call ran past the 200 ms event deadline without a job")
	}
	// Under a job it runs to the job's deadline.
	ctx := WithJob(context.Background(), JobCall{Deadline: 5 * time.Second})
	out, err := w.HandleEvent(ctx, pluginID, ev)
	if err != nil {
		t.Fatalf("job call: %v", err)
	}
	if string(out) != `{"slept":true}` {
		t.Fatalf("job answer = %s", out)
	}
	// A job deadline shorter than the call still ends it.
	ctx = WithJob(context.Background(), JobCall{Deadline: 50 * time.Millisecond})
	if _, err := w.HandleEvent(ctx, pluginID, ev); err == nil {
		t.Fatal("job ran past its own deadline")
	}
}

// A job never takes the reserved sale-path slot, whatever its event name.
func TestWasmJob_NeverSalePath_3908(t *testing.T) {
	w := NewWasmRuntime(t.TempDir())
	defer w.rt.Close(context.Background())
	const pluginID = "com.test.caps"
	compiled, err := w.rt.CompileModule(context.Background(), []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00})
	if err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	w.modules[pluginID] = compiled
	w.mu.Unlock()
	perCap, _ := wasmConcurrencyLimits("linux")
	w.calls = newWasmCallGate(perCap, 16)
	for i := 0; i < perCap-1; i++ {
		rel, err := w.calls.acquire(context.Background(), pluginID, false)
		if err != nil {
			t.Fatal(err)
		}
		defer rel()
	}
	// tax.rate.ask is sale-path: with the ordinary slots taken it runs on
	// the reserved one (TestWasmHandleEventSalePathGetsReservedSlot) —
	// but not as a job.
	ctx := WithJob(context.Background(), JobCall{Deadline: 80 * time.Millisecond})
	_, err = w.HandleEvent(ctx, pluginID, Event{Type: "tax.rate.ask"})
	if err == nil {
		t.Fatal("a job took the reserved sale-path slot")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a slot wait ending at the job deadline", err)
	}
	// The reserved slot is still free for a real sale-path call.
	if _, err := w.HandleEvent(shortCtx(t), pluginID, Event{Type: "tax.rate.ask"}); err != nil {
		t.Fatalf("sale-path call after the refused job: %v", err)
	}
}

// A job answer is capped like a ui.* answer, whatever its event name.
func TestWasmJob_AnswerCapped_3908(t *testing.T) {
	guest := buildBigViewGuest(t)
	w := NewWasmRuntime(t.TempDir())
	const pluginID = "com.test.bigview"
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load guest module: %v", err)
	}
	ctx := WithJob(context.Background(), JobCall{Deadline: 10 * time.Second})
	ev := Event{ID: "ev-big", Type: pluginID + ".identify", Timestamp: time.Now(), Payload: json.RawMessage(`{}`)}
	if resp, err := w.HandleEvent(ctx, pluginID, ev); err == nil {
		t.Fatalf("a 2 MiB job answer accepted (%d bytes)", len(resp))
	}
}

func TestWasmJob_ProgressHostFunction_3908(t *testing.T) {
	const pluginID = "com.test.jobs"
	w := jobGuestRuntime(t, pluginID)
	run := func(ctx context.Context, pct int, key string) int {
		t.Helper()
		ev := jobEvent(t, pluginID+".identify", map[string]any{"mode": "job_progress", "pct": pct, "progress_key": key})
		out, err := w.HandleEvent(ctx, pluginID, ev)
		if err != nil {
			t.Fatalf("HandleEvent: %v", err)
		}
		var res struct {
			Code *int `json:"progress_code"`
		}
		if err := json.Unmarshal(out, &res); err != nil || res.Code == nil {
			t.Fatalf("guest answer %s: %v", out, err)
		}
		return *res.Code
	}

	// Outside a job: an error code, nothing reported anywhere.
	if code := run(context.Background(), 50, "plugin.jobs.step"); code != hostErrNotFound {
		t.Fatalf("job_progress outside a job = %d, want %d", code, hostErrNotFound)
	}

	var mu sync.Mutex
	var gotPct []int
	var gotKey []string
	sink := func(pct int, key string) error {
		if key != "" && key != "plugin.jobs.step" {
			return errors.New("not an own key")
		}
		mu.Lock()
		defer mu.Unlock()
		gotPct = append(gotPct, pct)
		gotKey = append(gotKey, key)
		return nil
	}
	ctx := WithJob(context.Background(), JobCall{Deadline: 5 * time.Second, Progress: sink})

	// Own key: stored.
	if code := run(ctx, 40, "plugin.jobs.step"); code != 0 {
		t.Fatalf("job_progress with own key = %d, want 0", code)
	}
	// pct is clamped to 0..100.
	if code := run(ctx, 250, "plugin.jobs.step"); code != 0 {
		t.Fatalf("job_progress pct 250 = %d, want 0", code)
	}
	// A foreign key (another plugin's, or core's) is refused, nothing stored.
	if code := run(ctx, 60, "nav.home"); code != hostErrInvalid {
		t.Fatalf("job_progress with a foreign key = %d, want %d", code, hostErrInvalid)
	}
	// An oversize key is refused before it is read.
	if code := run(ctx, 60, "plugin.jobs."+string(make([]byte, maxJobProgressKeyLen))); code != hostErrInvalid {
		t.Fatalf("job_progress with an oversize key = %d, want %d", code, hostErrInvalid)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(gotPct) != 2 || gotPct[0] != 40 || gotPct[1] != 100 || gotKey[0] != "plugin.jobs.step" {
		t.Fatalf("sink got pct=%v key=%v, want [40 100] own key only", gotPct, gotKey)
	}
}

// JobCaps derives the job caps from the call gate's slots (ut-docs#3908
// review): per plugin a job may hold at most one ordinary slot short of
// the plugin's ceiling (so the plugin's own page asks still get one) and
// never more than two; across plugins at most (global-1)/2, leaving the
// reserved slot and half the ordinary ones for everything else.
func TestJobCaps_FollowCallGate_3908(t *testing.T) {
	for _, tc := range []struct {
		goos              string
		perPlugin, global int
	}{
		{"android", 1, 3},
		{"ios", 1, 3},
		{"linux", 2, 7},
		{"windows", 2, 7},
		{"darwin", 2, 7},
	} {
		per, glob := JobCaps(tc.goos)
		if per != tc.perPlugin || glob != tc.global {
			t.Errorf("JobCaps(%q) = (%d, %d), want (%d, %d)", tc.goos, per, glob, tc.perPlugin, tc.global)
		}
		gatePer, gateGlob := wasmConcurrencyLimits(tc.goos)
		if per > gatePer-1 || glob > gateGlob-1 {
			t.Errorf("JobCaps(%q) leaves no ordinary slot free", tc.goos)
		}
	}
}
