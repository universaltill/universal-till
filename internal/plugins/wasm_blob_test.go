package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/paths"
)

// ADR-0121 §3/§6 (ut-docs#3870): blob:own — blob_put_open / blob_write /
// blob_commit / blob_get_open / blob_read / blob_delete / blob_list. Driven
// through a real wasip1 guest (testdata/blob_guest) against a temp data root.

func buildBlobGuest(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "blob_guest.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/blob_guest")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasip1 blob guest: %v\n%s", err, raw)
	}
	return out
}

// blobFixture is one data root holding any number of test plugins.
type blobFixture struct {
	t     *testing.T
	root  string
	guest string
	db    *sql.DB
	w     *WasmRuntime
}

func newBlobFixture(t *testing.T) *blobFixture {
	t.Helper()
	root := t.TempDir()
	prev := paths.DataDir()
	paths.Init(root)
	t.Cleanup(func() { paths.Init(prev) })
	return &blobFixture{t: t, root: root, guest: buildBlobGuest(t), db: hostfnTestDB(t), w: NewWasmRuntime(t.TempDir())}
}

// plugin installs pluginID (manifest with limits.storage_mb = storageMB, 0 =
// default) and grants it perms.
func (f *blobFixture) plugin(pluginID string, storageMB int, perms ...string) {
	f.t.Helper()
	dir := filepath.Join(f.root, "plugins", pluginID, "1.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	limits := ""
	if storageMB > 0 {
		limits = fmt.Sprintf(`,"limits":{"storage_mb":%d}`, storageMB)
	}
	manifest := fmt.Sprintf(`{"id":%q,"name":"t","version":"1.0.0","entrypoint":"./plugin.wasm","runtime":"wasm"%s}`, pluginID, limits)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		f.t.Fatal(err)
	}
	seedPlugin(f.t, f.db, pluginID)
	grantPerm(f.t, f.db, pluginID, "storage")
	for _, p := range perms {
		grantPerm(f.t, f.db, pluginID, p)
	}
	if err := f.w.load(pluginID, "1.0.0", f.guest); err != nil {
		f.t.Fatalf("load: %v", err)
	}
}

type blobResult struct {
	Code  int32   `json:"code"`
	Codes []int32 `json:"codes"`
	Data  string  `json:"data"`
}

func (f *blobFixture) run(pluginID string, ops ...map[string]any) []blobResult {
	f.t.Helper()
	res := runGuestPayload(f.t, f.w, f.db, pluginID, map[string]any{"ops": ops})
	raw, _ := json.Marshal(res["results"])
	var out []blobResult
	if err := json.Unmarshal(raw, &out); err != nil {
		f.t.Fatalf("parse results %s: %v", raw, err)
	}
	if len(out) != len(ops) {
		f.t.Fatalf("guest ran %d of %d ops: %s", len(out), len(ops), raw)
	}
	return out
}

func (f *blobFixture) blobDir(pluginID string) string {
	return filepath.Join(f.root, "plugin-data", pluginID, "blobs")
}

