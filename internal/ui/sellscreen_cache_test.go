package ui

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeClock is the cache's injectable clock for these tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

// fakeMem is the cache's injectable MemAvailable reader.
type fakeMem struct {
	mu    sync.Mutex
	avail uint64
	ok    bool
	reads int
}

func (m *fakeMem) Read() (uint64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	return m.avail, m.ok
}

func (m *fakeMem) Set(avail uint64, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.avail, m.ok = avail, ok
}

func newTestSellScreenCache(clk *fakeClock, mem *fakeMem, budget, maxEntries int) *SellScreenCache {
	return newSellScreenCache(sellScreenCacheOptions{
		now:           clk.Now,
		memAvailable:  mem.Read,
		budgetBytes:   budget,
		maxEntries:    maxEntries,
		maxAge:        5 * time.Minute,
		memFloorBytes: 64 << 20,
		memCheckEvery: 5 * time.Second,
	})
}

func resp(body string) CachedResponse {
	h := http.Header{}
	h.Set("Content-Type", "text/html; charset=utf-8")
	return CachedResponse{Status: http.StatusOK, Header: h, Body: []byte(body)}
}

func newClockAndMem() (*fakeClock, *fakeMem) {
	return &fakeClock{t: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}, &fakeMem{avail: 1 << 30, ok: true}
}

func TestSellScreenCache_HitAndMiss(t *testing.T) {
	clk, mem := newClockAndMem()
	c := newTestSellScreenCache(clk, mem, 1<<20, 16)
	v := SellScreenVersion{Admin: 1, Sell: 1}
	if _, ok := c.Get("k", v); ok {
		t.Fatal("empty cache reported a hit")
	}
	if !c.Put("k", v, resp("<div>tiles</div>"), time.Time{}) {
		t.Fatal("Put refused a small entry with plenty of memory")
	}
	got, ok := c.Get("k", v)
	if !ok {
		t.Fatal("Get after Put missed")
	}
	if string(got.Body) != "<div>tiles</div>" || got.Status != 200 || got.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("cached response = %+v", got)
	}
	// The returned header must not alias the stored entry: a caller that
	// adds to what it was served can't corrupt the next hit. (Body is
	// shared, read-only by contract — http.ResponseWriter.Write never
	// modifies its argument — so a hit costs no copy of the whole fragment.)
	got.Header.Set("Content-Type", "text/plain")
	again, _ := c.Get("k", v)
	if string(again.Body) != "<div>tiles</div>" || again.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("mutating a served response changed the cache: %+v", again)
	}
}

func TestSellScreenCache_KeysAreSeparate(t *testing.T) {
	clk, mem := newClockAndMem()
	c := newTestSellScreenCache(clk, mem, 1<<20, 16)
	v := SellScreenVersion{Admin: 1, Sell: 1}
	base := sellScreenKeyParts{Route: "list", Locale: "en", Currency: "GBP", Translations: "t1", BrowsingMode: "category_tabs"}
	variants := map[string]sellScreenKeyParts{}
	variants["base"] = base
	k := base
	k.Locale = "de"
	variants["locale"] = k
	k = base
	k.Currency = "EUR"
	variants["currency"] = k
	k = base
	k.Translations = "t2"
	variants["translations"] = k
	k = base
	k.Granted = true
	variants["granted"] = k
	k = base
	k.BrowsingMode = "strip_overflow"
	variants["browsing mode"] = k
	k = base
	k.HideAllTab = true
	variants["hide all tab"] = k
	k = base
	k.Route = "category"
	k.Param = "cat-1"
	variants["category cat-1"] = k
	k.Param = "cat-2"
	variants["category cat-2"] = k

	seen := map[string]string{}
	for name, parts := range variants {
		key := parts.String()
		if other, dup := seen[key]; dup {
			t.Fatalf("%q and %q produced the same cache key %q", name, other, key)
		}
		seen[key] = name
		c.Put(key, v, resp("body for "+name), time.Time{})
	}
	for name, parts := range variants {
		got, ok := c.Get(parts.String(), v)
		if !ok || string(got.Body) != "body for "+name {
			t.Fatalf("%s: got %q ok=%v", name, got.Body, ok)
		}
	}
	// A field separator inside a value can't forge another key.
	a := sellScreenKeyParts{Route: "category", Param: "a|b"}
	b := sellScreenKeyParts{Route: "category", Param: "a", Locale: "b"}
	if a.String() == b.String() {
		t.Fatalf("key encoding is ambiguous: %q", a.String())
	}
}

