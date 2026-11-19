package pages

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/universaltill/universal-till/internal/readerlink"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/lantls"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#6538 R1a (ADR-0198 §6.2/6.3): a phone as a card reader for this
// main till. Identity only: enrolment, the reader bearer, the manager's
// pairing UI and revoke. Nothing here sends money; the reader link hub and
// the payment frames are R1b.

// Machine-readable refusals the phone and the tests key on.
const (
	readerErrTLSRequired     = "tls_required"
	readerErrNeedsRequest    = "reader_needs_request"
	readerErrRoleMismatch    = "role_mismatch"
	readerErrNoMethod        = "no_reader_method"
	readerErrLimit           = "reader_limit"
	readerLinkUnavailableErr = "reader_link_unavailable"
	readerErrPairingOff      = "reader_pairing_off"
)

// settingPhoneReaderPairing turns the phone-as-card-reader feature on. It
// ships dark (design-batch2 §10): until the owner enables it no pair request
// is taken and the Tills page shows nothing, so no shop changes behaviour.
const settingPhoneReaderPairing = "card_readers.phone_pairing"

func phoneReaderPairingOn(ctx context.Context, d *common.Deps) bool {
	if d == nil || d.Settings == nil {
		return false
	}
	v, _, _ := d.Settings.Get(ctx, settingPhoneReaderPairing)
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "on" || v == "1" || v == "true"
}

// readerJoinWithdrawWindow is how long after pairing a reader may withdraw its
// own enrolment (the same window a joining till has).
const readerJoinWithdrawWindow = joinWithdrawWindow

func writeReaderError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": code})
}

// readerRepoFor is the credential repository, nil when there is no database
// (a handler test that wires none): callers treat nil as "no readers".
func readerRepoFor(d *common.Deps) *data.CardReaderCredentialRepo {
	if d == nil || d.Db == nil {
		return nil
	}
	return data.NewCardReaderCredentialRepo(d.Db)
}

// syncReader authenticates a card reader's call by its bearer, the reader
// twin of syncTill (design-batch2 §3). It reads ONLY card_reader_credentials
// (joined to the active registry row), never tills, so a till-only handler
// that calls syncTill refuses a reader by construction, and a reader is
// accepted only by the handlers that call this. Beyond the bearer:
//   - TLS only: a reader never talks plain HTTP;
//   - the connection must have served the pin recorded at pairing (or the
//     announced next pin), so a bearer replayed through another certificate
//     is refused;
//   - a revoked credential or a removed row never matches.
//
// Failures feed the same per-IP limiter as syncTill.
func syncReader(w http.ResponseWriter, r *http.Request, repo *data.CardReaderCredentialRepo) (data.ReaderCredential, bool) {
	if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") == "" {
		return data.ReaderCredential{}, false
	}
	if bearerLockedOut(w, r) {
		return data.ReaderCredential{}, false
	}
	c, ok := authReader(r, repo)
	if !ok {
		recordBearerFailure(r)
	}
	return c, ok
}

func authReader(r *http.Request, repo *data.CardReaderCredentialRepo) (data.ReaderCredential, bool) {
	h := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if h == "" || repo == nil || r.TLS == nil {
		return data.ReaderCredential{}, false
	}
	c, ok, err := repo.ByBearerHashAt(r.Context(), hashBearer(h), time.Now())
	if err != nil || !ok {
		return data.ReaderCredential{}, false
	}
	served := lantls.ServedPin(r.Context())
	if served == "" || (served != c.PinSPKI && (c.NextPin == "" || served != c.NextPin)) {
		return data.ReaderCredential{}, false
	}
	return c, true
}

// syncTillOrReader is for the three handlers a reader may reach. It tries
// the till lookup first and only if that says "not a till bearer" the reader
// lookup (the two lookups are never merged); a failure of both counts ONCE.
func syncTillOrReader(w http.ResponseWriter, r *http.Request, tills *data.TillsRepo, readers *data.CardReaderCredentialRepo) (till data.TillRow, reader data.ReaderCredential, isReader, ok bool) {
	if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") == "" {
		return
	}
	if bearerLockedOut(w, r) {
		return
	}
	if t, tok := authTill(w, r, tills); tok {
		return t, data.ReaderCredential{}, false, true
	}
	if c, rok := authReader(r, readers); rok {
		return data.TillRow{}, c, true, true
	}
	recordBearerFailure(r)
	return
}

