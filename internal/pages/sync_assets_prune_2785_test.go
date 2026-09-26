package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/paths"
)

// ut-docs#2785: a photo deleted on the main till used to stay on every
// replica forever — the asset pull only added and replaced. These tests pin
// the prune: only files the replica downloaded, only after a grace, only
// from a main that says its manifest is complete, never mid-sale, bounded
// per tick, and never outside the scope's asset root.

type pruneHarness struct {
	t           *testing.T
	primaryRoot string
	replicaRoot string
	ledger      *data.SyncAssetLedgerRepo
	clock       time.Time
	busy        bool
}

func newPruneHarness(t *testing.T) *pruneHarness {
	t.Helper()
	primaryRoot := withDataDir(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	resetAssetPruneHeld()
	t.Cleanup(resetAssetPruneHeld)
	// A photo the main keeps throughout, so a test's deletion is never the
	// main's whole set (the safety valve holds that — tested separately).
	writeKeepAssets(t, primaryRoot)
	return &pruneHarness{
		t:           t,
		primaryRoot: primaryRoot,
		replicaRoot: t.TempDir(),
		ledger:      data.NewSyncAssetLedgerRepo(db),
		clock:       time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC),
	}
}

func (h *pruneHarness) pruner() *assetPruner {
	return &assetPruner{
		ledger:     h.ledger,
		busy:       func() bool { return h.busy },
		now:        func() time.Time { return h.clock },
		grace:      time.Hour,
		maxPerTick: syncAssetPruneMaxPerTick,
	}
}

// pull runs the real primary mux over primaryRoot and the real replica pull
// (with the pruner) into replicaRoot.
func (h *pruneHarness) pull() {
	h.t.Helper()
	mux := newAssetsPrimaryMux(h.t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths.Init(h.primaryRoot)
		defer paths.Init(h.replicaRoot)
		mux.ServeHTTP(w, r)
	}))
	defer srv.Close()
	paths.Init(h.replicaRoot)
	syncAssets(context.Background(), srv.Client(), srv.URL, "tok-assets", h.pruner())
}

func (h *pruneHarness) replicaFile(scope, rel string) string {
	return filepath.Join(h.replicaRoot, "public", "assets", scope, filepath.FromSlash(rel))
}

