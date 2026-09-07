package imaging

import (
	"image"
	"testing"
)

// TestDownscaleMaxEdge_NoOpWhenWithinBounds asserts an image already within
// maxEdge on both axes is returned as-is — the common case (most catalog
// photos are already small), so this must not do unnecessary work or
// introduce lossy re-sampling where none is needed.
func TestDownscaleMaxEdge_NoOpWhenWithinBounds(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 100, 80))
	got := DownscaleMaxEdge(src, 1600)
	if got.Bounds() != src.Bounds() {
		t.Fatalf("expected unchanged bounds %v, got %v", src.Bounds(), got.Bounds())
	}
}

// TestDownscaleMaxEdge_ScalesDownPreservingAspectRatio is ut-docs#1416's
// core requirement: an accepted-but-large photo must be shrunk to a fixed
// max edge rather than written and served at native resolution.
func TestDownscaleMaxEdge_ScalesDownPreservingAspectRatio(t *testing.T) {
	// 2800x2100 (4:3) is comfortably inside imaging.MaxPixels — a real
	// "accepted" photo, not a rejected one.
	src := image.NewRGBA(image.Rect(0, 0, 2800, 2100))
	got := DownscaleMaxEdge(src, 1600)
	b := got.Bounds()
	if b.Dx() != 1600 {
		t.Fatalf("longer edge = %d, want capped at 1600", b.Dx())
	}
	// 2800x2100 is exactly 4:3 — the shorter edge must scale by the same
	// factor (1600/2800), landing at 1200, or the photo is distorted.
	if want := 1200; b.Dy() != want {
		t.Fatalf("shorter edge = %d, want %d (aspect ratio not preserved)", b.Dy(), want)
	}
}

// TestDownscaleMaxEdge_TallImageCapsOnHeight covers the orientation ai_api.go's
// original inline version also had to handle: max(w,h) picks whichever edge
// is actually longer, not always width.
func TestDownscaleMaxEdge_TallImageCapsOnHeight(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 900, 1800))
	got := DownscaleMaxEdge(src, 900)
	b := got.Bounds()
	if b.Dy() != 900 {
		t.Fatalf("longer edge (height) = %d, want capped at 900", b.Dy())
	}
	if want := 450; b.Dx() != want {
		t.Fatalf("shorter edge = %d, want %d", b.Dx(), want)
	}
}

// TestDownscaleMaxEdge_LongerEdgeExactlyMaxEdge is the independent-review
// regression (ut-docs#1416): the original implementation computed both
// destination dimensions via the same float64 scale factor
// (int(float64(w)*scale)), which truncates the longer edge to maxEdge-1
// for a measured ~7.5% of real input dimensions — 1601x2156 is one
// concrete case that reproduced it (1599, not the documented 1600). This
// pins the fixed behavior: the longer edge must equal maxEdge exactly.
func TestDownscaleMaxEdge_LongerEdgeExactlyMaxEdge(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1601, 2156))
	got := DownscaleMaxEdge(src, 1600)
	b := got.Bounds()
	if b.Dy() != 1600 {
		t.Fatalf("longer edge (height) = %d, want exactly 1600 (the documented contract)", b.Dy())
	}
}

// TestDownscaleMaxEdge_AtExactBoundaryIsNoOp: an image exactly at maxEdge on
// its longer side must not be touched (off-by-one check on the <= vs <
// comparison).
func TestDownscaleMaxEdge_AtExactBoundaryIsNoOp(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1600, 900))
	got := DownscaleMaxEdge(src, 1600)
	if got.Bounds() != src.Bounds() {
		t.Fatalf("expected unchanged bounds at exact boundary, got %v", got.Bounds())
	}
}
