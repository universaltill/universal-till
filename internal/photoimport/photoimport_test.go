package photoimport

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/netaccess"
	"golang.org/x/text/encoding/charmap"
)

func pngBytes(t testing.TB) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(1, 1, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// ---- CSV ----

func TestParseCSV_GermanBOMSemicolon(t *testing.T) {
	raw := append([]byte{0xEF, 0xBB, 0xBF}, []byte("SKU;Bildpfad;Name\r\nA-1;fotos\\a1.jpg;Käse\r\nA-2;a2.png;x\r\n")...)
	rows, err := ParseCSV(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].SKU != "A-1" || rows[0].Ref != `fotos\a1.jpg` || rows[1].Ref != "a2.png" {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Line != 2 || rows[1].Line != 3 {
		t.Fatalf("lines = %+v", rows)
	}
}

func TestParseCSV_Windows1252AndAliases(t *testing.T) {
	enc, _ := charmap.Windows1252.NewEncoder().String("sku;Foto\nÄ-1;bäcker.png\n")
	rows, err := ParseCSV([]byte(enc))
	if err != nil || len(rows) != 1 || rows[0].SKU != "Ä-1" || rows[0].Ref != "bäcker.png" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	for _, h := range []string{"image_path", "image_url", "photo", "bild", "foto", "image"} {
		rows, err := ParseCSV([]byte("sku," + h + "\nA,x.png\n"))
		if err != nil || len(rows) != 1 || rows[0].Ref != "x.png" {
			t.Errorf("alias %s: %+v %v", h, rows, err)
		}
	}
}

func TestParseCSV_Errors(t *testing.T) {
	if _, err := ParseCSV([]byte("name,image\nA,b\n")); err != ErrNoSKUColumn {
		t.Fatalf("err = %v", err)
	}
	if _, err := ParseCSV(make([]byte, MaxCSVBytes+1)); err != ErrCSVTooLarge {
		t.Fatalf("err = %v", err)
	}
}

// ---- folder jail ----

func TestResolveInFolder(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.png"), pngBytes(t), 0o644)
	os.WriteFile(filepath.Join(outside, "secret.png"), pngBytes(t), 0o644)
	os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	os.WriteFile(filepath.Join(root, "sub", "b.png"), pngBytes(t), 0o644)
	if err := os.Symlink(filepath.Join(outside, "secret.png"), filepath.Join(root, "link.png")); err != nil {
		t.Skip("no symlinks")
	}
	os.Symlink(outside, filepath.Join(root, "linkdir"))

	ok := []string{"a.png", `sub\b.png`, "sub/b.png", filepath.Join(root, "a.png")}
	for _, ref := range ok {
		if _, err := ResolveInFolder(root, ref); err != nil {
			t.Errorf("%q: %v", ref, err)
		}
	}
	bad := []string{
		"../" + filepath.Base(outside) + "/secret.png", `..\x.png`, "sub/../../x.png",
		filepath.Join(outside, "secret.png"), "link.png", "linkdir/secret.png",
		`\\server\share\a.png`, "//server/share/a.png", "/etc/passwd", "a.png\x00.png", "",
	}
	for _, ref := range bad {
		if p, err := ResolveInFolder(root, ref); err == nil {
			t.Errorf("%q resolved to %q", ref, p)
		}
	}
	if _, err := ResolveInFolder(root, "missing.png"); err != ErrNotFound {
		t.Errorf("missing: %v", err)
	}
}

func TestReadInFolder(t *testing.T) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "big.png"), make([]byte, 100), 0o644)
	os.Mkdir(filepath.Join(d, "sub"), 0o755)
	if _, err := ReadInFolder(d, "big.png", 50); err != ErrTooBig {
		t.Fatalf("big: %v", err)
	}
	if _, err := ReadInFolder(d, "sub", 50); err != ErrNotRegular {
		t.Fatalf("dir: %v", err)
	}
	if b, err := ReadInFolder(d, "big.png", 500); err != nil || len(b) != 100 {
		t.Fatalf("ok read: %v", err)
	}
	// a symlink out of the folder is refused
	out := t.TempDir()
	os.WriteFile(filepath.Join(out, "s.png"), []byte("x"), 0o644)
	if err := os.Symlink(filepath.Join(out, "s.png"), filepath.Join(d, "l.png")); err == nil {
		if _, err := ReadInFolder(d, "l.png", 50); err != ErrOutside {
			t.Fatalf("symlink escape: %v", err)
		}
	}
}

