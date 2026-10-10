package bgremove

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Background removal (ut-docs#3126, architecture/ai-product-photo.md §4/§8 B).

func clearImageEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"UT_AI_IMAGE_PROVIDER", "UT_AI_IMAGE_ENDPOINT", "UT_AI_IMAGE_MODEL"} {
		t.Setenv(k, "")
	}
}

// pngBytes encodes img as PNG.
func pngBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// cutoutPNG is a 4x4 NRGBA PNG whose left half is transparent — what rembg
// answers for a product on a removed background.
func cutoutPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			a := uint8(0)
			if x >= 2 {
				a = 255
			}
			img.SetNRGBA(x, y, color.NRGBA{R: 200, G: 10, B: 10, A: a})
		}
	}
	return pngBytes(t, img)
}

// countingServer counts every request it receives, whatever the path.
func countingServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(cutoutPNG(t))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestFromEnvImage(t *testing.T) {
	clearImageEnv(t)
	if cfg := FromEnv(); cfg != (ImageConfig{}) {
		t.Fatalf("no image env: got %+v, want zero", cfg)
	}
	// An endpoint alone implies the self-hosted provider and the default model.
	t.Setenv("UT_AI_IMAGE_ENDPOINT", "http://rembg.local:7000")
	want := ImageConfig{Provider: "self_hosted", Endpoint: "http://rembg.local:7000", Model: DefaultImageModel}
	if cfg := FromEnv(); cfg != want {
		t.Fatalf("endpoint only: got %+v, want %+v", cfg, want)
	}
	t.Setenv("UT_AI_IMAGE_MODEL", "u2netp")
	t.Setenv("UT_AI_IMAGE_PROVIDER", "Self_Hosted")
	want = ImageConfig{Provider: "self_hosted", Endpoint: "http://rembg.local:7000", Model: "u2netp"}
	if cfg := FromEnv(); cfg != want {
		t.Fatalf("explicit env: got %+v, want %+v", cfg, want)
	}
}

func TestImageModelAllowList(t *testing.T) {
	for _, m := range []string{"birefnet-general-lite", "u2netp"} {
		if !ImageModelAllowed(m) {
			t.Errorf("%q must be allowed", m)
		}
	}
	for _, m := range []string{"", "bria-rmbg", "bria-rmbg-2.0", "BiRefNet-General-Lite", "u2net", "isnet-general-use", "birefnet-general", " u2netp"} {
		if ImageModelAllowed(m) {
			t.Errorf("%q must NOT be allowed", m)
		}
	}
	if !ImageModelAllowed(DefaultImageModel) {
		t.Fatal("the default model must be on the allow-list")
	}
}

func TestCanCutout(t *testing.T) {
	srv, _ := countingServer(t)
	var nilSvc *Service
	if nilSvc.CanCutout() {
		t.Fatal("nil service must not cut out")
	}
	if New(ImageConfig{}).CanCutout() {
		t.Fatal("no image endpoint: CanCutout must be false")
	}
	if New(ImageConfig{Provider: "self_hosted"}).CanCutout() {
		t.Fatal("self_hosted with no endpoint: CanCutout must be false")
	}
	if !New(ImageConfig{Provider: "self_hosted", Endpoint: srv.URL}).CanCutout() {
		t.Fatal("self_hosted with an endpoint: CanCutout must be true")
	}
	if !New(ImageConfig{Provider: "self_hosted", Endpoint: srv.URL, Model: "u2netp"}).CanCutout() {
		t.Fatal("self_hosted with an allowed model: CanCutout must be true")
	}
}

