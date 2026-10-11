package pages

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/imaging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// maxCutoutUploadBytes caps the whole cutout request body. The client
// downscales to a 1024 px long edge first, so a real photo is far smaller;
// a larger body is a 413, never a silent truncation (docs repo
// architecture/ai-product-photo.md §6).
const maxCutoutUploadBytes = 10 << 20

// registerAICutout wires POST /api/catalog/image/cutout (ut-docs#3126,
// design §6): the photo's background is removed by the shop's own
// background-removal service and the result is composited onto a square
// product tile (imaging.SquareTile). Stateless — nothing is written to
// disk; the item/category/variant form saves the returned PNG through its
// existing upload. 404 when the image capability is off, so the picker
// (ut-docs#3127) never offers it; nothing here is on the sale path
// (ADR-0003).
func registerAICutout(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("POST /api/catalog/image/cutout", func(w http.ResponseWriter, r *http.Request) {
		// Every page this serves saves under catalog_management (item form,
		// categories since #2479, variants), so the call gates on it too: a
		// cashier gets a 403, not a free trip to the shop's AI server.
		if !canPerform(d, r, "catalog_management") {
			writeCutoutError(w, http.StatusForbidden, "catalog management permission required")
			return
		}
		svc := cutoutService(r.Context(), d)
		if !svc.CanCutout() {
			writeCutoutError(w, http.StatusNotFound, "background removal is not configured")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxCutoutUploadBytes)
		// The body is capped at the same size as the in-memory limit, so
		// nothing is ever spilled to a temp file.
		if err := r.ParseMultipartForm(maxCutoutUploadBytes); err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				writeCutoutError(w, http.StatusRequestEntityTooLarge, "photo larger than 10MB")
				return
			}
			writeCutoutError(w, http.StatusBadRequest, "invalid upload")
			return
		}
		bg, ok := cutoutBackground(r.FormValue("background"), r.FormValue("tile_color"))
		if !ok {
			writeCutoutError(w, http.StatusBadRequest, "invalid background")
			return
		}
		raw, mediaType, err := readCutoutPhoto(r)
		if err != nil {
			writeCutoutError(w, http.StatusBadRequest, err.Error())
			return
		}
		cut, err := svc.RemoveBackground(r.Context(), raw, mediaType)
		if err != nil {
			log.Printf("warning: background removal failed: %v", err)
			writeCutoutError(w, http.StatusBadGateway, "background removal unavailable")
			return
		}
		writeCutoutTile(w, cut, bg)
	})
}

// registerCutoutTile wires POST /api/catalog/image/tile (ut-docs#4137,
// ADR-0154 Decision 3): the built-in engine has already cut the photo out
// in the browser, so the till only squares it onto the chosen background
// with the one tile implementation, imaging.SquareTile. Same gate, body
// cap, header check and envelope as /cutout; the photo must be a PNG that
// carries transparency (anything else is no cut-out). Needs no service, so
// it is always on; stateless, nothing written, never on the sale path.
func registerCutoutTile(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("POST /api/catalog/image/tile", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "catalog_management") {
			writeCutoutError(w, http.StatusForbidden, "catalog management permission required")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxCutoutUploadBytes)
		if err := r.ParseMultipartForm(maxCutoutUploadBytes); err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				writeCutoutError(w, http.StatusRequestEntityTooLarge, "photo larger than 10MB")
				return
			}
			writeCutoutError(w, http.StatusBadRequest, "invalid upload")
			return
		}
		bg, ok := cutoutBackground(r.FormValue("background"), r.FormValue("tile_color"))
		if !ok {
			writeCutoutError(w, http.StatusBadRequest, "invalid background")
			return
		}
		raw, mediaType, err := readCutoutPhoto(r)
		if err != nil {
			writeCutoutError(w, http.StatusBadRequest, err.Error())
			return
		}
		if mediaType != "image/png" {
			writeCutoutError(w, http.StatusBadRequest, "cut-out must be a PNG with transparency")
			return
		}
		cut, _, err := imaging.DecodeBoundedFormats(raw, imaging.MaxPixels, map[string]bool{"png": true})
		if err != nil {
			writeCutoutError(w, http.StatusBadRequest, "cut-out must be a PNG with transparency")
			return
		}
		if o, isOpaquer := cut.(interface{ Opaque() bool }); isOpaquer && o.Opaque() {
			writeCutoutError(w, http.StatusBadRequest, "cut-out must be a PNG with transparency")
			return
		}
		writeCutoutTile(w, cut, bg)
	})
}

