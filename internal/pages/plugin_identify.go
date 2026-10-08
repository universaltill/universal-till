package pages

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pluginview"
)

// The sell screen's camera-identify seam (ADR-0121 §7 "Core-owned
// suggestion seam", ut-docs#3873; format: ut-docs reference/plugin-views.md).
// When an active plugin answers catalog.identify, the sell screen shows
// core's own identify button and overlay (instead of the built-in AI one,
// which moves out in ut-docs#2851). The overlay posts one photo here; core
// stages it as an upload handle and runs catalog.identify as a job (§8,
// startPluginJob) — never on the sale path, never blocking it. The overlay
// polls GET ?_job=<id>; the plugin's answer is a document validated for the
// SeamSellIdentify seam (text, notice, suggestions with add_to_basket
// only), and each suggestion renders as a button posting its SKU through
// the normal /api/pos/scan path. Closing the overlay stops the poll, so the
// job is abandoned (pluginJobUnpolledTTL) and a late result dropped.

const (
	identifyEvent = "catalog.identify"
	identifyRoute = "/api/pos/identify/plugin"
	// identifyMaxPhotoBytes caps the photo (the overlay sends a ≤1024 px
	// JPEG, far below it).
	identifyMaxPhotoBytes = 8 << 20
	// identifyMultipartSlack bounds the multipart framing on top of it.
	identifyMultipartSlack = 64 << 10
	identifyPhotoField     = "photo"
)

// identifyPhotoTypes: the sniffed types a photo may have.
var identifyPhotoTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true}

// identifyPhotoNames: the filename the plugin is told, by sniffed type.
var identifyPhotoNames = map[string]string{"image/jpeg": "capture.jpg", "image/png": "capture.png", "image/webp": "capture.webp"}

var identifyFiles = []string{filepath.Join("web", "ui", "partials", "identify_suggestions.html")}

// identifyPluginID is the plugin answering catalog.identify: the lexically
// first subscriber holding events:receive, "" if none. A var so tests can
// stub it.
var identifyPluginID = func(ctx context.Context, d *common.Deps) string {
	ids := plugins.SharedBus(d.Db).SubscriberIDs(identifyEvent)
	sort.Strings(ids)
	repo := data.NewPluginRepo(d.Db)
	for _, id := range ids {
		// A plain read, not plugins.CheckPermission: this runs on every
		// sell-page render, and skipping a plugin is no denial worth an
		// audit row each time. The ask itself still checks (and audits).
		if granted, exists, err := repo.CheckPermission(ctx, id, "events:receive"); err == nil && exists && granted {
			return id
		}
	}
	return ""
}

// identifyEntry is the synthetic entry the seam's jobs run under: the
// registry keys a job by plugin and route, so a plugin page's job id can
// never be polled here, nor this seam's on a plugin page.
func identifyEntry(pluginID string) data.PageEntryRow {
	return data.PageEntryRow{PluginID: pluginID, Route: identifyRoute}
}

// identifyNotice is what identify_notice renders.
type identifyNotice struct{ Key, Level string }

// identifyResult is what identify_result renders.
type identifyResult struct {
	Texts   []pluginview.ViewComponent // text and notice components, in order
	Matches []identifyMatch
}

type identifyMatch struct {
	Label, Detail, SKU, ImageURL string
	Qty                          int
}

var (
	errIdentifyBadUpload = errors.New("identify: not exactly one photo part")
	errIdentifyNotImage  = errors.New("identify: the photo is not a JPEG, PNG or WebP image")
	errIdentifyTooLarge  = errors.New("identify: photo too large")
)

func registerPluginIdentify(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("POST /api/pos/identify/plugin", func(w http.ResponseWriter, r *http.Request) {
		servePluginIdentifyStart(w, r, d)
	})
	mux.HandleFunc("GET /api/pos/identify/plugin", func(w http.ResponseWriter, r *http.Request) {
		servePluginIdentifyPoll(w, r, d)
	})
}

func renderIdentify(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	httpx.RenderWith(identifyFiles, httpx.FuncsFor(httpx.ResolveLocale(w, r)))(name, data)(w, r)
}