func TestSellScreenCache_VersionBumpInvalidates(t *testing.T) {
	clk, mem := newClockAndMem()
	c := newTestSellScreenCache(clk, mem, 1<<20, 16)
	c.Put("k", SellScreenVersion{Admin: 1, Sell: 1}, resp("old"), time.Time{})
	if _, ok := c.Get("k", SellScreenVersion{Admin: 2, Sell: 1}); ok {
		t.Fatal("admin generation bump still hit")
	}
	c.Put("k", SellScreenVersion{Admin: 2, Sell: 1}, resp("new"), time.Time{})
	if _, ok := c.Get("k", SellScreenVersion{Admin: 2, Sell: 2}); ok {
		t.Fatal("sell generation bump still hit")
	}
	if c.Len() != 0 {
		t.Fatalf("a stale-version entry was kept after its miss: Len = %d", c.Len())
	}
}

func TestSellScreenCache_ExpiresAtPriceBoundary(t *testing.T) {
	clk, mem := newClockAndMem()
	c := newTestSellScreenCache(clk, mem, 1<<20, 16)
	v := SellScreenVersion{Admin: 1, Sell: 1}
	boundary := clk.Now().Add(90 * time.Second)
	c.Put("k", v, resp("before"), boundary)
	clk.Advance(89 * time.Second)
	if _, ok := c.Get("k", v); !ok {
		t.Fatal("entry expired before the price boundary")
	}
	clk.Advance(time.Second) // exactly at the boundary: the new price is live
	if _, ok := c.Get("k", v); ok {
		t.Fatal("entry still served at the price boundary")
	}
	// A boundary already in the past is not cached at all.
	if c.Put("k2", v, resp("x"), clk.Now().Add(-time.Second)) {
		t.Fatal("Put stored an entry whose price boundary has already passed")
	}
}

func TestSellScreenCache_MaxAge(t *testing.T) {
	clk, mem := newClockAndMem()
	c := newTestSellScreenCache(clk, mem, 1<<20, 16)
	v := SellScreenVersion{Admin: 1, Sell: 1}
	// A price boundary further out than maxAge never extends it.
	c.Put("k", v, resp("x"), clk.Now().Add(time.Hour))
	clk.Advance(5*time.Minute - time.Second)
	if _, ok := c.Get("k", v); !ok {
		t.Fatal("entry expired before max age")
	}
	clk.Advance(time.Second)
	if _, ok := c.Get("k", v); ok {
		t.Fatal("entry served past max age")
	}
}

func TestSellScreenCache_LRUEvictionUnderByteBudget(t *testing.T) {
	clk, mem := newClockAndMem()
	body := string(bytes.Repeat([]byte("a"), 1000))
	// Room for exactly three entries of this size (entry overhead included).
	one := sellScreenEntrySize("k0", resp(body))
	c := newTestSellScreenCache(clk, mem, 3*one, 100)
	v := SellScreenVersion{Admin: 1, Sell: 1}
	for i := 0; i < 3; i++ {
		c.Put(fmt.Sprintf("k%d", i), v, resp(body), time.Time{})
	}
	// Touch k0 so k1 becomes least recently used.
	if _, ok := c.Get("k0", v); !ok {
		t.Fatal("k0 missing before eviction")
	}
	c.Put("k3", v, resp(body), time.Time{})
	if _, ok := c.Get("k1", v); ok {
		t.Fatal("least-recently-used k1 survived eviction")
	}
	for _, k := range []string{"k0", "k2", "k3"} {
		if _, ok := c.Get(k, v); !ok {
			t.Fatalf("%s evicted, want only k1 evicted", k)
		}
	}
	if c.Bytes() > 3*one {
		t.Fatalf("cache holds %d bytes, budget %d", c.Bytes(), 3*one)
	}
}

func TestSellScreenCache_EntryCap(t *testing.T) {
	clk, mem := newClockAndMem()
	c := newTestSellScreenCache(clk, mem, 1<<20, 2)
	v := SellScreenVersion{Admin: 1, Sell: 1}
	for i := 0; i < 5; i++ {
		c.Put(fmt.Sprintf("k%d", i), v, resp("x"), time.Time{})
	}
	if c.Len() != 2 {
		t.Fatalf("Len = %d, want the entry cap 2", c.Len())
	}
}

