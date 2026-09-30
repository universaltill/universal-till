package imaging

import (
	"errors"
	"image"
	"image/color"
	"math"

	"golang.org/x/image/draw"
)

// TileSize is the edge, in pixels, of the square product tile callers ask
// SquareTile for (ut-docs#3125). The sale tile renders far smaller; 512
// stays under MaxThumbEdge, so the existing thumbnail pipeline stores it
// unchanged.
const TileSize = 512

// tileSubjectFraction is the share of the tile's edge the subject's longer
// side fills, leaving an 8% margin each side.
const tileSubjectFraction = 0.84

// tileMinAlpha is the lowest 8-bit alpha that counts as subject; fainter
// pixels are matting fringe/noise from a background remover.
const tileMinAlpha = 16

var (
	// ErrNoSubject means the image has no pixel with alpha >= 16, i.e. a
	// background remover found nothing to keep.
	ErrNoSubject = errors.New("no product found in the photo")
	// ErrBadTileSize means the requested tile edge is outside 1..MaxThumbEdge.
	ErrBadTileSize = errors.New("tile size out of range")
)

// SquareTile crops src to the bounding box of its subject (pixels with
// alpha >= 16), scales it, aspect kept, so the longer edge is size*0.84
// (an 8% margin each side), and centres it on a size x size canvas filled
// with bg, alpha-compositing the subject over it. A nil bg leaves the
// canvas transparent. An opaque image has no transparent margin to trim, so
// the whole image is squared. size must be in 1..MaxThumbEdge
// (ErrBadTileSize); an image with no subject returns ErrNoSubject.
// ut-docs#3125; design: architecture/ai-product-photo.md §5.
func SquareTile(src image.Image, bg color.Color, size int) (image.Image, error) {
	if size < 1 || size > MaxThumbEdge {
		return nil, ErrBadTileSize
	}
	crop, ok := subjectBounds(src)
	if !ok {
		return nil, ErrNoSubject
	}

	w, h := crop.Dx(), crop.Dy()
	target := max(int(math.Round(float64(size)*tileSubjectFraction)), 1)
	var dstW, dstH int
	if w >= h {
		dstW = target
		dstH = max(int(math.Round(float64(h)*float64(target)/float64(w))), 1)
	} else {
		dstH = target
		dstW = max(int(math.Round(float64(w)*float64(target)/float64(h))), 1)
	}

	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	if bg != nil {
		draw.Draw(dst, dst.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	}
	x0, y0 := (size-dstW)/2, (size-dstH)/2
	draw.CatmullRom.Scale(dst, image.Rect(x0, y0, x0+dstW, y0+dstH), src, crop, draw.Over, nil)
	return dst, nil
}

// subjectBounds returns the bounding box, in src's own coordinates, of the
// pixels whose 8-bit alpha is >= tileMinAlpha.
func subjectBounds(src image.Image) (image.Rectangle, bool) {
	b := src.Bounds()
	minX, minY, maxX, maxY := b.Max.X, b.Max.Y, b.Min.X-1, b.Min.Y-1
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := src.At(x, y).RGBA(); a>>8 < tileMinAlpha {
				continue
			}
			minX, maxX = min(minX, x), max(maxX, x)
			minY, maxY = min(minY, y), max(maxY, y)
		}
	}
	if maxX < minX {
		return image.Rectangle{}, false
	}
	return image.Rect(minX, minY, maxX+1, maxY+1), true
}