// dirNames lists every entry in pluginID's blob dir (temp files included).
func (f *blobFixture) dirNames(pluginID string) []string {
	f.t.Helper()
	ents, err := os.ReadDir(f.blobDir(pluginID))
	if err != nil && !os.IsNotExist(err) {
		f.t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

func put(name, data string) []map[string]any {
	return []map[string]any{
		{"op": "put_open", "name": name},
		{"op": "write", "h": -1, "data": data}, // h fixed up by seq
		{"op": "commit", "h": -1},
	}
}

// seq flattens op groups, pointing each "h": -1 at the group's first op.
func seq(groups ...[]map[string]any) []map[string]any {
	var out []map[string]any
	for _, g := range groups {
		base := len(out)
		for _, o := range g {
			c := map[string]any{}
			for k, v := range o {
				c[k] = v
			}
			if h, ok := c["h"].(int); ok && h == -1 {
				c["h"] = base
			}
			out = append(out, c)
		}
	}
	return out
}

func get(name string) []map[string]any {
	return []map[string]any{
		{"op": "get_open", "name": name},
		{"op": "read_all", "h": -1, "cap": 3},
	}
}

func TestBlobRoundTrip(t *testing.T) {
	f := newBlobFixture(t)
	const id = "com.test.blob.roundtrip"
	f.plugin(id, 0, "blob:own")

	r := f.run(id, seq(put("photo.jpg", "hello blob"), get("photo.jpg"))...)
	if r[0].Code < 0 || r[1].Code != 10 || r[2].Code != 0 {
		t.Fatalf("put = %+v", r[:3])
	}
	if r[3].Code < 0 || r[4].Data != "hello blob" || r[4].Code != 0 {
		t.Fatalf("get = %+v, want the data then EOF (0)", r[3:])
	}
	got, err := os.ReadFile(filepath.Join(f.blobDir(id), "photo.jpg"))
	if err != nil || string(got) != "hello blob" {
		t.Fatalf("on disk: %q, %v", got, err)
	}

	// A later event sees it, can overwrite it, list it and delete it.
	r = f.run(id, seq(put("photo.jpg", "v2"), put("a-b_c.1", "x"),
		[]map[string]any{{"op": "list", "cap": 4096}}, get("photo.jpg"),
		[]map[string]any{{"op": "delete", "name": "photo.jpg"}, {"op": "delete", "name": "photo.jpg"}, {"op": "get_open", "name": "photo.jpg"}, {"op": "list", "cap": 4096}})...)
	if r[2].Code != 0 || r[5].Code != 0 {
		t.Fatalf("puts = %+v", r[:6])
	}
	if want := `[{"name":"a-b_c.1","size":1},{"name":"photo.jpg","size":2}]`; r[6].Data != want {
		t.Fatalf("list = %q (%d), want %q", r[6].Data, r[6].Code, want)
	}
	if r[8].Data != "v2" {
		t.Fatalf("overwritten blob reads %q, want v2", r[8].Data)
	}
	if r[9].Code != 0 || r[10].Code != hostErrNotFound || r[11].Code != hostErrNotFound {
		t.Fatalf("delete, delete again, get after delete = %d %d %d; want 0 -1 -1", r[9].Code, r[10].Code, r[11].Code)
	}
	if want := `[{"name":"a-b_c.1","size":1}]`; r[12].Data != want {
		t.Fatalf("list after delete = %q, want %q", r[12].Data, want)
	}
}

func TestBlobListBufferABI(t *testing.T) {
	f := newBlobFixture(t)
	const id = "com.test.blob.list"
	f.plugin(id, 0, "blob:own")
	r := f.run(id, seq([]map[string]any{{"op": "list", "cap": 64}}, put("a", "1"), []map[string]any{{"op": "list", "cap": 4}})...)
	if r[0].Data != "[]" || r[0].Code != 2 {
		t.Fatalf("empty store list = %q (%d), want []", r[0].Data, r[0].Code)
	}
	if want := len(`[{"name":"a","size":1}]`); int(r[4].Code) != want {
		t.Fatalf("list into a short buffer returned %d, want the full length %d", r[4].Code, want)
	}
}

func TestBlobGetHandleReleasedAtEOF(t *testing.T) {
	f := newBlobFixture(t)
	const id = "com.test.blob.eof"
	f.plugin(id, 0, "blob:own")
	groups := [][]map[string]any{put("a", "abc")}
	// More reads than the handle cap: each handle is released at EOF.
	for i := 0; i < maxBlobHandles+2; i++ {
		groups = append(groups, get("a"))
	}
	r := f.run(id, seq(groups...)...)
	for i := 3; i < len(r); i += 2 {
		if r[i].Code < 0 || r[i+1].Data != "abc" {
			t.Fatalf("read #%d: open %d, data %q", (i-3)/2, r[i].Code, r[i+1].Data)
		}
	}
	// Reading a released handle is "not found".
	r = f.run(id, seq(get("a"), []map[string]any{{"op": "read", "h": 0, "cap": 8}})...)
	if r[2].Code != hostErrNotFound {
		t.Fatalf("read after EOF = %d, want %d", r[2].Code, hostErrNotFound)
	}
}

func TestBlobMaxHandles(t *testing.T) {
	f := newBlobFixture(t)
	const id = "com.test.blob.busy"
	f.plugin(id, 0, "blob:own")
	var ops []map[string]any
	for i := 0; i <= maxBlobHandles; i++ {
		ops = append(ops, map[string]any{"op": "put_open", "name": fmt.Sprintf("b%d", i)})
	}
	r := f.run(id, ops...)
	for i := 0; i < maxBlobHandles; i++ {
		if r[i].Code < 0 {
			t.Fatalf("open #%d = %d", i, r[i].Code)
		}
	}
	if r[maxBlobHandles].Code != hostErrBusy {
		t.Fatalf("open past the cap = %d, want %d", r[maxBlobHandles].Code, hostErrBusy)
	}
}

func TestBlobQuota(t *testing.T) {
	const oneMiB = 1 << 20
	t.Run("exactly the quota commits, one byte more is -5", func(t *testing.T) {
		f := newBlobFixture(t)
		const id = "com.test.blob.quota"
		f.plugin(id, 1, "blob:own")
		r := f.run(id,
			map[string]any{"op": "put_open", "name": "full"},
			map[string]any{"op": "write_n", "h": 0, "n": oneMiB},
			map[string]any{"op": "commit", "h": 0},
			map[string]any{"op": "put_open", "name": "more"},
			map[string]any{"op": "write", "h": 3, "data": "x"},
			map[string]any{"op": "commit", "h": 3},
		)
		if r[1].Code < 0 || r[2].Code != 0 {
			t.Fatalf("a blob of exactly the quota: write %v, commit %d", r[1].Codes, r[2].Code)
		}
		if r[4].Code != hostErrQuota {
			t.Fatalf("one byte over the quota: write = %d, want %d", r[4].Code, hostErrQuota)
		}
		if r[5].Code != hostErrNotFound {
			t.Fatalf("commit after a quota refusal = %d, want %d (handle discarded)", r[5].Code, hostErrNotFound)
		}
		if names := f.dirNames(id); len(names) != 1 || names[0] != "full" {
			t.Fatalf("blob dir = %v, want only [full] (no temp file left)", names)
		}
	})
	t.Run("overwriting counts the new size, not both", func(t *testing.T) {
		f := newBlobFixture(t)
		const id = "com.test.blob.quota.replace"
		f.plugin(id, 1, "blob:own")
		r := f.run(id,
			map[string]any{"op": "put_open", "name": "a"},
			map[string]any{"op": "write_n", "h": 0, "n": oneMiB - 10},
			map[string]any{"op": "commit", "h": 0},
			map[string]any{"op": "put_open", "name": "a"},
			map[string]any{"op": "write_n", "h": 3, "n": oneMiB},
			map[string]any{"op": "commit", "h": 3},
		)
		if r[2].Code != 0 || r[4].Code < 0 || r[5].Code != 0 {
			t.Fatalf("replace within quota: %+v", r)
		}
	})
	t.Run("two puts that fit alone but not together", func(t *testing.T) {
		f := newBlobFixture(t)
		const id = "com.test.blob.quota.pair"
		f.plugin(id, 1, "blob:own")
		r := f.run(id,
			map[string]any{"op": "put_open", "name": "a"},
			map[string]any{"op": "put_open", "name": "b"},
			map[string]any{"op": "write_n", "h": 0, "n": oneMiB / 2},
			map[string]any{"op": "write_n", "h": 1, "n": oneMiB/2 + 1},
			map[string]any{"op": "commit", "h": 0},
			map[string]any{"op": "commit", "h": 1},
		)
		if r[4].Code != 0 {
			t.Fatalf("first commit = %d, want 0", r[4].Code)
		}
		if r[3].Code >= 0 && r[5].Code != hostErrQuota {
			t.Fatalf("second blob pushes the store over quota: write %d, commit %d; want a %d", r[3].Code, r[5].Code, hostErrQuota)
		}
		if names := f.dirNames(id); len(names) != 1 || names[0] != "a" {
			t.Fatalf("blob dir = %v, want only [a]", names)
		}
	})
}

func TestBlobNameRefusals(t *testing.T) {
	f := newBlobFixture(t)
	const id = "com.test.blob.names"
	f.plugin(id, 0, "blob:own")
	// A sibling plugin's blob the traversal names try to reach.
	f.plugin("com.test.blob.victim", 0, "blob:own")
	f.run("com.test.blob.victim", seq(put("secret", "s"))...)

	bad := []string{"", ".", "..", "../com.test.blob.victim/blobs/secret", "a/b", `a\b`, "/etc/passwd",
		"Upper", "sp ace", "con", "nul.txt", "trailing.", strings.Repeat("a", 129), "~put-1", "naïve"}
	var ops []map[string]any
	for _, n := range bad {
		ops = append(ops,
			map[string]any{"op": "put_open", "name": n},
			map[string]any{"op": "get_open", "name": n},
			map[string]any{"op": "delete", "name": n})
	}
	r := f.run(id, ops...)
	for i, n := range bad {
		for j, op := range []string{"put_open", "get_open", "delete"} {
			if c := r[i*3+j].Code; c != hostErrInvalid {
				t.Errorf("%s(%q) = %d, want %d", op, n, c, hostErrInvalid)
			}
		}
	}
	if names := f.dirNames(id); len(names) != 0 {
		t.Fatalf("refused names left files: %v", names)
	}
	if _, err := os.Stat(filepath.Join(f.blobDir("com.test.blob.victim"), "secret")); err != nil {
		t.Fatalf("victim blob gone: %v", err)
	}
	// The longest legal name works.
	long := strings.Repeat("a", 128)
	if r := f.run(id, seq(put(long, "ok"))...); r[2].Code != 0 {
		t.Fatalf("128-char name: %+v", r)
	}
}

func TestBlobPermissionDenied(t *testing.T) {
	f := newBlobFixture(t)
	const id = "com.test.blob.denied"
	f.plugin(id, 0) // no blob:own
	r := f.run(id,
		map[string]any{"op": "put_open", "name": "a"},
		map[string]any{"op": "get_open", "name": "a"},
		map[string]any{"op": "delete", "name": "a"},
		map[string]any{"op": "list", "cap": 64},
		map[string]any{"op": "write", "h": 0, "data": "x"},
		map[string]any{"op": "commit", "h": 0},
		map[string]any{"op": "read", "h": 0, "cap": 8},
	)
	for i, res := range r {
		if res.Code != hostErrDenied {
			t.Errorf("op %d = %d, want %d", i, res.Code, hostErrDenied)
		}
	}
	if _, err := os.Stat(f.blobDir(id)); !os.IsNotExist(err) {
		t.Fatalf("a denied plugin got a blob dir: %v", err)
	}

	// Revoked live: the next call after revocation is denied.
	const id2 = "com.test.blob.revoked"
	f.plugin(id2, 0, "blob:own")
	r = f.run(id2, seq(put("kept", "1"))...)
	if r[2].Code != 0 {
		t.Fatalf("granted put: %+v", r)
	}
	revokePerm(t, f.db, id2, "blob:own")
	r = f.run(id2, seq(get("kept"))...)
	if r[0].Code != hostErrDenied {
		t.Fatalf("get after revocation = %d, want %d", r[0].Code, hostErrDenied)
	}
}

func TestBlobUncommittedPutLeavesNothing(t *testing.T) {
	f := newBlobFixture(t)
	const id = "com.test.blob.abandon"
	f.plugin(id, 0, "blob:own")
	var states []*hostState
	onHostState = func(s *hostState) { states = append(states, s) }
	t.Cleanup(func() { onHostState = nil })

	// Never committed: no blob and no temp file once the event returns.
	r := f.run(id,
		map[string]any{"op": "put_open", "name": "draft"},
		map[string]any{"op": "write", "h": 0, "data": "half"},
		map[string]any{"op": "get_open", "name": "draft"},
	)
	if r[0].Code < 0 || r[1].Code != 4 {
		t.Fatalf("put: %+v", r)
	}
	if r[2].Code != hostErrNotFound {
		t.Fatalf("an uncommitted blob is readable: get_open = %d", r[2].Code)
	}
	if names := f.dirNames(id); len(names) != 0 {
		t.Fatalf("blob dir after an uncommitted put = %v, want empty", names)
	}
	if n := states[0].blobs.count(); n != 0 {
		t.Fatalf("%d blob handles still open after the event returned", n)
	}

	// An uncommitted overwrite leaves the old blob untouched.
	f.run(id, seq(put("doc", "old"))...)
	f.run(id, map[string]any{"op": "put_open", "name": "doc"}, map[string]any{"op": "write", "h": 0, "data": "new!"})
	if r := f.run(id, seq(get("doc"))...); r[1].Data != "old" {
		t.Fatalf("uncommitted overwrite changed the blob to %q", r[1].Data)
	}
	if names := f.dirNames(id); len(names) != 1 {
		t.Fatalf("blob dir = %v, want only [doc]", names)
	}
}

func TestBlobStaleTempSwept(t *testing.T) {
	f := newBlobFixture(t)
	const id = "com.test.blob.stale"
	f.plugin(id, 1, "blob:own")
	// A crash mid-put leaves a temp file; it must not count toward the
	// quota or show in the list, and an old one is removed.
	dir := f.blobDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, blobTempPrefix+"123")
	if err := os.WriteFile(stale, make([]byte, 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * blobTempMaxAge)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	r := f.run(id, seq(put("a", "x"), []map[string]any{{"op": "list", "cap": 256}})...)
	if r[2].Code != 0 {
		t.Fatalf("put with a stale temp present: %+v", r)
	}
	if want := `[{"name":"a","size":1}]`; r[3].Data != want {
		t.Fatalf("list = %q, want %q", r[3].Data, want)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale temp not swept: %v", err)
	}
}

func TestBlobPluginIsolation(t *testing.T) {
	f := newBlobFixture(t)
	const a, b = "com.test.blob.a", "com.test.blob.b"
	f.plugin(a, 0, "blob:own")
	f.plugin(b, 0, "blob:own")
	f.run(a, seq(put("shared-name", "from a"))...)

	r := f.run(b, seq(get("shared-name"), []map[string]any{{"op": "list", "cap": 256}, {"op": "delete", "name": "shared-name"}})...)
	if r[0].Code != hostErrNotFound {
		t.Fatalf("plugin b opened plugin a's blob: %d", r[0].Code)
	}
	if r[2].Data != "[]" {
		t.Fatalf("plugin b lists %q, want []", r[2].Data)
	}
	if r[3].Code != hostErrNotFound {
		t.Fatalf("plugin b deleted plugin a's blob: %d", r[3].Code)
	}
	f.run(b, seq(put("shared-name", "from b"))...)
	if r := f.run(a, seq(get("shared-name"))...); r[1].Data != "from a" {
		t.Fatalf("plugin a reads %q after b wrote the same name", r[1].Data)
	}
}

func TestValidBlobName(t *testing.T) {
	for _, ok := range []string{"a", "photo.jpg", "a-b_c.1", "0", "..a", strings.Repeat("z", 128)} {
		if !validBlobName(ok) {
			t.Errorf("validBlobName(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", ".", "..", "a/b", "A", "lpt1", "x.", strings.Repeat("z", 129)} {
		if validBlobName(bad) {
			t.Errorf("validBlobName(%q) = true", bad)
		}
	}
}

func (r *blobHandles) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.open)
}

// In-flight puts count toward the quota: several open puts cannot together
// put more than the quota on disk (review finding, ut-docs#3870).
func TestBlobQuotaCountsInFlightPuts(t *testing.T) {
	const oneMiB = 1 << 20
	f := newBlobFixture(t)
	const id = "com.test.blob.inflight"
	f.plugin(id, 1, "blob:own")
	r := f.run(id,
		map[string]any{"op": "put_open", "name": "a"},
		map[string]any{"op": "put_open", "name": "b"},
		map[string]any{"op": "write_n", "h": 0, "n": oneMiB / 2},
		map[string]any{"op": "write_n", "h": 1, "n": oneMiB/2 + 1},
	)
	if r[2].Code < 0 {
		t.Fatalf("first half-quota write: %v", r[2].Codes)
	}
	if r[3].Code != hostErrQuota {
		t.Fatalf("second put's write takes the in-flight total past the quota: codes %v, want a trailing %d", r[3].Codes, hostErrQuota)
	}
}

// Concurrent events of one plugin cannot both commit past the quota.
func TestBlobQuotaConcurrentCommits(t *testing.T) {
	const kib700 = 700 << 10
	f := newBlobFixture(t)
	const id = "com.test.blob.concurrent"
	f.plugin(id, 1, "blob:own")
	f.w.mu.Lock()
	f.w.db = f.db
	f.w.mu.Unlock()
	for iter := 0; iter < 6; iter++ {
		errs := make(chan error, 2)
		for _, name := range []string{"x", "y"} {
			go func(name string) {
				body, _ := json.Marshal(map[string]any{"ops": []map[string]any{
					{"op": "put_open", "name": name},
					{"op": "write_n", "h": 0, "n": kib700},
					{"op": "commit", "h": 0},
				}})
				_, err := f.w.HandleEvent(context.Background(), id, Event{ID: "ev-" + name, Type: "test.event", Timestamp: time.Now(), Payload: body})
				errs <- err
			}(name)
		}
		for i := 0; i < 2; i++ {
			if err := <-errs; err != nil {
				t.Fatalf("HandleEvent: %v", err)
			}
		}
		var total int64
		ents, _ := os.ReadDir(f.blobDir(id))
		for _, e := range ents {
			if info, err := e.Info(); err == nil {
				total += info.Size()
			}
		}
		if total > 1<<20 {
			t.Fatalf("iteration %d: %d bytes committed under a 1 MiB quota", iter, total)
		}
		for _, n := range []string{"x", "y"} {
			_ = os.Remove(filepath.Join(f.blobDir(id), n))
		}
	}
}
