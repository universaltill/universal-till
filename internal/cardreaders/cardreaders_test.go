package cardreaders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseAnswer_Mapping(t *testing.T) {
	refs := []string{"A", "B", "C", "D", "E"}
	raw := `{"readers":[
	 {"reader_id":"A","state":"ready"},
	 {"reader_id":"B","state":"offline","reason":"reader_unreachable"},
	 {"reader_id":"C","state":"busy"},
	 {"reader_id":"D","state":"needs_setup","reason":"<script>"},
	 {"reader_id":"Z","state":"ready"},
	 {"reader_id":"E","state":"banana"}]}`
	got := ParseAnswer(json.RawMessage(raw), refs)
	want := map[string]Answer{
		"A": {Ready, ""},
		"B": {Offline, "reader_unreachable"},
		"C": {Busy, ""},
		"D": {NeedsSetup, ""}, // an unknown reason string is dropped, never echoed
		"E": {Unknown, ""},    // an unknown state value is unknown
	}
	for ref, w := range want {
		if got[ref] != w {
			t.Errorf("%s: got %+v want %+v", ref, got[ref], w)
		}
	}
	if _, ok := got["Z"]; ok {
		t.Errorf("a reader nobody asked about must be ignored")
	}
}

func TestParseAnswer_BadInputIsUnknown(t *testing.T) {
	refs := []string{"A"}
	cases := map[string]json.RawMessage{
		"malformed": json.RawMessage(`{"readers":[`),
		"empty":     nil,
		"missing":   json.RawMessage(`{"readers":[]}`),
		"oversize":  json.RawMessage(`{"readers":[{"reader_id":"A","state":"ready","reason":"` + strings.Repeat("x", MaxAnswerBytes) + `"}]}`),
	}
	for name, raw := range cases {
		got := ParseAnswer(raw, refs)
		if got["A"].State != Unknown {
			t.Errorf("%s: got %+v, want unknown", name, got["A"])
		}
	}
}

