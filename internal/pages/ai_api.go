package pages

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/ai"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/imaging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"golang.org/x/image/draw"
)

const (
	maxIdentifyPhotoBytes = 4 << 20
	maxReferenceImages    = 60
	maxAIRefsPerItem      = 5
)

// itemAssetDir is the stable per-user data dir item-image tree (uploads
// land here via internal/pages/catalog/handlers.go's paths.Data(...)
// calls) -- NOT a cwd-relative path. A plain "web/public/assets/items"
// constant here would only resolve when the process's cwd happens to be
// the repo checkout, silently finding zero images once installed
// (docs/code-reviews/2026-07-29-coverage-batch-11-sync-assets-regression.md
// fixed the identical bug class in sync_assets.go).
func itemAssetDir() string { return paths.Data("public", "assets", "items") }

// registerAIAPI wires the camera-identify endpoints (docs repo:
// architecture/ai-integration.md §g). Strictly assistive: the endpoints 404
// when no UT_AI_API_KEY is configured, and the sale screen never waits on
// them — barcode scan and manual search stay the primary path (ADR-0003).
func registerAIAPI(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)
	catRepo := data.NewCatalogRepo(d.Db)

	writeJSON := func(w http.ResponseWriter, status int, data any, errMsg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		var errField any
		if errMsg != "" {
			errField = map[string]string{"message": errMsg}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "error": errField})
	}

	// decodedPhoto carries both the raw uploaded bytes (still needed
	// as-is for the AI backend request — svc.Identify below sends the
	// original photo, not a re-encode) and the already-decoded image
	// (needed by the confirm handler to persist a fresh re-encode). One
	// struct, one decode: readPhoto's bounded decode is the only full
	// image.Decode either handler needs — a second call would double
	// peak memory on the same file for no benefit (ut-docs#1417 review).
	type decodedPhoto struct {
		Raw       []byte
		MediaType string
		Img       image.Image
	}

	readPhoto := func(r *http.Request) (decodedPhoto, error) {
		file, _, err := r.FormFile("photo")
		if err != nil {
			return decodedPhoto{}, fmt.Errorf("photo file required")
		}
		defer file.Close()
		raw, err := io.ReadAll(io.LimitReader(file, maxIdentifyPhotoBytes+1))
		if err != nil || len(raw) == 0 || len(raw) > maxIdentifyPhotoBytes {
			return decodedPhoto{}, fmt.Errorf("photo missing or larger than 4MB")
		}
		// Bounded decode (ut-docs#1417): the raw stdlib image.Decode this
		// used to call has no dimension check, so a small, well-formed
		// file declaring an enormous width×height allocates a decode
		// buffer for that declared size regardless — the exact pixel-bomb
		// class ut-docs#1328 fixed at the catalog upload call sites.
		img, format, err := imaging.DecodeBoundedFormats(raw, imaging.MaxPixels, imaging.DefaultFormats())
		if err != nil {
			return decodedPhoto{}, fmt.Errorf("photo must be a valid JPEG or PNG")
		}
		return decodedPhoto{Raw: raw, MediaType: "image/" + format, Img: img}, nil
	}

	mux.HandleFunc("POST /api/pos/identify", func(w http.ResponseWriter, r *http.Request) {
		svc := aiService(r.Context(), d)
		if !svc.Enabled() {
			writeJSON(w, http.StatusNotFound, nil, "ai identify is not configured")
			return
		}
		if err := r.ParseMultipartForm(maxIdentifyPhotoBytes); err != nil {
			writeJSON(w, http.StatusBadRequest, nil, "invalid upload")
			return
		}
		photo, err := readPhoto(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, nil, err.Error())
			return
		}

		rows, err := posRepo.SearchActiveItems(r.Context(), "", 0, 500)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, nil, "catalog unavailable")
			return
		}
		items := make([]ai.CatalogItem, 0, len(rows))
		type itemMeta struct{ SKU, Name string }
		meta := make(map[string]itemMeta, len(rows))
		for _, it := range rows {
			items = append(items, ai.CatalogItem{ID: it.ID, SKU: it.SKU, Name: it.Name})
			meta[it.ID] = itemMeta{SKU: it.SKU, Name: it.Name}
		}
		refs := loadReferenceImages(items)

		res, err := svc.Identify(r.Context(), photo.Raw, photo.MediaType, items, refs)
		if err != nil {
			log.Printf("warning: ai identify failed: %v", err)
			writeJSON(w, http.StatusBadGateway, nil, "identification unavailable")
			return
		}

		locale := httpx.ResolveLocale(w, r)
		type matchOut struct {
			ItemID       string `json:"item_id"`
			SKU          string `json:"sku"`
			Name         string `json:"name"`
			PriceMinor   int64  `json:"price_minor"`
			PriceDisplay string `json:"price_display"`
			Confidence   string `json:"confidence"`
			ThumbURL     string `json:"thumb_url,omitempty"`
		}
		matches := make([]matchOut, 0, len(res.Matches))
		for _, m := range res.Matches {
			mi := meta[m.ItemID]
			price, perr := posRepo.ResolveCurrentPrice(r.Context(), m.ItemID, "")
			if perr != nil {
				price = 0
			}
			out := matchOut{
				ItemID:       m.ItemID,
				SKU:          mi.SKU,
				Name:         mi.Name,
				PriceMinor:   price,
				PriceDisplay: httpx.FormatMoney(price, locale),
				Confidence:   m.Confidence,
			}
			if _, err := os.Stat(filepath.Join(itemAssetDir(), m.ItemID, "thumb.png")); err == nil {
				out.ThumbURL = "/public/assets/items/" + m.ItemID + "/thumb.png"
			}
			matches = append(matches, out)
		}

		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "ai", "-", "ai_identify",
			map[string]any{"matches": len(matches), "suggested_name": res.SuggestedName}, now, "")

		writeJSON(w, http.StatusOK, map[string]any{
			"matches":        matches,
			"suggested_name": res.SuggestedName,
		}, "")
	})

	// Confirming a match saves the till photo as an extra reference image for
	// that item (role "ai_ref"), so the shop's recognizer improves with use —
	// per-shop, no fine-tuning, nothing shared between shops.
	mux.HandleFunc("POST /api/pos/identify/confirm", func(w http.ResponseWriter, r *http.Request) {
		svc := aiService(r.Context(), d)
		if !svc.Enabled() {
			writeJSON(w, http.StatusNotFound, nil, "ai identify is not configured")
			return
		}
		if err := r.ParseMultipartForm(maxIdentifyPhotoBytes); err != nil {
			writeJSON(w, http.StatusBadRequest, nil, "invalid upload")
			return
		}
		itemID := strings.TrimSpace(r.Form.Get("item_id"))
		if itemID == "" || strings.ContainsAny(itemID, "/\\.") {
			writeJSON(w, http.StatusBadRequest, nil, "valid item_id required")
			return
		}
		if ok, err := catRepo.ItemExists(r.Context(), itemID); err != nil || !ok {
			writeJSON(w, http.StatusNotFound, nil, "item not found")
			return
		}
		photo, err := readPhoto(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, nil, err.Error())
			return
		}
		// Re-encode to a fresh PNG rather than persisting the raw upload
		// bytes verbatim (ut-docs#1417) — same convention the catalog
		// image-upload handlers already use, reusing the image readPhoto
		// already decoded (not a second decode of the same bytes). This
		// protects every file written FROM NOW ON: a hostile file could
		// never reach disk in the first place (readPhoto's bounded decode
		// already rejected it before this point), and re-encoding also
		// normalizes away anything a decode+encode round trip would drop
		// (e.g. trailing bytes past the real image data). It does NOT
		// retroactively touch any file already on disk from before this
		// fix shipped — those still exist as whatever the old client
		// uploaded, `.jpg` or `.png` alike (refImageNames matches both
		// extensions). loadRefJPEG's own bounded decode is what protects
		// those pre-existing files on every future identify call.
		dir := filepath.Join(itemAssetDir(), itemID, "ai_ref")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			writeJSON(w, http.StatusInternalServerError, nil, "cannot store reference image")
			return
		}
		name := fmt.Sprintf("%d.png", time.Now().UnixNano())
		outPath := filepath.Join(dir, name)
		out, err := os.Create(outPath)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, nil, "cannot store reference image")
			return
		}
		encErr := png.Encode(out, photo.Img)
		closeErr := out.Close()
		if encErr != nil || closeErr != nil {
			// Don't leave a partial/corrupt file behind (ut-docs#1417
			// review): unlike the catalog handlers' fixed thumb.png name
			// (which self-heals on the next upload), this filename is a
			// unique nanosecond timestamp — left in place, it would
			// silently poison every future identify call for this item
			// (latestAIRef picks the newest name, and loadReferenceImages
			// has no fallback to thumb.png when that decode then fails).
			_ = os.Remove(outPath)
			writeJSON(w, http.StatusInternalServerError, nil, "cannot store reference image")
			return
		}
		pruneAIRefs(dir)

		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "ai", itemID, "ai_identify_confirmed",
			map[string]any{"item_id": itemID}, now, "")
		writeJSON(w, http.StatusOK, map[string]any{"saved": true}, "")
	})
}

