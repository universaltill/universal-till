package imaging

import (
	"bytes"
	"image/jpeg"
	"testing"
)

// RefJPEG is the camera-identify reference-photo encoding shared by the
// built-in identify and the item_image_open host function (ADR-0121 R1,
// ut-docs#4005): bounded decode, ≤ RefMaxEdge long edge, JPEG quality 70.
func TestRefJPEG_DownscalesToJPEG(t *testing.T) {
	for _, in := range [][]byte{encodePNG(t, 512, 384), encodeJPEG(t, 300, 600)} {
		out, err := RefJPEG(in)
		if err != nil {
			t.Fatalf("RefJPEG: %v", err)
		}
		img, err := jpeg.Decode(bytes.NewReader(out))
		if err != nil {
			t.Fatalf("output is not JPEG: %v", err)
		}
		b := img.Bounds()
		if max(b.Dx(), b.Dy()) != RefMaxEdge {
			t.Fatalf("long edge = %dx%d, want %d", b.Dx(), b.Dy(), RefMaxEdge)
		}
	}
}

func TestRefJPEG_SmallImageKeepsSize(t *testing.T) {
	out, err := RefJPEG(encodePNG(t, 40, 20))
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil || img.Bounds().Dx() != 40 || img.Bounds().Dy() != 20 {
		t.Fatalf("small image changed size or failed: %v %v", img.Bounds(), err)
	}
}

func TestRefJPEG_RejectsUndecodable(t *testing.T) {
	for name, in := range map[string][]byte{
		"garbage": []byte("not an image"),
		"gif":     encodeGIF(t, 8, 8),
		"empty":   nil,
	} {
		if _, err := RefJPEG(in); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
}

func TestRefJPEG_RejectsPixelBomb(t *testing.T) {
	if _, err := RefJPEG(encodePNG(t, 3000, 3000)); err == nil {
		t.Fatal("expected an over-MaxPixels image to be refused")
	}
}
