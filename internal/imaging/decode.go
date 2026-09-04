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
	"sort"
	"strings"
)

// defaultFormats is every format this package's Decode/DecodeBounded
// callers actually mean to accept — PNG and JPEG uploads/imports.
// image.Decode dispatches to ANY format registered anywhere in the built
// binary (independent review, ut-docs#1328): this repo's internal/print
// blank-imports image/gif for an unrelated reason, which — before this
// allowlist — silently made GIF a 5th accepted format at every call site
// here, with its own dimension limits never audited against MaxPixels
// below. Checking the format string image.DecodeConfig/image.Decode
// already return is a one-line, standing guard against that ever
// recurring (e.g. a future package blank-importing a WEBP/TIFF decoder
// for some unrelated reason).
//
// Unexported deliberately (ut-docs#1417 review): an exported mutable map
// would let any package widen every Decode/DecodeBounded caller's
// allowlist with a single `imaging.DefaultFormats["x"] = true`, exactly
// the silent-widening class this allowlist exists to prevent. Use the
// DefaultFormats() function below to get a fresh, safely-mutable copy —
// a caller with a genuine need for a different format set (e.g.
// internal/print's RasterLogo, which must keep accepting GIF receipt
// logos) builds its own set from that rather than mutating this one.
var defaultFormats = map[string]bool{"png": true, "jpeg": true}

// DefaultFormats returns a fresh copy of the standard png/jpeg-only
// format allowlist Decode/DecodeBounded use. Safe for a caller to build
// on (e.g. widen its own copy for a DecodeBoundedFormats call) — mutating
// the returned map never affects this package's own default.
func DefaultFormats() map[string]bool {
	cp := make(map[string]bool, len(defaultFormats))
	for k, v := range defaultFormats {
		cp[k] = v
	}
	return cp
}

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

// DecodeBounded decodes raw image bytes against the standard png/jpeg-only
// allowlist, bounded by maxPixels. See DecodeBoundedFormats for the
// mechanism.
//
// maxPixels is a parameter rather than always using MaxPixels so a test
// can exercise the guard with a real (small) image instead of allocating
// one at the real threshold.
func DecodeBounded(raw []byte, maxPixels int64) (image.Image, error) {
	img, _, err := DecodeBoundedFormats(raw, maxPixels, defaultFormats)
	return img, err
}

// DecodeBoundedFormats decodes raw image bytes, first reading only the
// declared dimensions (image.DecodeConfig, which does not allocate a pixel
// buffer) and rejecting anything over maxPixels — or not present in
// formats — before the full image.Decode. Also returns the decoded format
// name (as image.Decode does), for a caller that needs it (e.g. to pick a
// file extension or media type string).
//
// Byte-size limits at each call site (io.LimitReader on an upload, a
// LimitReader on a fetch, …) already bound the COMPRESSED input; this
// bounds the DECODED output, which a crafted or corrupt file can inflate
// far beyond what its compressed size implies.
//
// formats is a parameter, not always DefaultFormats, because a call site
// can have a genuine, narrower-or-wider need — RasterLogo (ut-docs#1417)
// must keep accepting GIF receipt logos, which DefaultFormats deliberately
// excludes everywhere else.
func DecodeBoundedFormats(raw []byte, maxPixels int64, formats map[string]bool) (image.Image, string, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, "", err
	}
	if !formats[format] {
		return nil, "", fmt.Errorf("unsupported image format %q (accepted: %s)", format, formatNames(formats))
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, "", fmt.Errorf("invalid image dimensions %dx%d", cfg.Width, cfg.Height)
	}
	if pixels := int64(cfg.Width) * int64(cfg.Height); pixels > maxPixels {
		return nil, "", fmt.Errorf("image dimensions %dx%d (%d px) exceed the %d px limit", cfg.Width, cfg.Height, pixels, maxPixels)
	}
	img, decodedFormat, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, "", err
	}
	// Defense in depth: DecodeConfig and Decode independently parse the
	// same bytes. Nothing in the stdlib's PNG/JPEG/GIF decoders lets them
	// disagree (independent review verified this for PNG/JPEG, ut-docs#1328),
	// but re-checking the format costs nothing and removes the assumption
	// entirely.
	if !formats[decodedFormat] {
		return nil, "", fmt.Errorf("unsupported image format %q (accepted: %s)", decodedFormat, formatNames(formats))
	}
	return img, decodedFormat, nil
}

// formatNames renders a formats set as a stable, sorted, comma-joined
// list for an error message (e.g. "jpeg, png") — map iteration order is
// randomized, and an error message that reads differently each time it's
// hit is a debugging annoyance.
func formatNames(formats map[string]bool) string {
	names := make([]string, 0, len(formats))
	for name, ok := range formats {
		if ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