func TestCache_HitMakesNoSecondCall(t *testing.T) {
	var calls atomic.Int32
	q := func(ctx context.Context, method string, refs []string) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{"readers":[{"reader_id":"A","state":"ready"}]}`), nil
	}
	now := time.Unix(1000, 0)
	c := newCache(10*time.Second, func() time.Time { return now })
	p := []Probe{{ID: "row1", Method: "m", Ref: "A"}}
	if got := c.States(context.Background(), p, q); got["row1"].State != Ready {
		t.Fatalf("got %+v", got)
	}
	c.States(context.Background(), p, q)
	if calls.Load() != 1 {
		t.Fatalf("cache hit must not call the plugin again, calls=%d", calls.Load())
	}
	now = now.Add(11 * time.Second)
	c.States(context.Background(), p, q)
	if calls.Load() != 2 {
		t.Fatalf("expired entry must re-query, calls=%d", calls.Load())
	}
}

func TestCache_ErrorTimeoutAndNonZeroExitGiveUnknownAndAreNotCached(t *testing.T) {
	var calls atomic.Int32
	q := func(ctx context.Context, method string, refs []string) (json.RawMessage, error) {
		calls.Add(1)
		return nil, errors.New("exit 2")
	}
	c := newCache(10*time.Second, time.Now)
	p := []Probe{{ID: "r", Method: "m", Ref: "A"}}
	if got := c.States(context.Background(), p, q); got["r"].State != Unknown {
		t.Fatalf("got %+v", got)
	}
	c.States(context.Background(), p, q)
	if calls.Load() != 2 {
		t.Fatalf("a failed check must not be cached, calls=%d", calls.Load())
	}
}

func TestCache_DeadlineGivesUnknown(t *testing.T) {
	q := func(ctx context.Context, method string, refs []string) (json.RawMessage, error) {
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
		return nil, ctx.Err()
	}
	c := newCache(10*time.Second, time.Now)
	c.deadline = 50 * time.Millisecond
	start := time.Now()
	got := c.States(context.Background(), []Probe{{ID: "r", Method: "m", Ref: "A"}}, q)
	if got["r"].State != Unknown || time.Since(start) > time.Second {
		t.Fatalf("got %+v after %v", got, time.Since(start))
	}
}

func TestCache_NeverExceedsMax(t *testing.T) {
	q := func(ctx context.Context, method string, refs []string) (json.RawMessage, error) {
		var rs []string
		for _, r := range refs {
			rs = append(rs, fmt.Sprintf(`{"reader_id":%q,"state":"ready"}`, r))
		}
		return json.RawMessage(`{"readers":[` + strings.Join(rs, ",") + `]}`), nil
	}
	c := newCache(time.Hour, time.Now)
	for i := 0; i < 200; i++ {
		c.States(context.Background(), []Probe{{ID: fmt.Sprintf("row%d", i), Method: "m", Ref: fmt.Sprintf("ref%d", i)}}, q)
	}
	if n := c.Len(); n > CacheMax {
		t.Fatalf("cache holds %d entries, bound is %d", n, CacheMax)
	}
}

func TestCache_ChunksAtMaxRefsAndGroupsByMethod(t *testing.T) {
	var mu sync.Mutex
	seen := map[string][]int{}
	q := func(ctx context.Context, method string, refs []string) (json.RawMessage, error) {
		mu.Lock()
		seen[method] = append(seen[method], len(refs))
		mu.Unlock()
		return json.RawMessage(`{"readers":[]}`), nil
	}
	c := newCache(time.Second, time.Now)
	var ps []Probe
	for i := 0; i < 20; i++ {
		ps = append(ps, Probe{ID: fmt.Sprintf("a%d", i), Method: "a", Ref: fmt.Sprintf("r%d", i)})
	}
	ps = append(ps, Probe{ID: "b0", Method: "b", Ref: "rb"})
	c.States(context.Background(), ps, q)
	for m, sizes := range seen {
		for _, n := range sizes {
			if n > MaxRefsPerCall {
				t.Errorf("method %s: call with %d refs, max %d", m, n, MaxRefsPerCall)
			}
		}
	}
	if len(seen["b"]) != 1 {
		t.Errorf("method b must be asked once, got %v", seen["b"])
	}
}

func TestInFlight(t *testing.T) {
	f := NewInFlight()
	if !f.Acquire("r1") {
		t.Fatal("first acquire")
	}
	if f.Acquire("r1") {
		t.Fatal("second acquire of a held reader must fail")
	}
	if !f.Has("r1") {
		t.Fatal("Has")
	}
	f.Release("r1")
	if f.Has("r1") || !f.Acquire("r1") {
		t.Fatal("release frees the reader")
	}
}

// Len is the number of cached entries (tests only: no production caller).
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

// Tester finding (#6566): a panic in a plugin state call must not take the
// till down (every goroutine recovers, ut-docs#3304) and must not leave its
// single-flight slot behind, or that key stays unknown until a restart.
func TestCache_PanickingQuerierIsRecoveredAndDoesNotPoisonTheKey(t *testing.T) {
	var calls atomic.Int32
	q := func(ctx context.Context, method string, refs []string) (json.RawMessage, error) {
		if calls.Add(1) == 1 {
			panic("plugin host blew up")
		}
		return json.RawMessage(`{"readers":[{"reader_id":"A","state":"ready"}]}`), nil
	}
	c := newCache(10*time.Second, time.Now)
	p := []Probe{{ID: "r", Method: "m", Ref: "A"}}
	start := time.Now()
	if got := c.States(context.Background(), p, q); got["r"].State != Unknown {
		t.Fatalf("a panicking check is unknown, got %+v", got)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("a panicking check must not wait for the deadline, took %s", time.Since(start))
	}
	if got := c.States(context.Background(), p, q); got["r"].State != Ready {
		t.Fatalf("the key must be usable again after a panic, got %+v", got)
	}
}
