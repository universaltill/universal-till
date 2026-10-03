package catalogsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/netaccess"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Additional-till side. Every catalogue mutation handler in Routes calls
// Forward (or ForwardElevated) where it used to call its requirePrimary
// refusal: on the main till (or a stand-alone till) Forward returns false
// and the handler carries on locally exactly as before; on a till that
// follows a main till Forward sends the save to the main till and answers
// the request itself, so the handler must return.
//
// ANY failure -- main till unreachable, timeout, no bearer yet, a non-200
// or unusable answer -- refuses the change with NO local write, like the
// settings and users write-throughs and for the same reason: a local-only
// catalogue edit is reverted by the next admin pull. Form values are never
// logged. An admin screen; checkout never depends on it (offline-first).

// ClientTimeout bounds one write-through: an admin screen, the same budget
// as the users/settings write-throughs.
const ClientTimeout = 5 * time.Second

// Client is the additional-till -> main-till client. A variable so tests
// can swap it.
var Client = netaccess.NewClient(ClientTimeout)

// maxForwardBody bounds the form an additional till reads before sending
// it on. The category form allows a photo upload of up to 10 MiB; a form
// without a file is a few KiB.
const maxForwardBody = 12 << 20

// Locale (message) keys for a refused write-through — not settings keys.
const (
	MsgUnreachable = "catalog.sync.main_till_unreachable"
	MsgConflict    = "catalog.sync.conflict"
	MsgRefused     = "catalog.sync.main_till_refused"
	MsgPhoto       = "catalog.sync.photo_main_till_only"
	MsgForbidden   = "common.error.manager_or_admin_required"
)

// Refuser answers a refused write-through in the calling handler's own
// error shape (a plain localized error, the category dialog's fragment, the
// Designer's error div...). status is the suggested HTTP status; key is the
// locale key of the message.
type Refuser func(w http.ResponseWriter, r *http.Request, status int, key string)

// PlainRefuser answers with common.LocalizedError: the shape every
// requirePrimary refusal used before ut-docs#2817.
func PlainRefuser(w http.ResponseWriter, r *http.Request, status int, key string) {
	common.LocalizedError(w, r, status, key)
}

// Forward is ForwardElevated with no approver: the session user made the
// change on their own permission.
func Forward(w http.ResponseWriter, r *http.Request, d *common.Deps, refuse Refuser) bool {
	return ForwardElevated(w, r, d, "", refuse)
}

// ForwardElevated sends this catalogue save to the main till when this
// till follows one, and answers the request: the main till's own handler
// response on success, refuse(...) on any failure. approverID is the
// manager whose PIN this till verified for a cashier's change (the main
// till re-checks the approver's role with its own role table); "" when the
// session user acted on their own permission. Returns false -- and touches
// nothing, not even the body -- on a till that follows no main till.
func ForwardElevated(w http.ResponseWriter, r *http.Request, d *common.Deps, approverID string, refuse Refuser) bool {
	ctx := r.Context()
	mainURL := d.SyncPrimaryURL(ctx)
	if mainURL == "" {
		return false
	}
	if refuse == nil {
		refuse = PlainRefuser
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if r.MultipartForm == nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxForwardBody)
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				refuse(w, r, http.StatusBadRequest, MsgRefused)
				return true
			}
		}
		if hasFile(r) {
			// A photo travels through the asset ledger, never here.
			refuse(w, r, http.StatusConflict, MsgPhoto)
			return true
		}
	} else if r.PostForm == nil {
		r.Body = http.MaxBytesReader(w, r.Body, maxForwardBody)
		if err := r.ParseForm(); err != nil {
			refuse(w, r, http.StatusBadRequest, MsgRefused)
			return true
		}
	}

	form := cloneValues(r.PostForm)
	form.Del("override_pin")
	route, id, ok := Resolve(r.Method, r.URL.Path, form)
	if !ok {
		logging.L().Errorf("catalog sync: %s %s is not a write-through route — change refused", r.Method, r.URL.Path)
		refuse(w, r, http.StatusConflict, MsgRefused)
		return true
	}
	in := ApplyRequest{
		Method:     r.Method,
		Path:       r.URL.Path,
		Query:      r.URL.RawQuery,
		Form:       form,
		Locale:     httpx.ResolveLocale(w, r),
		ActorID:    auth.UserID(r),
		ApproverID: approverID,
	}
	for _, h := range ForwardedRequestHeaders {
		if v := r.Header.Get(h); v != "" {
			if in.Headers == nil {
				in.Headers = map[string]string{}
			}
			in.Headers[h] = v
		}
	}
	// shown is the record's stamp as the operator's form showed it; what
	// travels may be the main till's newer stamp from this till's own
	// previous save of the record (recentSaves).
	shown := ""
	if route.Conflict && id != "" {
		shown = strings.TrimSpace(form.Get("base_updated_at"))
		if shown == "" && d.Db != nil {
			// The form did not carry the stamp it loaded (ut-docs#3606:
			// only /api/buttons/add, whose add-from-search result shows
			// no button record): this till's own copy is what the screen
			// showed (the admin pull keeps it equal to the main till's
			// value until someone changes it there).
			if v, found, err := data.NewCatalogRepo(d.Db).CatalogUpdatedAt(ctx, route.Kind, id); err == nil && found {
				shown = v
			}
		}
		in.BaseUpdatedAt = recentSaves.base(mainURL, route.Kind, id, shown)
	}

	answer, err := applyOnMain(ctx, d, in)
	if err != nil {
		var se *Error
		if !errors.As(err, &se) {
			se = &Error{}
		}
		refuse(w, r, se.HTTPStatus(), se.MessageKey())
		return true
	}
	if in.BaseUpdatedAt != "" && answer.Status < 400 && answer.EntityID == id && answer.UpdatedAt != "" {
		recentSaves.remember(mainURL, route.Kind, id, in.BaseUpdatedAt, answer.UpdatedAt)
	}
	for _, h := range ForwardedResponseHeaders {
		if v, ok := answer.Headers[h]; ok {
			w.Header().Set(h, v)
		}
	}
	status := answer.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(answer.Body))
	return true
}

