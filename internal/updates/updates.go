// Package updates does a best-effort background check of the GitHub Releases
// API and reports whether a newer version is available, so the till can show an
// "update available" hint in its status bar. Offline-first: every failure is
// silent, it runs on a background goroutine, and it never touches checkout.
package updates

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
)

// releasesURL is a var (not const) purely as a test seam: tests point it at a
// local httptest server so no test ever talks to the real GitHub API.
var releasesURL = "https://api.github.com/repos/universaltill/universal-till/releases/latest"

// Status is the latest known release info.
type Status struct {
	Available bool   // a newer version than this build exists
	Latest    string // e.g. "0.1.3"
	URL       string // the release page
}

var state atomic.Value // Status

// Current returns the last checked status (zero value before the first check).
func Current() Status {
	if s, ok := state.Load().(Status); ok {
		return s
	}
	return Status{}
}

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
	wg.Add(1)
	go func() {
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
	resp, err := http.DefaultClient.Do(req)
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
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return
	}
	latest := strings.TrimPrefix(strings.TrimSpace(rel.TagName), "v")
	if latest == "" {
		return
	}
	state.Store(Status{
		Available: Newer(latest, buildinfo.Version),
		Latest:    latest,
		URL:       rel.HTMLURL,
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
