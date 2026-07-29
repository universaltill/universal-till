package pages

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/universaltill/universal-till/internal/paths"
	uiassets "github.com/universaltill/universal-till/web"
)

// fallbackFS serves, in order: the stable per-user data directory (a shop's
// uploaded item images, receipt logo, or theme overrides — see
// internal/pages/catalog/handlers.go and internal/pages/print_api.go, which
// write there, NOT to the release tree), then the release's own on-disk
// web/public (bundled defaults + anything a plugin drops there at runtime),
// then the binary's embedded defaults.
//
// The stable tier exists specifically because the release tree is NOT safe
// for user data: a self-update replaces it wholesale (internal/selfupdate's
// archive path renames the whole web/ dir aside; the macOS .app path
// replaces the entire bundle, whose Contents/Resources/web IS the "disk"
// tier). Writing uploads there means a routine update — the same one that
// fixed this — silently deletes a shop's item photos and receipt logo.
// Confirmed live 2026-07-29: an item's photo vanished after updating.
//
// disk.Open is attempted on every call, not gated by an existence check
// cached at construction time: os.DirFS is lazy (it doesn't require the
// directory to exist when built, only when a path inside it is actually
// opened), so a directory created moments after startup by, say, a shop's
// first item-image upload is picked up immediately — matching the
// self-healing behavior of the old http.FileServer(http.Dir(...)) exactly,
// just with a working fallback for the common case where it's missing.
type fallbackFS struct {
	stable fs.FS
	disk   fs.FS
	embed  fs.FS
}

func (f fallbackFS) Open(name string) (fs.File, error) {
	if f.stable != nil {
		if file, err := f.stable.Open(name); err == nil {
			return file, nil
		}
	}
	if f.disk != nil {
		if file, err := f.disk.Open(name); err == nil {
			return file, nil
		}
	}
	return f.embed.Open(name)
}

// ReadDir merges all three sides by name (stable wins, then disk, then
// embed) rather than preferring one side wholesale like Open does —
// otherwise an existing but empty/partial on-disk directory (e.g.
// web/public/themes created by some other action but not yet holding every
// built-in theme) would hide embedded defaults from a directory listing
// even though Open(a specific file) still correctly falls through to them.
func (f fallbackFS) ReadDir(name string) ([]fs.DirEntry, error) {
	byName := map[string]fs.DirEntry{}
	var anyOK bool
	for _, side := range []fs.FS{f.stable, f.disk, f.embed} {
		if side == nil {
			continue
		}
		if entries, err := fs.ReadDir(side, name); err == nil {
			anyOK = true
			for _, e := range entries {
				if _, exists := byName[e.Name()]; !exists {
					byName[e.Name()] = e
				}
			}
		}
	}
	if !anyOK {
		// None of the sides have it — surface the embed side's error, the
		// canonical "this doesn't exist" answer.
		_, err := fs.ReadDir(f.embed, name)
		return nil, err
	}
	merged := make([]fs.DirEntry, 0, len(byName))
	for _, e := range byName {
		merged = append(merged, e)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Name() < merged[j].Name() })
	return merged, nil
}

// newPublicFallbackFS builds the stable-data-then-disk-then-embedded-default
// fs.FS backing both the /public/ static file server and built-in theme
// resolution (internal/pages/themes.go). diskDir/embedRoot are the existing
// release-tree/embedded roots (e.g. "web/public" + "public", or
// "web/public/themes" + "public/themes"); the stable root mirrors embedRoot
// under the resolved per-user data directory (paths.DataDir()).
func newPublicFallbackFS(diskDir, embedRoot string) fs.FS {
	embedded, err := fs.Sub(uiassets.FS, embedRoot)
	if err != nil {
		// The embed directive guarantees these subtrees exist at build
		// time, so this is unreachable — fail loudly rather than silently
		// serving nothing if it ever isn't.
		panic("web.FS has no " + embedRoot + " subtree: " + err.Error())
	}
	stableDir := paths.Data(strings.Split(embedRoot, "/")...)
	return fallbackFS{stable: os.DirFS(stableDir), disk: os.DirFS(diskDir), embed: embedded}
}

func registerStatic(mux *http.ServeMux) {
	mux.Handle("/public/", http.StripPrefix("/public/", http.FileServerFS(newPublicFallbackFS(filepath.Join("web", "public"), "public"))))
}