// ADR-0126 §7 fail-safe: only the exact "self_hosted" builds the rembg
// adapter; anything else is off and never makes a call.
func TestCutoutUnknownProviderIsOffWithNoHTTPCall(t *testing.T) {
	srv, hits := countingServer(t)
	for _, p := range []string{"", "Self_Hosted", "self-hosted", "claude", "openai", "remove.bg", "hosted", "photoroom"} {
		svc := New(ImageConfig{Provider: p, Endpoint: srv.URL, Model: DefaultImageModel})
		if svc.CanCutout() {
			t.Errorf("provider %q: CanCutout must be false", p)
		}
		if _, err := svc.RemoveBackground(t.Context(), []byte("x"), "image/png"); err == nil {
			t.Errorf("provider %q: RemoveBackground must error", p)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("unknown providers made %d HTTP calls, want 0", n)
	}
}

func TestCutoutModelOffAllowListIsOff(t *testing.T) {
	srv, hits := countingServer(t)
	for _, m := range []string{"bria-rmbg", "bria-rmbg-2.0", "isnet-general-use", "something-new"} {
		svc := New(ImageConfig{Provider: "self_hosted", Endpoint: srv.URL, Model: m})
		if svc.CanCutout() {
			t.Errorf("model %q: CanCutout must be false", m)
		}
		_, _ = svc.RemoveBackground(t.Context(), []byte("x"), "image/png")
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("off-list models made %d HTTP calls, want 0", n)
	}
	// Empty model means the default, which is allowed.
	if !New(ImageConfig{Provider: "self_hosted", Endpoint: srv.URL}).CanCutout() {
		t.Fatal("empty model must default to an allowed model")
	}
}

func TestCutoutEndpointMustBeHTTP(t *testing.T) {
	for _, ep := range []string{"ftp://rembg.local", "file:///etc/passwd", "rembg.local:7000", "://bad", "http://", "javascript:alert(1)", "http://user:pw@rembg.local", "http://rembg.local/?x=1", " http://rembg.local"} {
		if New(ImageConfig{Provider: "self_hosted", Endpoint: ep}).CanCutout() {
			t.Errorf("endpoint %q: CanCutout must be false", ep)
		}
	}
	for _, ep := range []string{"http://192.168.1.20:7000", "https://rembg.example/", "http://rembg.local:7000/base"} {
		if !New(ImageConfig{Provider: "self_hosted", Endpoint: ep}).CanCutout() {
			t.Errorf("endpoint %q: CanCutout must be true", ep)
		}
	}
}

// Pins the request shape of rembg's `rembg s` server (rembg/commands/
// s_command.py, POST /api/remove: `file: bytes = File(...)` and
// `model: str = Form(default="bria-rmbg")`). The model is ALWAYS sent so
// rembg's bria-rmbg default can never run by accident.
func TestRembgAdapterSendsFileAndModel(t *testing.T) {
	photo := []byte("\xff\xd8\xff fake jpeg bytes")
	var gotPath, gotMethod, gotModel, gotFileCT string
	var gotFile []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
		}
		gotModel = r.FormValue("model")
		f, fh, err := r.FormFile("file")
		if err != nil {
			t.Errorf("file field: %v", err)
		} else {
			gotFile, _ = io.ReadAll(f)
			gotFileCT = fh.Header.Get("Content-Type")
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(cutoutPNG(t))
	}))
	defer srv.Close()

	for _, model := range []string{"", "u2netp"} {
		svc := New(ImageConfig{Provider: "self_hosted", Endpoint: srv.URL + "/", Model: model})
		img, err := svc.RemoveBackground(t.Context(), photo, "image/jpeg")
		if err != nil {
			t.Fatalf("model %q: %v", model, err)
		}
		if b := img.Bounds(); b.Dx() != 4 || b.Dy() != 4 {
			t.Fatalf("bounds %v", b)
		}
		want := model
		if want == "" {
			want = DefaultImageModel
		}
		if gotMethod != http.MethodPost || gotPath != "/api/remove" {
			t.Fatalf("got %s %s, want POST /api/remove", gotMethod, gotPath)
		}
		if gotModel != want {
			t.Fatalf("model field %q, want %q", gotModel, want)
		}
		if !bytes.Equal(gotFile, photo) || gotFileCT != "image/jpeg" {
			t.Fatalf("file field: %q (%s), want the photo as image/jpeg", gotFile, gotFileCT)
		}
	}
}

