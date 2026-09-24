package imaging

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
)

// PrepareThumb is the decode half of every uploaded-thumbnail write (item
// photo, variant photo, category image — ut-docs#2500 extracted it from the
// catalog handlers rather than adding a third copy): Decode (png/jpeg only,
// pixel-count bounded) then DownscaleMaxEdge to MaxThumbEdge. Decode's
// error comes back unchanged, so a caller still tells a too-large photo
// (errors.Is(err, ErrTooManyPixels)) from an undecodable one. Split from
// WriteThumbPNG so a caller can validate an upload BEFORE it writes
// anything else (the category dialog must not create a row and then
// refuse its photo).
func PrepareThumb(raw []byte) (image.Image, error) {
	img, err := Decode(raw)
	if err != nil {
		return nil, err
	}
	return DownscaleMaxEdge(img, MaxThumbEdge), nil
}

// WriteThumbPNG encodes img as PNG at dst, creating dst's directory first.
// It writes a temp file in the same directory and renames it over dst, so
// a sell screen fetching the old thumbnail mid-upload never reads a
// half-written file. Every error here is a server-side failure (disk), not
// a bad upload.
func WriteThumbPNG(img image.Image, dst string) error {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("thumb dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".thumb-*.png")
	if err != nil {
		return fmt.Errorf("thumb temp: %w", err)
	}
	tmpName := tmp.Name()
	if err := png.Encode(tmp, img); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("thumb encode: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("thumb close: %w", err)
	}
	// CreateTemp makes the file 0600; a thumbnail is a public asset served
	// by /public/, same mode os.Create gave it before this helper existed.
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("thumb chmod: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("thumb rename: %w", err)
	}
	return nil
}
