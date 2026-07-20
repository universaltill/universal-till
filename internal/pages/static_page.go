package pages

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	uiassets "github.com/universaltill/universal-till/web"
)

// fallbackFS serves from disk when present (a shop's uploaded item images,
// receipt logo, or theme overrides — internal/pages/catalog/handlers.go and
// friends still write to web/public/... on disk), falling back to the
// binary's embedded default assets otherwise. This is what lets the app run
// with zero loose files alongside it: a packaged install with no web/public
// directory at all still serves CSS/JS/logos/themes correctly, while a shop
// that HAS customized/uploaded assets still gets those instead of the
// bundled defaults.
//
// disk.Open is attempted on every call, not gated by an existence check
// cached at construction time: os.DirFS is lazy (it doesn't require the
// directory to exist when built, only when a path inside it is actually
// opened), so a directory created moments after startup by, say, a shop's
// first item-image upload is picked up immediately — matching the
// self-healing behavior of the old http.FileServer(http.Dir(...)) exactly,
// just with a working fallback for the common case where it's missing.
type fallbackFS struct {
	disk  fs.FS
	embed fs.FS
}

func (f fallbackFS) Open(name string) (fs.File, error) {
	if f.disk != nil {
		if file, err := f.disk.Open(name); err == nil {
			return file, nil
		}
	}
	return f.embed.Open(name)
}

// ReadDir merges both sides by name (disk wins on conflict) rather than
// preferring one side wholesale like Open does — otherwise an existing but
// empty/partial on-disk directory (e.g. web/public/themes created by some
// other action but not yet holding every built-in theme) would hide
// embedded defaults from a directory listing even though Open(a specific
// file) still correctly falls through to them.
func (f fallbackFS) ReadDir(name string) ([]fs.DirEntry, error) {
	byName := map[string]fs.DirEntry{}
	var anyOK bool
	if f.disk != nil {
		if entries, err := fs.ReadDir(f.disk, name); err == nil {
			anyOK = true
			for _, e := range entries {
				byName[e.Name()] = e
			}
		}
	}
	if entries, err := fs.ReadDir(f.embed, name); err == nil {
		anyOK = true
		for _, e := range entries {
			if _, exists := byName[e.Name()]; !exists {
				byName[e.Name()] = e
			}
		}
	}
	if !anyOK {
		// Neither side has it — surface the embed side's error, the
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

// newPublicFallbackFS builds the disk-then-embedded-default fs.FS backing
// both the /public/ static file server and built-in theme resolution
// (internal/pages/themes.go), rooted at embedRoot inside web.FS (e.g.
// "public" or "public/themes") and the matching on-disk path.
func newPublicFallbackFS(diskDir, embedRoot string) fs.FS {
	embedded, err := fs.Sub(uiassets.FS, embedRoot)
	if err != nil {
		// The embed directive guarantees these subtrees exist at build
		// time, so this is unreachable — fail loudly rather than silently
		// serving nothing if it ever isn't.
		panic("web.FS has no " + embedRoot + " subtree: " + err.Error())
	}
	return fallbackFS{disk: os.DirFS(diskDir), embed: embedded}
}

func registerStatic(mux *http.ServeMux) {
	mux.Handle("/public/", http.StripPrefix("/public/", http.FileServerFS(newPublicFallbackFS(filepath.Join("web", "public"), "public"))))
}
