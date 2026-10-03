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
	if d.SyncPrimaryURL(ctx) == "" {
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
	if route.Conflict && id != "" {
		in.BaseUpdatedAt = strings.TrimSpace(form.Get("base_updated_at"))
		if in.BaseUpdatedAt == "" && d.Db != nil {
			// The editor did not carry the stamp it loaded: this till's
			// own copy is what the editor showed (the admin pull keeps it
			// equal to the main till's value until someone changes it
			// there).
			if v, found, err := data.NewCatalogRepo(d.Db).CatalogUpdatedAt(ctx, route.Kind, id); err == nil && found {
				in.BaseUpdatedAt = v
			}
		}
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