// ---- zip ----

func makeZip(t *testing.T, files map[string][]byte) *Zip {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for n, c := range files {
		w, _ := zw.Create(n)
		w.Write(c)
	}
	zw.Close()
	z, err := OpenZip(bytes.NewReader(b.Bytes()), int64(b.Len()))
	if err != nil {
		t.Fatal(err)
	}
	return z
}

func TestZip(t *testing.T) {
	z := makeZip(t, map[string][]byte{
		"A-1.png": pngBytes(t), "../../evil.png": pngBytes(t), "dir/B-2.JPG": {1}, "__MACOSX/A-1.png": {1}, ".hidden": {1},
	})
	if f, _ := z.Find("a-1", ""); f == nil {
		t.Fatal("A-1 not found")
	}
	if f, _ := z.Find("B-2", ""); f == nil {
		t.Fatal("B-2 not found")
	}
	// zip-slip names are only ever matched by base name, never written.
	if f, _ := z.Find("evil", ""); f == nil {
		t.Fatal("evil base name should be indexed by base only")
	}
	f, _ := z.Find("A-1", "")
	if _, err := Read(f, 10); err != ErrTooBig {
		t.Fatalf("cap: %v", err)
	}
	z2 := makeZip(t, map[string][]byte{"x/A.png": {1}, "y/A.jpg": {1}})
	if _, amb := z2.Find("A", ""); !amb {
		t.Fatal("want ambiguous")
	}
}

// ---- SSRF ----

func TestBlockedIP(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1",
		"0.0.0.0", "::1", "fe80::1", "fc00::1", "::ffff:127.0.0.1", "::ffff:169.254.169.254", "224.0.0.1", "240.0.0.1"} {
		if !BlockedIP(netip.MustParseAddr(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "93.184.216.34", "2606:4700:4700::1111"} {
		if BlockedIP(netip.MustParseAddr(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
}

func TestFetch_RefusesBadURLs(t *testing.T) {
	f := NewFetcher()
	for _, u := range []string{
		"http://example.com/a.png", "ftp://example.com/a", "file:///etc/passwd", "https://127.0.0.1/a.png",
		"https://[::1]/a.png", "https://169.254.169.254/latest/meta-data", "https://user:pw@example.com/a.png",
		"https://example.com:8443/a.png", "https://localhost/a.png", "https:///a.png", "https://10.0.0.5/x",
	} {
		if _, err := f.Fetch(context.Background(), u, 1<<20); err == nil {
			t.Errorf("%s was fetched", u)
		}
	}
}

// testFetcher trusts the TLS test server and allows only loopback, to test
// the mechanics (size cap, redirects) that the production blocklist hides.
func testFetcher(srv *httptest.Server) *Fetcher {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return newFetcher(func(a netip.Addr) bool { return !a.IsLoopback() }, &tls.Config{RootCAs: pool})
}

func hostURL(srv *httptest.Server, p string) string {
	_ = srv
	return "https://example.com" + p // the dial hook below routes it to the test server
}

func TestFetch_Mechanics(t *testing.T) {
	png1 := pngBytes(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/ok.png", func(w http.ResponseWriter, r *http.Request) { w.Write(png1) })
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) { w.Write(make([]byte, 5000)) })
	mux.HandleFunc("/toplain", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "http://example.com/x", 302) })
	mux.HandleFunc("/tometa", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://169.254.169.254/latest", 302)
	})
	srv := httptest.NewUnstartedServer(mux)
	srv.StartTLS()
	defer srv.Close()
	f := testFetcher(srv)
	// Route "port 443" to the test server by swapping the transport dialer.
	tr := netaccess.BaseTransport(f.client.Transport).(*http.Transport)
	orig := tr.DialContext
	tr.DialContext = func(ctx context.Context, n, a string) (net.Conn, error) {
		return orig(ctx, n, srv.Listener.Addr().String())
	}
	ctx := context.Background()
	if b, err := f.Fetch(ctx, hostURL(srv, "/ok.png"), 1<<20); err != nil || !bytes.Equal(b, png1) {
		t.Fatalf("ok: %v", err)
	}
	if _, err := f.Fetch(ctx, hostURL(srv, "/big"), 1000); err != ErrTooBig {
		t.Fatalf("big: %v", err)
	}
	for _, p := range []string{"/toplain", "/tometa"} {
		if _, err := f.Fetch(ctx, hostURL(srv, p), 1<<20); err == nil {
			t.Errorf("redirect %s followed", p)
		}
	}
}

