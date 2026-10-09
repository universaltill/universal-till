package pages

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/ai"
	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/imaging"
)

// cutoutPNG is what a background-removal service answers: a 40×20 canvas,
// transparent except an opaque red 10×10 subject off-centre.
func cutoutPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 40, 20))
	for y := 5; y < 15; y++ {
		for x := 25; x < 35; x++ {
			img.Set(x, y, color.NRGBA{R: 0xff, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// emptyCutoutPNG is a cutout that removed everything: transparent, no subject.
func emptyCutoutPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeRembg stands in for the shop's rembg server and counts its calls.
func fakeRembg(t *testing.T, status int, answer []byte) (*ai.Service, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/remove" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(status)
		_, _ = w.Write(answer)
	}))
	t.Cleanup(srv.Close)
	svc := ai.New(ai.Config{Image: ai.ImageConfig{Provider: ai.ImageProviderSelfHosted, Endpoint: srv.URL, Model: "u2netp"}})
	if !svc.CanCutout() {
		t.Fatal("fixture: expected the image capability to be on")
	}
	return svc, &calls
}

func serveCutout(t *testing.T, mux http.Handler, req *http.Request) (*httptest.ResponseRecorder, image.Image) {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return rec, nil
	}
	img, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("200 answer is not a PNG: %v", err)
	}
	return rec, img
}

func TestCutoutAPI_NotConfiguredReturns404(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, _ := newAIAPITestDeps(t)
	rec, _ := serveCutout(t, mux, multipartPhotoRequest(t, "/api/catalog/image/cutout", testPNGBytes(t), nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 with background removal off, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCutoutAPI_ReturnsSquareTileOnTheChosenBackground(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newAIAPITestDeps(t)
	var calls *atomic.Int64
	dp.AI, calls = fakeRembg(t, http.StatusOK, cutoutPNG(t))

	cases := []struct {
		name   string
		fields map[string]string
		corner color.NRGBA
	}{
		{"default is the tile colour, white when none", nil, color.NRGBA{0xff, 0xff, 0xff, 0xff}},
		{"tile with a palette colour", map[string]string{"background": "tile", "tile_color": "#0f766e"}, color.NRGBA{0x0f, 0x76, 0x6e, 0xff}},
		{"white", map[string]string{"background": "white"}, color.NRGBA{0xff, 0xff, 0xff, 0xff}},
		{"transparent", map[string]string{"background": "transparent"}, color.NRGBA{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, img := serveCutout(t, mux, multipartPhotoRequest(t, "/api/catalog/image/cutout", testPNGBytes(t), tc.fields))
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
				t.Fatalf("Content-Type = %q, want image/png", ct)
			}
			if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", cc)
			}
			if b := img.Bounds(); b.Dx() != imaging.TileSize || b.Dy() != imaging.TileSize {
				t.Fatalf("tile is %dx%d, want %dx%d", b.Dx(), b.Dy(), imaging.TileSize, imaging.TileSize)
			}
			if got := color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA); got != tc.corner {
				t.Fatalf("corner = %#v, want %#v", got, tc.corner)
			}
			mid := imaging.TileSize / 2
			if got := color.NRGBAModel.Convert(img.At(mid, mid)).(color.NRGBA); got.R != 0xff || got.G != 0 || got.A != 0xff {
				t.Fatalf("centre = %#v, want the red subject centred", got)
			}
		})
	}
	if calls.Load() != int64(len(cases)) {
		t.Fatalf("service called %d times, want %d", calls.Load(), len(cases))
	}
}

func TestCutoutAPI_RefusesBadInputBeforeCallingTheService(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newAIAPITestDeps(t)
	var calls *atomic.Int64
	dp.AI, calls = fakeRembg(t, http.StatusOK, cutoutPNG(t))

	cases := []struct {
		name   string
		photo  []byte
		fields map[string]string
		want   int
	}{
		{"no photo", nil, nil, http.StatusBadRequest},
		{"not an image", []byte("not an image"), nil, http.StatusBadRequest},
		{"pixel bomb", oversizedPNGBytes(t), nil, http.StatusBadRequest},
		{"tile colour off the palette", testPNGBytes(t), map[string]string{"tile_color": "#ffffff"}, http.StatusBadRequest},
		{"css-shaped tile colour", testPNGBytes(t), map[string]string{"tile_color": "#0f172a;background:red"}, http.StatusBadRequest},
		{"unknown background", testPNGBytes(t), map[string]string{"background": "scene"}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, _ := serveCutout(t, mux, multipartPhotoRequest(t, "/api/catalog/image/cutout", tc.photo, tc.fields))
			if rec.Code != tc.want {
				t.Fatalf("expected %d, got %d: %s", tc.want, rec.Code, rec.Body.String())
			}
		})
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("service called %d times for refused input, want 0", n)
	}
}

