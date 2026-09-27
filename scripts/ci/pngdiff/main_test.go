package main

import (
	"image"
	"image/color"
	"testing"
)

func solid(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{255, 255, 255, 255})
		}
	}
	return img
}

func TestCompareIdentical(t *testing.T) {
	d := Compare(solid(20, 20), solid(20, 20), 8)
	if d.Pixels != 0 || !d.Box.Empty() || len(d.Bands) != 0 {
		t.Fatalf("identical images: got %+v", d)
	}
}

// Two changed regions far apart vertically are reported as separate bands,
// each with its own x-extent, inside one overall box.
func TestCompareBandsSeparateRegions(t *testing.T) {
	a, b := solid(100, 100), solid(100, 100)
	b.Set(10, 5, color.RGBA{0, 0, 0, 255})
	b.Set(12, 7, color.RGBA{0, 0, 0, 255})
	b.Set(80, 90, color.RGBA{250, 255, 255, 255})
	d := Compare(a, b, 8)
	if d.Pixels != 3 {
		t.Fatalf("pixels = %d, want 3", d.Pixels)
	}
	if want := image.Rect(10, 5, 81, 91); d.Box != want {
		t.Fatalf("box = %v, want %v", d.Box, want)
	}
	if len(d.Bands) != 2 {
		t.Fatalf("bands = %v, want 2", d.Bands)
	}
	if want := image.Rect(10, 5, 13, 8); d.Bands[0] != want {
		t.Fatalf("band 0 = %v, want %v", d.Bands[0], want)
	}
	if want := image.Rect(80, 90, 81, 91); d.Bands[1] != want {
		t.Fatalf("band 1 = %v, want %v", d.Bands[1], want)
	}
	if d.MaxDelta>>8 != 255 {
		t.Fatalf("max delta = %d, want 255", d.MaxDelta>>8)
	}
}

func TestCompareSizeMismatch(t *testing.T) {
	d := Compare(solid(10, 10), solid(10, 12), 8)
	if d.SizeA == d.SizeB {
		t.Fatalf("sizes should differ: %+v", d)
	}
	if d.Pixels != 0 {
		t.Fatalf("overlap is identical, got %d pixels", d.Pixels)
	}
}