func TestRembgAdapterTimeout(t *testing.T) {
	if c := newRembgCutter("http://x", DefaultImageModel); c.timeout != cutoutTimeout || cutoutTimeout != 90*time.Second {
		t.Fatalf("timeout %v / cutoutTimeout %v, want 90s", c.timeout, cutoutTimeout)
	}
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	c := newRembgCutter(srv.URL, DefaultImageModel)
	c.timeout = 50 * time.Millisecond
	start := time.Now()
	_, err := c.removeBackground(context.Background(), []byte("x"), "image/png")
	if err == nil {
		t.Fatal("a hung service must time out")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v, timeout not honoured", d)
	}
}

func TestRembgAdapterRejectsBadAnswers(t *testing.T) {
	opaque := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for i := range opaque.Pix {
		opaque.Pix[i] = 255
	}
	gray := image.NewGray(image.Rect(0, 0, 4, 4))
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, opaque, nil); err != nil {
		t.Fatal(err)
	}
	big := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, maxCutoutBytes)...)

	cases := map[string]struct {
		status int
		body   []byte
		want   string
	}{
		"opaque_rgba":   {200, pngBytes(t, opaque), "alpha"},
		"gray_no_alpha": {200, pngBytes(t, gray), "alpha"},
		"jpeg":          {200, jpg.Bytes(), "PNG"},
		"text":          {200, []byte("not an image"), "PNG"},
		"oversize":      {200, big, "20"},
		"server_error":  {500, cutoutPNG(t), "500"},
		"bad_request":   {400, []byte(`{"detail":"x"}`), "400"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write(tc.body)
			}))
			defer srv.Close()
			_, err := newRembgCutter(srv.URL, DefaultImageModel).removeBackground(t.Context(), []byte("x"), "image/png")
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestRembgAdapterDoesNotFollowRedirects(t *testing.T) {
	var targetHits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		_, _ = w.Write(cutoutPNG(t))
	}))
	defer target.Close()
	for _, code := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/api/remove", code)
		}))
		_, err := newRembgCutter(srv.URL, DefaultImageModel).removeBackground(t.Context(), []byte("x"), "image/png")
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), "redirect") {
			t.Fatalf("status %d: err %v, want a redirect error", code, err)
		}
	}
	if n := targetHits.Load(); n != 0 {
		t.Fatalf("redirect target was hit %d times, want 0", n)
	}
}

func TestRemoveBackgroundRequiresPhoto(t *testing.T) {
	srv, hits := countingServer(t)
	svc := New(ImageConfig{Provider: "self_hosted", Endpoint: srv.URL})
	if _, err := svc.RemoveBackground(t.Context(), nil, "image/png"); err == nil {
		t.Fatal("empty photo must error")
	}
	if hits.Load() != 0 {
		t.Fatal("empty photo must not reach the service")
	}
}

// A caller-supplied media type is copied into a MIME part header, so only
// image/png and image/jpeg pass; anything else (a CR/LF injection
// included) must not add parts or override the model field.
func TestRembgFormSanitisesMediaType(t *testing.T) {
	for _, mt := range []string{"image/png", "image/jpeg", "", "text/html", "image/png\r\n\r\n--x\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\nbria-rmbg"} {
		body, ctype, err := rembgForm([]byte("photo"), mt, DefaultImageModel)
		if err != nil {
			t.Fatalf("%q: %v", mt, err)
		}
		_, params, err := mime.ParseMediaType(ctype)
		if err != nil {
			t.Fatalf("%q: content type %q: %v", mt, ctype, err)
		}
		r := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		var names []string
		for {
			p, err := r.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%q: %v", mt, err)
			}
			names = append(names, p.FormName())
			if p.FormName() == "file" {
				want := mt
				if mt != "image/png" && mt != "image/jpeg" {
					want = "application/octet-stream"
				}
				if got := p.Header.Get("Content-Type"); got != want {
					t.Errorf("%q: file Content-Type = %q, want %q", mt, got, want)
				}
			}
			if p.FormName() == "model" {
				v, _ := io.ReadAll(p)
				if string(v) != DefaultImageModel {
					t.Errorf("%q: model = %q", mt, v)
				}
			}
		}
		if len(names) != 2 || names[0] != "file" || names[1] != "model" {
			t.Errorf("%q: parts = %v, want [file model]", mt, names)
		}
	}
}