func servePluginIdentifyStart(w http.ResponseWriter, r *http.Request, d *common.Deps) {
	pluginID := identifyPluginID(r.Context(), d)
	if pluginID == "" {
		renderIdentify(w, r, http.StatusNotFound, "identify_notice", identifyNotice{"plugin.view.unavailable", "warn"})
		return
	}
	up, err := readIdentifyPhoto(w, r, pluginID)
	if err != nil {
		logging.L().Infof("plugin identify %s: %v", pluginID, err)
		status := http.StatusBadRequest
		switch {
		case errors.Is(err, errIdentifyTooLarge):
			status = http.StatusRequestEntityTooLarge
		case errors.Is(err, errPluginUploadsBusy):
			renderIdentify(w, r, http.StatusTooManyRequests, "identify_notice", identifyNotice{"plugin.job.busy", "warn"})
			return
		case errors.Is(err, errPluginUploadStage):
			status = http.StatusServiceUnavailable
		}
		renderIdentify(w, r, status, "identify_notice", identifyNotice{"ai.identify.error", "warn"})
		return
	}
	locale := httpx.ResolveLocale(w, r)
	vctx := pluginview.Context{PluginID: pluginID, OwnKeys: pluginOwnLocaleKeys(d, pluginID), Seam: pluginview.SeamSellIdentify}
	payload := map[string]any{"upload_handles": []pluginUpload{up}, "locale": locale}
	tokens := []string{up.Handle}
	id, err := startPluginJob(r.Context(), d, identifyEntry(pluginID), identifyEvent, payload, vctx, tokens)
	if errors.Is(err, errPluginJobBusy) && pluginJobs.cancelRunning(pluginID, identifyRoute) > 0 {
		// A new capture supersedes this seam's earlier job: the overlay
		// shows one result at a time and stopped polling the old one
		// (Retake, or Close while its photo was still uploading). Without
		// this a tablet (one job per plugin) refused the next Capture until
		// the old job's unpolled TTL ran out. A job the plugin runs on its
		// own page is never cancelled here: that stays busy.
		id, err = startPluginJob(r.Context(), d, identifyEntry(pluginID), identifyEvent, payload, vctx, tokens)
	}
	if err != nil {
		// Only a nil error hands the photo to the job.
		plugins.ReleaseUploads(pluginID, tokens)
		if errors.Is(err, errPluginJobBusy) {
			renderIdentify(w, r, http.StatusTooManyRequests, "identify_notice", identifyNotice{"plugin.job.busy", "warn"})
			return
		}
		logging.L().Warnf("plugin identify %s: start job: %v", pluginID, err)
		renderIdentify(w, r, http.StatusBadGateway, "identify_notice", identifyNotice{"ai.identify.error", "warn"})
		return
	}
	renderIdentify(w, r, http.StatusOK, "identify_poll", &pluginJobPoll{ID: id})
}

// readIdentifyPhoto streams the post's one file part, "photo", to a temp
// file (never ParseMultipartForm, which would spool a second copy) and
// stages it for pluginID. On a nil error the caller owns the upload.
func readIdentifyPhoto(w http.ResponseWriter, r *http.Request, pluginID string) (pluginUpload, error) {
	if !isMultipartForm(r) {
		return pluginUpload{}, errIdentifyBadUpload
	}
	r.Body = http.MaxBytesReader(w, r.Body, identifyMaxPhotoBytes+identifyMultipartSlack)
	mr, err := r.MultipartReader()
	if err != nil {
		return pluginUpload{}, fmt.Errorf("read multipart: %w", err)
	}
	var up pluginUpload
	staged := false
	done := false
	defer func() {
		if staged && !done {
			plugins.ReleaseUploads(pluginID, []string{up.Handle})
		}
	}()
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				return pluginUpload{}, errIdentifyTooLarge
			}
			return pluginUpload{}, fmt.Errorf("read multipart: %w", err)
		}
		_, isFile := partFileName(part.Header.Get("Content-Disposition"))
		if staged || !isFile || part.FormName() != identifyPhotoField {
			_ = part.Close()
			return pluginUpload{}, errIdentifyBadUpload
		}
		up, err = stageIdentifyPhoto(part, pluginID)
		_ = part.Close()
		if err != nil {
			return pluginUpload{}, err
		}
		staged = true
	}
	if !staged {
		return pluginUpload{}, errIdentifyBadUpload
	}
	done = true
	return up, nil
}

