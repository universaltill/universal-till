// Package updates does a best-effort background check of the GitHub Releases
// API and reports whether a newer version is available, so the till can show an
// "update available" hint in its status bar. Offline-first: every failure is
// silent, it runs on a background goroutine, and it never touches checkout.
package updates

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/netaccess"
)

// defaultReleasesURL is the public GitHub Releases API for this repo.
const defaultReleasesURL = "https://api.github.com/repos/universaltill/universal-till/releases/latest"

// releasesURL is what the check polls: the default, or an operator override
// from UT_UPDATE_RELEASES_URL (read once at start-up) — for an air-gapped
// till pointed at a local mirror of the releases API, and for the e2e
// harness's fake release server (ut-docs#3940). It only decides where the
// till LEARNS about a release and its release notes: internal/selfupdate
// downloads the update itself from its own fixed URL, unaffected. Also a
// test seam: tests point it at a local httptest server so no test ever
// talks to the real GitHub API.
var releasesURL = releasesURLFromEnv()

// releasesURLFromEnv reads UT_UPDATE_RELEASES_URL; anything but an absolute
// http(s) URL is ignored and the default is used (Start logs that — this
// runs at package init, before logging is configured).
func releasesURLFromEnv() string {
	v := strings.TrimSpace(os.Getenv("UT_UPDATE_RELEASES_URL"))
	if v == "" {
		return defaultReleasesURL
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return defaultReleasesURL
	}
	return v
}

// Status is the latest known release info.
type Status struct {
	Available bool   // a newer version than this build exists
	Latest    string // e.g. "0.1.3"
	URL       string // the release page
	// NotesURL is the release's release-notes.json asset (ut-docs#3940), or
	// "" when the release has none or it is not on an allowed host
	// (notesURLAllowed).
	NotesURL string
}

var state atomic.Value // Status

// outboundClient replaces http.DefaultClient (same zero timeout, same
// default transport) so the public demo till refuses these requests
// (ADR-0113 §1.6, ut-docs#2795).
var outboundClient = netaccess.NewClient(0)

// Current returns the last checked status (zero value before the first check).
func Current() Status {
	if s, ok := state.Load().(Status); ok {
		return s
	}
	return Status{}
}

// autoUpdateStuck is published by the auto-update scheduler (internal/pages,
// ut-docs#2733): auto-update is on and an update is waiting, but this install
// can't apply it itself, so the nightly attempt is silently skipped. The
// status-bar chip reads it through httpx's autoupdatestuck template func —
// same request-free, offline-safe shape as Current().
var autoUpdateStuck atomic.Bool

// SetAutoUpdateStuck records the scheduler's latest stuck decision.
func SetAutoUpdateStuck(v bool) { autoUpdateStuck.Store(v) }

// AutoUpdateStuck reports the scheduler's latest stuck decision (false until
// its first tick).
func AutoUpdateStuck() bool { return autoUpdateStuck.Load() }

// CheckNow performs one synchronous check (the Settings "Check for updates"
// button) and returns the freshest status. A failed check leaves the
// previously known status in place — the caller can tell it failed by
// Latest staying empty on a first-ever check.
func CheckNow(ctx context.Context) Status {
	checkOnce(ctx)
	return Current()
}

