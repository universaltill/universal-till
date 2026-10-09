package plugins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/imaging"
	"github.com/universaltill/universal-till/internal/paths"
)

// ADR-0121 R1 (ut-docs#4005): item_image_open / item_image_read, driven
// through a real wasip1 guest (testdata/itemimage_guest) against a temp
// data root holding an items asset tree.

func buildItemImageGuest(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "itemimage_guest.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/itemimage_guest")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasip1 item image guest: %v\n%s", err, raw)
	}
	return out
}

// newItemImageFixture is a blobFixture (data root, db, runtime) running the
// item image guest.
func newItemImageFixture(t *testing.T) *blobFixture {
	t.Helper()
	root := t.TempDir()
	prev := paths.DataDir()
	paths.Init(root)
	t.Cleanup(func() { paths.Init(prev) })
	return &blobFixture{t: t, root: root, guest: buildItemImageGuest(t), db: hostfnTestDB(t), w: NewWasmRuntime(t.TempDir())}
}

type itemImageResult struct {
	Code  int32    `json:"code"`
	Codes []int32  `json:"codes"`
	Data  string   `json:"data"`
	Datas []string `json:"datas"`
}

func (r itemImageResult) bytes(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(r.Data)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func runItemImage(f *blobFixture, pluginID string, ops ...map[string]any) []itemImageResult {
	f.t.Helper()
	res := runGuestPayload(f.t, f.w, f.db, pluginID, map[string]any{"ops": ops})
	raw, _ := json.Marshal(res["results"])
	var out []itemImageResult
	if err := json.Unmarshal(raw, &out); err != nil {
		f.t.Fatalf("parse results %s: %v", raw, err)
	}
	if len(out) != len(ops) {
		f.t.Fatalf("guest ran %d of %d ops: %s", len(out), len(ops), raw)
	}
	return out
}

func testPNG(t *testing.T, w, h int, c color.Color) []byte {
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

// itemFile writes rel (e.g. "itm1/thumb.png") under the items asset dir.
func itemFile(t *testing.T, rel string, b []byte) {
	t.Helper()
	p := filepath.Join(paths.Data("public", "assets", "items"), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func refJPEG(t *testing.T, b []byte) []byte {
	t.Helper()
	out, err := imaging.RefJPEG(b)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func openOp(id, role string) map[string]any {
	return map[string]any{"op": "open", "id": id, "role": role}
}
func readAllOp(h, capacity int) map[string]any {
	return map[string]any{"op": "read_all", "h": h, "cap": capacity}
}

func TestItemImage_RolesReturnRefJPEGBytes(t *testing.T) {
	f := newItemImageFixture(t)
	const id = "com.test.itemimage"
	f.plugin(id, 0, permViewInventory)
	thumb := testPNG(t, 640, 480, color.RGBA{255, 0, 0, 255})
	newest := testPNG(t, 300, 900, color.RGBA{0, 255, 0, 255})
	itemFile(t, "itm1/thumb.png", thumb)
	itemFile(t, "itm1/ai_ref/100.png", testPNG(t, 50, 50, color.Black))
	itemFile(t, "itm1/ai_ref/200.png", newest)
	itemFile(t, "itm2/thumb.png", thumb)
	itemFile(t, "itm2/ai_ref/100.png", []byte("corrupt"))

	res := runItemImage(f, id,
		openOp("itm1", "thumb"), readAllOp(0, 100),
		openOp("itm1", "ai_ref"), readAllOp(2, 0),
		openOp("itm1", "ref"), readAllOp(4, 0),
		openOp("itm2", "ref"), readAllOp(6, 0),
		openOp("itm2", "ai_ref"),
	)
	for i, want := range map[int][]byte{1: refJPEG(t, thumb), 3: refJPEG(t, newest), 5: refJPEG(t, newest), 7: refJPEG(t, thumb)} {
		if res[i-1].Code != 0 {
			t.Fatalf("open op %d = %d", i-1, res[i-1].Code)
		}
		if got := res[i].bytes(t); !bytes.Equal(got, want) {
			t.Fatalf("op %d: %d bytes, want the %d RefJPEG bytes", i, len(got), len(want))
		}
		if last := res[i].Codes[len(res[i].Codes)-1]; last != 0 {
			t.Fatalf("op %d did not end with 0: %v", i, res[i].Codes)
		}
	}
	// A 100-byte buffer reads in 100-byte chunks.
	if c := res[1].Codes; len(c) < 3 || c[0] != 100 {
		t.Fatalf("chunked read codes = %v", c)
	}
	if res[8].Code != hostErrNotFound {
		t.Fatalf("undecodable ai_ref = %d, want -1", res[8].Code)
	}
}

func TestItemImage_DeniedWithoutViewInventory(t *testing.T) {
	f := newItemImageFixture(t)
	const id = "com.test.itemimage.denied"
	f.plugin(id, 0)
	itemFile(t, "itm1/thumb.png", testPNG(t, 8, 8, color.Black))
	res := runItemImage(f, id, openOp("itm1", "thumb"))
	if res[0].Code != hostErrDenied {
		t.Fatalf("open without view:inventory = %d, want -2", res[0].Code)
	}
	var n int
	if err := f.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE action = 'permission_denied' AND entity_id = ? AND data_json LIKE '%view:inventory%'`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("permission_denied audit rows = %d, want 1", n)
	}
}

func TestItemImage_InvalidAndNotFound(t *testing.T) {
	f := newItemImageFixture(t)
	const id = "com.test.itemimage.invalid"
	f.plugin(id, 0, permViewInventory)
	itemFile(t, "gif/thumb.png", []byte("GIF89a not really"))
	// A file the traversal would reach if the id were not validated.
	if err := os.WriteFile(filepath.Join(paths.Data("public", "assets"), "thumb.png"), testPNG(t, 8, 8, color.Black), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		id, role string
		want     int32
	}{
		{"nope", "ref", hostErrNotFound},
		{"nope", "thumb", hostErrNotFound},
		{"gif", "thumb", hostErrNotFound},
		{"..", "thumb", hostErrInvalid},
		{"a/b", "thumb", hostErrInvalid},
		{`a\b`, "thumb", hostErrInvalid},
		{"a.b", "thumb", hostErrInvalid},
		{"", "thumb", hostErrInvalid},
		{strings.Repeat("a", 129), "thumb", hostErrInvalid},
		{strings.Repeat("a", 128), "thumb", hostErrNotFound},
		{"itm1", "full", hostErrInvalid},
		{"itm1", "", hostErrInvalid},
	}
	var ops []map[string]any
	for _, c := range cases {
		ops = append(ops, openOp(c.id, c.role))
	}
	// read: unknown handle (a failed open's code), and dstCap 0.
	ops = append(ops, map[string]any{"op": "read", "h": 0, "cap": 16})
	res := runItemImage(f, id, ops...)
	for i, c := range cases {
		if res[i].Code != c.want {
			t.Fatalf("open(%q,%q) = %d, want %d", c.id, c.role, res[i].Code, c.want)
		}
	}
	if got := res[len(cases)].Code; got != hostErrNotFound {
		t.Fatalf("read on an unknown handle = %d, want -1", got)
	}
}

func TestItemImage_HandleLimitAndRelease(t *testing.T) {
	f := newItemImageFixture(t)
	const id = "com.test.itemimage.busy"
	f.plugin(id, 0, permViewInventory)
	itemFile(t, "itm1/thumb.png", testPNG(t, 8, 8, color.Black))
	res := runItemImage(f, id,
		openOp("itm1", "thumb"), openOp("itm1", "thumb"), openOp("itm1", "thumb"), openOp("itm1", "thumb"),
		openOp("itm1", "thumb"),                         // 4: fifth open handle → -6
		map[string]any{"op": "read", "h": 0, "cap": 0},  // 5: dstCap 0 → -4
		readAllOp(0, 0),                                 // 6: read to 0, releasing the handle
		map[string]any{"op": "read", "h": 0, "cap": 16}, // 7: released → -1
		openOp("itm1", "thumb"),                         // 8: a slot is free again
	)
	for i := 0; i < 4; i++ {
		if res[i].Code != 0 {
			t.Fatalf("open %d = %d", i, res[i].Code)
		}
	}
	if res[4].Code != hostErrBusy {
		t.Fatalf("fifth open handle = %d, want -6", res[4].Code)
	}
	if res[5].Code != hostErrInvalid {
		t.Fatalf("dstCap 0 = %d, want -4", res[5].Code)
	}
	if res[6].Code != 0 {
		t.Fatalf("read_all = %v", res[6].Codes)
	}
	if res[7].Code != hostErrNotFound {
		t.Fatalf("read after the releasing 0 = %d, want -1", res[7].Code)
	}
	if res[8].Code != 0 {
		t.Fatalf("open after a release = %d, want 0", res[8].Code)
	}
	// Handles never outlive the event: a new event opens four again.
	res = runItemImage(f, id, openOp("itm1", "thumb"), openOp("itm1", "thumb"), openOp("itm1", "thumb"), openOp("itm1", "thumb"))
	for i, r := range res {
		if r.Code != 0 {
			t.Fatalf("next event open %d = %d", i, r.Code)
		}
	}
}

func TestItemImage_SixtyFiveOpensPerEvent(t *testing.T) {
	f := newItemImageFixture(t)
	const id = "com.test.itemimage.quota"
	f.plugin(id, 0, permViewInventory)
	itemFile(t, "itm1/thumb.png", testPNG(t, 8, 8, color.Black))
	var ops []map[string]any
	for i := 0; i < 64; i++ {
		ops = append(ops, openOp(fmt.Sprintf("missing%d", i), "ref")) // failed opens count
	}
	ops = append(ops, openOp("itm1", "thumb"))
	res := runItemImage(f, id, ops...)
	for i := 0; i < 64; i++ {
		if res[i].Code != hostErrNotFound {
			t.Fatalf("open %d = %d, want -1", i, res[i].Code)
		}
	}
	if res[64].Code != hostErrQuota {
		t.Fatalf("65th open = %d, want -5", res[64].Code)
	}
	// The counter is per event.
	if res := runItemImage(f, id, openOp("itm1", "thumb")); res[0].Code != 0 {
		t.Fatalf("first open of the next event = %d", res[0].Code)
	}
}

// The built-in identify's reference set — 60 items, one `ref` each — fits
// one event (ADR-0121 R1).
func TestItemImage_SixtyRefsInOneEvent(t *testing.T) {
	f := newItemImageFixture(t)
	const id = "com.test.itemimage.sixty"
	f.plugin(id, 0, permViewInventory)
	var ids, want []string
	for i := 0; i < 60; i++ {
		item := fmt.Sprintf("itm%02d", i)
		img := testPNG(t, 200+i, 180, color.RGBA{uint8(i * 4), 100, 50, 255})
		if i%3 == 0 {
			itemFile(t, item+"/ai_ref/100.png", img)
			itemFile(t, item+"/thumb.png", testPNG(t, 10, 10, color.Black))
		} else {
			itemFile(t, item+"/thumb.png", img)
		}
		ids = append(ids, item)
		sum := sha256.Sum256(refJPEG(t, img))
		want = append(want, hex.EncodeToString(sum[:]))
	}
	res := runItemImage(f, id, map[string]any{"op": "refs", "ids": ids})
	if len(res[0].Codes) != 60 {
		t.Fatalf("guest read %d refs, want 60", len(res[0].Codes))
	}
	for i := range ids {
		if res[0].Codes[i] != 0 || res[0].Datas[i] != want[i] {
			t.Fatalf("ref %s: code %d, sha %s want %s", ids[i], res[0].Codes[i], res[0].Datas[i], want[i])
		}
	}
}

// handleEvent defers closeAll: after it no handle survives, and the table
// has room for a full set again.
func TestItemImageHandles_CloseAllReleasesEveryHandle(t *testing.T) {
	var r itemImageHandles
	var hs []int32
	for i := 0; i < itemImageMaxHandles; i++ {
		h, ok := r.add(&itemImageHandle{data: []byte("x")})
		if !ok {
			t.Fatalf("add %d refused", i)
		}
		hs = append(hs, h)
	}
	if !r.full() {
		t.Fatal("table not full after the max handles")
	}
	r.closeAll()
	for _, h := range hs {
		if _, ok := r.get(h); ok {
			t.Fatalf("handle %d survived closeAll", h)
		}
	}
	if r.full() {
		t.Fatal("table still full after closeAll")
	}
}
