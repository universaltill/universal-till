package pages

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRefJPEGDownscales(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "thumb.png")
	src := image.NewRGBA(image.Rect(0, 0, 512, 384))
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	data, ok := loadRefJPEG(path)
	if !ok {
		t.Fatal("expected re-encode to succeed")
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("output is not JPEG: %v", err)
	}
	if w := img.Bounds().Dx(); w != 160 {
		t.Fatalf("long edge = %d, want 160", w)
	}
}

func TestPruneAIRefsKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	names := []string{"100.jpg", "200.jpg", "300.jpg", "400.jpg", "500.jpg", "600.jpg", "700.jpg"}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pruneAIRefs(dir)
	entries, _ := os.ReadDir(dir)
	if len(entries) != maxAIRefsPerItem {
		t.Fatalf("kept %d refs, want %d", len(entries), maxAIRefsPerItem)
	}
	if _, err := os.Stat(filepath.Join(dir, "700.jpg")); err != nil {
		t.Fatal("newest ref must survive pruning")
	}
	if _, err := os.Stat(filepath.Join(dir, "100.jpg")); !os.IsNotExist(err) {
		t.Fatal("oldest ref must be pruned")
	}
}