// ---- plan ----

type fakeCat struct {
	skus   map[string][]string
	vars   map[string]bool
	photos map[string]bool
}

func (c *fakeCat) ItemsBySKU(_ context.Context, sku string) ([]string, bool, error) {
	ids := c.skus[strings.ToLower(sku)]
	return ids, len(ids) == 0 && c.vars[strings.ToLower(sku)], nil
}
func (c *fakeCat) HasPhoto(_ context.Context, id string) (bool, error) { return c.photos[id], nil }

type fakeStore struct{ stored map[string]int }

func (s *fakeStore) store(_ context.Context, id string, _ image.Image) error {
	s.stored[id]++
	return nil
}

func setup(t *testing.T) (root string, cat *fakeCat, st *fakeStore) {
	root = t.TempDir()
	os.WriteFile(filepath.Join(root, "a.png"), pngBytes(t), 0o644)
	os.WriteFile(filepath.Join(root, "b.png"), pngBytes(t), 0o644)
	os.WriteFile(filepath.Join(root, "notimg.png"), []byte("hello"), 0o644)
	os.WriteFile(filepath.Join(root, "huge.png"), make([]byte, MaxImageBytes+1), 0o644)
	os.Mkdir(filepath.Join(root, "adir"), 0o755)
	cat = &fakeCat{
		skus:   map[string][]string{"a": {"id-a"}, "b": {"id-b"}, "c": {"id-c"}, "amb": {"x", "y"}, "d": {"id-d"}, "e": {"id-e"}, "f": {"id-f"}, "g": {"id-g"}},
		vars:   map[string]bool{"var": true},
		photos: map[string]bool{"id-b": true},
	}
	return root, cat, &fakeStore{stored: map[string]int{}}
}

func TestRun_StatusesAndParity(t *testing.T) {
	root, cat, st := setup(t)
	rows := []Row{
		{2, "A", "a.png"}, {3, "B", "b.png"}, {4, "ZZZ", "a.png"}, {5, "AMB", "a.png"}, {6, "VAR", "a.png"},
		{7, "C", "../x.png"}, {8, "D", "notimg.png"}, {9, "E", "huge.png"}, {10, "F", "adir"}, {11, "G", "nope.png"},
		{12, "dup", "a.png"}, {13, "DUP", "a.png"}, {14, "A", ""},
	}
	// "A" appears twice (rows 2 and 14) -> both duplicate.
	o := Options{Folder: root, Catalog: cat, Store: st.store}
	prev, err := Run(context.Background(), rows, o)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]string{2: StatusDuplicateSKU, 14: StatusDuplicateSKU, 3: StatusSkipHasPhoto, 4: StatusUnknownSKU,
		5: StatusAmbiguousSKU, 6: StatusVariantSKU, 7: StatusOutside, 8: StatusNotImage, 9: StatusTooBig,
		10: StatusNotRegular, 11: StatusNotFound, 12: StatusDuplicateSKU, 13: StatusDuplicateSKU}
	for _, r := range prev.Rows {
		if want[r.Line] != r.Status {
			t.Errorf("line %d = %s, want %s", r.Line, r.Status, want[r.Line])
		}
	}
	if len(st.stored) != 0 {
		t.Fatal("preview stored something")
	}
	o.Commit = true
	app, err := Run(context.Background(), rows, o)
	if err != nil {
		t.Fatal(err)
	}
	for i := range prev.Rows {
		if prev.Rows[i] != app.Rows[i] {
			t.Errorf("parity: %v vs %v", prev.Rows[i], app.Rows[i])
		}
	}
}