// serveReaderLink answers an authenticated reader's link upgrade. The hub
// that carries reader frames is R1b; until then the reader is authenticated
// and told the link is not available yet, and is never handed to the till
// hub (a reader must not become a till peer).
func serveReaderLink(w http.ResponseWriter, r *http.Request, d *common.Deps, c data.ReaderCredential) {
	if d.SyncPrimaryURL(r.Context()) != "" {
		http.Error(w, "connect to the main till", http.StatusConflict)
		return
	}
	if refuseIfReplaced(w, r, d) {
		return
	}
	if repo := readerRepoFor(d); repo != nil {
		repo.Touch(r.Context(), c.ReaderID, sourceOf(r))
	}
	writeReaderError(w, http.StatusServiceUnavailable, readerLinkUnavailableErr)
}

// disconnectReaderLink closes a reader's live link at once (revoke, withdraw).
// A seam: R1b's hub registers readers under their reader id, which the till
// hub's Disconnect already keys on.
var disconnectReaderLink = func(d *common.Deps, readerID string) {
	if d != nil && d.Link != nil {
		d.Link.Disconnect(readerID)
	}
}

// rotateReaderBearer answers POST /api/sync/bearer/rotate for a reader.
func rotateReaderBearer(w http.ResponseWriter, r *http.Request, d *common.Deps, c data.ReaderCredential) {
	repo := readerRepoFor(d)
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	bearer := hex.EncodeToString(raw)
	swapped, err := repo.RotateBearer(r.Context(), c.ReaderID, hashBearer(bearerOf(r)), hashBearer(bearer), time.Now())
	if err != nil {
		common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "sync_bearer_rotate", err)
		return
	}
	if !swapped {
		writeProofError(w, http.StatusConflict, "this bearer was already replaced")
		return
	}
	logging.L().Infof("sync: card reader %s rotated its credential", c.ReaderID)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"bearer": bearer}, "error": nil})
}

// withdrawReader removes a freshly paired reader's own enrolment (the phone's
// pairing failed after approval), within the join window.
func withdrawReader(w http.ResponseWriter, r *http.Request, d *common.Deps, c data.ReaderCredential) {
	if !withinJoinWithdrawWindow(c.CreatedAt, time.Now()) {
		http.Error(w, "enrolment is too old to withdraw; remove the reader on the main till", http.StatusConflict)
		return
	}
	if _, err := data.NewCardReaderRepo(d.Db).SoftRemove(r.Context(), c.ReaderID); err != nil {
		common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "sync_api", err)
		return
	}
	disconnectReaderLink(d, c.ReaderID)
	_ = data.NewPOSRepo(d.Db).InsertAudit(r.Context(), nil, "system", "card_reader", c.ReaderID, "card_reader_withdrawn",
		map[string]any{"name": c.Name}, time.Now().UTC().Format(time.RFC3339), "")
	w.Header().Set("HX-Trigger", "tills-changed")
	w.WriteHeader(http.StatusNoContent)
}

// errNoReaderMethod: no single reader-capable payment method is configured.
var errNoReaderMethod = errors.New("no single reader-capable payment method")

// resolveReaderMethod is the payment method a paired phone takes taps for: the
// one active payment method whose plugin declares the readers hook. Zero or
// several is refused (the manager configures one first); core never names a
// provider.
func resolveReaderMethod(ctx context.Context, d *common.Deps) (method, pluginID string, err error) {
	plug := data.NewPluginRepo(d.Db)
	entries, err := plug.ListPaymentEntries(ctx)
	if err != nil {
		return "", "", err
	}
	found := 0
	for _, e := range entries {
		ok, herr := plug.HasActiveHook(ctx, e.PluginID, readersHook(e.EntryKey))
		if herr != nil {
			return "", "", herr
		}
		if ok {
			found++
			method, pluginID = e.EntryKey, e.PluginID
		}
	}
	if found != 1 {
		return "", "", errNoReaderMethod
	}
	return method, pluginID, nil
}

