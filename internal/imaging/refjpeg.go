package imaging

import (
	"bytes"
	"image/jpeg"
)

// RefMaxEdge bounds the longer edge of a camera-identify reference photo.
// Full-size thumbs would put megabytes of base64 on every identify request
// for no recognition benefit.
const RefMaxEdge = 160

// refJPEGQuality is the reference photo's JPEG quality.
const refJPEGQuality = 70

// RefJPEG re-encodes an item photo as a camera-identify reference image: the
// bounded Decode (png/jpeg only, pixel-count capped — ut-docs#1417), then
// DownscaleMaxEdge to RefMaxEdge, then JPEG at quality 70. The built-in
// identify (internal/pages) and the item_image_open host function
// (internal/plugins, ADR-0121 R1, ut-docs#4005) both call this, so a plugin
// gets byte-for-byte what the built-in sends.
//
// The bounded decode matters: reference photos are re-read on every
// identify call, so an unbounded decode would turn one hostile file already
// on disk into a repeatable OOM.
func RefJPEG(raw []byte) ([]byte, error) {
	src, err := Decode(raw)
	if err != nil {
		return nil, err
	}
	src = DownscaleMaxEdge(src, RefMaxEdge)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: refJPEGQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
