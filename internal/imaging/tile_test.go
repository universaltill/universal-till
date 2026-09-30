package imaging

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"testing"
)

// ut-docs#3125 (design: ut-docs architecture/ai-product-photo.md §5).

// subjectImage returns a transparent w×h canvas with an opaque red
// rectangle filling r.
func subjectImage(w, h int, r image.Rectangle) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, r, image.NewUniform(color.NRGBA{200, 30, 30, 255}), image.Point{}, draw.Src)
	return img
}

// alphaBBox is the bounding box of pixels with 8-bit alpha >= min.
func alphaBBox(img image.Image, min uint32) image.Rectangle {
	b := img.Bounds()
	out := image.Rectangle{}
	first := true
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			if a>>8 < min {
				continue
			}
			p := image.Rect(x, y, x+1, y+1)
			if first {
				out, first = p, false
			} else {
				out = out.Union(p)
			}
		}
	}
	return out
}

func absInt(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

func TestSquareTile_CentresOffCentreSubject(t *testing.T) {
	src := subjectImage(200, 200, image.Rect(20, 30, 80, 110)) // 60x80, top-left
	out, err := SquareTile(src, nil, TileSize)
	if err != nil {
		t.Fatalf("SquareTile: %v", err)
	}
	bb := alphaBBox(out, 128)
	left, right := bb.Min.X, TileSize-bb.Max.X
	top, bottom := bb.Min.Y, TileSize-bb.Max.Y
	if absInt(left-right) > 1 || absInt(top-bottom) > 1 {
		t.Fatalf("not centred: margins l=%d r=%d t=%d b=%d", left, right, top, bottom)
	}
	if long := max(bb.Dx(), bb.Dy()); absInt(long-430) > 1 {
		t.Fatalf("longer edge = %d, want ~430", long)
	}
}

func TestSquareTile_KeepsAspectAndTargetEdge(t *testing.T) {
	cases := []struct {
		name string
		w, h int // subject size
		size int
	}{
		{"tall", 40, 160, 512},
		{"wide", 160, 40, 512},
		{"square", 50, 50, 512},
		{"small tile", 30, 90, 100},
		{"upscale", 10, 20, 512},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := subjectImage(300, 300, image.Rect(100, 100, 100+tc.w, 100+tc.h))
			out, err := SquareTile(src, nil, tc.size)
			if err != nil {
				t.Fatalf("SquareTile: %v", err)
			}
			bb := alphaBBox(out, 128)
			target := int(math.Round(float64(tc.size) * 0.84))
			long, short := max(bb.Dx(), bb.Dy()), min(bb.Dx(), bb.Dy())
			if absInt(long-target) > 1 {
				t.Fatalf("longer edge = %d, want %d", long, target)
			}
			wantShort := float64(min(tc.w, tc.h)) * float64(target) / float64(max(tc.w, tc.h))
			if math.Abs(float64(short)-wantShort) > 1.5 {
				t.Fatalf("shorter edge = %d, want ~%.1f (aspect kept)", short, wantShort)
			}
			if (tc.w > tc.h) != (bb.Dx() > bb.Dy()) && tc.w != tc.h {
				t.Fatalf("orientation flipped: bbox %v", bb)
			}
		})
	}
}

func TestSquareTile_NoSubject(t *testing.T) {
	faint := image.NewNRGBA(image.Rect(0, 0, 20, 20))
	draw.Draw(faint, faint.Bounds(), image.NewUniform(color.NRGBA{255, 0, 0, 15}), image.Point{}, draw.Src)
	cases := map[string]image.Image{
		"transparent": image.NewNRGBA(image.Rect(0, 0, 20, 20)),
		"alpha<16":    faint,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := SquareTile(src, color.White, TileSize); !errors.Is(err, ErrNoSubject) {
				t.Fatalf("err = %v, want ErrNoSubject", err)
			}
		})
	}
}