// stageIdentifyPhoto sniffs, caps and stages one photo part.
func stageIdentifyPhoto(part io.Reader, pluginID string) (pluginUpload, error) {
	head := make([]byte, pluginUploadSniffBytes)
	n, rerr := io.ReadFull(part, head)
	if rerr != nil && !errors.Is(rerr, io.EOF) && !errors.Is(rerr, io.ErrUnexpectedEOF) {
		var tooBig *http.MaxBytesError
		if errors.As(rerr, &tooBig) {
			return pluginUpload{}, errIdentifyTooLarge
		}
		return pluginUpload{}, fmt.Errorf("read photo: %w", rerr)
	}
	head = head[:n]
	if n == 0 {
		return pluginUpload{}, errIdentifyBadUpload
	}
	ctype := http.DetectContentType(head)
	if !identifyPhotoTypes[ctype] {
		return pluginUpload{}, fmt.Errorf("%w (sniffed %s)", errIdentifyNotImage, ctype)
	}
	tmp, err := os.CreateTemp("", "ut-view-upload-*.upload")
	if err != nil {
		logging.L().Errorf("plugin identify %s: create temp file: %v", pluginID, err)
		return pluginUpload{}, errPluginUploadStage
	}
	path := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(path)
		}
	}()
	written, werr := tmp.Write(head)
	var copied int64
	if werr == nil {
		copied, werr = io.Copy(tmp, io.LimitReader(part, identifyMaxPhotoBytes+1-int64(n)))
	}
	cerr := tmp.Close()
	size := int64(written) + copied
	if werr != nil || cerr != nil {
		var tooBig *http.MaxBytesError
		if errors.As(werr, &tooBig) {
			return pluginUpload{}, errIdentifyTooLarge
		}
		logging.L().Errorf("plugin identify %s: stage: write=%v close=%v", pluginID, werr, cerr)
		return pluginUpload{}, errPluginUploadStage
	}
	if size > identifyMaxPhotoBytes {
		return pluginUpload{}, errIdentifyTooLarge
	}
	tok, err := plugins.StageUpload(pluginID, path)
	if err != nil {
		return pluginUpload{}, errPluginUploadsBusy
	}
	keep = true
	return pluginUpload{
		Field:       identifyPhotoField,
		Handle:      tok,
		Filename:    identifyPhotoNames[ctype],
		Size:        size,
		ContentType: ctype,
	}, nil
}

func servePluginIdentifyPoll(w http.ResponseWriter, r *http.Request, d *common.Deps) {
	locale := httpx.ResolveLocale(w, r)
	jobID := r.URL.Query().Get(pluginJobParam)
	// The job names its own plugin: a poll every second never re-resolves
	// the identify plugin (a DB read, and an audit row per skipped plugin).
	var snap pluginJobSnapshot
	ok := false
	if pluginID, found := pluginJobs.owner(jobID, identifyRoute); found {
		snap, ok = pluginJobs.poll(jobID, pluginID, identifyRoute, r.Method != http.MethodHead)
	}
	switch {
	case !ok:
		renderIdentify(w, r, http.StatusOK, "identify_notice", identifyNotice{"plugin.job.gone", "warn"})
	case snap.state == pluginJobRunning && snap.unchanged && r.Header.Get("HX-Request") == "true":
		// Nothing new: no swap, no re-announcement of the live region.
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
	case snap.state == pluginJobRunning:
		p := &pluginJobPoll{ID: jobID, HasPct: snap.pct >= 0, Pct: snap.pct}
		if snap.key != "" {
			p.Message = httpx.T(locale, snap.key)
		}
		renderIdentify(w, r, http.StatusOK, "identify_poll", p)
	case snap.state == pluginJobFailed || snap.doc == nil:
		// A refused answer (a redirect, a form, apply_fields…) failed the
		// job; the reason is in the log.
		renderIdentify(w, r, http.StatusOK, "identify_notice", identifyNotice{"ai.identify.error", "warn"})
	default:
		renderIdentify(w, r, http.StatusOK, "identify_result", identifyResultFor(r.Context(), d, snap.doc.Prepare(locale)))
	}
}

// identifyResultFor builds the overlay's result: the document's text and
// notices, and one button per suggestion, with the catalog photo of the
// item its SKU resolves to (the same resolution /api/pos/scan makes).
func identifyResultFor(ctx context.Context, d *common.Deps, v pluginview.View) identifyResult {
	var res identifyResult
	repo := data.NewPOSRepo(d.Db)
	for _, c := range v.Components {
		switch c.Type {
		case "text", "notice":
			res.Texts = append(res.Texts, c)
		case "suggestions":
			for _, s := range c.Suggestions {
				if s.SKU == "" {
					continue // apply_fields: never at this seam (refused)
				}
				m := identifyMatch{Label: s.Label, Detail: s.Detail, SKU: s.SKU, Qty: s.Qty}
				if line, ok := repo.ResolveShortcutLine(ctx, s.SKU); ok {
					m.ImageURL = line.ImageURL
				}
				res.Matches = append(res.Matches, m)
			}
		}
	}
	return res
}
