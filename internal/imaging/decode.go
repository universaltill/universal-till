// Package imaging provides a decode helper shared by every call site that
// turns an untrusted uploaded/imported/downloaded image into a re-encoded
// thumbnail (ut-docs#1328).
package imaging

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg" // register the JPEG decoder
	_ "image/png"  // register the PNG decoder
)

// MaxPixels bounds a decoded image's total pixel count (width×height). A
// small, well-formed file can declare an enormous width×height — a classic
// "pixel bomb" — and image.Decode allocates a buffer for the DECODED
// dimensions regardless of how small the compressed source was, on the same
// low-memory Android/Pi hardware this product targets. 40M pixels is
// generous headroom over any real product photo (a 6000×6000 source is 36M)
// while still bounding worst-case decode memory to a low, fixed multiple of
// what a normal photo needs.
const MaxPixels = 40_000_000

// Decode decodes raw image bytes, bounded by the package's standard
// MaxPixels limit. This is the entry point every call site should use.
func Decode(raw []byte) (image.Image, error) {
	return DecodeBounded(raw, MaxPixels)
}

// DecodeBounded decodes raw image bytes, first reading only the declared
// dimensions (image.DecodeConfig, which does not allocate a pixel buffer)
// and rejecting anything over maxPixels before the full image.Decode.
//
// Byte-size limits at each call site (io.LimitReader on an upload, a
// LimitReader on a fetch, …) already bound the COMPRESSED input; this
// bounds the DECODED output, which a crafted or corrupt file can inflate
// far beyond what its compressed size implies. maxPixels is a parameter
// rather than always using MaxPixels so a test can exercise the guard with
// a real (small) image instead of allocating one at the real threshold.
func DecodeBounded(raw []byte, maxPixels int64) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, fmt.Errorf("invalid image dimensions %dx%d", cfg.Width, cfg.Height)
	}
	if pixels := int64(cfg.Width) * int64(cfg.Height); pixels > maxPixels {
		return nil, fmt.Errorf("image dimensions %dx%d (%d px) exceed the %d px limit", cfg.Width, cfg.Height, pixels, maxPixels)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	return img, err
}
