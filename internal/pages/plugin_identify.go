package pages

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pluginview"
	"github.com/universaltill/universal-till/internal/stagedupload"
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
//
// A suggestion may name a thumbnail: a blob in the answering plugin's own
// store (blob:own, ut-docs#3957). Core shows it only if the blob is a real
// JPEG, PNG or WebP image there now; otherwise the button keeps the catalog
// item's photo (or none) and the list never breaks. The image URL core
// emits is signed (identifyThumbURL) with a key that lives only in this
// process, so the thumbnail route serves exactly the (plugin, blob) pairs
// core itself put on a page — never a blob anyone names by hand, never
// another plugin's, and nothing after a restart.

const (
	identifyEvent = "catalog.identify"
	identifyRoute = "/api/pos/identify/plugin"
	// identifyThumbRoute serves a suggestion's plugin-blob thumbnail.
	identifyThumbRoute = "/api/pos/identify/plugin/thumb"
	// identifyThumbMaxBytes caps a thumbnail blob: a list icon, not a photo.
	identifyThumbMaxBytes = 1 << 20
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

// identifyMatch is one suggestion button. ThumbURL (the plugin's blob)
// wins over ImageURL (the catalog item's photo).
type identifyMatch struct {
	Label, Detail, SKU, ImageURL, ThumbURL string
	Qty                                    int
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
	mux.HandleFunc("GET /api/pos/identify/plugin/thumb", func(w http.ResponseWriter, r *http.Request) {
		servePluginIdentifyThumb(w, r, d)
	})
}

// identifyThumbKey signs thumbnail URLs; random per process.
var identifyThumbKey = func() []byte {
	k := make([]byte, 32)
	_, _ = rand.Read(k) // crypto/rand.Read never fails (it crashes instead)
	return k
}()

func identifyThumbSig(pluginID, name string) string {
	m := hmac.New(sha256.New, identifyThumbKey)
	m.Write([]byte(pluginID))
	m.Write([]byte{0})
	m.Write([]byte(name))
	return hex.EncodeToString(m.Sum(nil))
}

// identifyThumbURL is the signed URL of pluginID's blob name.
func identifyThumbURL(pluginID, name string) string {
	q := url.Values{"p": {pluginID}, "n": {name}, "s": {identifyThumbSig(pluginID, name)}}
	return identifyThumbRoute + "?" + q.Encode()
}

// identifyThumbAllowed: the plugin is still installed and active and
// still holds blob:own — revoking either stops its thumbnails at once.
// Plain reads, like identifyPluginID: an image per suggestion is no
// denial worth an audit row.
func identifyThumbAllowed(ctx context.Context, d *common.Deps, pluginID string) bool {
	repo := data.NewPluginRepo(d.Db)
	if _, active, err := repo.GetActivePluginVersion(ctx, pluginID); err != nil || !active {
		return false
	}
	granted, exists, err := repo.CheckPermission(ctx, pluginID, "blob:own")
	return err == nil && exists && granted
}

// servePluginIdentifyThumb serves one signed (plugin, blob) pair as an
// image: sniffed type, never sniffed again by the browser, and inert even
// if opened as a page. Every refusal is the same 404.
func servePluginIdentifyThumb(w http.ResponseWriter, r *http.Request, d *common.Deps) {
	q := r.URL.Query()
	pluginID, name, sig := q.Get("p"), q.Get("n"), q.Get("s")
	want := identifyThumbSig(pluginID, name)
	if !hmac.Equal([]byte(sig), []byte(want)) || !identifyThumbAllowed(r.Context(), d, pluginID) {
		http.NotFound(w, r)
		return
	}
	f, ctype, size, err := plugins.OpenBlobImage(pluginID, name, identifyThumbMaxBytes)
	if err != nil {
		logging.L().Infof("plugin identify %s: thumbnail %q: %v", pluginID, name, err)
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Length", strconv.FormatInt(size, 10))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.CopyN(w, f, size)
}

// identifyThumbFor is the thumbnail URL for pluginID's blob name, or ""
// when the blob is missing, too large or not an image. The caller has
// already checked identifyThumbAllowed.
func identifyThumbFor(pluginID, name string) string {
	f, _, _, err := plugins.OpenBlobImage(pluginID, name, identifyThumbMaxBytes)
	if err != nil {
		logging.L().Infof("plugin identify %s: no thumbnail %q: %v", pluginID, name, err)
		return ""
	}
	_ = f.Close()
	return identifyThumbURL(pluginID, name)
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
	tmp, err := os.CreateTemp("", stagedupload.ViewUploadPattern)
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
	pluginID, found := pluginJobs.owner(jobID, identifyRoute)
	if found {
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
		renderIdentify(w, r, http.StatusOK, "identify_result", identifyResultFor(r.Context(), d, pluginID, snap.doc.Prepare(locale)))
	}
}

// identifyResultFor builds the overlay's result: the document's text and
// notices, and one button per suggestion, with pluginID's own thumbnail
// blob when it names one that checks out, else the catalog photo of the
// item its SKU resolves to (the same resolution /api/pos/scan makes).
func identifyResultFor(ctx context.Context, d *common.Deps, pluginID string, v pluginview.View) identifyResult {
	var res identifyResult
	repo := data.NewPOSRepo(d.Db)
	thumbsChecked, thumbsAllowed := false, false
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
				if s.Thumbnail != "" {
					if !thumbsChecked {
						thumbsChecked, thumbsAllowed = true, identifyThumbAllowed(ctx, d, pluginID)
					}
					if thumbsAllowed {
						m.ThumbURL = identifyThumbFor(pluginID, s.Thumbnail)
					}
				}
				if line, ok := repo.ResolveShortcutLine(ctx, s.SKU); ok {
					m.ImageURL = line.ImageURL
				}
				res.Matches = append(res.Matches, m)
			}
		}
	}
	return res
}
