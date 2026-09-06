package imaging

import (
	"image"
	"math"

	"golang.org/x/image/draw"
)

// MaxThumbEdge bounds the longer edge of a catalog item/variant thumbnail
// written to disk (ut-docs#1416). Before this existed, every accepted photo
// — up to the full MaxPixels decode cap (a ~2828x2121 image) — was written
// and served at its native resolution: the admin Catalog table, the POS
// sale-screen grid/basket, the self-order kiosk and search suggestions all
// downloaded and decoded the full file just to shrink it for a small tile.
// 1600px is generous headroom above any tile/preview size this UI actually
// renders a thumbnail at (including a 2x-retina zoom of a fairly large
// panel), while still cutting a near-cap photo's linear dimensions by
// ~40%+ and its pixel count — so its served file size — by more than half.
const MaxThumbEdge = 1600

// DownscaleMaxEdge returns img unchanged if its longer edge already fits
// within maxEdge, otherwise a new image scaled down (aspect ratio
// preserved) so its longer edge equals maxEdge. Shared by every call site
// that writes a decoded image back out for display or re-transmission —
// internal/pages/ai_api.go's loadRefJPEG used to inline this same
// draw.ApproxBiLinear.Scale math for its own (much smaller, 160px)
// reference-image case; this extracts it once rather than a third copy
// appearing at the catalog thumbnail call sites (ut-docs#1416).
func DownscaleMaxEdge(img image.Image, maxEdge int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxEdge && h <= maxEdge {
		return img
	}
	// Set the longer edge to maxEdge EXACTLY, deriving only the shorter
	// edge from the scale factor — independent review (ut-docs#1416)
	// caught that computing both edges via the same float64 scale
	// (int(float64(w)*scale)) truncates the longer edge to maxEdge-1 for
	// a measured ~7.5% of real input dimensions (e.g. 1601x2156 → 1599,
	// not 1600), silently breaking this function's own documented
	// contract. math.Round (not truncation) on the shorter edge keeps
	// the aspect ratio as close as an integer result allows.
	var dstW, dstH int
	if w >= h {
		dstW = maxEdge
		dstH = max(int(math.Round(float64(h)*float64(maxEdge)/float64(w))), 1)
	} else {
		dstH = maxEdge
		dstW = max(int(math.Round(float64(w)*float64(maxEdge)/float64(h))), 1)
	}
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	return dst
}
