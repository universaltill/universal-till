package issuereport

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func withTempPendingDir(t *testing.T) {
	t.Helper()
	orig := PendingDir
	PendingDir = t.TempDir()
	t.Cleanup(func() { PendingDir = orig })
}

func TestSaveRequiresNoteOrAudio(t *testing.T) {
	withTempPendingDir(t)
	if _, err := Save("", "", nil, nil, nil); err == nil {
		t.Fatal("expected error when both note and audio are empty")
	}
}

func TestSaveAcceptsNoteOnly(t *testing.T) {
	withTempPendingDir(t)
	id, err := Save("printer jammed, no voice note", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("Save with note only: %v", err)
	}
	bundles, err := Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(bundles) != 1 {
		t.Fatalf("expected 1 pending bundle, got %d", len(bundles))
	}
	if bundles[0].Meta.ID != id {
		t.Errorf("Meta.ID = %q, want %q", bundles[0].Meta.ID, id)
	}
	if bundles[0].AudioPath != "" {
		t.Errorf("AudioPath = %q, want empty (no voice note captured)", bundles[0].AudioPath)
	}
}

func TestSaveAcceptsAudioOnly(t *testing.T) {
	withTempPendingDir(t)
	if _, err := Save("", "", []byte("fake-audio"), nil, nil); err != nil {
		t.Fatalf("Save with audio only: %v", err)
	}
}

// The operator's UI locale at capture time (ut-docs#397) must survive the
// save: written into meta.json under the "locale" key and read back on the
// bundle by Pending(), so cloud-side transcription can hand it to Whisper.
func TestSaveLocaleRoundTrips(t *testing.T) {
	withTempPendingDir(t)
	id, err := Save("", "fa", []byte("fake-audio"), nil, nil)
	if err != nil {
		t.Fatalf("Save with locale: %v", err)
	}

	// The raw meta.json must carry the snake_case "locale" key — that file is
	// the on-disk contract, not just the in-process struct.
	raw, err := os.ReadFile(filepath.Join(PendingDir, id, "meta.json"))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	if !strings.Contains(string(raw), `"locale": "fa"`) {
		t.Errorf("meta.json = %s, want a %q entry", raw, `"locale": "fa"`)
	}

	bundles, err := Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(bundles) != 1 {
		t.Fatalf("expected 1 pending bundle, got %d", len(bundles))
	}
	if bundles[0].Meta.Locale != "fa" {
		t.Errorf("Meta.Locale = %q, want %q", bundles[0].Meta.Locale, "fa")
	}
}

// An empty locale (never captured — e.g. the JS-less path) is valid: the
// bundle saves exactly as before and the field just round-trips empty.
func TestSaveEmptyLocaleStillSaves(t *testing.T) {
	withTempPendingDir(t)
	if _, err := Save("printer jammed", "", []byte("fake-audio"), nil, nil); err != nil {
		t.Fatalf("Save with empty locale: %v", err)
	}
	bundles, err := Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(bundles) != 1 {
		t.Fatalf("expected 1 pending bundle, got %d", len(bundles))
	}
	if bundles[0].Meta.Locale != "" {
		t.Errorf("Meta.Locale = %q, want empty", bundles[0].Meta.Locale)
	}
}

func TestSaveThenPendingRoundTrip(t *testing.T) {
	withTempPendingDir(t)
	id, err := Save("printer jammed", "", []byte("fake-audio"), []byte("fake-video"), nil)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if id == "" {
		t.Fatal("expected a non-empty id")
	}

	bundles, err := Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(bundles) != 1 {
		t.Fatalf("expected 1 pending bundle, got %d", len(bundles))
	}
	b := bundles[0]
	if b.Meta.ID != id {
		t.Errorf("Meta.ID = %q, want %q", b.Meta.ID, id)
	}
	if b.Meta.Note != "printer jammed" {
		t.Errorf("Meta.Note = %q, want %q", b.Meta.Note, "printer jammed")
	}
	if b.AudioPath == "" {
		t.Error("expected AudioPath to be set")
	}
	if b.VideoPath == "" {
		t.Error("expected VideoPath to be set when a video was captured")
	}
}

