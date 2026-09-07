package secrets

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/paths"
)

func withTestDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	paths.Init(dir)
	t.Cleanup(func() { paths.Init("") })
	return dir
}

func keyPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "secrets", "plugin_settings_key.bin")
}

// Primary/standalone: no fetch closure — the first Load generates a fresh
// 32-byte key, persists it, and every later Load (same store or a new one
// at the same path) returns the identical bytes.
func TestKeyStoreLoadGeneratesAndPersistsOnFirstUse(t *testing.T) {
	path := keyPath(t)
	ks := NewKeyStoreAt(path, nil)
	if ks.Exists() {
		t.Fatal("fresh store must not report a stored key")
	}
	k1, err := ks.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(k1) != KeySize {
		t.Fatalf("generated key is %d bytes, want %d", len(k1), KeySize)
	}
	if bytes.Equal(k1, make([]byte, KeySize)) {
		t.Fatal("generated key is all zeros")
	}
	if !ks.Exists() {
		t.Fatal("key must be persisted after first Load")
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read key file: %v", err)
	}
	if !bytes.Equal(onDisk, k1) {
		t.Fatal("persisted bytes differ from the returned key")
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("key file mode = %o, want 0600", fi.Mode().Perm())
		}
		dfi, _ := os.Stat(filepath.Dir(path))
		if dfi.Mode().Perm() != 0o700 {
			t.Fatalf("key dir mode = %o, want 0700", dfi.Mode().Perm())
		}
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("write-tmp-then-rename must not leave the .tmp file behind")
	}

	k2, err := ks.Load(context.Background())
	if err != nil || !bytes.Equal(k1, k2) {
		t.Fatalf("second Load: %x %v, want the same key", k2, err)
	}
	k3, err := NewKeyStoreAt(path, nil).Load(context.Background())
	if err != nil || !bytes.Equal(k1, k3) {
		t.Fatalf("fresh store at the same path: %x %v, want the same key", k3, err)
	}
	// Returned slices are copies: a caller scribbling on one must not
	// corrupt the cached key.
	k3[0] ^= 0xff
	k4, _ := ks.Load(context.Background())
	if !bytes.Equal(k1, k4) {
		t.Fatal("Load must return a copy, not the cached slice")
	}
}

func TestNewKeyStoreUsesDataRoot(t *testing.T) {
	dir := withTestDataDir(t)
	ks := NewKeyStore(nil)
	want := filepath.Join(dir, "secrets", "plugin_settings_key.bin")
	if ks.Path() != want {
		t.Fatalf("Path() = %q, want %q", ks.Path(), want)
	}
}

// A wrong-length file is corrupt, not a key: Load must refuse it rather
// than silently encrypting with garbage (which would make every value
// written from then on unreadable once the file is repaired), and must not
// treat it as "absent" and overwrite it with a fresh key either.
func TestKeyStoreLoadRejectsCorruptFile(t *testing.T) {
	path := keyPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	ks := NewKeyStoreAt(path, func(context.Context) ([]byte, error) { calls++; return testKey(t), nil })
	_, err := ks.Load(context.Background())
	if err == nil {
		t.Fatal("Load must fail on a 5-byte key file")
	}
	if errors.Is(err, ErrNoKeyYet) {
		t.Fatalf("a corrupt file is not 'no key yet': %v", err)
	}
	if calls != 0 {
		t.Fatal("a corrupt local file must not trigger a fetch (it would silently replace the key)")
	}
	if b, _ := os.ReadFile(path); string(b) != "short" {
		t.Fatal("corrupt file must be left in place for diagnosis, not overwritten")
	}
}

// Replica: the fetch closure runs only while no local file exists; once it
// succeeds the result is persisted and never fetched again — not on a later
// Load, and not by a fresh store at the same path.
func TestKeyStoreLoadFetchesOnceThenPersists(t *testing.T) {
	path := keyPath(t)
	want := testKey(t)
	calls := 0
	fetch := func(context.Context) ([]byte, error) { calls++; return want, nil }

	ks := NewKeyStoreAt(path, fetch)
	got, err := ks.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("Load must return the fetched key")
	}
	if calls != 1 {
		t.Fatalf("fetch called %d times on first Load, want 1", calls)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, want) {
		t.Fatal("fetched key must be persisted")
	}
	if _, err := ks.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := NewKeyStoreAt(path, fetch).Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("fetch called %d times overall, want exactly 1 (file present ⇒ no refetch)", calls)
	}
}

