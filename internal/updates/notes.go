package updates

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/releasenotes"
)

// Incoming release notes (ut-docs#3940): before a manager installs an
// update, Settings → Software update shows what the new version brings. The
// notes of a version this till is not running are not embedded in it, so
// they come from the release's release-notes.json asset (built by
// scripts/release-notes-bundle, attached by .goreleaser.yaml), fetched
// lazily — only when a manager opens Settings with an update waiting — and
// kept in memory for that offered version. Best effort like the check
// itself: every failure is reported as "unavailable" and never blocks the
// install.

// NotesAssetName is the release asset that carries every release note.
const NotesAssetName = "release-notes.json"

// githubDownloadPrefix is the only place a notes asset may come from on a
// till using the public releases API.
const githubDownloadPrefix = "https://github.com/universaltill/universal-till/releases/download/"

// maxNotesBytes caps the download; the real bundle is a few hundred KB.
const maxNotesBytes = 4 << 20

// Seams (tests): the clock for the failure memo, how long a failure is
// remembered, and the per-download timeout.
var (
	notesNow        = time.Now
	notesFailureTTL = 10 * time.Minute
	notesTimeout    = 10 * time.Second
)

// ErrNoNotes means the offered release has no usable notes for this till.
var ErrNoNotes = errors.New("updates: no release notes for the offered version")

type notesEntry struct {
	key string
	lib *releasenotes.Library
	err error
	at  time.Time
}

// notesCache holds the last offered version's bundle (or its failure). The
// mutex is held across the download on purpose: concurrent Settings loads
// for the same offer share one request instead of each starting their own.
var notesCache struct {
	sync.Mutex
	entry notesEntry
}

// notesURLAllowed reports whether a notes asset URL may be fetched: this
// repo's GitHub release downloads, or — when an operator pointed the check at
// their own releases URL (UT_UPDATE_RELEASES_URL) — that same scheme and host.
func notesURLAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Host == "" {
		return false
	}
	// No dot segments, however encoded: the cleaned path must be the path.
	if strings.Contains(u.Path, "..") || path.Clean(u.Path) != u.Path {
		return false
	}
	if u.Scheme == "https" && u.Host == "github.com" && strings.HasPrefix(raw, githubDownloadPrefix) &&
		strings.HasPrefix(u.Path, "/universaltill/universal-till/releases/download/") {
		return true
	}
	if releasesURL == defaultReleasesURL {
		return false
	}
	base, err := url.Parse(releasesURL)
	return err == nil && u.Scheme == base.Scheme && u.Host == base.Host
}

// IncomingNotes returns the release notes a till running `running` would get
// by installing st's offered version — every version after running up to and
// including the offered one, newest first — in locale, falling back to
// English per note (Note.Translated false). An error (no asset, a refused
// URL, a failed or malformed download, nothing in range) means the caller
// shows "Release notes unavailable"; it never affects installing.
func IncomingNotes(ctx context.Context, st Status, locale, running string) ([]*releasenotes.Note, error) {
	if !st.Available || st.NotesURL == "" || !notesURLAllowed(st.NotesURL) {
		return nil, ErrNoNotes
	}
	lib, err := notesLibrary(ctx, st.Latest, st.NotesURL)
	if err != nil {
		return nil, err
	}
	notes := lib.Incoming(locale, running, st.Latest)
	if len(notes) == 0 {
		return nil, ErrNoNotes
	}
	return notes, nil
}

// notesLibrary returns the cached bundle for (version, url), downloading it
// once. A failure is remembered for notesFailureTTL so a broken asset is not
// re-requested on every Settings load, then retried.
func notesLibrary(ctx context.Context, version, notesURL string) (*releasenotes.Library, error) {
	key := version + "\x00" + notesURL
	notesCache.Lock()
	defer notesCache.Unlock()
	e := notesCache.entry
	if e.key == key {
		if e.err == nil {
			return e.lib, nil
		}
		if age := notesNow().Sub(e.at); age >= 0 && age < notesFailureTTL {
			return nil, e.err
		}
	}
	lib, err := fetchNotes(ctx, notesURL)
	notesCache.entry = notesEntry{key: key, lib: lib, err: err, at: notesNow()}
	return lib, err
}

func fetchNotes(ctx context.Context, notesURL string) (*releasenotes.Library, error) {
	// WithoutCancel: a manager leaving Settings mid-download must not turn
	// into a remembered failure (review finding 1); the download finishes on
	// notesTimeout alone and the next load reads the cached result.
	reqCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), notesTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, notesURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, application/octet-stream")
	resp, err := outboundClient.Do(req) // netaccess: refused on the demo till
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("updates: release notes: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxNotesBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxNotesBytes {
		return nil, fmt.Errorf("updates: release notes: larger than %d bytes", maxNotesBytes)
	}
	return releasenotes.LoadBundle(b)
}
