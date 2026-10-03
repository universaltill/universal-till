package pages

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/catalogsync"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Catalogue write-through, main-till side (ut-docs#2817; same mechanism as
// the settings write-through, sync_settings.go, and ADR-0115 §1's users
// write-through). items, item_variants (+ barcodes), categories,
// shortcut_buttons and item_modifier_groups (+ options and links) travel
// main -> additional tills in the admin bundle (main-till-wins), so a
// catalogue change made only on an additional till used to be refused
// there (requirePrimary). An additional till's catalogue handlers now send
// the save here first (catalogsync.Forward).
//
//   - POST /api/sync/catalog/apply -- one editor save:
//     {method, path, query, form, headers, locale, base_updated_at,
//     actor_id, approver_id} (catalogsync.ApplyRequest). The route must be
//     one of catalogsync.Routes (anything else is refused
//     not_supported_via_sync); the record it edits is derived HERE from the
//     route and form, never taken from the caller.
//   - approver_id is set only when the additional till's operator was
//     PIN-elevated; then the APPROVER must hold catalog_management,
//     otherwise the actor must. Both are looked up in THIS till's users
//     table and decided with ITS role_permissions -- never trusted from the
//     additional till.
//   - Optimistic conflict check (per parent record): when base_updated_at
//     is set and the record's current updated_at differs, the save is
//     refused with 409 `conflict` and nothing is written.
//   - The save then runs THIS till's own handler for the route, as the
//     permission holder -- the exact code a save made on the main till runs
//     (its validation, its transaction, its own audit row). A photo upload
//     never travels: the body is re-encoded as a plain form, and a form
//     field named "image" is refused photo_not_supported.
//   - On a 2xx/3xx answer the save is audited as catalog_changed_via_till
//     with provenance {via: till-sync, till, entity, entity_id, route} and
//     the admin link is nudged so every till -- the calling one included --
//     pulls the change. A handler refusal (validation, a duplicate SKU...)
//     is relayed as answered and neither audited nor nudged.
//   - The answer is { "data": {status, headers, body, entity, entity_id,
//     updated_at}, "error": null } -- the handler's own response, which the
//     additional till relays to the operator unchanged -- or { "data": null,
//     "error": {code, message} } with a 4xx on a refusal.
//
// The conflict check and the handler's own write are not one SQL
// transaction (the write is the handler's, unchanged); catalogApplyMu
// serialises write-throughs on this till so two additional tills can never
// interleave between one save's check and its write. A save made directly
// on the main till inside that window (milliseconds) is not detected.
// updated_at has one-second resolution (datetime('now'), the schema's
// convention), so two changes to the same record within the same second
// share a stamp.
//
// Bearer-authed via syncTill and on internal/auth/middleware.go's exempt
// list (the /api/sync/ prefix). Form values are never logged.

// catalogApplyMu serialises catalogue write-throughs on this till.
var catalogApplyMu sync.Mutex

const (
	// maxSyncCatalogBody bounds the request body: one editor form.
	maxSyncCatalogBody = 1 << 20
	// maxSyncCatalogAnswer bounds the relayed handler answer.
	maxSyncCatalogAnswer = 4 << 20
)

// syncCatalogLocale is the shape of a locale code the request may name.
var syncCatalogLocale = regexp.MustCompile(`^[A-Za-z]{2,3}([-_][A-Za-z0-9]{2,8})?$`)

// errSyncCatalogAnswerTooLarge is a dispatched handler answer over
// maxSyncCatalogAnswer.
var errSyncCatalogAnswerTooLarge = errors.New("handler answer too large")