func writeKeepAssets(t *testing.T, root string) {
	t.Helper()
	writeAsset(t, root, "items", "keep/thumb.png", []byte("keep"))
	writeAsset(t, root, "categories", "keep/thumb.png", []byte("keep"))
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func TestPrune_DeletedOnMainIsGoneAfterGraceAndNextPull(t *testing.T) {
	h := newPruneHarness(t)
	main := writeAsset(t, h.primaryRoot, "items", "itm001/thumb.png", []byte("photo"))
	writeAsset(t, h.primaryRoot, "categories", "cat1/thumb.png", []byte("cat"))
	h.pull()
	local := h.replicaFile("items", "itm001/thumb.png")
	if !exists(local) {
		t.Fatal("setup: expected the photo downloaded")
	}

	if err := os.Remove(main); err != nil { // the owner deletes the photo on the main
		t.Fatal(err)
	}
	h.pull() // first miss: grace starts
	if !exists(local) {
		t.Fatal("expected the photo kept on the first pull that misses it (grace)")
	}
	h.clock = h.clock.Add(59 * time.Minute)
	h.pull()
	if !exists(local) {
		t.Fatal("expected the photo kept inside the 1h grace")
	}
	h.clock = h.clock.Add(2 * time.Minute)
	h.pull()
	if exists(local) {
		t.Fatal("expected the photo deleted on the main to be pruned after the grace")
	}
	if exists(filepath.Dir(local)) {
		t.Fatal("expected the emptied item directory removed too")
	}
	if !exists(h.replicaFile("categories", "cat1/thumb.png")) {
		t.Fatal("a still-listed category photo must be kept")
	}
	rows, _ := h.ledger.List(context.Background(), "items")
	if _, ok := rows["itm001/thumb.png"]; ok {
		t.Fatal("expected the pruned file's ledger row dropped")
	}
}

func TestPrune_StillReferencedIsKept(t *testing.T) {
	h := newPruneHarness(t)
	writeAsset(t, h.primaryRoot, "items", "itm001/thumb.png", []byte("photo"))
	h.pull()
	h.clock = h.clock.Add(3 * time.Hour)
	h.pull()
	h.clock = h.clock.Add(3 * time.Hour)
	h.pull()
	if !exists(h.replicaFile("items", "itm001/thumb.png")) {
		t.Fatal("a photo the main still lists must never be pruned")
	}
}

func TestPrune_ReappearingPathRestartsGrace(t *testing.T) {
	h := newPruneHarness(t)
	main := writeAsset(t, h.primaryRoot, "items", "itm001/thumb.png", []byte("photo"))
	h.pull()
	_ = os.Remove(main)
	h.pull() // grace starts
	writeAsset(t, h.primaryRoot, "items", "itm001/thumb.png", []byte("photo"))
	h.clock = h.clock.Add(30 * time.Minute)
	h.pull() // listed again: grace cleared
	if r := mustLedger(t, h, "itm001/thumb.png"); r.Misses != 0 || r.UnreferencedSince != 0 {
		t.Fatalf("a relisting must reset the miss count and the time, got %+v", r)
	}
	_ = os.Remove(main)
	h.clock = h.clock.Add(40 * time.Minute)
	h.pull() // new first miss
	h.clock = h.clock.Add(40 * time.Minute)
	h.pull()
	if !exists(h.replicaFile("items", "itm001/thumb.png")) {
		t.Fatal("the grace must restart when the main lists the path again")
	}
}

// An older main has no "complete" field in its manifest: the replica then
// prunes nothing, whatever the manifest omits.
func TestPrune_OlderMainWithoutCompleteFlagPrunesNothing(t *testing.T) {
	h := newPruneHarness(t)
	paths.Init(h.replicaRoot)
	local := writeAsset(t, h.replicaRoot, "items", "itm001/thumb.png", []byte("photo"))
	st, _ := os.Stat(local)
	if err := h.ledger.Record(context.Background(), "items", "itm001/thumb.png", st.Size(), st.ModTime().Unix()); err != nil {
		t.Fatal(err)
	}
	primary := newStubAssetsPrimary(t, []assetEntry{}, nil) // lists nothing, no "complete"
	for i := 0; i < 3; i++ {
		syncAssets(context.Background(), primary.server.Client(), primary.server.URL, "b", h.pruner())
		h.clock = h.clock.Add(2 * time.Hour)
	}
	if !exists(local) {
		t.Fatal("expected nothing pruned against an older main's manifest")
	}
}

func TestPrune_MainManifestSaysComplete(t *testing.T) {
	withDataDir(t)
	mux := newAssetsPrimaryMux(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sync/assets", nil)
	req.Header.Set("Authorization", "Bearer tok-assets")
	mux.ServeHTTP(rec, req)
	var out struct {
		Complete bool `json:"complete"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || !out.Complete {
		t.Fatalf("expected complete:true on a clean walk, got %s (%v)", rec.Body.String(), err)
	}
}

func TestPrune_BatchIsBoundedPerPull(t *testing.T) {
	h := newPruneHarness(t)
	const n = syncAssetPruneMaxPerTick + 10
	var mains []string
	for i := 0; i < n; i++ {
		mains = append(mains, writeAsset(t, h.primaryRoot, "items", fmt.Sprintf("itm%03d/thumb.png", i), []byte("p")))
	}
	for i := 0; i < n; i++ { // still listed: keeps the removal under the safety valve's 50%
		writeAsset(t, h.primaryRoot, "items", fmt.Sprintf("kept%03d/thumb.png", i), []byte("k"))
	}
	h.pull()
	for _, m := range mains {
		_ = os.Remove(m)
	}
	h.pull()
	h.clock = h.clock.Add(2 * time.Hour)
	h.pull()
	left := 0
	for i := 0; i < n; i++ {
		if exists(h.replicaFile("items", fmt.Sprintf("itm%03d/thumb.png", i))) {
			left++
		}
	}
	if left != n-syncAssetPruneMaxPerTick {
		t.Fatalf("expected at most %d pruned per pull (%d left), got %d left", syncAssetPruneMaxPerTick, n-syncAssetPruneMaxPerTick, left)
	}
	h.pull()
	for i := 0; i < n; i++ {
		if exists(h.replicaFile("items", fmt.Sprintf("itm%03d/thumb.png", i))) {
			t.Fatalf("expected the rest pruned on the next pull, itm%03d left", i)
		}
	}
}

func TestPrune_OpenSaleDefers(t *testing.T) {
	h := newPruneHarness(t)
	main := writeAsset(t, h.primaryRoot, "items", "itm001/thumb.png", []byte("photo"))
	h.pull()
	_ = os.Remove(main)
	h.pull()
	h.clock = h.clock.Add(2 * time.Hour)
	h.busy = true
	h.pull()
	local := h.replicaFile("items", "itm001/thumb.png")
	if !exists(local) {
		t.Fatal("expected no prune while a basket has lines")
	}
	h.busy = false
	h.pull()
	if exists(local) {
		t.Fatal("expected the deferred prune to run once the sale is over")
	}
}

// Photos uploaded on the replica itself (item/image is deliberately not
// primary-gated, #1689) are never touched: never downloaded, or replaced
// locally after the download.
func TestPrune_ReplicaLocalUploadsAreNeverTouched(t *testing.T) {
	h := newPruneHarness(t)
	main := writeAsset(t, h.primaryRoot, "items", "itm001/thumb.png", []byte("photo"))
	h.pull()
	own := writeAsset(t, h.replicaRoot, "items", "own/thumb.png", []byte("replica-upload"))
	// Replaced on the replica after the download (different bytes + mtime).
	replaced := writeAsset(t, h.replicaRoot, "items", "itm001/thumb.png", []byte("replica-replacement"))
	_ = os.Remove(main)
	h.pull()
	h.clock = h.clock.Add(2 * time.Hour)
	h.pull()
	if !exists(own) {
		t.Fatal("a replica-local upload must never be pruned")
	}
	if !exists(replaced) {
		t.Fatal("a downloaded file replaced locally must never be pruned")
	}
}

// A file downloaded before this change has no ledger row; a pull that sees
// it still matching the main's manifest backfills one, so it is pruned later.
func TestPrune_PreUpgradeDownloadIsBackfilled(t *testing.T) {
	h := newPruneHarness(t)
	main := writeAsset(t, h.primaryRoot, "items", "itm001/thumb.png", []byte("photo"))
	runPull(t, h.primaryRoot, h.replicaRoot) // the old version: no ledger
	h.pull()
	_ = os.Remove(main)
	h.pull()
	h.clock = h.clock.Add(2 * time.Hour)
	h.pull()
	if exists(h.replicaFile("items", "itm001/thumb.png")) {
		t.Fatal("expected a pre-upgrade download backfilled into the ledger and pruned")
	}
}

// A ledger row is data, not proof: a traversal path, an absolute path, or a
// symlink pointing out of the asset root must never be deleted.
func TestPrune_RefusesPathsOutsideTheAssetRoot(t *testing.T) {
	h := newPruneHarness(t)
	ctx := context.Background()
	paths.Init(h.replicaRoot)
	outside := filepath.Join(h.replicaRoot, "precious.db")
	if err := os.WriteFile(outside, []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(outside)
	for _, p := range []string{"../../../precious.db", outside, "..\\..\\..\\precious.db", "C:precious.db"} {
		if err := h.ledger.Record(ctx, "items", p, st.Size(), st.ModTime().Unix()); err != nil {
			t.Fatal(err)
		}
	}
	link := h.replicaFile("items", "lnk/thumb.png")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	lst, _ := os.Lstat(link)
	_ = h.ledger.Record(ctx, "items", "lnk/thumb.png", lst.Size(), lst.ModTime().Unix())

	// A main whose complete manifest lists none of them.
	h.primaryRoot = t.TempDir()
	writeKeepAssets(t, h.primaryRoot)
	h.pull()
	h.clock = h.clock.Add(2 * time.Hour)
	h.pull()
	if !exists(outside) {
		t.Fatal("a ledger path outside the asset root must never be deleted")
	}
	if !exists(link) {
		t.Fatal("a symlink must never be followed or removed by the prune")
	}
	if b, _ := os.ReadFile(outside); !strings.EqualFold(string(b), "db") {
		t.Fatal("the outside file was modified")
	}
}

// A symlinked item directory pointing out of the asset root: the file
// behind it is regular and matches its ledger row, but lives elsewhere.
func TestPrune_RefusesSymlinkedParentDirectory(t *testing.T) {
	h := newPruneHarness(t)
	paths.Init(h.replicaRoot)
	elsewhere := t.TempDir()
	victim := filepath.Join(elsewhere, "thumb.png")
	if err := os.WriteFile(victim, []byte("not-ours"), 0o644); err != nil {
		t.Fatal(err)
	}
	itemsRoot := filepath.Join(h.replicaRoot, "public", "assets", "items")
	if err := os.MkdirAll(itemsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(itemsRoot, "evil")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	st, _ := os.Stat(victim)
	_ = h.ledger.Record(context.Background(), "items", "evil/thumb.png", st.Size(), st.ModTime().Unix())
	h.primaryRoot = t.TempDir()
	writeKeepAssets(t, h.primaryRoot)
	h.pull()
	h.clock = h.clock.Add(2 * time.Hour)
	h.pull()
	if !exists(victim) {
		t.Fatal("a file behind a symlinked directory must never be deleted")
	}
}

// seedItems puts n item photos on the main and pulls them to the replica.
func (h *pruneHarness) seedItems(n int) []string {
	h.t.Helper()
	var mains []string
	for i := 0; i < n; i++ {
		mains = append(mains, writeAsset(h.t, h.primaryRoot, "items", fmt.Sprintf("itm%03d/thumb.png", i), []byte("p")))
	}
	h.pull()
	return mains
}

func (h *pruneHarness) itemsLeft(n int) int {
	left := 0
	for i := 0; i < n; i++ {
		if exists(h.replicaFile("items", fmt.Sprintf("itm%03d/thumb.png", i))) {
			left++
		}
	}
	return left
}

// Safety valve: a main whose photo directory was lost (or restored empty)
// serves a complete but EMPTY manifest. The replica must not read that as
// "delete every copy" — its copies may be the last ones.
func TestPrune_EmptyMainManifestHoldsPruneAndWarns(t *testing.T) {
	h := newPruneHarness(t)
	logs := captureLog(t)
	h.seedItems(3)
	if err := os.RemoveAll(filepath.Join(h.primaryRoot, "public", "assets", "items")); err != nil {
		t.Fatal(err)
	}
	before, _ := h.ledger.List(context.Background(), "items")
	for i := 0; i < 4; i++ {
		h.pull()
		h.clock = h.clock.Add(2 * time.Hour)
	}
	if left := h.itemsLeft(3); left != 3 {
		t.Fatalf("expected every copy kept against an empty main manifest, %d of 3 left", left)
	}
	if !exists(h.replicaFile("items", "keep/thumb.png")) {
		t.Fatal("expected the kept photo kept too")
	}
	after, _ := h.ledger.List(context.Background(), "items")
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("expected the ledger untouched while the prune is held:\nbefore %v\nafter  %v", before, after)
	}
	out := logs.String()
	if !strings.Contains(out, "[WARN]") || !strings.Contains(out, "items") || !strings.Contains(out, "4 of 4") {
		t.Fatalf("expected a warning naming the scope and counts, got:\n%s", out)
	}
	if n := strings.Count(out, "prune held"); n != 1 {
		t.Fatalf("expected the held warning logged once per condition, not every pull (%d times)", n)
	}
}

// Safety valve: more than half of a scope's downloaded photos vanishing at
// once looks like a lost/restored directory, not an owner tidying up.
func TestPrune_MassDisappearanceHoldsPrune(t *testing.T) {
	h := newPruneHarness(t)
	logs := captureLog(t)
	mains := h.seedItems(9) // + keep = 10 ledger rows
	for _, m := range mains[:6] {
		_ = os.Remove(m)
	}
	for i := 0; i < 4; i++ {
		h.pull()
		h.clock = h.clock.Add(2 * time.Hour)
	}
	if left := h.itemsLeft(9); left != 9 {
		t.Fatalf("expected nothing pruned when 60%% vanish at once, %d of 9 left", left)
	}
	if !strings.Contains(logs.String(), "6 of 10") {
		t.Fatalf("expected a warning with the counts, got:\n%s", logs.String())
	}
}

func TestPrune_SmallDisappearancePrunesNormally(t *testing.T) {
	h := newPruneHarness(t)
	logs := captureLog(t)
	mains := h.seedItems(9) // + keep = 10 ledger rows
	for _, m := range mains[:2] {
		_ = os.Remove(m)
	}
	h.pull()
	h.clock = h.clock.Add(2 * time.Hour)
	h.pull()
	if left := h.itemsLeft(9); left != 7 {
		t.Fatalf("expected the 2 deleted photos (20%%) pruned, %d of 9 left", left)
	}
	if strings.Contains(logs.String(), "prune held") {
		t.Fatalf("no held warning expected, got:\n%s", logs.String())
	}
}

// Clock independence: a wall-clock jump past the grace after the first
// miss is not enough — a second complete manifest must miss the file too.
func TestPrune_ClockJumpStillNeedsSecondMiss(t *testing.T) {
	h := newPruneHarness(t)
	main := writeAsset(t, h.primaryRoot, "items", "itm001/thumb.png", []byte("photo"))
	h.pull()
	_ = os.Remove(main)
	h.pull() // first miss
	rows, _ := h.ledger.List(context.Background(), "items")
	if r := rows["itm001/thumb.png"]; r.Misses != 1 || r.UnreferencedSince == 0 {
		t.Fatalf("expected one miss recorded, got %+v", r)
	}
	h.clock = h.clock.Add(2 * time.Hour)
	// The main is unreachable on the next tick: no manifest, no miss.
	paths.Init(h.replicaRoot)
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	syncAssets(context.Background(), down.Client(), down.URL, "tok-assets", h.pruner())
	local := h.replicaFile("items", "itm001/thumb.png")
	if !exists(local) {
		t.Fatal("expected no prune without a second complete manifest")
	}
	h.pull() // second consecutive complete miss, grace long past
	if exists(local) {
		t.Fatal("expected the prune on the second complete miss")
	}
}

func mustLedger(t *testing.T, h *pruneHarness, rel string) data.SyncAssetLedgerRow {
	t.Helper()
	rows, err := h.ledger.List(context.Background(), "items")
	if err != nil {
		t.Fatal(err)
	}
	r, ok := rows[rel]
	if !ok {
		t.Fatalf("no ledger row for %s", rel)
	}
	return r
}
