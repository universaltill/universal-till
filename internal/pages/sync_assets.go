package pages

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
)

// Item-image sync (ADR-0011 D2b follow-up): the DB snapshot and admin
// pull carry rows, not files — product photos lived only on the primary,
// so replica tiles rendered without images. The primary now serves an
// asset manifest + files (bearer-gated, items/ scope only) and the
// replica's pull tick downloads whatever it is missing.

// assetsRoot is the only directory the sync surface will ever serve —
// the stable per-user data dir uploads actually land in (paths.Data),
// NOT a cwd-relative path. This used to be a plain "web/public/assets/items"
// constant; once item image uploads moved to paths.Data (fixed 2026-07-29,
// uploads were being lost on every app self-update), this constant silently
// stopped matching reality — listItemAssets() walked a directory nothing
// was ever written to anymore, so replica image sync went quietly dead
// with no error anywhere to notice it by. Caught by a regression test,
// not a live report.
func assetsRoot() string { return paths.Data("public", "assets", "items") }

// assetEntry is one file in the manifest; size is the change detector
// (content-addressed enough for thumbnails, no hashing per poll).
type assetEntry struct {
	Path string `json:"path"` // relative to assetsRoot, always forward slashes
	Size int64  `json:"size"`
}

// listItemAssets walks the items asset tree.
func listItemAssets() ([]assetEntry, error) {
	root := assetsRoot()
	var out []assetEntry
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil // a missing tree just means "no images yet"
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		out = append(out, assetEntry{Path: filepath.ToSlash(rel), Size: info.Size()})
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	return out, err
}

// safeAssetPath resolves a manifest path under assetsRoot, refusing
// traversal and absolute paths.
func safeAssetPath(rel string) (string, bool) {
	if rel == "" || strings.Contains(rel, "..") || strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") {
		return "", false
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return "", false
	}
	return filepath.Join(assetsRoot(), clean), true
}

// registerSyncAssets mounts the primary-side asset surface.
func registerSyncAssets(mux *http.ServeMux, d *common.Deps) {
	tills := data.NewTillsRepo(d.Db)

	mux.HandleFunc("GET /api/sync/assets", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := syncTill(r, tills); !ok {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": "unauthorized"})
			return
		}
		list, err := listItemAssets()
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "sync_assets", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": list, "error": nil})
	})

	mux.HandleFunc("GET /api/sync/assets/file", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := syncTill(r, tills); !ok {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": "unauthorized"})
			return
		}
		path, ok := safeAssetPath(r.URL.Query().Get("path"))
		if !ok {
			common.LocalizedError(w, r, http.StatusBadRequest, "sync.error.bad_path")
			return
		}
		http.ServeFile(w, r, path)
	})
}

// syncItemAssets runs on the replica's pull tick: fetch the primary's
// manifest and download anything missing or size-changed. Failures log
// and wait for the next tick — never fatal (ADR-0003).
func syncItemAssets(ctx context.Context, client *http.Client, primary, bearer string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(primary, "/")+"/api/sync/assets", nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var out struct {
		Data []assetEntry `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return
	}

	fetched := 0
	for _, e := range out.Data {
		local, ok := safeAssetPath(e.Path)
		if !ok {
			continue // never trust the wire, even from our own primary
		}
		if st, err := os.Stat(local); err == nil && st.Size() == e.Size {
			continue
		}
		freq, err := http.NewRequestWithContext(ctx, http.MethodGet,
			strings.TrimSuffix(primary, "/")+"/api/sync/assets/file?path="+url.QueryEscape(e.Path), nil)
		if err != nil {
			continue
		}
		freq.Header.Set("Authorization", "Bearer "+bearer)
		fresp, err := client.Do(freq)
		if err != nil {
			return // primary went away; next tick retries
		}
		if fresp.StatusCode != http.StatusOK {
			fresp.Body.Close()
			continue
		}
		if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
			fresp.Body.Close()
			continue
		}
		tmp := local + ".sync-tmp"
		f, err := os.Create(tmp)
		if err != nil {
			fresp.Body.Close()
			continue
		}
		_, cpErr := io.Copy(f, fresp.Body)
		f.Close()
		fresp.Body.Close()
		if cpErr != nil {
			os.Remove(tmp)
			continue
		}
		if err := os.Rename(tmp, local); err != nil {
			os.Remove(tmp)
			continue
		}
		fetched++
	}
	if fetched > 0 {
		logging.L().Infof("sync pull: %d item image(s) fetched from the primary", fetched)
	}
}