func TestRun_ReplaceAndIdempotent(t *testing.T) {
	root, cat, st := setup(t)
	rows := []Row{{2, "a", "a.png"}, {3, "b", "b.png"}}
	o := Options{Folder: root, Catalog: cat, Store: st.store, Commit: true}
	rep, _ := Run(context.Background(), rows, o)
	if rep.Rows[0].Status != StatusAdd || rep.Rows[1].Status != StatusSkipHasPhoto {
		t.Fatalf("replace off: %+v", rep.Rows)
	}
	if st.stored["id-a"] != 1 || st.stored["id-b"] != 0 {
		t.Fatalf("stored %v", st.stored)
	}
	// Idempotent re-run: a now has a photo.
	cat.photos["id-a"] = true
	rep, _ = Run(context.Background(), rows, o)
	if rep.Rows[0].Status != StatusSkipHasPhoto || st.stored["id-a"] != 1 {
		t.Fatalf("rerun: %+v %v", rep.Rows, st.stored)
	}
	// Replace on.
	o.Replace = true
	rep, _ = Run(context.Background(), rows, o)
	if rep.Rows[0].Status != StatusReplace || rep.Rows[1].Status != StatusReplace || st.stored["id-b"] != 1 {
		t.Fatalf("replace on: %+v %v", rep.Rows, st.stored)
	}
}

func TestRun_ZipAndURLRows(t *testing.T) {
	_, cat, st := setup(t)
	z := makeZip(t, map[string][]byte{"A.png": pngBytes(t), "B.png": []byte("nope")})
	rows := RowsFromZip(z)
	rep, err := Run(context.Background(), rows, Options{Zip: z, Catalog: cat, Store: st.store, Commit: true})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range rep.Rows {
		got[r.SKU] = r.Status
	}
	if got["A"] != StatusAdd || got["B"] != StatusSkipHasPhoto {
		t.Fatalf("zip rows: %v", got)
	}
	// URL rows: http refused, no fetcher refused.
	rep, _ = Run(context.Background(), []Row{{2, "C", "http://x.example/a.png"}, {3, "D", "https://x.example/a.png"}},
		Options{Catalog: cat, Store: st.store})
	if rep.Rows[0].Status != StatusBlockedURL || rep.Rows[1].Status != StatusBlockedURL {
		t.Fatalf("url rows: %+v", rep.Rows)
	}
}

func TestRun_NoFolderNoSource(t *testing.T) {
	_, cat, st := setup(t)
	rep, _ := Run(context.Background(), []Row{{2, "C", "a.png"}, {3, "D", ""}}, Options{Catalog: cat, Store: st.store})
	if rep.Rows[0].Status != StatusNoSource || rep.Rows[1].Status != StatusNoSource {
		t.Fatalf("%+v", rep.Rows)
	}
}

func TestFailedCSV(t *testing.T) {
	rep := &Report{Rows: []RowResult{{2, "=cmd", StatusNotFound}, {3, "ok", StatusAdd}, {4, "b", StatusSkipHasPhoto}}}
	var b bytes.Buffer
	if err := rep.WriteFailedCSV(&b); err != nil {
		t.Fatal(err)
	}
	if b.String() != "line,sku,reason\n2,'=cmd,not_found\n" {
		t.Fatalf("csv = %q", b.String())
	}
}

// The dial-time guard is what stops DNS rebinding and redirects to a private
// name: with the production blocklist, dialling a loopback listener (what a
// rebinding name resolves to) must be refused before any connection is made.
func TestFetch_DialGuardRefusesPrivateAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			c.Close()
			accepted <- struct{}{}
		}
	}()
	f := NewFetcher()
	tr := netaccess.BaseTransport(f.client.Transport).(*http.Transport)
	c, err := tr.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err == nil {
		c.Close()
		t.Fatal("dial to a loopback address was allowed")
	}
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("err = %v, want ErrBlocked", err)
	}
	select {
	case <-accepted:
		t.Fatal("a connection reached the loopback listener")
	case <-time.After(200 * time.Millisecond):
	}
}

// ---- review fixes (2026-11-16) ----

// Two entries with the same base name in different folders: a CSV ref
// naming that base name must be refused as ambiguous, not silently resolved
// to whichever entry was indexed last.
func TestZip_SameBaseNameRefIsAmbiguous(t *testing.T) {
	z := makeZip(t, map[string][]byte{"x/a.png": {1}, "y/A.PNG": {2}, "b.png": {3}})
	if _, amb := z.Find("SKU", "photos\\a.png"); !amb {
		t.Fatal("same base name in two folders should be ambiguous")
	}
	if f, amb := z.Find("SKU", "b.png"); f == nil || amb {
		t.Fatalf("unique base name: f=%v amb=%v", f, amb)
	}
}

