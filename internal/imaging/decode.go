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

// allowedFormats is every format this package's callers actually mean to
// accept — PNG and JPEG uploads/imports. image.Decode dispatches to ANY
// format registered anywhere in the built binary (independent review,
// ut-docs#1328): this repo's internal/print blank-imports image/gif for an
// unrelated reason, which — before this allowlist — silently made GIF a
// 5th accepted format at every call site here, with its own dimension
// limits never audited against MaxPixels below. Checking the format string
// image.DecodeConfig/image.Decode already return is a one-line, standing
// guard against that ever recurring (e.g. a future package blank-importing
// a WEBP/TIFF decoder for some unrelated reason).
var allowedFormats = map[string]bool{"png": true, "jpeg": true}

// MaxPixels bounds a decoded image's total pixel count (width×height). A
// small, well-formed file can declare an enormous width×height — a classic
// "pixel bomb" — and image.Decode allocates a buffer for the DECODED
// dimensions regardless of how small the compressed source was, on the same
// low-memory Android/Pi hardware this product targets.
//
// The cap is sized from the WORST CASE format's memory cost, not a naive
// 4-bytes-per-pixel RGBA assumption (an earlier version of this constant
// got that wrong — independent review, ut-docs#1328): a progressive JPEG's
// decoder holds per-component coefficient blocks alongside the YCbCr
// output plane, measured at ~15 bytes of decode memory per pixel on this
// Go toolchain (vs. ~4-8 B/px for a flat PNG). Targeting roughly a 100MB
// ceiling for a single decode — generous on any device with >=1GB RAM,
// while still safe if 2 uploads land concurrently — gives
// 100_000_000 / 15 ≈ 6.6M pixels; rounded down for margin. 6M pixels is a
// ~2828×2121 image, comfortably above what this product's own catalog
// photos need (thumbnails), though a modern phone's default (often
// 12MP+) can exceed it — see ut-docs#1328's follow-up card for downscaling
// an accepted-but-large photo instead of rejecting it outright.
const MaxPixels = 6_000_000

// Decode decodes raw image bytes, bounded by the package's standard
// MaxPixels limit. This is the entry point every call site should use.
func Decode(raw []byte) (image.Image, error) {
	return DecodeBounded(raw, MaxPixels)
}

// DecodeBounded decodes raw image bytes, first reading only the declared
// dimensions (image.DecodeConfig, which does not allocate a pixel buffer)
// and rejecting anything over maxPixels — or not PNG/JPEG — before the
// full image.Decode.
//
// Byte-size limits at each call site (io.LimitReader on an upload, a
// LimitReader on a fetch, …) already bound the COMPRESSED input; this
// bounds the DECODED output, which a crafted or corrupt file can inflate
// far beyond what its compressed size implies. maxPixels is a parameter
// rather than always using MaxPixels so a test can exercise the guard with
// a real (small) image instead of allocating one at the real threshold.
func DecodeBounded(raw []byte, maxPixels int64) (image.Image, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if !allowedFormats[format] {
		return nil, fmt.Errorf("unsupported image format %q (only png/jpeg accepted)", format)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, fmt.Errorf("invalid image dimensions %dx%d", cfg.Width, cfg.Height)
	}
	if pixels := int64(cfg.Width) * int64(cfg.Height); pixels > maxPixels {
		return nil, fmt.Errorf("image dimensions %dx%d (%d px) exceed the %d px limit", cfg.Width, cfg.Height, pixels, maxPixels)
	}
	img, decodedFormat, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	// Defense in depth: DecodeConfig and Decode independently parse the
	// same bytes. Nothing in the stdlib's PNG/JPEG decoders lets them
	// disagree (independent review verified this), but re-checking the
	// format costs nothing and removes the assumption entirely.
	if !allowedFormats[decodedFormat] {
		return nil, fmt.Errorf("unsupported image format %q (only png/jpeg accepted)", decodedFormat)
	}
	return img, nil
}
