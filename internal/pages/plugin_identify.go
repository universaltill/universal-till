package pages

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/imaging"
	"github.com/universaltill/universal-till/internal/itemimages"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pluginview"
	"github.com/universaltill/universal-till/internal/stagedupload"
)

// The sell screen's camera-identify seam (ADR-0121 §7 "Core-owned
// suggestion seam", ut-docs#3873; format: ut-docs reference/plugin-views.md).
// When an active plugin answers catalog.identify, the sell screen shows
// core's own identify button and overlay (the built-in AI one was removed
// in ut-docs#2851; the AI Assistant plugin answers this seam). The overlay posts one photo here; core
// stages it as an upload handle and runs catalog.identify as a job (§8,
// startPluginJob) — never on the sale path, never blocking it. The overlay
// polls GET ?_job=<id>; the plugin's answer is a document validated for the
// SeamSellIdentify seam (text, notice, suggestions with add_to_basket
// only), and each suggestion renders as a button posting {_job, sku, qty}
// to the pick route, which adds the line through the normal /api/pos/scan
// handler and then — off the request — stores the photo as the item's
// newest ai_ref (ADR-0121 amendment 2026-10-09 R2a, ut-docs#4006; the photo
// waits in identify_slot.go's pending-confirm slot). Closing the overlay
// stops the poll, so the job is abandoned (pluginJobUnpolledTTL) and a late
// result dropped.
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
	// identifyConfirmedEvent tells the slot's plugin a pick was learned
	// (ADR-0121 amendment 2026-10-09 R2b).
	identifyConfirmedEvent = "catalog.identify.confirmed"
	identifyRoute          = "/api/pos/identify/plugin"
	// identifyPickMaxBytes bounds the pick's form body (three short values).
	identifyPickMaxBytes = 4 << 10
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
	Job     string                     // the job id each pick carries back
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
	// A new capture replaces the photo an earlier result kept for a pick.
	identifySlots.clear(pluginID)
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
	// A valid result keeps the photo for a pick (R2a): taken back before
	// the job releases its uploads. A failed job keeps nothing.
	keep := func(id string, doc *pluginview.Document, redirect string, err error) {
		if err != nil || doc == nil || redirect != "" {
			return
		}
		// A job a newer capture cancelled may still deliver its answer:
		// it must not replace the newer job's slot.
		if _, live := pluginJobs.owner(id, identifyRoute); !live {
			return
		}
		if path, ok := plugins.TakeUpload(pluginID, up.Handle); ok {
			identifySlots.put(pluginID, id, path, up.ContentType, pluginJobUnpolledTTL+identifySlotTTL)
		}
	}
	id, err := startPluginJob(r.Context(), d, identifyEntry(pluginID), identifyEvent, payload, vctx, tokens, keep)
	if errors.Is(err, errPluginJobBusy) && pluginJobs.cancelRunning(pluginID, identifyRoute) > 0 {
		// A new capture supersedes this seam's earlier job: the overlay
		// shows one result at a time and stopped polling the old one
		// (Retake, or Close while its photo was still uploading). Without
		// this a tablet (one job per plugin) refused the next Capture until
		// the old job's unpolled TTL ran out. A job the plugin runs on its
		// own page is never cancelled here: that stays busy.
		id, err = startPluginJob(r.Context(), d, identifyEntry(pluginID), identifyEvent, payload, vctx, tokens, keep)
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
	// Kept: the plugin's upload_close must not delete the photo a pick
	// may still store (ut-docs#4006).
	tok, err := plugins.StageUploadKept(pluginID, path)
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
		identifySlots.handedOut(pluginID, jobID)
		res := identifyResultFor(r.Context(), d, pluginID, snap.doc.Prepare(locale))
		res.Job = jobID
		renderIdentify(w, r, http.StatusOK, "identify_result", res)
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

// identifyStoreRef stores img as itemID's newest ai_ref. A var so tests
// can make the store slow or fail.
var identifyStoreRef = func(_ context.Context, itemID string, img image.Image) error {
	_, err := itemimages.StoreAIRef(itemID, img)
	return err
}

// servePluginIdentifyPick adds a picked suggestion through scan — the
// /api/pos/scan handler itself, so the line, the basket swap and every
// scan rule are exactly a scan's — and only after the response is written
// learns from the pick on its own goroutine: the sale is never delayed or
// failed by it (ADR-0121 R2a). A cashier's sale action (#3079), session-
// gated under /api/pos/* like the built-in confirm.
func servePluginIdentifyPick(w http.ResponseWriter, r *http.Request, d *common.Deps, scan http.HandlerFunc) {
	// A form body only: scan's JSON branch reads its own "code", which
	// would add one item while the photo is stored on sku's.
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, identifyPickMaxBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	jobID := r.Form.Get(pluginJobParam)
	sku := strings.TrimSpace(r.Form.Get("sku"))
	// The scan reads only its own fields; the pick's _job never reaches it.
	r.Form.Set("code", sku)
	r.Form.Del(pluginJobParam)
	scan(w, r)
	if sku == "" || !identifyJobIDRe.MatchString(jobID) {
		return // nothing to learn: an unknown _job only adds the line
	}
	userID := getSessionUserID(r)
	ctx := context.WithoutCancel(r.Context())
	// Tracked async work: shutdown drains it (bounded) instead of cutting
	// a photo write short.
	d.AsyncWork.Add(1)
	go func() {
		defer logging.RecoverAndLog("pages.identifyPick")
		defer d.AsyncWork.Done()
		learnIdentifyPick(ctx, d, jobID, sku, userID)
	}()
}

// identifyJobIDRe: a job id is 32 hex characters (pluginJobRegistry).
var identifyJobIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// learnIdentifyPick stores the slot's photo for jobID — taken at most
// once; none (expired, replaced, never existed) stores nothing — as the
// newest ai_ref of the item sku resolves to (resolved by core, the way the
// scan does, never named by the client), then nudges linked tills and
// audits it, exactly like the built-in confirm. Whenever sku resolves to
// an item, the slot's plugin — and only it — then gets
// catalog.identify.confirmed (R2b). The slot's file is deleted whatever
// happens.
func learnIdentifyPick(ctx context.Context, d *common.Deps, jobID, sku, userID string) {
	pluginID, path, ctype, ok := identifySlots.take(jobID)
	if !ok {
		return
	}
	base, ok := d.Engine.ResolveBase(sku)
	if !ok || !itemimages.ValidID(base.ItemID) {
		_ = os.Remove(path)
		logging.L().Infof("plugin identify pick: %q resolves to no item; photo not stored", sku)
		return
	}
	stored := storeIdentifyPick(ctx, d, path, ctype, base.ItemID, userID)
	_ = os.Remove(path) // stored or not, before the plugin is told
	dispatchIdentifyConfirmed(ctx, d, pluginID, jobID, base.ItemID, stored)
}

// storeIdentifyPick stores the photo at path as itemID's newest ai_ref,
// nudges linked tills and audits it; true only when the ai_ref was written.
func storeIdentifyPick(ctx context.Context, d *common.Deps, path, ctype, itemID, userID string) bool {
	if ctype != "image/jpeg" && ctype != "image/png" {
		return false // WebP: the bounded decode takes PNG/JPEG only
	}
	raw, err := readCapped(path, identifyMaxPhotoBytes)
	if err != nil {
		logging.L().Infof("plugin identify pick: read photo: %v", err)
		return false
	}
	img, err := imaging.Decode(raw)
	if err != nil {
		logging.L().Infof("plugin identify pick: photo not stored: %v", err)
		return false
	}
	if err := identifyStoreRef(ctx, itemID, img); err != nil {
		logging.L().Warnf("plugin identify pick: store reference photo for %s: %v", itemID, err)
		return false
	}
	// Reference photos live under the items asset tree, which linked
	// tills pull: nudge them (ADR-0114 §2), as the built-in confirm does.
	d.NudgeLink(fleetlink.ScopeAdmin)
	now := time.Now().UTC().Format(time.RFC3339)
	if err := data.NewPOSRepo(d.Db).InsertAudit(ctx, nil, userID, "ai", itemID, "ai_identify_confirmed",
		map[string]any{"item_id": itemID}, now, ""); err != nil {
		logging.L().Warnf("plugin identify pick: audit: %v", err)
	}
	return true
}

// identifyConfirmed is catalog.identify.confirmed's payload. Stored: the
// photo is now itemID's newest ai_ref (item_image_open(item_id, "ai_ref")).
// No upload handle: the stored ai_ref is the same photo, read through R1.
type identifyConfirmed struct {
	JobID  string `json:"job_id"`
	ItemID string `json:"item_id"`
	SKU    string `json:"sku"`
	Stored bool   `json:"stored"`
}

// dispatchIdentifyConfirmed tells pluginID alone — never a bus broadcast
// — about the pick of jobID, when it hooks the event and holds
// events:receive (AskPlugin checks both) and view:inventory (catalog
// data). A plugin that never hooked the event is skipped silently; one
// that hooks it without view:inventory is skipped with its first denial
// audited. sku is the item's own SKU, read by item_id: the resolved line
// carries the picked code (a barcode, say), not the SKU. The answer is
// discarded. It runs on the learning goroutine: the event's name takes an
// ordinary call slot and deadline, never the reserved sale-path one, so a
// slow or broken plugin delays nothing.
func dispatchIdentifyConfirmed(ctx context.Context, d *common.Deps, pluginID, jobID, itemID string, stored bool) {
	bus := plugins.SharedBus(d.Db)
	if !slices.Contains(bus.SubscriberIDs(identifyConfirmedEvent), pluginID) {
		return
	}
	granted, _, err := plugins.CheckPermissionAuditOnce(ctx, d.Db, pluginID, "view:inventory")
	if err != nil || !granted {
		logging.L().Infof("plugin identify pick: %s not told %s (view:inventory granted=%v, err=%v)", pluginID, identifyConfirmedEvent, granted, err)
		return
	}
	item, ok, err := data.NewCatalogRepo(d.Db).GetItem(ctx, itemID)
	if err != nil || !ok {
		logging.L().Infof("plugin identify pick: %s: item %s not found (err=%v)", identifyConfirmedEvent, itemID, err)
		return
	}
	ev := identifyConfirmed{JobID: jobID, ItemID: itemID, SKU: item.SKU, Stored: stored}
	if _, _, err := bus.AskPlugin(ctx, pluginID, identifyConfirmedEvent, ev); err != nil {
		logging.L().Infof("plugin identify pick: %s %s: %v", pluginID, identifyConfirmedEvent, err)
	}
}

// readCapped reads the file at path, refusing one over max bytes.
func readCapped(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > max {
		return nil, errIdentifyTooLarge
	}
	return raw, nil
}
