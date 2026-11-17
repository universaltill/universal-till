package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/lantls"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#5198: rotation of a replica's LAN sync bearer, end to end over the
// real main-till routes and syncTill.

// rotationMain is a main till with the sync surface and the rotation route
// mounted, on a clock the test moves.
type rotationMain struct {
	dp    *common.Deps
	mux   *http.ServeMux
	tills *data.TillsRepo
	now   time.Time
}

func newRotationMain(t *testing.T) *rotationMain {
	t.Helper()
	t.Setenv("UT_AUTH", "off")
	m := &rotationMain{dp: newSyncPairingGateTestDeps(t), mux: http.NewServeMux(), now: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	registerSyncAPI(m.mux, m.dp)
	registerSyncAdmin(m.mux, m.dp)
	registerSyncSales(m.mux, m.dp)
	registerSyncBearerRotate(m.mux, m.dp)
	m.tills = data.NewTillsRepo(m.dp.Db)
	prev := syncBearerNow
	syncBearerNow = func() time.Time { return m.now }
	t.Cleanup(func() { syncBearerNow = prev })
	return m
}

// enrolLegacy enrols a till whose bearer predates rotation (issued_at NULL),
// i.e. what every till has right after the upgrade.
func (m *rotationMain) enrolLegacy(t *testing.T, bearer string) string {
	t.Helper()
	id, err := m.tills.InsertTill(context.Background(), "Till 2", hashBearer(bearer))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.dp.Db.Exec(`UPDATE tills SET bearer_issued_at = NULL WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (m *rotationMain) call(method, path, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader("[]"))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	m.mux.ServeHTTP(rec, req)
	return rec
}

func (m *rotationMain) rotate(t *testing.T, bearer string) (string, *httptest.ResponseRecorder) {
	t.Helper()
	rec := m.call(http.MethodPost, lantls.BearerRotatePath, bearer)
	var out struct {
		Data struct {
			Bearer string `json:"bearer"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out.Data.Bearer, rec
}

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestSyncBearerRotate_BackupBearerRefusedAfterGrace(t *testing.T) {
	m := newRotationMain(t)
	m.enrolLegacy(t, "legacy-bearer")

	// A legacy bearer works, and the answer asks the replica to rotate.
	rec := m.call(http.MethodGet, "/api/sync/admin", "legacy-bearer")
	if rec.Code != http.StatusOK || rec.Header().Get(lantls.BearerRotateHeader) != "1" {
		t.Fatalf("legacy bearer: code=%d rotate-header=%q, want 200 and 1", rec.Code, rec.Header().Get(lantls.BearerRotateHeader))
	}
	fresh, rec := m.rotate(t, "legacy-bearer")
	if rec.Code != http.StatusOK || !hex64.MatchString(fresh) || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("rotate: code=%d bearer=%q cache-control=%q", rec.Code, fresh, rec.Header().Get("Cache-Control"))
	}
	// The new bearer works and is not asked to rotate again.
	if rec = m.call(http.MethodGet, "/api/sync/admin", fresh); rec.Code != http.StatusOK || rec.Header().Get(lantls.BearerRotateHeader) != "" {
		t.Fatalf("new bearer: code=%d rotate-header=%q", rec.Code, rec.Header().Get(lantls.BearerRotateHeader))
	}
	// The copy of the old bearer (an old backup) is good only for the grace.
	m.now = m.now.Add(data.SyncBearerGrace - time.Minute)
	if rec = m.call(http.MethodGet, "/api/sync/admin", "legacy-bearer"); rec.Code != http.StatusOK {
		t.Fatalf("legacy bearer inside the grace = %d, want 200", rec.Code)
	}
	m.now = m.now.Add(2 * time.Minute)
	for _, c := range [][2]string{{http.MethodGet, "/api/sync/admin"}, {http.MethodPost, "/api/sync/sales"}} {
		if rec = m.call(c[0], c[1], "legacy-bearer"); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with the old bearer after the grace = %d, want 401", c[0], c[1], rec.Code)
		}
		if rec = m.call(c[0], c[1], fresh); rec.Code == http.StatusUnauthorized {
			t.Errorf("%s %s with the new bearer = 401", c[0], c[1])
		}
	}
}

