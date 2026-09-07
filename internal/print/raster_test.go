package print

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"testing"

	"github.com/universaltill/universal-till/internal/imaging"
)

// pngBytes renders a test image: left half black, right half white.
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := color.RGBA{255, 255, 255, 255}
			if x < w/2 {
				c = color.RGBA{0, 0, 0, 255}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

func TestRasterLogoEncodesGSv0(t *testing.T) {
	raw := pngBytes(t, 16, 8)
	out := RasterLogo(raw)
	if out == nil {
		t.Fatal("no raster produced")
	}
	// GS v 0 m=0 header.
	if !bytes.HasPrefix(out, []byte{0x1d, 0x76, 0x30, 0x00}) {
		t.Fatalf("missing GS v 0 header: % x", out[:8])
	}
	// 16px wide = 2 bytes/row; 8 rows.
	if out[4] != 2 || out[5] != 0 || out[6] != 8 || out[7] != 0 {
		t.Fatalf("dimensions wrong: xL=%d xH=%d yL=%d yH=%d", out[4], out[5], out[6], out[7])
	}
	data := out[8:]
	if len(data) != 2*8 {
		t.Fatalf("data length %d, want 16", len(data))
	}
	// Left byte fully black (0xff), right byte fully white (0x00).
	for row := range 8 {
		if data[row*2] != 0xff || data[row*2+1] != 0x00 {
			t.Fatalf("row %d wrong: % x", row, data[row*2:row*2+2])
		}
	}
}

func TestRasterLogoDownscalesAndRejectsGarbage(t *testing.T) {
	out := RasterLogo(pngBytes(t, 2000, 100))
	if out == nil {
		t.Fatal("oversize image should downscale, not fail")
	}
	widthBytes := int(out[4]) | int(out[5])<<8
	if widthBytes*8 > rasterMaxWidth+7 {
		t.Fatalf("not downscaled: %d dots", widthBytes*8)
	}
	if RasterLogo([]byte("not an image")) != nil {
		t.Fatal("garbage must yield nil")
	}
}

// gifBytes renders a tiny, real, valid GIF — receipt logos are an
// intentional GIF-accepting upload path (web/ui/pages/receipt_designer.html
// advertises image/gif), and ut-docs#1417's fix must not silently break it.
func gifBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, w, h), color.Palette{color.Black, color.White})
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode gif: %v", err)
	}
	return buf.Bytes()
}

// oversizedPNGBytes is a real, fully valid, fully decodable PNG whose pixel
// count sits just over imaging.MaxPixels — the actual pixel-bomb shape
// (ut-docs#1328/#1417): a solid color compresses to a tiny file while
// still being genuinely decodable, proving the guard rejects it via the
// cheap dimension check rather than some unrelated "corrupt file" reason.
func oversizedPNGBytes(t *testing.T) []byte {
	t.Helper()
	const w, h = 7000, 6000
	if int64(w)*int64(h) <= imaging.MaxPixels {
		t.Fatalf("test fixture %dx%d must exceed imaging.MaxPixels (%d)", w, h, imaging.MaxPixels)
	}
	img := image.NewGray(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode oversized test png: %v", err)
	}
	return buf.Bytes()
}

// TestRasterLogoRejectsPixelBomb is the ut-docs#1417 regression: before
// this fix, RasterLogo's raw image.Decode would attempt to allocate a full
// decode buffer for whatever dimensions a crafted/oversized upload
// declared, regardless of how small the file was on disk. It must now be
// rejected the same way a genuinely corrupt file already is (nil, no
// panic, no multi-hundred-MB allocation).
func TestRasterLogoRejectsPixelBomb(t *testing.T) {
	if out := RasterLogo(oversizedPNGBytes(t)); out != nil {
		t.Fatal("expected a pixel-bomb-sized PNG to be rejected (nil), got a raster")
	}
}

// TestRasterLogoAcceptsGIF is the compatibility guard for the fix above:
// bounding RasterLogo's decode must not narrow it to imaging.DefaultFormats
// (png/jpeg only) and silently break the GIF logo upload path.
func TestRasterLogoAcceptsGIF(t *testing.T) {
	out := RasterLogo(gifBytes(t, 16, 8))
	if out == nil {
		t.Fatal("expected a valid small GIF to still raster, got nil")
	}
	if !bytes.HasPrefix(out, []byte{0x1d, 0x76, 0x30, 0x00}) {
		t.Fatalf("missing GS v 0 header: % x", out[:8])
	}
}

func TestRenderIncludesLogo(t *testing.T) {
	logo := RasterLogo(pngBytes(t, 16, 8))
	doc := Doc{StoreName: "Shop", Logo: logo, Totals: []KV{{Label: "TOTAL", Amount: "£1.00"}}}
	out := Render(doc)
	if !bytes.Contains(out, logo) {
		t.Fatal("rendered document does not contain the logo raster")
	}
	if Render(Doc{StoreName: "Shop"}) == nil {
		t.Fatal("logoless render broke")
	}
}