func TestSaveWithoutVideoLeavesVideoPathEmpty(t *testing.T) {
	withTempPendingDir(t)
	if _, err := Save("", "", []byte("fake-audio"), nil, nil); err != nil {
		t.Fatalf("Save: %v", err)
	}
	bundles, err := Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(bundles) != 1 {
		t.Fatalf("expected 1 pending bundle, got %d", len(bundles))
	}
	if bundles[0].VideoPath != "" {
		t.Errorf("VideoPath = %q, want empty (no screen recording captured)", bundles[0].VideoPath)
	}
}

func TestPendingOrdersOldestFirst(t *testing.T) {
	withTempPendingDir(t)
	first, err := Save("first", "", []byte("a"), nil, nil)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Force a distinguishable timestamp — two Saves in the same test can
	// land in the same nanosecond-resolution instant on a fast machine.
	time.Sleep(2 * time.Millisecond)
	second, err := Save("second", "", []byte("a"), nil, nil)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	bundles, err := Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(bundles) != 2 {
		t.Fatalf("expected 2 pending bundles, got %d", len(bundles))
	}
	if bundles[0].Meta.ID != first || bundles[1].Meta.ID != second {
		t.Errorf("expected oldest-first order [%s, %s], got [%s, %s]",
			first, second, bundles[0].Meta.ID, bundles[1].Meta.ID)
	}
}

func TestDiscardRemovesBundle(t *testing.T) {
	withTempPendingDir(t)
	id, err := Save("note", "", []byte("a"), nil, nil)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := Discard(id); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	bundles, err := Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(bundles) != 0 {
		t.Fatalf("expected 0 pending bundles after Discard, got %d", len(bundles))
	}
}

// A write failure partway through Save (e.g. disk full on the video file,
// after audio already succeeded) must not leave an orphaned bundle
// directory behind — one with no meta.json, invisible to Pending() and so
// never reachable by Discard() either.
func TestSaveCleansUpDirectoryOnWriteFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission semantics differ on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root; read-only dir does not block writes")
	}
	withTempPendingDir(t)

	origID := newBundleID
	newBundleID = func() string { return "fixed-id-for-test" }
	t.Cleanup(func() { newBundleID = origID })

	// Pre-create the bundle directory read-only, so Save's os.MkdirAll is a
	// no-op (already exists) and the first file write inside it fails.
	dir := filepath.Join(PendingDir, "fixed-id-for-test")
	if err := os.MkdirAll(dir, 0o500); err != nil {
		t.Fatalf("pre-create read-only dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	if _, err := Save("note", "", []byte("audio"), nil, nil); err == nil {
		t.Fatal("expected Save to fail on a read-only bundle directory")
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("expected the failed bundle directory to be cleaned up, stat err = %v", err)
	}
}

func TestSaveWithImagesRoundTrip(t *testing.T) {
	withTempPendingDir(t)
	images := [][]byte{[]byte("fake-png-0"), []byte("fake-png-1"), []byte("fake-png-2")}
	id, err := Save("printer jammed", "", nil, nil, images)
	if err != nil {
		t.Fatalf("Save with images: %v", err)
	}

	bundles, err := Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(bundles) != 1 {
		t.Fatalf("expected 1 pending bundle, got %d", len(bundles))
	}
	b := bundles[0]
	if b.Meta.ID != id {
		t.Errorf("Meta.ID = %q, want %q", b.Meta.ID, id)
	}
	if len(b.ImagePaths) != 3 {
		t.Fatalf("expected 3 ImagePaths, got %d: %v", len(b.ImagePaths), b.ImagePaths)
	}
	// Order must be deterministic and match capture order. NOTE: with only
	// three images numeric and lexicographic order coincide, so this case
	// cannot tell the two apart — TestPendingOrdersImagesNumericallyNotLexically
	// below is what actually pins the ordering rule down.
	for i, p := range b.ImagePaths {
		want := filepath.Join(b.Dir, "image-"+strconv.Itoa(i)+".png")
		if p != want {
			t.Errorf("ImagePaths[%d] = %q, want %q", i, p, want)
		}
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		if string(got) != string(images[i]) {
			t.Errorf("image-%d content = %q, want %q", i, got, images[i])
		}
	}
}

