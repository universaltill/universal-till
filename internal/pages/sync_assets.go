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
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
)

// Photo sync (ADR-0011 D2b follow-up, ut-docs#2566): the DB snapshot and
// admin pull carry rows, not files — uploaded photos lived only on the
// primary, so replica tiles rendered without images. The primary serves an
// asset manifest + files per scope (bearer-gated; item and category photos)
// and the replica's pull tick downloads whatever it is missing.

// assetScope is one asset tree the sync surface serves (ut-docs#2566):
// items/ (ADR-0011 D2b follow-up) and categories/ (uploaded category photos,
// ut-docs#2500). Each scope has its own endpoint pair rather than a query
// parameter, so a new replica asking an older primary for categories gets a
// 404 and skips — it can never mistake the items manifest for the
// categories one and write item photos into the categories tree.
type assetScope struct {
	dir      string // subdirectory of paths.Data("public", "assets")
	listPath string
	filePath string
}

var (
	itemAssetScope     = assetScope{dir: "items", listPath: "/api/sync/assets", filePath: "/api/sync/assets/file"}
	categoryAssetScope = assetScope{dir: "categories", listPath: "/api/sync/assets/categories", filePath: "/api/sync/assets/categories/file"}
	syncAssetScopes    = []assetScope{itemAssetScope, categoryAssetScope}
)

// syncAssetMaxBytes caps one synced file, on both sides (ut-docs#2566): the
// primary leaves bigger files out of its manifest and the replica refuses to
// write more than this, whatever the wire claims. It matches the 10MB upload
// cap the item and category photo handlers apply, so no legitimate upload is
// ever left behind.
const syncAssetMaxBytes = 10 << 20

// root is the only directory a scope will ever serve — the stable per-user
// data dir uploads actually land in (paths.Data), NOT a cwd-relative path.
// Items used to be a plain "web/public/assets/items" constant; once item
// image uploads moved to paths.Data (fixed 2026-07-29, uploads were being
// lost on every app self-update), that constant silently stopped matching
// reality — the manifest walked a directory nothing was ever written to
// anymore, so replica image sync went quietly dead with no error anywhere to
// notice it by. Caught by a regression test, not a live report.
func (s assetScope) root() string { return paths.Data("public", "assets", s.dir) }

// assetEntry is one file in the manifest. Size plus modification time is the
// change detector (no hashing per poll): the replica stamps each downloaded
// file with the primary's mtime, so a photo replaced by one of the same size
// still re-downloads. Mod is omitted by primaries older than ut-docs#2566;
// a zero Mod falls back to size alone.
type assetEntry struct {
	Path string `json:"path"` // relative to the scope root, always forward slashes
	Size int64  `json:"size"`
	Mod  int64  `json:"mod,omitempty"` // unix seconds
}

// list walks the scope's asset tree. Files over syncAssetMaxBytes and the
// replica's own in-flight temp files are left out.
func (s assetScope) list() ([]assetEntry, error) {
	root := s.root()
	var out []assetEntry
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil // a missing tree just means "no images yet"
		}
		if info.Size() > syncAssetMaxBytes || strings.HasSuffix(path, syncTmpSuffix) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		out = append(out, assetEntry{Path: filepath.ToSlash(rel), Size: info.Size(), Mod: info.ModTime().Unix()})
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	return out, err
}

// safePath resolves a manifest path under the scope root, refusing
// traversal and absolute paths (":" too, so a Windows drive-relative
// "C:x" never reaches filepath.Join).
func (s assetScope) safePath(rel string) (string, bool) {
	if rel == "" || strings.Contains(rel, "..") || strings.HasPrefix(rel, "/") || strings.ContainsAny(rel, "\\:") {
		return "", false
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return "", false
	}
	return filepath.Join(s.root(), clean), true
}

