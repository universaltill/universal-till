package updates

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/netaccess"
)

// ut-docs#3940: the till shows the incoming version's release notes before
// installing it, from the release's release-notes.json asset.

func noteFile(version, date, body string) string {
	return "---\nversion: " + version + "\ndate: " + date + "\n---\n" + body
}

func testBundle(t *testing.T) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"format": 1, "files": map[string]string{
		"en/v1.0.0.md": noteFile("v1.0.0", "2026-01-01", "## New\n\n- One.\n"),
		"en/v1.1.0.md": noteFile("v1.1.0", "2026-02-01", "## New\n\n- Eleven.\n"),
		"en/v1.2.0.md": noteFile("v1.2.0", "2026-03-01", "## New\n\n- Twelve.\n"),
		"tr/v1.2.0.md": noteFile("v1.2.0", "2026-03-01", "## Yeni\n\n- On iki.\n"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func resetNotesCache(t *testing.T) {
	t.Helper()
	clear := func() {
		notesCache.Lock()
		notesCache.entry = notesEntry{}
		notesCache.Unlock()
	}
	clear()
	oldNow := notesNow
	t.Cleanup(func() { clear(); notesNow = oldNow })
}

func TestCheckOnce_RecordsReleaseNotesAsset(t *testing.T) {
	resetState(t)
	var base string
	withCheckServer(t, "1.0.0", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v1.2.0","html_url":"https://example.test/rel","assets":[
			{"name":"checksums.txt","browser_download_url":"` + base + `/checksums.txt"},
			{"name":"release-notes.json","browser_download_url":"` + base + `/release-notes.json"}]}`))
	})
	base = releasesURL // the override's own scheme+host
	st := CheckNow(context.Background())
	if st.NotesURL != base+"/release-notes.json" {
		t.Fatalf("NotesURL = %q, want the release-notes.json asset on the override host", st.NotesURL)
	}
}

func TestCheckOnce_ForeignNotesAssetDropped(t *testing.T) {
	resetState(t)
	withCheckServer(t, "1.0.0", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v1.2.0","html_url":"u","assets":[
			{"name":"release-notes.json","browser_download_url":"https://evil.example/release-notes.json"}]}`))
	})
	st := CheckNow(context.Background())
	if !st.Available || st.NotesURL != "" {
		t.Fatalf("a notes asset on a foreign host must be ignored (update still offered): %+v", st)
	}
}

func TestNotesURLAllowed(t *testing.T) {
	const gh = "https://github.com/universaltill/universal-till/releases/download/v1.2.0/release-notes.json"
	old := releasesURL
	t.Cleanup(func() { releasesURL = old })

	releasesURL = defaultReleasesURL
	for u, want := range map[string]bool{
		gh: true,
		"http://github.com/universaltill/universal-till/releases/download/v1.2.0/release-notes.json":            false,
		"https://github.com/someone/universal-till/releases/download/v1.2.0/release-notes.json":                 false,
		"https://github.com/universaltill/universal-till/releases/download/../../../x/release-notes.json":       false,
		"https://github.com/universaltill/universal-till/releases/download/v1/%2e%2e/%2e%2e/release-notes.json": false,
		"https://user@github.com/universaltill/universal-till/releases/download/v1.2.0/release-notes.json":      false,
		"https://github.com.evil.example/universaltill/universal-till/releases/download/v1/release-notes.json":  false,
		"https://api.github.com/repos/universaltill/universal-till/releases/assets/1":                           false,
		"https://evil.example/release-notes.json":                                                               false,
		"": false,
	} {
		if got := notesURLAllowed(u); got != want {
			t.Errorf("default releases URL: notesURLAllowed(%q) = %v, want %v", u, got, want)
		}
	}

	releasesURL = "http://127.0.0.1:9000/latest" // operator override (mirror / e2e)
	for u, want := range map[string]bool{
		gh: true,
		"http://127.0.0.1:9000/release-notes.json":  true,
		"http://127.0.0.1:9001/release-notes.json":  false,
		"https://127.0.0.1:9000/release-notes.json": false,
		"http://localhost:9000/release-notes.json":  false,
	} {
		if got := notesURLAllowed(u); got != want {
			t.Errorf("override: notesURLAllowed(%q) = %v, want %v", u, got, want)
		}
	}
}

