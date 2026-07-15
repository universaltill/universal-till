package print

import (
	"bytes"
	"image"
	_ "image/gif"  // logo decode
	_ "image/jpeg" // logo decode
	_ "image/png"  // logo decode
)

// Receipt logo (docs: receipt-designer.md follow-up): the owner's PNG is
// rendered at the top of every thermal document via ESC/POS raster mode
// GS v 0 — universally supported, no printer-side logo registration.

const (
	// rasterMaxWidth is the printable dot width of a standard 80mm head.
	rasterMaxWidth = 576
	// rasterMaxHeight keeps a mis-sized upload from feeding paper forever.
	rasterMaxHeight = 240
	// rasterThreshold: luma below this prints black (0–255).
	rasterThreshold = 160
)

// RasterLogo converts an uploaded image to a GS v 0 block, downscaling to
// the printable width and thresholding to 1-bit. Returns nil when the
// image can't be decoded — a receipt without a logo beats no receipt.
func RasterLogo(imgBytes []byte) []byte {
	img, _, err := image.Decode(bytes.NewReader(imgBytes))
	if err != nil {
		return nil
	}
	bounds := img.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()
	if srcW == 0 || srcH == 0 {
		return nil
	}

	w, h := srcW, srcH
	if w > rasterMaxWidth {
		h = h * rasterMaxWidth / w
		w = rasterMaxWidth
	}
	if h > rasterMaxHeight {
		w = w * rasterMaxHeight / h
		h = rasterMaxHeight
	}
	if w == 0 || h == 0 {
		return nil
	}

	widthBytes := (w + 7) / 8
	data := make([]byte, widthBytes*h)
	for y := range h {
		for x := range w {
			// Nearest-neighbour sample; logos are flat art, not photos.
			sx := bounds.Min.X + x*srcW/w
			sy := bounds.Min.Y + y*srcH/h
			r, g, bl, a := img.At(sx, sy).RGBA()
			// Transparent = paper (white).
			if a < 0x8000 {
				continue
			}
			// Rec. 601 luma on the 16-bit channels.
			luma := (299*r + 587*g + 114*bl) / 1000 >> 8
			if luma < rasterThreshold {
				data[y*widthBytes+x/8] |= 0x80 >> (x % 8)
			}
		}
	}

	var out bytes.Buffer
	// GS v 0 m xL xH yL yH data — m=0 normal density.
	out.Write([]byte{0x1d, 0x76, 0x30, 0x00,
		byte(widthBytes & 0xff), byte(widthBytes >> 8),
		byte(h & 0xff), byte(h >> 8)})
	out.Write(data)
	return out.Bytes()
}