// loadReferenceImages picks one reference photo per item — the latest
// cashier-confirmed ai_ref when present, else the catalog thumbnail — capped
// and downscaled so the request stays small, cheap and cacheable.
func loadReferenceImages(items []ai.CatalogItem) []ai.RefImage {
	refs := make([]ai.RefImage, 0, maxReferenceImages)
	for _, it := range items {
		if len(refs) >= maxReferenceImages {
			break
		}
		path := filepath.Join(itemAssetDir(), it.ID, "thumb.png")
		if p, _, ok := latestAIRef(filepath.Join(itemAssetDir(), it.ID, "ai_ref")); ok {
			path = p
		}
		if data, ok := loadRefJPEG(path); ok {
			refs = append(refs, ai.RefImage{ItemID: it.ID, MediaType: "image/jpeg", Data: data})
		}
	}
	return refs
}

// loadRefJPEG reads an image and re-encodes it as a small JPEG (max 160px
// long edge). Full-size thumbs would put megabytes of base64 on every
// identify request for no recognition benefit.
//
// Decoded via the bounded internal/imaging helper (ut-docs#1417), not raw
// image.Decode: this file re-runs on EVERY identify call, for up to
// maxReferenceImages items, so an unbounded decode here turns one hostile
// file already on disk (from before this fix shipped, or from any other
// path that once wrote to this tree) into a repeatable OOM rather than a
// one-time risk — the confirm handler re-encoding at write time only
// protects files written after this fix ships, not ones already present.
func loadRefJPEG(path string) ([]byte, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	src, err := imaging.Decode(raw)
	if err != nil {
		return nil, false
	}
	const maxEdge = 160
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > maxEdge || h > maxEdge {
		scale := float64(maxEdge) / float64(max(w, h))
		dst := image.NewRGBA(image.Rect(0, 0, int(float64(w)*scale), int(float64(h)*scale)))
		draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
		src = dst
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: 70}); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

func latestAIRef(dir string) (path, mediaType string, ok bool) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return "", "", false
	}
	names := refImageNames(entries)
	if len(names) == 0 {
		return "", "", false
	}
	newest := names[len(names)-1]
	media := "image/jpeg"
	if strings.HasSuffix(newest, ".png") {
		media = "image/png"
	}
	return filepath.Join(dir, newest), media, true
}

// pruneAIRefs keeps only the newest reference photos so the folder (and the
// identify-request payload) can't grow without bound.
func pruneAIRefs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	names := refImageNames(entries)
	for len(names) > maxAIRefsPerItem {
		_ = os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
}

// refImageNames returns image filenames sorted oldest-first (names are
// nanosecond timestamps, so lexical order is chronological).
func refImageNames(entries []os.DirEntry) []string {
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".jpg") || strings.HasSuffix(e.Name(), ".png") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}
