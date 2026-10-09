// Package itemimages owns the catalog item image tree on disk — the
// per-item thumb.png and the cashier-confirmed camera-identify reference
// photos under ai_ref/ — and the one rule for picking an item's reference
// photo. The built-in identify (internal/pages) and the item_image_open
// plugin host function (internal/plugins, ADR-0121 R1, ut-docs#4005) both
// resolve through Ref, so they always pick, and encode, the same photo.
//
// Paths are built only from a validated item id under AssetDir; nothing
// here reads the item_images table (its rows arrive by sync and import).
package itemimages

import (
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/imaging"
	"github.com/universaltill/universal-till/internal/paths"
)

// MaxAIRefsPerItem is how many confirmed reference photos an item keeps.
const MaxAIRefsPerItem = 5

// MaxIDLen bounds an item id accepted by Ref.
const MaxIDLen = 128

// MaxFileBytes bounds an image file Ref reads into memory. Uploads are
// capped well below it (categoryUploadMaxBytes, 11 MiB); a bigger file in the
// items tree (sync, import, an old client) is refused unread, because a guest
// may open 64 per event and imaging.Decode bounds pixels, not bytes.
const MaxFileBytes = 16 << 20

// Reference photo roles (ADR-0121 R1).
const (
	// RoleAIRef is the item's newest cashier-confirmed photo.
	RoleAIRef = "ai_ref"
	// RoleThumb is the item's catalog thumbnail.
	RoleThumb = "thumb"
	// RoleRef is RoleAIRef when it decodes, else RoleThumb — the photo the
	// built-in identify sends for the item.
	RoleRef = "ref"
)

var (
	// ErrInvalid: the id or role is not acceptable (nothing was read).
	ErrInvalid = errors.New("itemimages: invalid item id or role")
	// ErrNotFound: no image of that role, or not a decodable PNG/JPEG.
	ErrNotFound = errors.New("itemimages: no such image")
)

// AssetDir is the item image tree in the stable per-user data dir (uploads
// land here via internal/pages/catalog's paths.Data(...) calls) — never a
// cwd-relative path, which would only resolve when the process's cwd is
// the repo checkout and silently find zero images once installed
// (docs/code-reviews/2026-07-29-coverage-batch-11-sync-assets-regression.md).
func AssetDir() string { return paths.Data("public", "assets", "items") }

// AIRefDir is the item's confirmed reference photo directory.
func AIRefDir(itemID string) string { return filepath.Join(AssetDir(), itemID, "ai_ref") }

// ValidID reports whether id is safe to join onto AssetDir: 1–MaxIDLen
// bytes with no '/', '\' or '.' (so no traversal, no "." / ".."), and a
// portable file name — no control bytes, no ':', no trailing space, no
// Windows device name — so a guest can't turn a bad id into an I/O error
// and a log line instead of -4 (#4005 review). Item ids are UUIDs.
func ValidID(id string) bool {
	if len(id) < 1 || len(id) > MaxIDLen || strings.ContainsAny(id, `/\.:`) || strings.HasSuffix(id, " ") {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 0x20 || id[i] == 0x7f {
			return false
		}
	}
	return !windowsDevice(id)
}

// windowsDevice reports a reserved Windows device name (CON, PRN, AUX, NUL,
// COM0–9, LPT0–9), case-insensitive. ValidID already refuses '.', so the
// "CON.txt" form can't occur.
func windowsDevice(id string) bool {
	base := strings.ToLower(id)
	switch base {
	case "con", "prn", "aux", "nul":
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "com") || strings.HasPrefix(base, "lpt")) && base[3] >= '0' && base[3] <= '9'
}

