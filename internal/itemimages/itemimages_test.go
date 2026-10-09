package itemimages

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/imaging"
	"github.com/universaltill/universal-till/internal/paths"
)

func useDataDir(t *testing.T) string {
	t.Helper()
	orig := paths.DataDir()
	root := t.TempDir()
	paths.Init(root)
	t.Cleanup(func() { paths.Init(orig) })
	return root
}

// pngOf is a w×h PNG filled with c, so tests can tell sources apart.
func pngOf(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func write(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func refOf(t *testing.T, b []byte) []byte {
	t.Helper()
	out, err := imaging.RefJPEG(b)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAssetDir_IsUnderTheDataDir(t *testing.T) {
	root := useDataDir(t)
	if got, want := AssetDir(), filepath.Join(root, "public", "assets", "items"); got != want {
		t.Fatalf("AssetDir = %q, want %q", got, want)
	}
}

func TestRef_Roles(t *testing.T) {
	useDataDir(t)
	thumb := pngOf(t, 400, 300, color.RGBA{255, 0, 0, 255})
	older := pngOf(t, 300, 300, color.RGBA{0, 0, 255, 255})
	newest := pngOf(t, 200, 500, color.RGBA{0, 255, 0, 255})
	write(t, filepath.Join(AssetDir(), "itm1", "thumb.png"), thumb)
	write(t, filepath.Join(AssetDir(), "itm1", "ai_ref", "100.png"), older)
	write(t, filepath.Join(AssetDir(), "itm1", "ai_ref", "200.png"), newest)

	for role, want := range map[string][]byte{
		RoleThumb: refOf(t, thumb),
		RoleAIRef: refOf(t, newest),
		RoleRef:   refOf(t, newest),
	} {
		got, err := Ref("itm1", role)
		if err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s: bytes differ from RefJPEG of the expected file", role)
		}
	}
}

// `ref` falls back to the thumbnail when the newest ai_ref does not decode
// (ADR-0121 R1: "ai_ref if one decodes, else thumb").
func TestRef_RefFallsBackToThumbWhenAIRefUndecodable(t *testing.T) {
	useDataDir(t)
	thumb := pngOf(t, 64, 64, color.RGBA{255, 0, 0, 255})
	write(t, filepath.Join(AssetDir(), "itm1", "thumb.png"), thumb)
	write(t, filepath.Join(AssetDir(), "itm1", "ai_ref", "100.png"), []byte("corrupt"))

	got, err := Ref("itm1", RoleRef)
	if err != nil {
		t.Fatalf("ref: %v", err)
	}
	if !bytes.Equal(got, refOf(t, thumb)) {
		t.Fatal("ref did not fall back to the thumbnail")
	}
	if _, err := Ref("itm1", RoleAIRef); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ai_ref of an undecodable file: err = %v, want ErrNotFound", err)
	}
}

func TestRef_RefUsesAIRefWithoutThumb(t *testing.T) {
	useDataDir(t)
	only := pngOf(t, 32, 32, color.RGBA{0, 0, 255, 255})
	write(t, filepath.Join(AssetDir(), "itm1", "ai_ref", "100.jpg"), only)
	got, err := Ref("itm1", RoleRef)
	if err != nil || !bytes.Equal(got, refOf(t, only)) {
		t.Fatalf("ref with only an ai_ref: err=%v", err)
	}
	if _, err := Ref("itm1", RoleThumb); !errors.Is(err, ErrNotFound) {
		t.Fatalf("thumb missing: err = %v, want ErrNotFound", err)
	}
}

func TestRef_NotFound(t *testing.T) {
	useDataDir(t)
	write(t, filepath.Join(AssetDir(), "gif", "thumb.png"), []byte("GIF89a garbage"))
	for _, c := range []struct{ id, role string }{
		{"nope", RoleRef}, {"nope", RoleThumb}, {"nope", RoleAIRef}, {"gif", RoleThumb}, {"gif", RoleRef},
	} {
		if _, err := Ref(c.id, c.role); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Ref(%q,%q) err = %v, want ErrNotFound", c.id, c.role, err)
		}
	}
}