// hasFile reports whether the parsed multipart form carries any non-empty
// file part.
func hasFile(r *http.Request) bool {
	if r.MultipartForm == nil {
		return false
	}
	for _, files := range r.MultipartForm.File {
		for _, f := range files {
			if f != nil && f.Size > 0 {
				return true
			}
		}
	}
	return false
}

// recentSaves is this till's memory of its own successful conflict-checked
// write-throughs (ut-docs#3606). A screen re-rendered from this till's own
// copy right after a save -- the Designer's buttons-changed refresh, the
// /categories redirect -- shows a stamp the admin pull has not yet caught
// up from (about a second), so saving the same record again from it would
// read on the main till as a conflict with this till's own previous save.
// Forward therefore keeps, per main till and record, the unbroken chain of
// stamps this till's own accepted saves moved the record past, and the
// main till's stamp after the latest of them; a later save whose form shows
// any stamp in that chain sends the latest instead. The chain restarts
// whenever a save's base is not the remembered latest stamp (someone else
// changed the record in between), so it only ever spans changes made by
// this till: a form showing anything else (a change made on the main till
// or another till since) is sent as shown, and a real conflict is still
// refused. In memory only and bounded: entries expire after
// recentSavesTTL (many admin pulls), the oldest is dropped past
// recentSavesMax records, and a restart forgets everything (the next pull
// makes the re-rendered stamps current again). Entries are keyed per main
// till URL, so tests (each pair on its own httptest server) never share one.
var recentSaves = &savedStamps{m: map[string]*savedStamp{}, now: time.Now}

const (
	// recentSavesMax bounds recentSaves: far more records than one
	// operator edits between two admin pulls.
	recentSavesMax = 512
	// recentSavesChain bounds one record's chain of superseded stamps.
	recentSavesChain = 16
	// recentSavesTTL: by then the admin pull has made every re-rendered
	// stamp current many times over.
	recentSavesTTL = 10 * time.Minute
)

type savedStamp struct {
	past   []string // stamps this till's own saves moved past, oldest first
	latest string   // the main till's stamp after the latest of them
	at     time.Time
}

type savedStamps struct {
	mu  sync.Mutex
	m   map[string]*savedStamp
	now func() time.Time
}

func savedStampKey(main, kind, id string) string { return main + "\x00" + kind + "\x00" + id }

// base is the base_updated_at to send for a save whose form showed shown.
func (s *savedStamps) base(main, kind, id, shown string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[savedStampKey(main, kind, id)]
	if !ok || shown == "" || s.now().Sub(e.at) > recentSavesTTL {
		return shown
	}
	for _, p := range e.past {
		if p == shown {
			return e.latest
		}
	}
	return shown
}