// The whole reason Pending() sorts on the parsed index rather than taking
// filepath.Glob's own order: Glob returns lexicographic order, which puts
// image-10 before image-2. Capture order is what a reader of the report needs
// ("then I clicked here, then this happened"), so it must survive double
// digits. Twelve images is the smallest count that tells the two orders apart
// — with three of them (TestSaveWithImagesRoundTrip) they are identical, so
// that case passes with the sort removed entirely.
func TestPendingOrdersImagesNumericallyNotLexically(t *testing.T) {
	withTempPendingDir(t)
	const n = 12
	images := make([][]byte, n)
	for i := range images {
		images[i] = []byte("fake-png-" + strconv.Itoa(i))
	}
	if _, err := Save("twelve screenshots", "", nil, nil, images); err != nil {
		t.Fatalf("Save with %d images: %v", n, err)
	}

	bundles, err := Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(bundles) != 1 {
		t.Fatalf("expected 1 pending bundle, got %d", len(bundles))
	}
	b := bundles[0]
	if len(b.ImagePaths) != n {
		t.Fatalf("expected %d ImagePaths, got %d: %v", n, len(b.ImagePaths), b.ImagePaths)
	}
	for i, p := range b.ImagePaths {
		want := filepath.Join(b.Dir, "image-"+strconv.Itoa(i)+".png")
		if p != want {
			t.Fatalf("ImagePaths[%d] = %q, want %q — images are in lexicographic, not capture, order", i, p, want)
		}
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		if string(got) != "fake-png-"+strconv.Itoa(i) {
			t.Errorf("ImagePaths[%d] content = %q, want %q — path order and content order disagree", i, got, "fake-png-"+strconv.Itoa(i))
		}
	}
}

func TestSaveWithoutImagesLeavesImagePathsEmpty(t *testing.T) {
	withTempPendingDir(t)
	if _, err := Save("printer jammed", "", nil, nil, nil); err != nil {
		t.Fatalf("Save without images: %v", err)
	}
	bundles, err := Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(bundles) != 1 {
		t.Fatalf("expected 1 pending bundle, got %d", len(bundles))
	}
	if len(bundles[0].ImagePaths) != 0 {
		t.Errorf("ImagePaths = %v, want empty", bundles[0].ImagePaths)
	}
}

func TestPendingOnMissingDirReturnsEmpty(t *testing.T) {
	withTempPendingDir(t)
	PendingDir = PendingDir + "/does-not-exist"
	bundles, err := Pending()
	if err != nil {
		t.Fatalf("Pending on missing dir: %v", err)
	}
	if len(bundles) != 0 {
		t.Fatalf("expected 0 bundles, got %d", len(bundles))
	}
}