func TestSyncBearerRotate_LostResponseReplicaNotLockedOut(t *testing.T) {
	m := newRotationMain(t)
	m.enrolLegacy(t, "legacy-bearer")

	lost, rec := m.rotate(t, "legacy-bearer") // the answer never reaches the replica
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate = %d", rec.Code)
	}
	// Hours later the replica still holds only the old bearer: it works, and
	// asks again.
	m.now = m.now.Add(5 * time.Hour)
	rec = m.call(http.MethodGet, "/api/sync/admin", "legacy-bearer")
	if rec.Code != http.StatusOK || rec.Header().Get(lantls.BearerRotateHeader) != "1" {
		t.Fatalf("old bearer while the new one is unused: code=%d header=%q", rec.Code, rec.Header().Get(lantls.BearerRotateHeader))
	}
	again, rec := m.rotate(t, "legacy-bearer")
	if rec.Code != http.StatusOK || !hex64.MatchString(again) || again == lost {
		t.Fatalf("second rotate: code=%d bearer=%q", rec.Code, again)
	}
	if rec = m.call(http.MethodGet, "/api/sync/admin", lost); rec.Code != http.StatusUnauthorized {
		t.Fatalf("the bearer of the lost answer = %d, want 401 (replaced)", rec.Code)
	}
	if rec = m.call(http.MethodGet, "/api/sync/admin", again); rec.Code != http.StatusOK {
		t.Fatalf("the replacement bearer = %d, want 200", rec.Code)
	}
	m.now = m.now.Add(data.SyncBearerGrace + time.Second)
	if rec = m.call(http.MethodGet, "/api/sync/admin", "legacy-bearer"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old bearer after the grace = %d, want 401", rec.Code)
	}
}

func TestSyncBearerRotate_OldBearerCannotRotateOnceNewOneIsInUse(t *testing.T) {
	m := newRotationMain(t)
	m.enrolLegacy(t, "legacy-bearer")
	fresh, _ := m.rotate(t, "legacy-bearer")
	m.call(http.MethodGet, "/api/sync/admin", fresh) // grace starts

	if stolen, rec := m.rotate(t, "legacy-bearer"); rec.Code != http.StatusConflict || stolen != "" {
		t.Fatalf("rotate with the old bearer after the new one was used = %d (bearer %q), want 409", rec.Code, stolen)
	}
	if rec := m.call(http.MethodGet, "/api/sync/admin", fresh); rec.Code != http.StatusOK {
		t.Fatalf("the current bearer = %d after the refused rotation", rec.Code)
	}
	if _, rec := m.rotate(t, "no-such-bearer"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("rotate with an unknown bearer = %d, want 401", rec.Code)
	}
	if _, rec := m.rotate(t, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("rotate with no bearer = %d, want 401", rec.Code)
	}
}

func TestSyncBearerRotate_RefusedOnReplica(t *testing.T) {
	m := newRotationMain(t)
	m.enrolLegacy(t, "legacy-bearer")
	if err := m.dp.Settings.Set(context.Background(), "sync.primary_url", "http://192.0.2.1:8080"); err != nil {
		t.Fatal(err)
	}
	if bearer, rec := m.rotate(t, "legacy-bearer"); rec.Code != http.StatusConflict || bearer != "" {
		t.Fatalf("rotate on a replica = %d (bearer %q), want 409", rec.Code, bearer)
	}
}

// A pinned till's bearer -- the previous one included -- is refused on plain
// HTTP: rotation opens no downgrade path.
func TestSyncTill_PinnedTillRefusesPlainHTTPWithPrevBearer(t *testing.T) {
	m := newRotationMain(t)
	id := m.enrolLegacy(t, "legacy-bearer")
	fresh, _ := m.rotate(t, "legacy-bearer")
	if err := m.tills.MarkTLSPinned(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	for name, bearer := range map[string]string{"previous": "legacy-bearer", "current": fresh} {
		if rec := m.call(http.MethodGet, "/api/sync/admin", bearer); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s bearer of a pinned till over plain HTTP = %d, want 401", name, rec.Code)
		}
		req := overTLS(httptest.NewRequest(http.MethodGet, "/api/sync/admin", nil))
		req.Header.Set("Authorization", "Bearer "+bearer)
		rec := httptest.NewRecorder()
		m.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s bearer of a pinned till over TLS = %d, want 200", name, rec.Code)
		}
	}
}

