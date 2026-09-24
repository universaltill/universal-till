package imaging

import (
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// ut-docs#2500: the item photo, variant photo and category image uploads
// share ONE decode → downscale → PNG-to-disk path instead of three copies.

func TestPrepareThumb_DecodesAndDownscales(t *testing.T) {
	img, err := PrepareThumb(encodePNG(t, MaxThumbEdge+400, 100))
	if err != nil {
		t.Fatalf("PrepareThumb: %v", err)
	}
	if got := img.Bounds().Dx(); got != MaxThumbEdge {
		t.Fatalf("longer edge = %d, want %d (downscaled)", got, MaxThumbEdge)
	}
}

func TestPrepareThumb_RejectsGarbageAndPixelBomb(t *testing.T) {
	if _, err := PrepareThumb([]byte("not an image")); err == nil || errors.Is(err, ErrTooManyPixels) {
		t.Fatalf("garbage: err=%v, want a plain decode error", err)
	}
	if _, err := PrepareThumb(pixelBombPNG(t, 60000, 60000)); !errors.Is(err, ErrTooManyPixels) {
		t.Fatalf("pixel bomb: err=%v, want ErrTooManyPixels", err)
	}
}

func TestWriteThumbPNG_CreatesDirAndWritesPNG(t *testing.T) {
	img, err := PrepareThumb(encodePNG(t, 4, 3))
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "a", "b", "thumb.png")
	if err := WriteThumbPNG(img, dst); err != nil {
		t.Fatalf("WriteThumbPNG: %v", err)
	}
	f, err := os.Open(dst)
	if err != nil {
		t.Fatalf("open written thumb: %v", err)
	}
	defer f.Close()
	got, err := png.Decode(f)
	if err != nil {
		t.Fatalf("written file is not a PNG: %v", err)
	}
	if got.Bounds().Dx() != 4 || got.Bounds().Dy() != 3 {
		t.Fatalf("written size = %v, want 4x3", got.Bounds())
	}
	// Overwrite in place (a re-upload) leaves no temp file behind.
	if err := WriteThumbPNG(img, dst); err != nil {
		t.Fatalf("second WriteThumbPNG: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(dst))
	if len(entries) != 1 {
		t.Fatalf("dir holds %d entries after two writes, want just thumb.png", len(entries))
	}
}