// ZIP-only rows get stable line numbers (sorted by SKU), so a preview and the
// failed-rows CSV of the apply agree on what "line 3" is.
func TestRowsFromZip_Deterministic(t *testing.T) {
	files := map[string][]byte{}
	for _, s := range []string{"d", "a", "c", "b", "e", "f", "g", "h"} {
		files[s+".png"] = []byte{1}
	}
	z := makeZip(t, files)
	for i := 0; i < 20; i++ {
		rows := RowsFromZip(z)
		var got []string
		for _, r := range rows {
			got = append(got, r.SKU)
		}
		if strings.Join(got, "") != "abcdefgh" || rows[0].Line != 1 {
			t.Fatalf("rows = %v", got)
		}
	}
}

// IPv6 forms that carry an IPv4 address (NAT64, 6to4) are judged by that
// address; Teredo is refused outright.
func TestBlockedIP_EmbeddedIPv4(t *testing.T) {
	for _, s := range []string{"64:ff9b::7f00:1", "64:ff9b::a9fe:a9fe", "64:ff9b::c0a8:101", "2002:c0a8:101::1", "2002:7f00:1::1", "2001:0:4136:e378::1"} {
		if !BlockedIP(netip.MustParseAddr(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	for _, s := range []string{"64:ff9b::808:808", "2002:808:808::1"} {
		if BlockedIP(netip.MustParseAddr(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
}

// A ref missing from the uploaded ZIP with no folder is "not found" (there
// was a source, the file is not in it), not "no source".
func TestRun_ZipRefMissingIsNotFound(t *testing.T) {
	_, cat, st := setup(t)
	z := makeZip(t, map[string][]byte{"other.png": pngBytes(t)})
	rep, _ := Run(context.Background(), []Row{{2, "C", "a.png"}}, Options{Zip: z, Catalog: cat, Store: st.store})
	if rep.Rows[0].Status != StatusNotFound {
		t.Fatalf("%+v", rep.Rows)
	}
}

func TestValidateFolder(t *testing.T) {
	for _, bad := range []string{`\\host\share`, "//host/share", "/", "relative/dir", "/dev", "/proc", "/etc"} {
		if err := ValidateFolder(bad); err == nil {
			t.Errorf("ValidateFolder(%q) accepted", bad)
		} else if err != ErrOutside && err != ErrNotFound {
			t.Errorf("ValidateFolder(%q) = %v", bad, err)
		}
	}
	if err := ValidateFolder(t.TempDir()); err != nil {
		t.Errorf("temp dir refused: %v", err)
	}
}

func TestOpenZip_DeclaredEntryCountRefusedBeforeParsing(t *testing.T) {
	var b bytes.Buffer
	b.Write([]byte{'P', 'K', 5, 6, 0, 0, 0, 0, 0xFF, 0xFF, 0xFF, 0xFF, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	if _, err := OpenZip(bytes.NewReader(b.Bytes()), int64(b.Len())); err != ErrBadZip {
		t.Fatalf("got %v", err)
	}
}

func TestBlockedIP_CompatAndSiteLocal(t *testing.T) {
	for _, s := range []string{"::127.0.0.1", "::10.0.0.1", "fec0::1", "::ffff:127.0.0.1", "::1", "::"} {
		if !BlockedIP(netip.MustParseAddr(s)) {
			t.Errorf("%s not blocked", s)
		}
	}
	if BlockedIP(netip.MustParseAddr("::8.8.8.8")) {
		t.Error("compat of a public v4 should pass")
	}
}

func TestOpenZip_DirectoryMustEndAtTheEndRecord(t *testing.T) {
	// Declares 1 entry / 46 directory bytes at offset 0, but 138 bytes of
	// "records" precede the end record: archive/zip would keep parsing them.
	var b bytes.Buffer
	b.Write(make([]byte, 138))
	b.Write([]byte{'P', 'K', 5, 6, 0, 0, 0, 0, 1, 0, 1, 0, 46, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	if _, err := OpenZip(bytes.NewReader(b.Bytes()), int64(b.Len())); err != ErrBadZip {
		t.Fatalf("got %v", err)
	}
}

func TestReservedDeviceName(t *testing.T) {
	for ref, want := range map[string]bool{"NUL.png": true, "sub/com1.jpg": true, `a\LPT9`: true, "CONSOLE.png": false, "ABC-123.jpg": false, "COM10.png": false} {
		if got := reservedDeviceName(ref); got != want {
			t.Errorf("%q = %v, want %v", ref, got, want)
		}
	}
}