// uniqueReaderName makes the phone's name unique among active readers by
// adding " (2)", " (3)"... within the 40 character bound.
func uniqueReaderName(ctx context.Context, repo *data.CardReaderRepo, name string) string {
	rows, err := repo.ListActive(ctx)
	if err != nil {
		return name
	}
	taken := map[string]bool{}
	for _, c := range rows {
		taken[strings.ToLower(c.Name)] = true
	}
	if !taken[strings.ToLower(name)] {
		return name
	}
	for i := 2; i < 100; i++ {
		suffix := fmt.Sprintf(" (%d)", i)
		base := []rune(name)
		if max := data.MaxCardReaderNameLen - len([]rune(suffix)); len(base) > max {
			base = base[:max]
		}
		cand := string(base) + suffix
		if !taken[strings.ToLower(cand)] {
			return cand
		}
	}
	return name
}

// completeReaderPairing runs at the phone's poll with its request secret: it
// claims the approval (once), creates the reader row and credential in one
// transaction and hands the bearer over, once, over the pinned TLS link.
func completeReaderPairing(w http.ResponseWriter, r *http.Request, d *common.Deps, pairings *data.PairingRepo, row data.PendingPairingRow) {
	// TLS only, and on the very pin the request saw: a poll through another
	// certificate never collects the bearer.
	if r.TLS == nil || row.ServedPin == "" || lantls.ServedPin(r.Context()) != row.ServedPin {
		http.NotFound(w, r)
		return
	}
	method, pluginID, err := resolveReaderMethod(r.Context(), d)
	if err != nil {
		writeReaderError(w, http.StatusConflict, readerErrNoMethod)
		return
	}
	won, err := pairings.Consume(r.Context(), row.ID)
	if err != nil {
		common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pairings.error.server", "pairing_api", err)
		return
	}
	if !won {
		http.NotFound(w, r)
		return
	}
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	bearer := hex.EncodeToString(raw)
	ref := make([]byte, 6)
	_, _ = rand.Read(ref)
	readers := data.NewCardReaderRepo(d.Db)
	c, err := readers.CreatePaired(r.Context(), data.CardReader{
		Name:      uniqueReaderName(r.Context(), readers, row.DeviceName),
		MethodKey: method, PluginID: pluginID,
		ReaderRef: "rd_" + hex.EncodeToString(ref),
		Kind:      data.CardReaderKindPhone,
	}, data.NewReaderCredential{BearerHash: hashBearer(bearer), PinSPKI: row.ServedPin})
	if err != nil {
		status := http.StatusInternalServerError
		code := ""
		if errors.Is(err, data.ErrCardReaderLimit) {
			status, code = http.StatusConflict, readerErrLimit
		}
		if code != "" {
			writeReaderError(w, status, code)
			return
		}
		common.LogAndLocalizedError(w, r, status, "pairings.error.server", "pairing_api", err)
		return
	}
	publishPairingChange(d)
	_ = data.NewPOSRepo(d.Db).InsertAudit(r.Context(), nil, "system", "card_reader", c.ID, "card_reader_paired",
		map[string]any{"name": c.Name, "method": method}, time.Now().UTC().Format(time.RFC3339), "")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data":  map[string]any{"reader_id": c.ID, "bearer": bearer, "pin": row.ServedPin},
		"error": nil,
	})
}

// --- manager UI: Tills page "Card readers" (UX B1-B4) ---

type readerRequestRow struct {
	ID               string
	DeviceName       string
	VerificationCode string
}

type readerListRow struct {
	ID       string
	Name     string
	State    string // i18n suffix: offline (presence arrives with the reader link, R1b)
	LastUsed string
}

func pairedPhoneReaders(ctx context.Context, d *common.Deps) ([]readerListRow, error) {
	rows, err := data.NewCardReaderRepo(d.Db).ListActive(ctx)
	if err != nil {
		return nil, err
	}
	out := []readerListRow{}
	for _, c := range rows {
		if c.Kind != data.CardReaderKindPhone || c.Source != data.CardReaderSourcePaired {
			continue
		}
		out = append(out, readerListRow{ID: c.ID, Name: c.Name, State: "offline", LastUsed: c.LastUsedAt})
	}
	return out, nil
}