// Ids are validated before any filesystem access: a traversal never reaches
// a file, even one that exists.
func TestRef_InvalidIDAndRole(t *testing.T) {
	root := useDataDir(t)
	write(t, filepath.Join(root, "public", "assets", "thumb.png"), pngOf(t, 8, 8, color.Black))
	long := string(bytes.Repeat([]byte("a"), 129))
	for _, id := range []string{"", long, "..", ".", "a/b", `a\b`, "a.b", "../items", "x/../../"} {
		if _, err := Ref(id, RoleThumb); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Ref(%q) err = %v, want ErrInvalid", id, err)
		}
	}
	if _, err := Ref(string(bytes.Repeat([]byte("a"), 128)), RoleThumb); !errors.Is(err, ErrNotFound) {
		t.Fatalf("128-byte id: err = %v, want ErrNotFound", err)
	}
	for _, role := range []string{"", "THUMB", "full", "thumb.png"} {
		if _, err := Ref("itm1", role); !errors.Is(err, ErrInvalid) {
			t.Fatalf("role %q err = %v, want ErrInvalid", role, err)
		}
	}
}

func TestLatestAIRef(t *testing.T) {
	dir := t.TempDir()
	if _, _, ok := LatestAIRef(dir); ok {
		t.Fatal("empty dir reported a ref")
	}
	write(t, filepath.Join(dir, "100.jpg"), []byte("x"))
	write(t, filepath.Join(dir, "200.png"), []byte("x"))
	write(t, filepath.Join(dir, "300.txt"), []byte("x"))
	p, media, ok := LatestAIRef(dir)
	if !ok || p != filepath.Join(dir, "200.png") || media != "image/png" {
		t.Fatalf("LatestAIRef = %q %q %v", p, media, ok)
	}
}

func TestPruneAIRefsKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"100.jpg", "200.jpg", "300.jpg", "400.jpg", "500.jpg", "600.jpg", "700.jpg"} {
		write(t, filepath.Join(dir, n), []byte("x"))
	}
	PruneAIRefs(dir)
	entries, _ := os.ReadDir(dir)
	if len(entries) != MaxAIRefsPerItem {
		t.Fatalf("kept %d refs, want %d", len(entries), MaxAIRefsPerItem)
	}
	if _, err := os.Stat(filepath.Join(dir, "700.jpg")); err != nil {
		t.Fatal("newest ref must survive pruning")
	}
	if _, err := os.Stat(filepath.Join(dir, "100.jpg")); !os.IsNotExist(err) {
		t.Fatal("oldest ref must be pruned")
	}
}

// Ids that pass the ADR's /\. rule but are no portable file name (control
// bytes, ':', Windows device names, a trailing space) are refused before any
// filesystem access, so a guest can't turn them into I/O errors and log
// lines (#4005 review).
func TestRef_RejectsNonPortableIDs(t *testing.T) {
	useDataDir(t)
	for _, id := range []string{"a\x00b", "a\nb", "a\x7fb", "c:x", "CON", "nul", "Com1", "lpt9", "abc "} {
		if _, err := Ref(id, RoleThumb); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Ref(%q) err = %v, want ErrInvalid", id, err)
		}
	}
	for _, id := range []string{"console", "com10", "3f2c9a1e-7b4d-4c1a-9e2f-0a1b2c3d4e5f", "itm_1"} {
		if _, err := Ref(id, RoleThumb); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Ref(%q) err = %v, want ErrNotFound", id, err)
		}
	}
}

// A file over MaxFileBytes is refused before it is read into memory: a
// guest may open 64 per event (#4005 review). The file is a valid PNG
// padded with trailing zeros, which the decoder alone would accept.
func TestRef_RefusesOversizedFileWithoutReadingIt(t *testing.T) {
	root := useDataDir(t)
	p := filepath.Join(root, "public", "assets", "items", "big", "thumb.png")
	write(t, p, pngOf(t, 8, 8, color.Black))
	if _, err := Ref("big", RoleThumb); err != nil {
		t.Fatalf("small file: %v", err)
	}
	if err := os.Truncate(p, MaxFileBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, err := Ref("big", RoleThumb); !errors.Is(err, ErrNotFound) {
		t.Fatalf("oversized file: err = %v, want ErrNotFound", err)
	}
}