// remember records a successful save that sent sent as its base and left
// the record at saved on the main till.
func (s *savedStamps) remember(main, kind, id, sent, saved string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	key := savedStampKey(main, kind, id)
	e, ok := s.m[key]
	if !ok || e.latest != sent || now.Sub(e.at) > recentSavesTTL {
		// No chain to extend: the main till accepted sent, so this
		// till's save is the only change since it.
		if !ok {
			s.evict(now)
		}
		e = &savedStamp{}
		s.m[key] = e
	}
	e.past = append(e.past, sent)
	if len(e.past) > recentSavesChain {
		e.past = e.past[len(e.past)-recentSavesChain:]
	}
	e.latest, e.at = saved, now
}

// evict makes room for one more record: expired entries go first, then the
// oldest one. Called with s.mu held.
func (s *savedStamps) evict(now time.Time) {
	if len(s.m) < recentSavesMax {
		return
	}
	oldestKey, oldest := "", now
	for k, e := range s.m {
		if now.Sub(e.at) > recentSavesTTL {
			delete(s.m, k)
			continue
		}
		if !e.at.After(oldest) {
			oldestKey, oldest = k, e.at
		}
	}
	if len(s.m) >= recentSavesMax && oldestKey != "" {
		delete(s.m, oldestKey)
	}
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

// Error is a failed write-through. Code is the main till's refusal code on
// a 4xx answer; "" means the main till could not be reached or its answer
// could not be used.
type Error struct {
	Status int
	Code   string
}

func (e *Error) Error() string {
	if e.Code == "" {
		return "main till unreachable"
	}
	return "main till refused the change: " + e.Code
}

// MessageKey is the locale key the operator sees for this failure.
func (e *Error) MessageKey() string {
	switch e.Code {
	case "":
		return MsgUnreachable
	case CodeConflict:
		return MsgConflict
	case CodePhoto:
		return MsgPhoto
	case "forbidden":
		return MsgForbidden
	default:
		return MsgRefused
	}
}

// HTTPStatus is the status the refusal answers with: 403 for a main-till
// permission refusal, 409 for any other main-till refusal (a conflict
// included), 502 when the main till could not be reached.
func (e *Error) HTTPStatus() int {
	switch {
	case e.Code == "forbidden":
		return http.StatusForbidden
	case e.Code != "":
		return http.StatusConflict
	default:
		return http.StatusBadGateway
	}
}

// applyOnMain POSTs one save to the main till and returns its answer, or an
// *Error. Never logs the form.
func applyOnMain(ctx context.Context, d *common.Deps, in ApplyRequest) (ApplyAnswer, error) {
	unreachable := &Error{}
	what := in.Method + " " + in.Path
	base, bearer, ok := d.SyncTarget(ctx)
	if !ok {
		logging.L().Infof("catalog sync: %s refused — this till has no sync bearer for its main till yet", what)
		return ApplyAnswer{}, unreachable
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return ApplyAnswer{}, unreachable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+ApplyPath, bytes.NewReader(payload))
	if err != nil {
		return ApplyAnswer{}, unreachable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := Client.Do(req)
	if err != nil {
		logging.L().Infof("catalog sync: main till unreachable on %s (%v) — change refused", what, err)
		return ApplyAnswer{}, unreachable
	}
	defer resp.Body.Close()
	var out struct {
		Data  *ApplyAnswer `json:"data"`
		Error *WireError   `json:"error"`
	}
	decodeErr := json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK {
		se := &Error{Status: resp.StatusCode}
		// Only a 4xx is the main till's decision about this change; a 5xx
		// or a proxy's page is "couldn't get an answer".
		if decodeErr == nil && out.Error != nil && resp.StatusCode < 500 {
			se.Code = out.Error.Code
		}
		logging.L().Infof("catalog sync: main till answered %s on %s (code %q) — change refused", resp.Status, what, se.Code)
		return ApplyAnswer{}, se
	}
	if decodeErr != nil || out.Data == nil {
		logging.L().Infof("catalog sync: unusable main-till answer on %s — change refused", what)
		return ApplyAnswer{}, unreachable
	}
	logging.L().Infof("catalog sync: %s applied on the main till (%s)", what, describeAnswer(*out.Data))
	return *out.Data, nil
}

func describeAnswer(a ApplyAnswer) string {
	if a.EntityID == "" {
		return fmt.Sprintf("status %d", a.Status)
	}
	return fmt.Sprintf("status %d, %s %s", a.Status, a.Entity, a.EntityID)
}