func TestCutoutAPI_OversizeBodyIs413(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newAIAPITestDeps(t)
	var calls *atomic.Int64
	dp.AI, calls = fakeRembg(t, http.StatusOK, cutoutPNG(t))

	big := append(testPNGBytes(t), bytes.Repeat([]byte{0}, maxCutoutUploadBytes)...)
	rec, _ := serveCutout(t, mux, multipartPhotoRequest(t, "/api/catalog/image/cutout", big, nil))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for a body over %d bytes, got %d: %s", maxCutoutUploadBytes, rec.Code, rec.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatal("service called for an oversize body")
	}
}

func TestCutoutAPI_ServiceFailuresAreSoftErrors(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	cases := []struct {
		name   string
		status int
		answer []byte
		want   int
	}{
		{"service error", http.StatusInternalServerError, nil, http.StatusBadGateway},
		{"not a PNG", http.StatusOK, []byte("nope"), http.StatusBadGateway},
		{"nothing kept", http.StatusOK, emptyCutoutPNG(t), http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux, dp, _ := newAIAPITestDeps(t)
			dp.AI, _ = fakeRembg(t, tc.status, tc.answer)
			rec, _ := serveCutout(t, mux, multipartPhotoRequest(t, "/api/catalog/image/cutout", testPNGBytes(t), nil))
			if rec.Code != tc.want {
				t.Fatalf("expected %d, got %d: %s", tc.want, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"error"`) {
				t.Fatalf("expected the API error envelope, got %s", rec.Body.String())
			}
		})
	}
}

// A cashier lacks catalog_management: the POST is refused and the shop's
// AI server is never called (design §6 "Gate").
func TestCutoutAPI_CashierIsRefused(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, dp, _ := newAIAPITestDeps(t)
	dp.AuthSvc = auth.NewService(dp.Db)
	var calls *atomic.Int64
	dp.AI, calls = fakeRembg(t, http.StatusOK, cutoutPNG(t))
	authRepo := data.NewAuthRepo(dp.Db)
	cashierID, err := authRepo.CreateUser(t.Context(), "cutout-cashier", "Cashier", "cashier")
	if err != nil {
		t.Fatalf("create cashier: %v", err)
	}
	managerID, err := authRepo.CreateUser(t.Context(), "cutout-manager", "Manager", "manager")
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}

	req := auth.WithUser(multipartPhotoRequest(t, "/api/catalog/image/cutout", testPNGBytes(t), nil), auth.User{ID: cashierID, Role: "cashier"})
	rec, _ := serveCutout(t, mux, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier: expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatal("cashier request reached the background-removal service")
	}

	req = auth.WithUser(multipartPhotoRequest(t, "/api/catalog/image/cutout", testPNGBytes(t), nil), auth.User{ID: managerID, Role: "manager"})
	if rec, _ := serveCutout(t, mux, req); rec.Code != http.StatusOK {
		t.Fatalf("manager: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// Every value the palette offers parses to its own RGB, so the tile
// background is exactly the colour the item shows.
func TestHexColorMatchesThePalette(t *testing.T) {
	for _, pc := range catalogtypes.ItemColors() {
		got, err := hexColor(pc.Hex)
		if err != nil {
			t.Fatalf("palette colour %s (%s) does not parse: %v", pc.Hex, pc.Key, err)
		}
		want, _ := strconv.ParseUint(pc.Hex[1:], 16, 32)
		if got != (color.RGBA{uint8(want >> 16), uint8(want >> 8), uint8(want), 0xff}) {
			t.Fatalf("hexColor(%s) = %#v", pc.Hex, got)
		}
	}
	for _, bad := range []string{"", "#12345", "#gggggg", "0f172a", "#0f172a00"} {
		if _, err := hexColor(bad); err == nil {
			t.Fatalf("hexColor(%q) accepted a malformed value", bad)
		}
	}
}