// writeCutoutTile squares a cut-out onto bg and answers it as a PNG — the
// shared tail of /cutout and /tile.
func writeCutoutTile(w http.ResponseWriter, cut image.Image, bg color.Color) {
	tile, err := imaging.SquareTile(cut, bg, imaging.TileSize)
	if errors.Is(err, imaging.ErrNoSubject) {
		writeCutoutError(w, http.StatusUnprocessableEntity, "no product found in the photo")
		return
	}
	if err != nil {
		writeCutoutError(w, http.StatusInternalServerError, "cannot build tile")
		return
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, tile); err != nil {
		writeCutoutError(w, http.StatusInternalServerError, "cannot build tile")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

// The engine a photo picker's "Remove background" runs on (ut-docs#4137,
// ADR-0154 Decision 2), rendered as the panel's data-cutout-engine.
const (
	// cutoutEngineService: the shop's own rembg service, through
	// POST /api/catalog/image/cutout.
	cutoutEngineService = "service"
	// cutoutEngineBuiltin: the on-device model vendored under
	// web/public/vendor/, run in the browser; the till only squares the
	// result through POST /api/catalog/image/tile.
	cutoutEngineBuiltin = "builtin"
)

// aiCutoutOffered is the one gate every photo picker asks before it renders
// the "Remove background" offer (ut-docs#3127, design §6 "Gate"): the
// viewer must hold the permission the page saves under — catalog_management
// for the item form, the category form and the variants panel alike, the
// same check both routes make. It returns the engine to use: the shop's
// rembg service when it is configured and valid, else the built-in
// on-device model (ut-docs#4137), which needs no setup. "" (no permission)
// renders the page exactly as before (no markup, no binding).
func aiCutoutOffered(r *http.Request, d *common.Deps) string {
	if !canPerform(d, r, "catalog_management") {
		return ""
	}
	if cutoutService(r.Context(), d).CanCutout() {
		return cutoutEngineService
	}
	return cutoutEngineBuiltin
}

// wireCutoutOffer hands aiCutoutOffered to the catalog subpackage, which
// renders the item form and the variants panel but cannot import this
// package (pages already imports catalog — a cycle). Called from Init
// before catalog.Register.
func wireCutoutOffer(d *common.Deps) {
	d.AICutoutOffered = func(r *http.Request) string { return aiCutoutOffered(r, d) }
}

// readCutoutPhoto returns the uploaded photo's bytes and media type. Only
// the header is decoded (image.DecodeConfig allocates no pixel buffer): a
// pixel bomb (ut-docs#1328/#1417) or a non-JPEG/PNG file is refused before
// anything is sent to the service, without paying for a full decode the
// handler would throw away — the till's own decode is of the service's
// answer, bounded in internal/bgremove.
func readCutoutPhoto(r *http.Request) ([]byte, string, error) {
	file, _, err := r.FormFile("photo")
	if err != nil {
		return nil, "", errors.New("photo file required")
	}
	defer file.Close()
	raw, err := io.ReadAll(file)
	if err != nil || len(raw) == 0 {
		return nil, "", errors.New("photo file required")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || !imaging.DefaultFormats()[format] || cfg.Width <= 0 || cfg.Height <= 0 ||
		int64(cfg.Width)*int64(cfg.Height) > imaging.MaxPixels {
		return nil, "", errors.New("photo must be a valid JPEG or PNG")
	}
	return raw, "image/" + format, nil
}

// cutoutBackground maps the form's background choice to the tile fill
// (design §5): "tile" (the default) fills with tileColor, which must be a
// palette colour (catalogtypes.ValidItemColor — never a free value) or
// empty for white; "white"; "transparent" (nil, no fill).
func cutoutBackground(choice, tileColor string) (color.Color, bool) {
	switch strings.TrimSpace(choice) {
	case "", "tile":
		tileColor = strings.TrimSpace(tileColor)
		if !catalogtypes.ValidItemColor(tileColor) {
			return nil, false
		}
		if tileColor == "" {
			return color.White, true
		}
		c, err := hexColor(tileColor)
		if err != nil {
			return nil, false
		}
		return c, true
	case "white":
		return color.White, true
	case "transparent":
		return nil, true
	}
	return nil, false
}

// hexColor parses a "#rrggbb" palette value. Callers pass only values
// catalogtypes.ValidItemColor accepted; anything else is an error, never a
// silent black tile.
func hexColor(s string) (color.Color, error) {
	b, err := hex.DecodeString(strings.TrimPrefix(s, "#"))
	if err != nil || len(b) != 3 || !strings.HasPrefix(s, "#") {
		return nil, fmt.Errorf("not a #rrggbb colour: %q", s)
	}
	return color.RGBA{R: b[0], G: b[1], B: b[2], A: 0xff}, nil
}

// writeCutoutError answers in the API envelope (`{"data": null, "error":
// {"message": …}}`), the shape the identify endpoints use.
func writeCutoutError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"data": nil, "error": map[string]string{"message": msg}})
}