func TestSellScreenCache_OversizeEntrySkipped(t *testing.T) {
	clk, mem := newClockAndMem()
	c := newTestSellScreenCache(clk, mem, 4096, 16)
	v := SellScreenVersion{Admin: 1, Sell: 1}
	c.Put("small", v, resp("x"), time.Time{})
	if c.Put("huge", v, resp(string(bytes.Repeat([]byte("a"), 8192))), time.Time{}) {
		t.Fatal("Put stored an entry larger than the whole budget")
	}
	if _, ok := c.Get("small", v); !ok {
		t.Fatal("an oversize Put evicted existing entries it was never going to fit beside")
	}
}

func TestSellScreenCache_MemoryFloorRefusesAndPurges(t *testing.T) {
	clk, mem := newClockAndMem()
	c := newTestSellScreenCache(clk, mem, 1<<20, 16)
	v := SellScreenVersion{Admin: 1, Sell: 1}
	c.Put("a", v, resp("x"), time.Time{})

	mem.Set(32<<20, true) // below the 64 MiB floor
	clk.Advance(6 * time.Second)
	if c.Put("b", v, resp("y"), time.Time{}) {
		t.Fatal("Put stored an entry while MemAvailable was below the floor")
	}
	if c.Len() != 0 {
		t.Fatalf("low memory did not purge the cache: Len = %d", c.Len())
	}

	// Recovered memory: storing works again after the next check interval.
	mem.Set(1<<30, true)
	clk.Advance(6 * time.Second)
	if !c.Put("c", v, resp("z"), time.Time{}) {
		t.Fatal("Put still refused after memory recovered")
	}
	// Unreadable meminfo (non-Linux): budget only, never a refusal.
	mem.Set(0, false)
	clk.Advance(6 * time.Second)
	if !c.Put("d", v, resp("w"), time.Time{}) {
		t.Fatal("unreadable MemAvailable refused a Put; want budget-only behaviour")
	}
}

func TestSellScreenCache_MemInfoReadIsRateLimited(t *testing.T) {
	clk, mem := newClockAndMem()
	c := newTestSellScreenCache(clk, mem, 1<<20, 1000)
	v := SellScreenVersion{Admin: 1, Sell: 1}
	for i := 0; i < 50; i++ {
		c.Put(fmt.Sprintf("k%d", i), v, resp("x"), time.Time{})
	}
	if mem.reads != 1 {
		t.Fatalf("MemAvailable read %d times within one check interval, want 1", mem.reads)
	}
}

func TestParseMemAvailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meminfo")
	content := "MemTotal:        8000000 kB\nMemFree:          100000 kB\nMemAvailable:     2048000 kB\nBuffers: 1 kB\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok := readMemAvailableFrom(path)
	if !ok || got != 2048000*1024 {
		t.Fatalf("readMemAvailableFrom = %d, %v; want %d, true", got, ok, 2048000*1024)
	}
	if _, ok := readMemAvailableFrom(filepath.Join(t.TempDir(), "missing")); ok {
		t.Fatal("missing meminfo reported ok")
	}
	bad := filepath.Join(t.TempDir(), "bad")
	_ = os.WriteFile(bad, []byte("MemTotal: 1 kB\n"), 0o600)
	if _, ok := readMemAvailableFrom(bad); ok {
		t.Fatal("meminfo without MemAvailable reported ok")
	}
}

func TestSellScreenCache_ConcurrentUse(t *testing.T) {
	clk, mem := newClockAndMem()
	c := newTestSellScreenCache(clk, mem, 64<<10, 8)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				v := SellScreenVersion{Admin: int64(i % 3), Sell: 1}
				k := fmt.Sprintf("k%d", (g+i)%12)
				if got, ok := c.Get(k, v); ok {
					got.Header.Set("X-Test", "mine") // served headers are the caller's own
				} else {
					c.Put(k, v, resp("body"), time.Time{})
				}
				if i%100 == 0 {
					clk.Advance(time.Second)
				}
			}
		}(g)
	}
	wg.Wait()
	if c.Bytes() > 64<<10 || c.Len() > 8 {
		t.Fatalf("bounds broken under concurrency: %d bytes, %d entries", c.Bytes(), c.Len())
	}
}
