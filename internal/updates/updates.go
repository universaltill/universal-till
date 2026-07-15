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
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
)

const releasesURL = "https://api.github.com/repos/universaltill/universal-till/releases/latest"

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

// Start launches the background checker: once ~30s after boot, then daily.
// Set UT_UPDATE_CHECK=0 to disable (e.g. air-gapped tills).
func Start(ctx context.Context) {
	if v := os.Getenv("UT_UPDATE_CHECK"); v != "" {
		if on, err := strconv.ParseBool(v); err == nil && !on {
			return
		}
	}
	go func() {
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
// any real release.
func Newer(a, b string) bool {
	if b == "dev" || b == "" {
		return true
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
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
	return false
}

func numPrefix(s string) string {
	for i, r := range s {
		if r < '0' || r > '9' {
			return s[:i]
		}
	}
	return s
}
