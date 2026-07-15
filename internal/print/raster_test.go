package print

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
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