func TestReleasesURLFromEnv(t *testing.T) {
	t.Setenv("UT_UPDATE_RELEASES_URL", "")
	if got := releasesURLFromEnv(); got != defaultReleasesURL {
		t.Fatalf("unset: %q", got)
	}
	t.Setenv("UT_UPDATE_RELEASES_URL", " http://127.0.0.1:9000/latest ")
	if got := releasesURLFromEnv(); got != "http://127.0.0.1:9000/latest" {
		t.Fatalf("override: %q", got)
	}
	for _, bad := range []string{"ftp://x/latest", "not a url", "/latest", "javascript:alert(1)"} {
		t.Setenv("UT_UPDATE_RELEASES_URL", bad)
		if got := releasesURLFromEnv(); got != defaultReleasesURL {
			t.Errorf("invalid override %q must fall back to the default, got %q", bad, got)
		}
	}
}

// notesServer serves the bundle (or status) and counts requests.
func notesServer(t *testing.T, status int, body []byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	old := releasesURL
	releasesURL = srv.URL + "/latest"
	t.Cleanup(func() { releasesURL = old; srv.Close() })
	return srv, &hits
}

func TestIncomingNotes_SkippedVersionsNewestFirstAndCached(t *testing.T) {
	resetNotesCache(t)
	srv, hits := notesServer(t, http.StatusOK, testBundle(t))
	st := Status{Available: true, Latest: "1.2.0", NotesURL: srv.URL + "/release-notes.json"}

	notes, err := IncomingNotes(context.Background(), st, "tr", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 || notes[0].Version != "v1.2.0" || notes[1].Version != "v1.1.0" {
		t.Fatalf("got %d notes, want v1.2.0 then v1.1.0", len(notes))
	}
	if !notes[0].Translated || !strings.Contains(string(notes[0].HTML), "On iki") {
		t.Errorf("v1.2.0 should be the Turkish note: %+v", notes[0])
	}
	if notes[1].Translated {
		t.Errorf("v1.1.0 has no Turkish note: English with Translated=false")
	}
	if _, err := IncomingNotes(context.Background(), st, "en", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("bundle downloaded %d times, want once per offered version", n)
	}
	// A dev build gets just the offered version's note.
	notes, err = IncomingNotes(context.Background(), st, "en", "dev")
	if err != nil || len(notes) != 1 || notes[0].Version != "v1.2.0" {
		t.Fatalf("dev: %v %v", notes, err)
	}
}

func TestIncomingNotes_NewOfferRefetches(t *testing.T) {
	resetNotesCache(t)
	srv, hits := notesServer(t, http.StatusOK, testBundle(t))
	u := srv.URL + "/release-notes.json"
	_, _ = IncomingNotes(context.Background(), Status{Available: true, Latest: "1.1.0", NotesURL: u}, "en", "1.0.0")
	_, _ = IncomingNotes(context.Background(), Status{Available: true, Latest: "1.2.0", NotesURL: u}, "en", "1.0.0")
	if n := hits.Load(); n != 2 {
		t.Fatalf("a newer offer must fetch its own bundle: %d hits, want 2", n)
	}
}

func TestIncomingNotes_FailureRememberedThenRetried(t *testing.T) {
	resetNotesCache(t)
	srv, hits := notesServer(t, http.StatusNotFound, nil)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	notesNow = func() time.Time { return now }
	st := Status{Available: true, Latest: "1.2.0", NotesURL: srv.URL + "/release-notes.json"}

	for i := 0; i < 3; i++ {
		if _, err := IncomingNotes(context.Background(), st, "en", "1.0.0"); err == nil {
			t.Fatal("a 404 must be an error")
		}
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("a failure must not be retried on every request: %d hits, want 1", n)
	}
	now = now.Add(notesFailureTTL)
	_, _ = IncomingNotes(context.Background(), st, "en", "1.0.0")
	if n := hits.Load(); n != 2 {
		t.Fatalf("after the failure TTL the fetch is retried: %d hits, want 2", n)
	}
}

func TestIncomingNotes_SizeCap(t *testing.T) {
	resetNotesCache(t)
	big := append(testBundle(t)[:0:0], testBundle(t)...)
	big = append(big, []byte(strings.Repeat(" ", maxNotesBytes))...) // valid JSON, just too big
	srv, _ := notesServer(t, http.StatusOK, big)
	st := Status{Available: true, Latest: "1.2.0", NotesURL: srv.URL + "/release-notes.json"}
	if _, err := IncomingNotes(context.Background(), st, "en", "1.0.0"); err == nil {
		t.Fatalf("a bundle over %d bytes must be refused", maxNotesBytes)
	}
}

func TestIncomingNotes_RejectsMalformedAndMissing(t *testing.T) {
	resetNotesCache(t)
	srv, hits := notesServer(t, http.StatusOK, []byte(`{"format":1,"files":{"../x/v1.2.0.md":"x"}}`))
	if _, err := IncomingNotes(context.Background(), Status{Available: true, Latest: "1.2.0", NotesURL: srv.URL + "/n.json"}, "en", "1.0.0"); err == nil {
		t.Fatal("a malformed bundle must be an error")
	}
	resetNotesCache(t)
	if _, err := IncomingNotes(context.Background(), Status{Available: true, Latest: "1.2.0"}, "en", "1.0.0"); err == nil {
		t.Fatal("no notes asset must be an error")
	}
	// A URL the check would never have stored is refused without a request.
	resetNotesCache(t)
	before := hits.Load()
	if _, err := IncomingNotes(context.Background(), Status{Available: true, Latest: "1.2.0", NotesURL: "https://evil.example/n.json"}, "en", "1.0.0"); err == nil {
		t.Fatal("a foreign notes URL must be refused")
	}
	if hits.Load() != before {
		t.Fatal("refused URL must not be fetched")
	}
}

func TestIncomingNotes_OfferedVersionMissingFromBundle(t *testing.T) {
	resetNotesCache(t)
	srv, _ := notesServer(t, http.StatusOK, testBundle(t))
	st := Status{Available: true, Latest: "1.3.0", NotesURL: srv.URL + "/release-notes.json"}
	// 1.2.0 < running? No: running 1.2.0, offered 1.3.0 with no note → nothing to show.
	if _, err := IncomingNotes(context.Background(), st, "en", "1.2.0"); err == nil {
		t.Fatal("no notes in range must read as unavailable")
	}
}

func TestIncomingNotes_NoRequestInDemoMode(t *testing.T) {
	resetNotesCache(t)
	srv, hits := notesServer(t, http.StatusOK, testBundle(t))
	netaccess.SetDemo(true)
	t.Cleanup(func() { netaccess.SetDemo(false) })
	_, _ = IncomingNotes(context.Background(), Status{Available: true, Latest: "1.2.0", NotesURL: srv.URL + "/n.json"}, "en", "1.0.0")
	if hits.Load() != 0 {
		t.Fatal("the demo till must not fetch release notes")
	}
}

// Review finding 1: a manager navigating away mid-download cancels the
// request context. That must not be remembered as a 10-minute "unavailable":
// the download finishes on its own timeout and the next Settings load reads
// the cached bundle.
func TestIncomingNotes_CallerCancelNotMemoised(t *testing.T) {
	resetNotesCache(t)
	body := testBundle(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write(body)
	}))
	old := releasesURL
	releasesURL = srv.URL + "/latest"
	t.Cleanup(func() { releasesURL = old; srv.Close() })
	st := Status{Available: true, Latest: "1.2.0", NotesURL: srv.URL + "/release-notes.json"}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, _ = IncomingNotes(ctx, st, "en", "1.0.0")

	notes, err := IncomingNotes(context.Background(), st, "en", "1.0.0")
	if err != nil || len(notes) == 0 {
		t.Fatalf("after a cancelled first request the notes must still load: notes=%d err=%v", len(notes), err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("the cancelled request's download must be reused: %d hits, want 1", n)
	}
}