// RecordSentFailure durably increments the bundle's failure count on disk
// (survives a till restart, unlike an in-memory counter — ut-docs#446) and
// returns the new count each time.
func TestRecordSentFailureIncrementsAndPersists(t *testing.T) {
	withTempPendingDir(t)
	id, err := Save("printer jammed", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	for want := 1; want <= 3; want++ {
		got, err := RecordSentFailure(id)
		if err != nil {
			t.Fatalf("RecordSentFailure #%d: %v", want, err)
		}
		if got != want {
			t.Fatalf("RecordSentFailure #%d = %d, want %d", want, got, want)
		}
	}

	// Persisted, not in-memory: re-reading the bundle from disk (as Pending()
	// does on every fresh cloudsync tick, including after a till restart)
	// must see the same count.
	bundles, err := Pending()
	if err != nil || len(bundles) != 1 {
		t.Fatalf("Pending: %v (%d)", err, len(bundles))
	}
	if bundles[0].Meta.SentFailCount != 3 {
		t.Fatalf("Meta.SentFailCount = %d, want 3", bundles[0].Meta.SentFailCount)
	}
}

// A bundle id with no meta.json (already discarded, or never existed) is a
// real error, not a silent 0 — callers must not mistake "can't find it" for
// "this is its first failure".
func TestRecordSentFailureUnknownIDErrors(t *testing.T) {
	withTempPendingDir(t)
	if _, err := RecordSentFailure("does-not-exist"); err == nil {
		t.Fatal("expected an error for an unknown bundle id")
	}
}

// The core ut-docs#446 blocker (independent review): if the durable write
// itself keeps failing — the disk is genuinely the problem, which is the
// ticket's own headline scenario — the cap must still engage via the
// in-memory fallback, not silently stop counting forever.
func TestRecordSentFailureFallsBackToMemoryWhenWriteFails(t *testing.T) {
	withTempPendingDir(t)
	id, err := Save("disk is full", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	origWriteMeta := writeMeta
	writeMeta = func(string, Meta) error { return fmt.Errorf("simulated: no space left on device") }
	defer func() { writeMeta = origWriteMeta }()

	for want := 1; want <= MaxSentFailCount; want++ {
		got, err := RecordSentFailure(id)
		if err != nil {
			t.Fatalf("RecordSentFailure #%d: %v", want, err)
		}
		if got != want {
			t.Fatalf("RecordSentFailure #%d = %d, want %d (cap must still advance while every write fails)", want, got, want)
		}
	}

	// The on-disk file must be untouched by the failed writes — atomicity
	// (writeMetaAtomic never renames a half-written temp file over it) means
	// a bundle whose write can't succeed still has a valid, parsable
	// meta.json, not a truncated one that would strand it from Pending().
	bundles, err := Pending()
	if err != nil || len(bundles) != 1 {
		t.Fatalf("Pending: %v (%d) — a corrupted meta.json would silently drop the bundle here", err, len(bundles))
	}
	if bundles[0].Meta.SentFailCount != 0 {
		t.Fatalf("on-disk SentFailCount = %d, want 0 (every write failed, so disk must still hold the pre-failure value)", bundles[0].Meta.SentFailCount)
	}

	// Once the disk recovers, the fallback's progress must be folded back
	// in — not discarded, and not silently restarted from the stale
	// on-disk value.
	writeMeta = origWriteMeta
	got, err := RecordSentFailure(id)
	if err != nil {
		t.Fatalf("RecordSentFailure after recovery: %v", err)
	}
	if got != MaxSentFailCount+1 {
		t.Fatalf("RecordSentFailure after recovery = %d, want %d (fallback progress must carry over)", got, MaxSentFailCount+1)
	}
	bundles, err = Pending()
	if err != nil || len(bundles) != 1 {
		t.Fatalf("Pending after recovery: %v (%d)", err, len(bundles))
	}
	if bundles[0].Meta.SentFailCount != MaxSentFailCount+1 {
		t.Fatalf("on-disk SentFailCount after recovery = %d, want %d", bundles[0].Meta.SentFailCount, MaxSentFailCount+1)
	}
}

// writeMetaAtomic itself: a successful write leaves valid content and no
// leftover temp file behind.
func TestWriteMetaAtomicLeavesNoTempFileBehind(t *testing.T) {
	withTempPendingDir(t)
	id, err := Save("printer jammed", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	path := filepath.Join(PendingDir, id, "meta.json")
	meta := Meta{ID: id, Note: "updated", CreatedAt: time.Now().UTC(), SentFailCount: 2}
	if err := writeMetaAtomic(path, meta); err != nil {
		t.Fatalf("writeMetaAtomic: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(PendingDir, id))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}
	mb, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written meta: %v", err)
	}
	var got Meta
	if err := json.Unmarshal(mb, &got); err != nil {
		t.Fatalf("unmarshal written meta: %v", err)
	}
	if got.Note != "updated" || got.SentFailCount != 2 {
		t.Fatalf("written meta = %+v, want Note=updated SentFailCount=2", got)
	}
}