// registerSyncAssets mounts the primary-side asset surface, one manifest +
// file endpoint pair per scope. Every path here must also be in
// internal/auth's bearer-exempt list (TestSyncPullPathsAreExempt).
func registerSyncAssets(mux *http.ServeMux, d *common.Deps) {
	tills := data.NewTillsRepo(d.Db)
	for _, scope := range syncAssetScopes {
		scope := scope
		mux.HandleFunc("GET "+scope.listPath, func(w http.ResponseWriter, r *http.Request) {
			if _, ok := syncTill(r, tills); !ok {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": "unauthorized"})
				return
			}
			list, err := scope.list()
			if err != nil {
				common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "sync_assets", err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": list, "error": nil})
		})

		mux.HandleFunc("GET "+scope.filePath, func(w http.ResponseWriter, r *http.Request) {
			if _, ok := syncTill(r, tills); !ok {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": "unauthorized"})
				return
			}
			path, ok := scope.safePath(r.URL.Query().Get("path"))
			if !ok {
				common.LocalizedError(w, r, http.StatusBadRequest, "sync.error.bad_path")
				return
			}
			http.ServeFile(w, r, path)
		})
	}
}

const syncTmpSuffix = ".sync-tmp"

// syncManifestMaxBytes bounds the manifest a replica will decode, so a
// hostile or broken primary can't exhaust its memory (~40k entries).
const syncManifestMaxBytes = 4 << 20

// sameMod reports whether a local mtime matches the manifest's. Zero (an
// older primary) means size alone decides. The 2s slack covers FAT/exFAT
// SD cards, which round mtimes to 2s — an exact compare would re-download
// every file on every tick there.
func sameMod(local, remote int64) bool {
	if remote == 0 {
		return true
	}
	d := local - remote
	return d >= -2 && d <= 2
}

// syncAssets runs on the replica's pull tick: for every scope, fetch the
// primary's manifest and download anything missing or changed. Failures log
// and wait for the next tick — never fatal (ADR-0003).
func syncAssets(ctx context.Context, client *http.Client, primary, bearer string) {
	for _, scope := range syncAssetScopes {
		if !scope.pull(ctx, client, primary, bearer) {
			return // primary went away; next tick retries
		}
	}
}

// pull syncs one scope. It reports false only when the primary is
// unreachable, so the caller can stop instead of timing out per scope.
func (s assetScope) pull(ctx context.Context, client *http.Client, primary, bearer string) bool {
	base := strings.TrimSuffix(primary, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+s.listPath, nil)
	if err != nil {
		return true
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return true // an older primary without this scope answers 404
	}
	var out struct {
		Data []assetEntry `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, syncManifestMaxBytes)).Decode(&out); err != nil {
		return true
	}

	fetched := 0
	for _, e := range out.Data {
		local, ok := s.safePath(e.Path)
		if !ok || e.Size < 0 || e.Size > syncAssetMaxBytes {
			continue // never trust the wire, even from our own primary
		}
		if st, err := os.Stat(local); err == nil && st.Size() == e.Size && sameMod(st.ModTime().Unix(), e.Mod) {
			continue
		}
		freq, err := http.NewRequestWithContext(ctx, http.MethodGet,
			base+s.filePath+"?path="+url.QueryEscape(e.Path), nil)
		if err != nil {
			continue
		}
		freq.Header.Set("Authorization", "Bearer "+bearer)
		fresp, err := client.Do(freq)
		if err != nil {
			return false
		}
		if s.store(fresp, local, e) {
			fetched++
		}
	}
	if fetched > 0 {
		logging.L().Infof("sync pull: %d %s image(s) fetched from the primary", fetched, s.dir)
	}
	return true
}

// store writes one fetched file atomically (temp file + rename), refusing a
// body larger than the cap or different in size from the manifest — a file
// replaced between the manifest and the fetch, or a truncated transfer,
// waits for the next tick instead of landing half-written.
func (s assetScope) store(fresp *http.Response, local string, e assetEntry) bool {
	defer fresp.Body.Close()
	if fresp.StatusCode != http.StatusOK {
		return false
	}
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return false
	}
	tmp := local + syncTmpSuffix
	f, err := os.Create(tmp)
	if err != nil {
		return false
	}
	n, cpErr := io.Copy(f, io.LimitReader(fresp.Body, syncAssetMaxBytes+1))
	closeErr := f.Close()
	if cpErr != nil || closeErr != nil || n != e.Size {
		os.Remove(tmp)
		return false
	}
	if e.Mod != 0 {
		mt := time.Unix(e.Mod, 0)
		if err := os.Chtimes(tmp, mt, mt); err != nil {
			// Not fatal: the file is right, the next tick just re-fetches it.
			logging.L().Errorf("sync pull: stamp %s mtime: %v", s.dir, err)
		}
	}
	if err := os.Rename(tmp, local); err != nil {
		os.Remove(tmp)
		return false
	}
	return true
}
