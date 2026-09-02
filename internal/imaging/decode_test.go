package imaging

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 10, B: 10, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	return buf.Bytes()
}

func encodeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode JPEG: %v", err)
	}
	return buf.Bytes()
}

func TestDecode_ValidSmallPNG(t *testing.T) {
	raw := encodePNG(t, 10, 10)
	img, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 10 || b.Dy() != 10 {
		t.Fatalf("got %dx%d, want 10x10", b.Dx(), b.Dy())
	}
}

// TestDecode_RejectsGIF is the independent-review regression: this package
// only ever meant to accept PNG/JPEG (every caller's error text says so),
// but image.Decode dispatches to any format registered ANYWHERE in the
// built binary — internal/print blank-imports image/gif for an unrelated
// reason, which before the format allowlist silently made GIF a 5th
// accepted format here too, with its own dimension limits never audited
// against MaxPixels.
func TestDecode_RejectsGIF(t *testing.T) {
	img := image.NewPaletted(image.Rect(0, 0, 10, 10), color.Palette{color.Black, color.White})
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode GIF: %v", err)
	}
	_, err := Decode(buf.Bytes())
	if err == nil {
		t.Fatal("expected a valid GIF to be rejected (png/jpeg only), got nil error")
	}
	if !strings.Contains(err.Error(), "unsupported image format") {
		t.Fatalf("expected an unsupported-format error, got: %v", err)
	}
}

func TestDecode_ValidSmallJPEG(t *testing.T) {
	raw := encodeJPEG(t, 10, 10)
	img, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 10 || b.Dy() != 10 {
		t.Fatalf("got %dx%d, want 10x10", b.Dx(), b.Dy())
	}
}

// TestDecodeBounded_RejectsOverLimit is the pixel-bomb regression case: a
// real, well-formed, tiny-on-disk image whose DECODED pixel count exceeds
// the caller's budget must be rejected before the full image.Decode
// allocates a buffer for it — this is exactly what a crafted file with an
// inflated declared width/height would also hit.
func TestDecodeBounded_RejectsOverLimit(t *testing.T) {
	raw := encodePNG(t, 100, 100) // 10,000 px
	_, err := DecodeBounded(raw, 5_000)
	if err == nil {
		t.Fatal("expected an error for an over-limit image, got nil")
	}
	if !strings.Contains(err.Error(), "exceed") {
		t.Fatalf("expected a dimension-limit error, got: %v", err)
	}
}

func TestDecodeBounded_AtLimitAccepted(t *testing.T) {
	raw := encodePNG(t, 100, 50) // exactly 5,000 px
	if _, err := DecodeBounded(raw, 5_000); err != nil {
		t.Fatalf("DecodeBounded at exactly the limit: %v", err)
	}
}

func TestDecodeBounded_CorruptDataReturnsError(t *testing.T) {
	_, err := DecodeBounded([]byte("not an image"), MaxPixels)
	if err == nil {
		t.Fatal("expected an error for corrupt input, got nil")
	}
}

func TestDecodeBounded_EmptyDataReturnsError(t *testing.T) {
	_, err := DecodeBounded(nil, MaxPixels)
	if err == nil {
		t.Fatal("expected an error for empty input, got nil")
	}
}

// pngChunk builds one length-prefixed, CRC-checked PNG chunk per the PNG
// spec (used below to hand-craft a minimal file — see pixelBombPNG).
func pngChunk(typ string, data []byte) []byte {
	var buf bytes.Buffer
	var lenb [4]byte
	binary.BigEndian.PutUint32(lenb[:], uint32(len(data)))
	buf.Write(lenb[:])
	buf.WriteString(typ)
	buf.Write(data)
	crc := crc32.NewIEEE()
	crc.Write([]byte(typ))
	crc.Write(data)
	var crcb [4]byte
	binary.BigEndian.PutUint32(crcb[:], crc.Sum32())
	buf.Write(crcb[:])
	return buf.Bytes()
}

// pixelBombPNG builds a minimal-but-valid PNG — signature + a single IHDR
// chunk declaring width×height, no pixel data at all — the actual shape of
// a real pixel-bomb attack file: tiny on disk, an enormous decoded size.
func pixelBombPNG(t *testing.T, width, height uint32) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], width)
	binary.BigEndian.PutUint32(ihdr[4:8], height)
	ihdr[8] = 8 // bit depth
	ihdr[9] = 2 // color type: truecolor
	buf.Write(pngChunk("IHDR", ihdr))
	return buf.Bytes()
}

// TestDecode_RejectsCraftedPixelBomb is the actual attack this ticket
// (ut-docs#1328) exists for: a hand-crafted, well-formed PNG that is a few
// dozen bytes on disk but declares 50000×50000 (2.5 BILLION) pixels — no
// real image content required, since image.DecodeConfig only reads the
// IHDR chunk. Before this fix, image.Decode would attempt to allocate a
// buffer for that many pixels straight from this tiny file; Decode must
// reject it using the package's real MaxPixels limit, without attempting
// the full decode.
func TestDecode_RejectsCraftedPixelBomb(t *testing.T) {
	raw := pixelBombPNG(t, 50_000, 50_000)
	if len(raw) > 64 {
		t.Fatalf("test fixture should be tiny on disk (attack premise), got %d bytes", len(raw))
	}
	_, err := Decode(raw)
	if err == nil {
		t.Fatal("expected the 2.5-billion-pixel crafted file to be rejected, got nil error")
	}
	if !strings.Contains(err.Error(), "exceed") {
		t.Fatalf("expected a dimension-limit error, got: %v", err)
	}
}

// A genuinely tiny declared size in the same crafted-file shape must still
// decode fine — confirms the guard is about pixel count, not about
// rejecting every hand-built/minimal PNG on principle.
func TestDecode_CraftedSmallImageStillNeedsPixelData(t *testing.T) {
	raw := pixelBombPNG(t, 10, 10)
	// A real decode still needs IDAT/IEND to succeed; DecodeConfig alone
	// (what the dimension guard uses) must pass for this small size —
	// the eventual image.Decode failure below is expected (no pixel data
	// was written) and must be the "corrupt" case, not "exceed".
	_, err := Decode(raw)
	if err == nil {
		t.Fatal("expected an error (no pixel data was written), got nil")
	}
	if strings.Contains(err.Error(), "exceed") {
		t.Fatalf("a 10x10 image must not be rejected by the pixel-count guard, got: %v", err)
	}
}