// Ref returns the item's reference photo of the given role as
// imaging.RefJPEG bytes. The id is validated before any filesystem access.
// ErrInvalid for a bad id or unknown role; ErrNotFound when the image is
// missing or does not decode; any other error is an I/O failure.
func Ref(itemID, role string) ([]byte, error) {
	if !ValidID(itemID) {
		return nil, ErrInvalid
	}
	switch role {
	case RoleAIRef:
		return aiRef(itemID)
	case RoleThumb:
		return thumb(itemID)
	case RoleRef:
		b, err := aiRef(itemID)
		if errors.Is(err, ErrNotFound) {
			return thumb(itemID)
		}
		return b, err
	}
	return nil, ErrInvalid
}

func aiRef(itemID string) ([]byte, error) {
	p, _, ok := LatestAIRef(AIRefDir(itemID))
	if !ok {
		return nil, ErrNotFound
	}
	return refFile(p)
}

func thumb(itemID string) ([]byte, error) {
	return refFile(filepath.Join(AssetDir(), itemID, "thumb.png"))
}

// refFile reads and re-encodes one image; a missing or undecodable file is
// ErrNotFound.
func refFile(p string) ([]byte, error) {
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("itemimages: read: %w", err)
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > MaxFileBytes {
		return nil, fmt.Errorf("%w: file over %d bytes", ErrNotFound, MaxFileBytes)
	}
	// Stat can lie (a file growing under us); read at most one byte past the cap, so an oversized file costs
	// MaxFileBytes+1 of memory at worst and is refused (#4005 review).
	raw, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("itemimages: read: %w", err)
	}
	if len(raw) > MaxFileBytes {
		return nil, fmt.Errorf("%w: file over %d bytes", ErrNotFound, MaxFileBytes)
	}
	out, err := imaging.RefJPEG(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	return out, nil
}

// LatestAIRef returns the newest reference photo in dir and its media type.
func LatestAIRef(dir string) (path, mediaType string, ok bool) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return "", "", false
	}
	names := refImageNames(entries)
	if len(names) == 0 {
		return "", "", false
	}
	newest := names[len(names)-1]
	media := "image/jpeg"
	if strings.HasSuffix(newest, ".png") {
		media = "image/png"
	}
	return filepath.Join(dir, newest), media, true
}

// StoreAIRef writes img as itemID's newest confirmed reference photo — a
// fresh PNG under AIRefDir (created first), never the uploaded bytes
// (ut-docs#1417) — prunes the folder to MaxAIRefsPerItem and returns the
// file's path. The built-in confirm and the plugin seam's pick (ADR-0121
// R2a, ut-docs#4006) both store through it. A failed write leaves nothing
// behind: a partial file with the newest name would shadow every older
// photo (LatestAIRef picks only the newest).
func StoreAIRef(itemID string, img image.Image) (string, error) {
	if !ValidID(itemID) {
		return "", ErrInvalid
	}
	dir := AIRefDir(itemID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// Names are nanosecond timestamps (lexical = chronological); O_EXCL
	// with a bump keeps two stores on a coarse clock (Windows) apart.
	n := time.Now().UnixNano()
	var out *os.File
	var outPath string
	for i := 0; ; i++ {
		outPath = filepath.Join(dir, fmt.Sprintf("%d.png", n+int64(i)))
		f, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			out = f
			break
		}
		if !errors.Is(err, fs.ErrExist) || i >= 16 {
			return "", err
		}
	}
	encErr := png.Encode(out, img)
	closeErr := out.Close()
	if encErr != nil || closeErr != nil {
		_ = os.Remove(outPath)
		return "", errors.Join(encErr, closeErr)
	}
	PruneAIRefs(dir)
	return outPath, nil
}

// PruneAIRefs keeps only the newest MaxAIRefsPerItem reference photos so the
// folder (and the identify-request payload) can't grow without bound.
func PruneAIRefs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	names := refImageNames(entries)
	for len(names) > MaxAIRefsPerItem {
		_ = os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
}

// refImageNames returns image filenames sorted oldest-first (names are
// nanosecond timestamps, so lexical order is chronological).
func refImageNames(entries []os.DirEntry) []string {
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".jpg") || strings.HasSuffix(e.Name(), ".png") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}