func TestSquareTile_AlphaBoundary(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 20, 20))
	src.SetNRGBA(5, 5, color.NRGBA{255, 0, 0, 16})
	src.SetNRGBA(15, 15, color.NRGBA{255, 0, 0, 15}) // below threshold, must be ignored
	out, err := SquareTile(src, nil, 64)
	if err != nil {
		t.Fatalf("alpha 16 must count as subject: %v", err)
	}
	if out.Bounds() != image.Rect(0, 0, 64, 64) {
		t.Fatalf("bounds = %v", out.Bounds())
	}
	// The alpha-15 pixel is outside the crop: a 1x1 subject scaled up fills
	// the target square, so the output has a visible centre.
	if _, _, _, a := out.At(32, 32).RGBA(); a == 0 {
		t.Fatal("centre pixel transparent, subject not drawn")
	}
}

func TestSquareTile_OpaqueInputSquaredWhole(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 200, 100))
	draw.Draw(src, src.Bounds(), image.NewUniform(color.RGBA{10, 120, 200, 255}), image.Point{}, draw.Src)
	out, err := SquareTile(src, nil, TileSize)
	if err != nil {
		t.Fatalf("SquareTile: %v", err)
	}
	if out.Bounds() != image.Rect(0, 0, TileSize, TileSize) {
		t.Fatalf("bounds = %v", out.Bounds())
	}
	bb := alphaBBox(out, 128)
	if absInt(bb.Dx()-430) > 1 || absInt(bb.Dy()-215) > 1 {
		t.Fatalf("content bbox = %v (%dx%d), want ~430x215", bb, bb.Dx(), bb.Dy())
	}
}

func TestSquareTile_SizeBounds(t *testing.T) {
	src := subjectImage(10, 10, image.Rect(2, 2, 8, 8))
	for _, size := range []int{0, -1, MaxThumbEdge + 1} {
		if _, err := SquareTile(src, nil, size); !errors.Is(err, ErrBadTileSize) {
			t.Fatalf("size %d: err = %v, want ErrBadTileSize", size, err)
		}
	}
	for _, size := range []int{1, MaxThumbEdge} {
		out, err := SquareTile(src, nil, size)
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if out.Bounds() != image.Rect(0, 0, size, size) {
			t.Fatalf("size %d: bounds = %v", size, out.Bounds())
		}
	}
}

func TestSquareTile_BackgroundFillsMargin(t *testing.T) {
	src := subjectImage(100, 100, image.Rect(40, 40, 60, 60))
	palette := color.RGBA{R: 30, G: 144, B: 255, A: 255}
	cases := []struct {
		name string
		bg   color.Color
		want color.RGBA
	}{
		{"white", color.White, color.RGBA{255, 255, 255, 255}},
		{"nil is transparent", nil, color.RGBA{}},
		{"palette", palette, palette},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := SquareTile(src, tc.bg, 128)
			if err != nil {
				t.Fatalf("SquareTile: %v", err)
			}
			for _, p := range []image.Point{{0, 0}, {127, 0}, {0, 127}, {127, 127}} {
				if got := color.RGBAModel.Convert(out.At(p.X, p.Y)); got != tc.want {
					t.Fatalf("corner %v = %v, want %v", p, got, tc.want)
				}
			}
		})
	}
}

func TestSquareTile_CompositesOverBackground(t *testing.T) {
	src := subjectImage(100, 100, image.Rect(40, 40, 60, 60))
	out, err := SquareTile(src, color.White, 128)
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, a := out.At(64, 64).RGBA()
	if a>>8 != 255 || r>>8 < 190 || g>>8 > 40 || b>>8 > 40 {
		t.Fatalf("centre = %d,%d,%d,%d, want opaque subject red", r>>8, g>>8, b>>8, a>>8)
	}
}