// Start launches the background checker: once ~30s after boot, then daily.
// Set UT_UPDATE_CHECK=0 to disable (e.g. air-gapped tills). wg is marked Done
// once the checker goroutine has fully exited (ctx cancelled) — callers use
// it to wait out shutdown instead of returning while the goroutine still runs.
func Start(ctx context.Context, wg *sync.WaitGroup) {
	if !enabledFromEnv() {
		return
	}
	if v := strings.TrimSpace(os.Getenv("UT_UPDATE_RELEASES_URL")); v != "" && releasesURL == defaultReleasesURL {
		logging.L().Warnf("updates: ignoring UT_UPDATE_RELEASES_URL %q: not an absolute http(s) URL", v)
	}
	wg.Add(1)
	go func() {
		defer logging.RecoverAndLog("updates.check")
		defer wg.Done()
		select {
		case <-time.After(30 * time.Second):
		case <-ctx.Done():
			return
		}
		checkOnce(ctx)
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				checkOnce(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()
}

// Enabled reports whether an air-gapped/opted-out install has explicitly
// disabled update checking via UT_UPDATE_CHECK — exported (ut-docs#1165
// review finding N2) so a caller other than Start's own background loop
// (e.g. the setup wizard's step-1 auto-triggered check, which — unlike
// Settings' manual "Check for updates" button — fires without an explicit
// user action and so must honor the same opt-out) can check the same
// decision before making an outbound call.
func Enabled() bool { return enabledFromEnv() }

// enabledFromEnv reports whether the background checker should run at all:
// only an explicit falsy UT_UPDATE_CHECK (e.g. "0", "false") disables it;
// unset, truthy, or unparseable values leave it enabled.
func enabledFromEnv() bool {
	if v := os.Getenv("UT_UPDATE_CHECK"); v != "" {
		if on, err := strconv.ParseBool(v); err == nil && !on {
			return false
		}
	}
	return true
}

func checkOnce(ctx context.Context) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, releasesURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := outboundClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var rel struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return
	}
	latest := strings.TrimPrefix(strings.TrimSpace(rel.TagName), "v")
	if latest == "" {
		return
	}
	notesURL := ""
	for _, a := range rel.Assets {
		if a.Name == NotesAssetName && notesURLAllowed(a.URL) {
			notesURL = a.URL
			break
		}
	}
	state.Store(Status{
		Available: Newer(latest, buildinfo.Version),
		Latest:    latest,
		URL:       rel.HTMLURL,
		NotesURL:  notesURL,
	})
}

// Newer reports whether version a is newer than b, compared as dotted numeric
// versions. A non-numeric current build (e.g. "dev") is treated as older than
// any real release. A prerelease suffix ("0.22.2-rc1") ranks below the same
// version without one, and prereleases of one version order naturally
// (rc2 < rc10); "+build" metadata never affects the order (ut-docs#2759).
func Newer(a, b string) bool {
	if b == "dev" || b == "" {
		return true
	}
	a, b = stripBuild(a), stripBuild(b)
	aCore, aPre, _ := strings.Cut(a, "-")
	bCore, bPre, _ := strings.Cut(b, "-")
	as, bs := strings.Split(aCore, "."), strings.Split(bCore, ".")
	n := max(len(as), len(bs))
	for i := range n {
		var ai, bi int
		if i < len(as) {
			ai, _ = strconv.Atoi(numPrefix(as[i]))
		}
		if i < len(bs) {
			bi, _ = strconv.Atoi(numPrefix(bs[i]))
		}
		if ai != bi {
			return ai > bi
		}
	}
	switch {
	case aPre == bPre:
		return false
	case aPre == "":
		return true // a release beats any of its own prereleases
	case bPre == "":
		return false
	}
	return naturalLess(bPre, aPre)
}

func stripBuild(v string) string {
	v, _, _ = strings.Cut(v, "+")
	return v
}

// naturalLess compares prerelease tags piecewise: runs of digits as numbers,
// everything else as text ("rc2" < "rc10", "alpha" < "beta").
func naturalLess(x, y string) bool {
	for x != "" && y != "" {
		xs, xr := splitRun(x)
		ys, yr := splitRun(y)
		xn, xerr := strconv.Atoi(xs)
		yn, yerr := strconv.Atoi(ys)
		switch {
		case xerr == nil && yerr == nil && xn != yn:
			return xn < yn
		case (xerr == nil) != (yerr == nil):
			return xerr == nil // numbers sort before text (as in semver)
		case xs != ys:
			return xs < ys
		}
		x, y = xr, yr
	}
	return x == "" && y != ""
}

// splitRun returns the leading run of s that is all digits or all
// non-digits, and the rest.
func splitRun(s string) (string, string) {
	digit := s[0] >= '0' && s[0] <= '9'
	i := 1
	for i < len(s) && (s[i] >= '0' && s[i] <= '9') == digit {
		i++
	}
	return s[:i], s[i:]
}

func numPrefix(s string) string {
	for i, r := range s {
		if r < '0' || r > '9' {
			return s[:i]
		}
	}
	return s
}