// replicaOf is a replica whose bearer lives in a file under a temp dir, as on
// a real till, following the main till at primaryURL.
func replicaOf(t *testing.T, primaryURL, bearer string) (*common.Deps, string) {
	t.Helper()
	d, _ := newSyncDepsWithPath(t, "replica.db")
	file := filepath.Join(t.TempDir(), "secrets", "sync-bearer")
	if err := data.KeepSyncBearerOutOfDB(t.Context(), d.Db, file); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"sync.primary_url": primaryURL, "sync.bearer": bearer, "sync.till_id": "till-2"} {
		if err := d.Settings.Set(t.Context(), k, v); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(lantls.SetPeerSource(func(ctx context.Context) (lantls.PeerTarget, error) {
		u, _, err := d.Settings.Get(ctx, "sync.primary_url")
		return lantls.PeerTarget{PrimaryURL: u}, err
	}))
	return d, file
}

// peerAdmin is the replica's own GET /api/sync/admin to its main till.
func peerAdmin(d *common.Deps) (int, error) {
	base, bearer, ok := d.SyncTarget(context.Background())
	if !ok {
		return 0, http.ErrNoLocation
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/api/sync/admin", nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := lantls.NewPeerClient(5 * time.Second).Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

func fileBearer(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func waitForRotation(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestSyncBearerRotate_OfflineReplicaRotatesOnNextContact(t *testing.T) {
	m := newRotationMain(t)
	m.enrolLegacy(t, "legacy-bearer")
	var down atomic.Bool
	var served atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			panic(http.ErrAbortHandler) // the connection dies with no answer
		}
		served.Add(1)
		m.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	replica, file := replicaOf(t, srv.URL, "legacy-bearer")
	t.Cleanup(lantls.SetBearerRotate(SyncBearerRotator(replica)))

	// The main till is off: the replica keeps its bearer, nothing rotates.
	down.Store(true)
	if _, err := peerAdmin(replica); err == nil {
		t.Fatal("a request to a downed main till succeeded")
	}
	if got := fileBearer(t, file); got != "legacy-bearer" {
		t.Fatalf("bearer changed to %q while the main till was unreachable", got)
	}

	// The next contact carries the request; the replica rotates in the background.
	down.Store(false)
	if code, err := peerAdmin(replica); err != nil || code != http.StatusOK {
		t.Fatalf("first contact: %d, %v", code, err)
	}
	waitForRotation(t, "the replica's bearer file to hold the new bearer", func() bool {
		b, err := os.ReadFile(file)
		return err == nil && hex64.MatchString(strings.TrimSpace(string(b)))
	})
	// One contact plus the one rotate call it triggered: nothing reached the
	// main till while it was down, and the hook ran once.
	if got := served.Load(); got != 2 {
		t.Fatalf("main till served %d requests after coming up, want 2 (admin + rotate)", got)
	}
	fresh := fileBearer(t, file)
	if got, _, _ := replica.Settings.Get(t.Context(), "sync.bearer"); got != fresh {
		t.Fatalf("settings read %q, file holds %q", got, fresh)
	}
	// The replica now syncs with the new bearer; the old one dies after the grace.
	if code, err := peerAdmin(replica); err != nil || code != http.StatusOK {
		t.Fatalf("sync with the new bearer: %d, %v", code, err)
	}
	m.now = m.now.Add(data.SyncBearerGrace + time.Second)
	if rec := m.call(http.MethodGet, "/api/sync/admin", "legacy-bearer"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("the old bearer after the grace = %d, want 401", rec.Code)
	}
	if code, err := peerAdmin(replica); err != nil || code != http.StatusOK {
		t.Fatalf("sync after the grace: %d, %v", code, err)
	}
}

// If the new bearer cannot be stored, the replica keeps the old one -- which
// the main till still honours, since the new one was never used.
func TestSyncBearerRotate_StoreFailureKeepsOldBearer(t *testing.T) {
	m := newRotationMain(t)
	m.enrolLegacy(t, "legacy-bearer")
	srv := httptest.NewServer(m.mux)
	t.Cleanup(srv.Close)

	replica, _ := newSyncDepsWithPath(t, "replica.db")
	ctx := context.Background()
	for k, v := range map[string]string{"sync.primary_url": srv.URL, "sync.bearer": "legacy-bearer"} {
		if err := replica.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := replica.Db.Exec(`CREATE TRIGGER block_bearer BEFORE UPDATE ON settings WHEN NEW.key = 'sync.bearer'
BEGIN SELECT RAISE(ABORT, 'read-only'); END`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lantls.SetPeerSource(func(context.Context) (lantls.PeerTarget, error) {
		return lantls.PeerTarget{PrimaryURL: srv.URL}, nil
	}))
	logging.ResetRecent()

	SyncBearerRotator(replica)(ctx)

	if got, _, _ := replica.Settings.Get(ctx, "sync.bearer"); got != "legacy-bearer" {
		t.Fatalf("bearer = %q after a failed store, want the old one", got)
	}
	m.now = m.now.Add(24 * time.Hour)
	if rec := m.call(http.MethodGet, "/api/sync/admin", "legacy-bearer"); rec.Code != http.StatusOK {
		t.Fatalf("old bearer after the replica failed to store the new one = %d, want 200", rec.Code)
	}
	for _, p := range logging.Recent() {
		if strings.Contains(p.Msg, "legacy-bearer") {
			t.Fatalf("a log line carries the bearer: %q", p.Msg)
		}
	}
}

// While a rotation is unconfirmed the main till's primary-proof answer also
// carries the proof under the previous bearer hash, so a replica that lost
// the rotation answer can still re-find a moved main till; once the new
// bearer is in use it stops.
func TestPrimaryProof_CarriesPrevProofOnlyWhilePending(t *testing.T) {
	m := newRotationMain(t)
	registerPrimaryProof(m.mux, m.dp)
	id := m.enrolLegacy(t, "legacy-bearer")

	proof := func() (cur, prev string) {
		rec := httptest.NewRecorder()
		m.mux.ServeHTTP(rec, proofReq(id, false))
		var out struct {
			Data struct {
				Proof     string `json:"proof"`
				PrevProof string `json:"prev_proof"`
			} `json:"data"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("primary-proof = %d %s", rec.Code, rec.Body)
		}
		return out.Data.Proof, out.Data.PrevProof
	}
	if _, prev := proof(); prev != "" {
		t.Fatalf("prev_proof %q before any rotation", prev)
	}
	fresh, _ := m.rotate(t, "legacy-bearer")
	cur, prev := proof()
	if cur == "" || prev == "" || cur == prev {
		t.Fatalf("pending rotation: proof=%q prev_proof=%q, want two different proofs", cur, prev)
	}
	m.call(http.MethodGet, "/api/sync/admin", fresh) // the new bearer is in use
	if _, prev = proof(); prev != "" {
		t.Fatalf("prev_proof %q after the new bearer was used", prev)
	}
}

// ut-docs#5203: a main till restored from a backup taken before the replica's
// last rotation knows only the replica's previous bearer. The replica falls
// back to it on the 401, then rotates again, with no re-join.

// snapshotTill captures the main till's tills row as a backup taken before a
// rotation holds it; the returned func puts the bearer columns back.
func snapshotTill(t *testing.T, m *rotationMain, id string) func() {
	t.Helper()
	var hash string
	var issued any
	if err := m.dp.Db.QueryRow(`SELECT bearer_hash, bearer_issued_at FROM tills WHERE id = ?`, id).Scan(&hash, &issued); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := m.dp.Db.Exec(`UPDATE tills SET bearer_hash = ?, bearer_prev_hash = NULL, bearer_prev_expires_at = NULL, bearer_issued_at = ? WHERE id = ?`,
			hash, issued, id); err != nil {
			t.Fatal(err)
		}
	}
}

func tillHash(t *testing.T, m *rotationMain, id string) string {
	t.Helper()
	var h string
	if err := m.dp.Db.QueryRow(`SELECT bearer_hash FROM tills WHERE id = ?`, id).Scan(&h); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestSyncBearerFallback_MainRestoredFromOlderBackup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		issued func(m *rotationMain) any // the restored row's bearer_issued_at
	}{
		{"legacy NULL issued_at", func(*rotationMain) any { return nil }},
		{"recent issued_at", func(m *rotationMain) any { return m.now.Add(-time.Hour) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newRotationMain(t)
			id := m.enrolLegacy(t, "legacy-bearer")
			srv := httptest.NewServer(m.mux)
			t.Cleanup(srv.Close)
			replica, file := replicaOf(t, srv.URL, "legacy-bearer")
			t.Cleanup(lantls.SetBearerRotate(SyncBearerRotator(replica)))
			t.Cleanup(lantls.SetBearerFallback(SyncBearerFallback(replica)))

			restore := snapshotTill(t, m, id) // the backup
			if code, err := peerAdmin(replica); err != nil || code != http.StatusOK {
				t.Fatalf("first contact: %d, %v", code, err)
			}
			waitForRotation(t, "the rotation", func() bool { return hex64.MatchString(fileBearer(t, file)) })
			rotated := fileBearer(t, file)
			if got, _ := replica.Settings.SyncBearerPrev(t.Context()); got != "legacy-bearer" {
				t.Fatalf("previous bearer = %q, want the legacy one", got)
			}

			// The main till is restored from the backup.
			restore()
			if _, err := m.dp.Db.Exec(`UPDATE tills SET bearer_issued_at = ? WHERE id = ?`, tc.issued(m), id); err != nil {
				t.Fatal(err)
			}
			if h := tillHash(t, m, id); h != hashBearer("legacy-bearer") {
				t.Fatal("setup: the row was not restored")
			}

			code, err := peerAdmin(replica)
			if err != nil || code != http.StatusOK {
				t.Fatalf("sync after the main till was restored: %d, %v -- want 200 with no re-join", code, err)
			}
			// Adopted the old bearer, then rotated again at once.
			waitForRotation(t, "the forced rotation", func() bool {
				b := fileBearer(t, file)
				return hex64.MatchString(b) && b != rotated
			})
			fresh := fileBearer(t, file)
			waitForRotation(t, "the main till to hold the new hash", func() bool {
				return tillHash(t, m, id) == hashBearer(fresh)
			})
			if got, _, _ := replica.Settings.Get(t.Context(), "sync.bearer"); got != fresh {
				t.Fatalf("settings read %q, file holds %q", got, fresh)
			}
			if code, err := peerAdmin(replica); err != nil || code != http.StatusOK {
				t.Fatalf("sync with the fresh bearer: %d, %v", code, err)
			}
		})
	}
}

// The fallback never resurrects a bearer the main till has expired: a replica
// restored from its own pre-rotation backup (current = the old bearer, no
// prev) and a replica whose previous bearer is long dead both stay out.
func TestSyncBearerFallback_DoesNotResurrectExpiredBearer(t *testing.T) {
	m := newRotationMain(t)
	m.enrolLegacy(t, "legacy-bearer")
	srv := httptest.NewServer(m.mux)
	t.Cleanup(srv.Close)
	replica, file := replicaOf(t, srv.URL, "legacy-bearer")
	t.Cleanup(lantls.SetBearerRotate(SyncBearerRotator(replica)))
	t.Cleanup(lantls.SetBearerFallback(SyncBearerFallback(replica)))

	if code, err := peerAdmin(replica); err != nil || code != http.StatusOK {
		t.Fatalf("first contact: %d, %v", code, err)
	}
	waitForRotation(t, "the rotation", func() bool { return hex64.MatchString(fileBearer(t, file)) })
	fresh := fileBearer(t, file)
	if code, err := peerAdmin(replica); err != nil || code != http.StatusOK { // fresh is in use: grace starts
		t.Fatalf("sync with the fresh bearer: %d, %v", code, err)
	}
	// Past the grace the main till refuses the legacy bearer.
	m.now = m.now.Add(data.SyncBearerGrace + time.Minute)
	// Make the replica's CURRENT bearer unknown to the main till (it was
	// revoked or the row replaced). The row keeps bearer_prev_hash = the
	// legacy bearer with an expired grace, so the past-grace branch of
	// TillByBearerHashAt is what refuses the previous bearer.
	if _, err := m.dp.Db.Exec(`UPDATE tills SET bearer_hash = ?`, hashBearer("someone-else")); err != nil {
		t.Fatal(err)
	}
	code, err := peerAdmin(replica)
	if err != nil || code != http.StatusUnauthorized {
		t.Fatalf("expired bearers: %d, %v, want 401", code, err)
	}
	if got := fileBearer(t, file); got != fresh {
		t.Fatalf("bearer file = %q after a refused fallback, want %q (no adopt)", got, fresh)
	}
	if got, _ := replica.Settings.SyncBearerPrev(t.Context()); got != "legacy-bearer" {
		t.Fatalf("previous bearer = %q, want it left alone", got)
	}
}

// A re-join starts a new pairing: the previous bearer of the old one is gone,
// so a 401 afterwards is final.
func TestSyncBearerFallback_RejoinDropsPrevious(t *testing.T) {
	m := newRotationMain(t)
	m.enrolLegacy(t, "legacy-bearer")
	srv := httptest.NewServer(m.mux)
	t.Cleanup(srv.Close)
	replica, _ := replicaOf(t, srv.URL, "legacy-bearer")
	if ok, err := replica.Settings.RotateSyncBearer(t.Context(), "legacy-bearer", strings.Repeat("a", 64)); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := replica.Settings.Set(t.Context(), "sync.bearer", "joined-anew"); err != nil {
		t.Fatal(err)
	}
	if got, _ := replica.Settings.SyncBearerPrev(t.Context()); got != "" {
		t.Fatalf("previous bearer %q survived a re-join", got)
	}
}

// A non-auth 401 (SQLITE_BUSY on the main till's bearer lookup, plain HTTP to
// a pinned till) inside the grace window must not lock a healthy replica out:
// the mistaken fallback to the previous bearer keeps the current one as the
// new previous, so the next 401 for the old one heals it (ut-docs#5203).
func TestSyncBearerFallback_TransientNonAuth401DoesNotLockOut(t *testing.T) {
	m := newRotationMain(t)
	m.enrolLegacy(t, "legacy-bearer")
	var armed atomic.Bool
	var rotateCalls atomic.Int32 // rotation requests the main till has fully answered
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sync/admin" && armed.CompareAndSwap(true, false) {
			http.Error(w, "transient", http.StatusUnauthorized)
			return
		}
		m.mux.ServeHTTP(w, r)
		if r.URL.Path == lantls.BearerRotatePath {
			rotateCalls.Add(1)
		}
	}))
	t.Cleanup(srv.Close)
	replica, file := replicaOf(t, srv.URL, "legacy-bearer")
	t.Cleanup(lantls.SetBearerRotate(SyncBearerRotator(replica)))
	t.Cleanup(lantls.SetBearerFallback(SyncBearerFallback(replica)))

	if code, err := peerAdmin(replica); err != nil || code != http.StatusOK {
		t.Fatalf("first contact: %d, %v", code, err)
	}
	waitForRotation(t, "the rotation", func() bool { return hex64.MatchString(fileBearer(t, file)) })
	if code, err := peerAdmin(replica); err != nil || code != http.StatusOK { // grace starts
		t.Fatalf("sync with the fresh bearer: %d, %v", code, err)
	}

	// One transient 401 for the fresh bearer; the legacy retry is accepted
	// (still in grace) and adopted.
	armed.Store(true)
	if code, err := peerAdmin(replica); err != nil || code != http.StatusOK {
		t.Fatalf("sync across the transient 401: %d, %v", code, err)
	}
	// The adoption asks for an immediate rotation with the legacy bearer; the
	// main till refuses it (409) inside the grace. Wait for that answer so
	// the clock below does not move under a request in flight.
	waitForRotation(t, "the forced rotation to be answered", func() bool { return rotateCalls.Load() >= 2 })

	// The grace passes; the main till refuses legacy, knows fresh.
	m.now = m.now.Add(data.SyncBearerGrace + time.Minute)
	if code, err := peerAdmin(replica); err != nil || code != http.StatusOK {
		t.Fatalf("sync after the grace: %d, %v -- the replica was locked out", code, err)
	}
	held := fileBearer(t, file)
	if rec := m.call(http.MethodGet, "/api/sync/admin", held); rec.Code != http.StatusOK {
		t.Fatalf("the bearer the replica holds (%q) is refused by the main till: %d", held, rec.Code)
	}
}