func registerReaderManagement(mux *http.ServeMux, d *common.Deps) {
	deny := func(w http.ResponseWriter, r *http.Request) bool {
		if canPerform(d, r, "sync_management") {
			return false
		}
		w.WriteHeader(http.StatusForbidden)
		return true
	}
	renderList := func(w http.ResponseWriter, r *http.Request, status int, msgKey string) {
		rows, err := pairedPhoneReaders(r.Context(), d)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pairings.error.server", "card_readers", err)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
		}
		httpx.RenderPartial("ui/partials/tills_card_readers.html", map[string]any{"Readers": rows, "MsgKey": msgKey})(w, r)
	}

	mux.HandleFunc("GET /ui/tills/reader-requests", func(w http.ResponseWriter, r *http.Request) {
		if deny(w, r) {
			return
		}
		if !phoneReaderPairingOn(r.Context(), d) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// A replica holds no reader requests; a plain-HTTP main cannot take them.
		view := map[string]any{"NeedsTLS": lantls.Serving() == nil, "Requests": []readerRequestRow{}}
		if d.SyncPrimaryURL(r.Context()) != "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if lantls.Serving() != nil {
			list, err := data.NewPairingRepo(d.Db).ListPending(r.Context())
			if err != nil {
				common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pairings.error.server", "pairings", err)
				return
			}
			primaryTillID, err := discovery.TillID(r.Context(), data.NewSettingsRepo(d.Db))
			if err != nil {
				common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pairings.error.server", "pairings", err)
				return
			}
			reqs := []readerRequestRow{}
			for _, p := range list {
				if p.RequestedRole != data.TillRoleCardReader {
					continue
				}
				reqs = append(reqs, readerRequestRow{ID: p.ID, DeviceName: p.DeviceName,
					VerificationCode: derivedVerificationCode(p.Commitment, primaryTillID, p.ServedPin)})
			}
			view["Requests"] = reqs
		}
		httpx.RenderPartial("ui/partials/tills_reader_requests.html", view)(w, r)
	})

	mux.HandleFunc("GET /ui/tills/readers", func(w http.ResponseWriter, r *http.Request) {
		if deny(w, r) {
			return
		}
		if !phoneReaderPairingOn(r.Context(), d) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		renderList(w, r, http.StatusOK, "")
	})

	pairedReader := func(r *http.Request, id string) (data.CardReader, bool) {
		c, err := data.NewCardReaderRepo(d.Db).Get(r.Context(), id)
		if err != nil || c.Kind != data.CardReaderKindPhone || c.Source != data.CardReaderSourcePaired || c.RemovedAt != "" {
			return data.CardReader{}, false
		}
		return c, true
	}

	mux.HandleFunc("POST /ui/tills/readers/{id}/rename", func(w http.ResponseWriter, r *http.Request) {
		if deny(w, r) {
			return
		}
		id := r.PathValue("id")
		if _, ok := pairedReader(r, id); !ok {
			http.NotFound(w, r)
			return
		}
		_ = r.ParseForm()
		switch err := data.NewCardReaderRepo(d.Db).Rename(r.Context(), id, r.Form.Get("name")); {
		case errors.Is(err, data.ErrCardReaderName):
			renderList(w, r, http.StatusConflict, "tap.reader.rename_taken")
			return
		case err != nil:
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pairings.error.server", "card_readers", err)
			return
		}
		_ = data.NewPOSRepo(d.Db).InsertAudit(r.Context(), nil, getSessionUserID(r), "card_reader", id, "card_reader_renamed",
			nil, time.Now().UTC().Format(time.RFC3339), "")
		renderList(w, r, http.StatusOK, "")
	})

	mux.HandleFunc("POST /ui/tills/readers/{id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		if deny(w, r) {
			return
		}
		actor, ok := authorizePairingManager(w, r, d.AuthSvc)
		if !ok {
			return
		}
		id := r.PathValue("id")
		if _, ok := pairedReader(r, id); !ok {
			http.NotFound(w, r)
			return
		}
		if _, err := data.NewCardReaderRepo(d.Db).SoftRemove(r.Context(), id); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pairings.error.server", "card_readers", err)
			return
		}
		// The link closes now, not at the next ping.
		disconnectReaderLink(d, id)
		_ = data.NewPOSRepo(d.Db).InsertAudit(r.Context(), nil, actor, "card_reader", id, "card_reader_revoked",
			nil, time.Now().UTC().Format(time.RFC3339), "")
		w.Header().Set("HX-Trigger", "tills-changed")
		renderList(w, r, http.StatusOK, "tap.reader.revoked")
	})
}

// wireReaderLink builds the remote collector for paired phone readers, hangs
// it on the link hub (reader.result sink, reader.token handler) and registers
// it with devicepay (ut-docs#6538 R1b). Stubbed until implemented.
func wireReaderLink(d *common.Deps) *readerlink.Service { return nil }