func TestSquareTile_NonZeroOriginBounds(t *testing.T) {
	full := subjectImage(200, 200, image.Rect(120, 130, 160, 190))
	sub := full.SubImage(image.Rect(100, 100, 200, 200))
	if sub.Bounds().Min == (image.Point{}) {
		t.Fatal("test setup: SubImage should not start at origin")
	}
	out, err := SquareTile(sub, nil, TileSize)
	if err != nil {
		t.Fatalf("SquareTile: %v", err)
	}
	bb := alphaBBox(out, 128)
	if absInt(bb.Min.X-(TileSize-bb.Max.X)) > 1 || absInt(bb.Min.Y-(TileSize-bb.Max.Y)) > 1 {
		t.Fatalf("not centred: %v", bb)
	}
	if absInt(max(bb.Dx(), bb.Dy())-430) > 1 {
		t.Fatalf("longer edge = %d, want ~430", max(bb.Dx(), bb.Dy()))
	}
}

func TestSquareTile_OutputEncodesAsPNG(t *testing.T) {
	src := subjectImage(50, 50, image.Rect(10, 10, 40, 30))
	out, err := SquareTile(src, nil, TileSize)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	if _, err := png.Decode(&buf); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
}

// A semi-transparent subject pixel (a background remover's soft edge) must
// be blended over bg, not replace it — draw.Src would leave a see-through
// halo on a white or palette tile.
func TestSquareTile_BlendsSoftEdgeOverBackground(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 20, 20))
	draw.Draw(src, src.Bounds(), image.NewUniform(color.NRGBA{255, 0, 0, 128}), image.Point{}, draw.Src)
	out, err := SquareTile(src, color.White, 128)
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, a := out.At(64, 64).RGBA()
	if a>>8 != 255 {
		t.Fatalf("centre alpha = %d, want 255 (composited over opaque white)", a>>8)
	}
	if r>>8 < 250 || g>>8 < 110 || g>>8 > 145 || b>>8 < 110 || b>>8 > 145 {
		t.Fatalf("centre = %d,%d,%d, want red blended ~50%% over white", r>>8, g>>8, b>>8)
	}
}

// A subject lying entirely at negative coordinates: the bbox scan must start
// at src.Bounds().Min, and its "empty" sentinels below it.
func TestSquareTile_NegativeOriginBounds(t *testing.T) {
	src := image.NewNRGBA(image.Rect(-50, -40, 10, 20))
	draw.Draw(src, image.Rect(-45, -35, -15, -5), image.NewUniform(color.NRGBA{200, 30, 30, 255}), image.Point{}, draw.Src)
	out, err := SquareTile(src, nil, TileSize)
	if err != nil {
		t.Fatalf("SquareTile: %v", err)
	}
	bb := alphaBBox(out, 128)
	if absInt(bb.Min.X-(TileSize-bb.Max.X)) > 1 || absInt(bb.Min.Y-(TileSize-bb.Max.Y)) > 1 {
		t.Fatalf("not centred: %v", bb)
	}
	if absInt(bb.Dx()-430) > 1 || absInt(bb.Dy()-430) > 1 {
		t.Fatalf("subject = %dx%d, want ~430x430", bb.Dx(), bb.Dy())
	}
}

// Exact edges where rounding and truncation differ: 128*0.84 = 107.52 → 108;
// 40*108/60 = 72.
func TestSquareTile_RoundsTargetEdge(t *testing.T) {
	src := subjectImage(100, 100, image.Rect(10, 10, 70, 50)) // 60x40
	out, err := SquareTile(src, nil, 128)
	if err != nil {
		t.Fatal(err)
	}
	if bb := alphaBBox(out, 1); bb.Dx() != 108 || bb.Dy() != 72 {
		t.Fatalf("subject = %dx%d, want exactly 108x72", bb.Dx(), bb.Dy())
	}
}

// A 1-px-wide subject still gets a 1-px column, not a zero-width draw that
// leaves a background-only tile.
func TestSquareTile_ThinSubjectKeepsOnePixel(t *testing.T) {
	src := subjectImage(10, 1000, image.Rect(5, 0, 6, 1000))
	out, err := SquareTile(src, nil, 64)
	if err != nil {
		t.Fatal(err)
	}
	if bb := alphaBBox(out, 1); bb.Dx() != 1 || bb.Dy() != 54 {
		t.Fatalf("subject = %v (%dx%d), want 1x54", bb, bb.Dx(), bb.Dy())
	}
}