// A fetch that returns a wrong-length key must be refused and NOT persisted:
// the primary answered, but with something that isn't a key.
func TestKeyStoreLoadRejectsBadFetchResult(t *testing.T) {
	path := keyPath(t)
	ks := NewKeyStoreAt(path, func(context.Context) ([]byte, error) { return []byte("nope"), nil })
	if _, err := ks.Load(context.Background()); err == nil {
		t.Fatal("Load must reject a fetched key of the wrong length")
	}
	if ks.Exists() {
		t.Fatal("a rejected fetch result must not be persisted")
	}
}

// A fetch that declines with (nil, nil) means "this till is not a replica
// right now" — the store self-generates exactly as a primary would, so a
// till whose role is decided at runtime (enrolment after boot) does not
// need its KeyStore rebuilt.
func TestKeyStoreLoadSelfGeneratesWhenFetchDeclines(t *testing.T) {
	path := keyPath(t)
	ks := NewKeyStoreAt(path, func(context.Context) ([]byte, error) { return nil, nil })
	k, err := ks.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(k) != KeySize || !ks.Exists() {
		t.Fatal("declined fetch must fall back to local generation and persist")
	}
}

// Negative cache: after a failed fetch, a Load inside the retry floor must
// return ErrNoKeyYet immediately WITHOUT calling fetch again; once the floor
// has elapsed the fetch is retried; a success clears the floor for good.
// Driven by an injected clock — no sleeping.
func TestKeyStoreNegativeCacheFloorAfterFailedFetch(t *testing.T) {
	path := keyPath(t)
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	calls := 0
	fail := true
	want := testKey(t)
	fetch := func(context.Context) ([]byte, error) {
		calls++
		if fail {
			return nil, errors.New("primary unreachable")
		}
		return want, nil
	}
	ks := NewKeyStoreAt(path, fetch)
	ks.now = func() time.Time { return now }

	// Before any attempt: no floor — the first Load fetches.
	_, err := ks.Load(context.Background())
	if !errors.Is(err, ErrNoKeyYet) {
		t.Fatalf("failed fetch: want ErrNoKeyYet, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("fetch calls = %d, want 1", calls)
	}

	// Inside the floor: same error, no network.
	now = now.Add(fetchRetryFloor - time.Second)
	_, err = ks.Load(context.Background())
	if !errors.Is(err, ErrNoKeyYet) {
		t.Fatalf("inside floor: want ErrNoKeyYet, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("fetch must not be retried inside the %s floor (calls=%d)", fetchRetryFloor, calls)
	}

	// Floor elapsed: retried (and fails again, re-arming the floor).
	now = now.Add(2 * time.Second)
	if _, err = ks.Load(context.Background()); !errors.Is(err, ErrNoKeyYet) {
		t.Fatalf("after floor: want ErrNoKeyYet, got %v", err)
	}
	if calls != 2 {
		t.Fatalf("fetch calls = %d after the floor elapsed, want 2", calls)
	}
	now = now.Add(time.Second)
	_, _ = ks.Load(context.Background())
	if calls != 2 {
		t.Fatalf("second failure must re-arm the floor (calls=%d)", calls)
	}

	// Floor elapsed again and the primary is back: success, persisted, and
	// no further fetches ever — even with the clock frozen.
	fail = false
	now = now.Add(fetchRetryFloor)
	k, err := ks.Load(context.Background())
	if err != nil || !bytes.Equal(k, want) {
		t.Fatalf("recovered fetch: %x %v", k, err)
	}
	if calls != 3 {
		t.Fatalf("fetch calls = %d, want 3", calls)
	}
	for i := 0; i < 3; i++ {
		if _, err := ks.Load(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatalf("no fetch may happen after success (calls=%d)", calls)
	}
}

// ut-docs#1747: a persist failure after a SUCCESSFUL fetch (a wedged disk,
// not primary unreachability) must arm the same retry floor a failed fetch
// already does — otherwise a wedged disk turns every Load into a fresh
// network fetch instead of being rate-limited. Driven by an injected clock,
// same as TestKeyStoreNegativeCacheFloorAfterFailedFetch.
func TestKeyStoreFetchSucceedsButPersistFailureArmsNegativeCache(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	// Occupy the persist step's ".tmp" target with a NON-EMPTY directory, so
	// os.WriteFile(tmp, ...) inside persist() fails deterministically
	// (EISDIR) regardless of the test process's user — including root,
	// where a chmod-based permission failure wouldn't fire. Non-empty
	// matters: persist's own failure path does `os.Remove(tmp)`, which
	// silently succeeds (and clears the obstruction after one use) on an
	// EMPTY directory but fails on a non-empty one — this obstruction must
	// survive a second persist attempt for the "still fails inside the
	// floor" assertion below to mean anything. This leaves path itself
	// absent, so readFile's initial "not found" check (which must succeed
	// for Load to reach the fetch path at all) is unaffected.
	path := filepath.Join(secretsDir, "plugin_settings_key.bin")
	tmpObstruction := path + ".tmp"
	if err := os.MkdirAll(tmpObstruction, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpObstruction, "occupied"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	calls := 0
	want := testKey(t)
	fetch := func(context.Context) ([]byte, error) { calls++; return want, nil }
	ks := NewKeyStoreAt(path, fetch)
	ks.now = func() time.Time { return now }

	// First Load: fetch succeeds, persist fails.
	if _, err := ks.Load(context.Background()); err == nil {
		t.Fatal("Load must fail when persisting a successfully-fetched key fails")
	}
	if calls != 1 {
		t.Fatalf("fetch calls = %d, want 1", calls)
	}
	if ks.Exists() {
		t.Fatal("a key that failed to persist must not be reported as present")
	}

	// Inside the retry floor: must fail fast WITHOUT calling fetch again.
	// Before the #1747 fix, a persist failure left lastFail unset, so this
	// second Load re-fetched immediately instead of being rate-limited.
	now = now.Add(fetchRetryFloor - time.Second)
	if _, err := ks.Load(context.Background()); err == nil {
		t.Fatal("Load must still fail inside the retry floor")
	}
	if calls != 1 {
		t.Fatalf("fetch must not be retried inside the retry floor after a persist failure (calls=%d)", calls)
	}

	// Floor elapsed: retried. Clear the obstruction first so this second
	// attempt can actually succeed, proving the floor lifts normally too.
	now = now.Add(2 * time.Second)
	if err := os.RemoveAll(path + ".tmp"); err != nil {
		t.Fatal(err)
	}
	k, err := ks.Load(context.Background())
	if err != nil || !bytes.Equal(k, want) {
		t.Fatalf("after floor elapsed and obstruction cleared: %x %v", k, err)
	}
	if calls != 2 {
		t.Fatalf("fetch calls = %d after the floor elapsed, want 2", calls)
	}
}

// Concurrent callers during an in-flight fetch must not each start their
// own fetch (this backs a WASM host call a plugin can hammer): exactly one
// fetch runs; the rest fail fast with ErrNoKeyYet or observe the result.
func TestKeyStoreSingleFlightFetch(t *testing.T) {
	path := keyPath(t)
	started := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	want := testKey(t)
	ks := NewKeyStoreAt(path, func(context.Context) ([]byte, error) {
		calls++
		close(started)
		<-release
		return want, nil
	})
	done := make(chan error, 1)
	go func() {
		_, err := ks.Load(context.Background())
		done <- err
	}()
	<-started
	// While the first fetch is blocked, a second caller must return
	// immediately (not block behind the network) and must not fetch.
	_, err := ks.Load(context.Background())
	if !errors.Is(err, ErrNoKeyYet) {
		t.Fatalf("concurrent caller during fetch: want ErrNoKeyYet, got %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first caller: %v", err)
	}
	if calls != 1 {
		t.Fatalf("fetch calls = %d, want 1", calls)
	}
	if k, err := ks.Load(context.Background()); err != nil || !bytes.Equal(k, want) {
		t.Fatalf("after fetch: %x %v", k, err)
	}
}

// ClearLocalKeyFile is the replica-join hook (internal/db.ApplyReplicaIdentity):
// removing the file at the canonical production path is what makes the next
// Load (via the registered fetch closure) fetch the shop's key from the
// primary instead of continuing to use a stale standalone-generated one.
func TestClearLocalKeyFileRemovesExistingKey(t *testing.T) {
	dir := withTestDataDir(t)
	path := filepath.Join(dir, "secrets", "plugin_settings_key.bin")

	ks := NewKeyStore(nil)
	if ks.Path() != path {
		t.Fatalf("test setup: KeyStore path = %q, want %q", ks.Path(), path)
	}
	if _, err := ks.Load(context.Background()); err != nil {
		t.Fatalf("seed a local key: %v", err)
	}
	if !ks.Exists() {
		t.Fatal("test setup: key file must exist before clearing")
	}

	if err := ClearLocalKeyFile(); err != nil {
		t.Fatalf("ClearLocalKeyFile: %v", err)
	}
	if ks.Exists() {
		t.Fatal("key file must be gone after ClearLocalKeyFile")
	}
}

// Independent-review finding (ut-docs#1745): persist writes path+".tmp"
// then renames it into place — a crash between those two steps leaves a
// plaintext-key .tmp file behind. Clearing the key is supposed to destroy
// it; leaving that sibling on disk forever defeats the whole point.
func TestClearLocalKeyFileRemovesTmpSibling(t *testing.T) {
	dir := withTestDataDir(t)
	path := filepath.Join(dir, "secrets", "plugin_settings_key.bin")
	tmp := path + ".tmp"

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, testKey(t), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmp, testKey(t), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := ClearLocalKeyFile(); err != nil {
		t.Fatalf("ClearLocalKeyFile: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stat key file: %v, want not-exist", err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("stat .tmp sibling: %v, want not-exist — a crash-leftover plaintext key must not survive a clear", err)
	}
}

// The .tmp sibling can exist with no finished key file at all (a crash
// during the very first persist) — clearing must still remove it and must
// not error just because the "real" file was never there.
func TestClearLocalKeyFileRemovesOrphanTmpWithNoRealFile(t *testing.T) {
	dir := withTestDataDir(t)
	path := filepath.Join(dir, "secrets", "plugin_settings_key.bin")
	tmp := path + ".tmp"

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmp, testKey(t), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := ClearLocalKeyFile(); err != nil {
		t.Fatalf("ClearLocalKeyFile: %v", err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("stat orphan .tmp: %v, want not-exist", err)
	}
}

// A till joining for the first time has no local key at all — clearing it
// must be a harmless no-op, not an error, so ApplyReplicaIdentity doesn't
// have to special-case "never had a key".
func TestClearLocalKeyFileNoopWhenAbsent(t *testing.T) {
	withTestDataDir(t)
	if NewKeyStore(nil).Exists() {
		t.Fatal("test setup: no key should exist yet")
	}
	if err := ClearLocalKeyFile(); err != nil {
		t.Fatalf("ClearLocalKeyFile on an absent file must not error: %v", err)
	}
}

func TestDefaultSingleton(t *testing.T) {
	prev := Default()
	t.Cleanup(func() { SetDefault(prev) })
	SetDefault(nil)
	if Default() != nil {
		t.Fatal("Default must be nil after SetDefault(nil)")
	}
	if _, err := SealWithDefault(context.Background(), []byte("x")); err == nil {
		t.Fatal("SealWithDefault must fail when no store is registered (never store plaintext by accident)")
	}
	ks := NewKeyStoreAt(keyPath(t), nil)
	SetDefault(ks)
	if Default() != ks {
		t.Fatal("Default must return the registered store")
	}
	sealed, err := SealWithDefault(context.Background(), []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := OpenWithDefault(context.Background(), sealed)
	if err != nil || string(got) != "hello" {
		t.Fatalf("OpenWithDefault: %q %v", got, err)
	}
	if _, err := OpenWithDefault(context.Background(), "plain"); !errors.Is(err, ErrNotSealed) {
		t.Fatalf("OpenWithDefault on plaintext: want ErrNotSealed, got %v", err)
	}
}