// registerSyncCatalog mounts the main-till catalogue write-through endpoint
// on the bearer-authed /api/sync/* surface. It dispatches a save into the
// catalogue handlers mounted on mux, so it must be given the same mux
// (pages.Init does).
func registerSyncCatalog(mux *http.ServeMux, d *common.Deps) {
	tills := data.NewTillsRepo(d.Db)
	repo := data.NewAuthRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)
	catRepo := data.NewCatalogRepo(d.Db)

	mux.HandleFunc("POST /api/sync/catalog/apply", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		fail := func(status int, code, message string) {
			writeSyncOrdersJSON(w, status, nil, syncUserError{Code: code, Message: message})
		}
		till, ok := syncTill(r, tills)
		if !ok {
			fail(http.StatusUnauthorized, "unauthorized", "unauthorized")
			return
		}
		// A till that itself follows a main till would have this change
		// reverted by its own next pull -- never accept it here.
		if d.SyncPrimaryURL(ctx) != "" {
			fail(http.StatusConflict, "replica", "this till follows a main till")
			return
		}
		var in catalogsync.ApplyRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSyncCatalogBody)).Decode(&in); err != nil {
			fail(http.StatusBadRequest, "invalid_body", "invalid body")
			return
		}
		in.ActorID = strings.TrimSpace(in.ActorID)
		in.ApproverID = strings.TrimSpace(in.ApproverID)
		in.BaseUpdatedAt = strings.TrimSpace(in.BaseUpdatedAt)
		if in.Form == nil {
			in.Form = url.Values{}
		}
		// path.Clean: a "." or ".." segment (or "//") passes Resolve's
		// {id} wildcard but makes ServeMux answer its own 301 to the cleaned
		// path before any handler runs — a 3xx that would otherwise be
		// relayed and audited below as a successful save.
		if !strings.HasPrefix(in.Path, "/") || strings.ContainsAny(in.Path, "?#") || path.Clean(in.Path) != in.Path {
			fail(http.StatusBadRequest, "invalid_body", "invalid path")
			return
		}
		route, entityID, known := catalogsync.Resolve(in.Method, in.Path, in.Form)
		if !known {
			fail(http.StatusBadRequest, catalogsync.CodeNotSupported, "not a catalogue write-through route")
			return
		}
		if _, photo := in.Form["image"]; photo {
			fail(http.StatusBadRequest, catalogsync.CodePhoto, "photos are changed on the main till")
			return
		}

		serverError := func(step string, err error) {
			logging.L().Errorf("sync catalog from %s: %s: %v", till.Name, step, err)
			fail(http.StatusInternalServerError, "server_error", "server error")
		}
		// audit_log.actor_id FKs onto users: the actor must be a real user
		// here, which it is for any operator the admin bundle delivered.
		if in.ActorID == "" {
			fail(http.StatusBadRequest, "unknown_actor", "actor_id required")
			return
		}
		actor, found, err := repo.GetUser(ctx, in.ActorID)
		if err != nil {
			serverError("actor lookup", err)
			return
		}
		if !found {
			fail(http.StatusBadRequest, "unknown_actor", "unknown actor")
			return
		}
		holder := actor
		if in.ApproverID != "" {
			approver, found, err := repo.GetUser(ctx, in.ApproverID)
			if err != nil {
				serverError("approver lookup", err)
				return
			}
			if !found {
				fail(http.StatusBadRequest, "unknown_approver", "unknown approver")
				return
			}
			holder = approver
		}
		// The permission holder must be active and its role must hold
		// catalog_management on THIS till.
		allowed := holder.IsActive
		if allowed {
			allowed, err = repo.HasPermission(ctx, holder.Role, "catalog_management")
			if err != nil {
				serverError("permission check", err)
				return
			}
		}
		if !allowed {
			logging.L().Infof("sync catalog: %s by %s (%s) from %s refused — no catalog_management on the main till", route.Pattern, holder.ID, holder.Role, till.Name)
			fail(http.StatusForbidden, "forbidden", "actor may not make this change")
			return
		}

		catalogApplyMu.Lock()
		defer catalogApplyMu.Unlock()

		if route.Conflict && entityID != "" && in.BaseUpdatedAt != "" {
			current, exists, err := catRepo.CatalogUpdatedAt(ctx, route.Kind, entityID)
			if err != nil {
				serverError("conflict check", err)
				return
			}
			if exists && current != in.BaseUpdatedAt {
				logging.L().Infof("sync catalog: %s %s from %s refused — changed on the main till since the editor loaded it", route.Kind, entityID, till.Name)
				fail(http.StatusConflict, catalogsync.CodeConflict, "the record changed on the main till")
				return
			}
		}

		target := in.Path
		if in.Query != "" {
			target += "?" + in.Query
		}
		req, err := http.NewRequestWithContext(ctx, in.Method, target, strings.NewReader(in.Form.Encode()))
		if err != nil {
			fail(http.StatusBadRequest, "invalid_body", "invalid path")
			return
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, h := range catalogsync.ForwardedRequestHeaders {
			if v := strings.TrimSpace(in.Headers[h]); v != "" {
				req.Header.Set(h, v)
			}
		}
		if syncCatalogLocale.MatchString(in.Locale) {
			req.AddCookie(&http.Cookie{Name: "ut_lang", Value: httpx.LocaleOverrideValue(in.Locale)})
		}
		req.RemoteAddr = r.RemoteAddr
		req = auth.WithUser(req, auth.User{ID: holder.ID, Username: holder.Username, DisplayName: holder.DisplayName, Role: holder.Role})
		cw := newSyncCatalogCapture()
		mux.ServeHTTP(cw, req)
		if cw.overflow {
			serverError("answer", errSyncCatalogAnswerTooLarge)
			return
		}

		answer := catalogsync.ApplyAnswer{Status: cw.status, Body: cw.buf.String(), Entity: route.Kind, EntityID: entityID}
		for _, h := range catalogsync.ForwardedResponseHeaders {
			if v := cw.header.Get(h); v != "" {
				if answer.Headers == nil {
					answer.Headers = map[string]string{}
				}
				answer.Headers[h] = v
			}
		}
		if cw.status >= 200 && cw.status < 400 {
			if entityID != "" {
				if v, exists, err := catRepo.CatalogUpdatedAt(ctx, route.Kind, entityID); err == nil && exists {
					answer.UpdatedAt = v
				}
			}
			now := time.Now().UTC().Format(time.RFC3339)
			payload := map[string]any{"via": "till-sync", "till": till.Name, "entity": route.Kind, "entity_id": entityID, "route": route.Pattern}
			auditID := entityID
			if auditID == "" {
				auditID = route.Kind
			}
			if in.ApproverID != "" {
				err = posRepo.InsertAuditElevated(ctx, nil, in.ApproverID, in.ActorID, "catalog", auditID, "catalog_changed_via_till", payload, now, "")
			} else {
				err = posRepo.InsertAudit(ctx, nil, in.ActorID, "catalog", auditID, "catalog_changed_via_till", payload, now, "")
			}
			if err != nil {
				// Best-effort like the settings write-through: the write
				// already succeeded.
				logging.L().Errorf("sync catalog: audit of %s %s from %s failed: %v", route.Kind, entityID, till.Name, err)
			}
			d.NudgeLink(fleetlink.ScopeAdmin)
			logging.L().Infof("sync catalog: %s (%s %s) applied from %s by %s (ut-docs#2817)", route.Pattern, route.Kind, entityID, till.Name, holder.ID)
		} else {
			logging.L().Infof("sync catalog: %s from %s answered %d by the handler — relayed, not applied", route.Pattern, till.Name, cw.status)
		}
		writeSyncOrdersJSON(w, http.StatusOK, answer, nil)
	})
}

// syncCatalogCapture records a dispatched handler's answer for relaying.
type syncCatalogCapture struct {
	header   http.Header
	status   int
	wrote    bool
	buf      bytes.Buffer
	overflow bool
}

func newSyncCatalogCapture() *syncCatalogCapture {
	return &syncCatalogCapture{header: http.Header{}, status: http.StatusOK}
}

func (c *syncCatalogCapture) Header() http.Header { return c.header }

func (c *syncCatalogCapture) WriteHeader(status int) {
	if c.wrote {
		return
	}
	c.wrote = true
	c.status = status
}

func (c *syncCatalogCapture) Write(b []byte) (int, error) {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	if c.buf.Len()+len(b) > maxSyncCatalogAnswer {
		c.overflow = true
		return len(b), nil
	}
	return c.buf.Write(b)
}
