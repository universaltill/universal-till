package pages

// Additional tills push their own day closes to the main till (ut-docs#5074
// M7, design D9), so the main till holds every close of the shop and relays
// them in the sales copy, and a promoted till keeps them all.
//
//   - POST /api/sync/closes (additional till → main till): a batch of the
//     pushing till's own report_archive rows, stored in peer_closes — never
//     the main till's report_archive, so its own Z chain and numbers are
//     untouched.
//   - syncPushCloses: the additional till's step in the journal push tick.
//
// Only while the copy is on: with sales_mirror.enabled off (the default) the
// push step returns after its settings reads and the route answers 409.
// Pinned TLS only, like the feed (D5).

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

const (
	// salesMirrorClosesBatch bounds one push; a close is ~10 KB.
	salesMirrorClosesBatch = 20
	// salesMirrorClosesMaxBody bounds the request the main till reads.
	salesMirrorClosesMaxBody = 4 << 20
	// salesMirrorClosePushCursorKey is this till's position in its own
	// report_archive ("created_at|id"); per till ("sync." prefix).
	salesMirrorClosePushCursorKey = "sync.close_push_cursor"
)

func registerSyncPeerCloses(mux *http.ServeMux, d *common.Deps) {
	tills := data.NewTillsRepo(d.Db)
	repo := data.NewSalesMirrorRepo(d.Db)
	mux.HandleFunc("POST /api/sync/closes", func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			writeSalesMirrorError(w, http.StatusForbidden, salesMirrorError{Code: "tls_required", Message: "closes are sent over pinned TLS only"})
			return
		}
		till, ok := syncTill(r, tills)
		if !ok {
			writeSalesMirrorError(w, http.StatusUnauthorized, salesMirrorError{Code: "unauthorized", Message: "unknown till"})
			return
		}
		if till.Role == data.TillRoleSatellite {
			writeSalesMirrorError(w, http.StatusForbidden, salesMirrorError{Code: "satellite", Message: "satellites keep no day closes (ADR-0086)"})
			return
		}
		if !salesMirrorMainGate(r.Context(), d) {
			writeSalesMirrorError(w, http.StatusConflict, salesMirrorError{Code: "sales_mirror_off", Message: "the sales copy is off"})
			return
		}
		var batch []data.MirrorClose
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, salesMirrorClosesMaxBody)).Decode(&batch); err != nil || len(batch) > salesMirrorClosesBatch {
			writeSalesMirrorError(w, http.StatusBadRequest, salesMirrorError{Code: "bad_request", Message: "a JSON array of at most 20 closes"})
			return
		}
		for i := range batch {
			if !validPeerClose(batch[i]) {
				writeSalesMirrorError(w, http.StatusBadRequest, salesMirrorError{Code: "bad_request", Message: "close " + batch[i].ID + " is incomplete"})
				return
			}
		}
		n, err := repo.InsertPeerCloses(r.Context(), till.ID, batch, time.Now())
		if err != nil {
			logging.L().Errorf("sales mirror: store closes from till %s: %v", till.ID, err)
			writeSalesMirrorError(w, http.StatusInternalServerError, salesMirrorError{Code: "server", Message: "could not store the closes"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]int{"applied": n, "skipped": len(batch) - n}, "error": nil})
	})
}

// salesMirrorCloseFieldMax bounds the identifiers of a pushed close.
const salesMirrorCloseFieldMax = 128

// validPeerClose is the main till's check of a pushed close (external
// input): identifiers present and bounded, created_at in report_archive's
// own text form (the state re-sync orders and windows by it), content JSON.
func validPeerClose(c data.MirrorClose) bool {
	for _, f := range []string{c.ID, c.Kind, c.Period} {
		if f == "" || f != strings.TrimSpace(f) || len(f) > salesMirrorCloseFieldMax {
			return false
		}
	}
	if _, err := time.Parse("2006-01-02 15:04:05", c.CreatedAt); err != nil {
		return false
	}
	return json.Valid([]byte(c.ContentJSON))
}

func parseClosePushCursor(v string) data.MirrorCloseCursor {
	at, id, _ := strings.Cut(v, "|")
	return data.MirrorCloseCursor{CreatedAt: at, ID: id}
}

// syncPushCloses is the journal push tick's closes step on an additional
// till: send this till's own closes past its cursor, advance on a 200. A
// satellite keeps no closes for the shop (ADR-0086) and sends nothing. A
// close the main till would refuse as malformed is skipped here with a
// warning, so one bad row never stops every later close (M7 review F1).
func syncPushCloses(ctx context.Context, d *common.Deps, client *http.Client, primary, bearer string, pinned func(context.Context) bool) {
	if !salesMirrorGate(ctx, d) || !pinned(ctx) {
		return
	}
	if role, _, _ := d.Settings.Get(ctx, "sync.till_role"); strings.TrimSpace(role) == data.TillRoleSatellite {
		return
	}
	repo := data.NewSalesMirrorRepo(d.Db)
	cur, _, _ := d.Settings.Get(ctx, salesMirrorClosePushCursorKey)
	closes, err := repo.OwnClosesAfter(ctx, parseClosePushCursor(strings.TrimSpace(cur)), salesMirrorClosesBatch)
	if err != nil {
		logging.L().Warnf("sales mirror: read this till's closes to push: %v", err)
		return
	}
	if len(closes) == 0 {
		return
	}
	last := closes[len(closes)-1]
	send := closes[:0:0]
	for _, c := range closes {
		if validPeerClose(c) {
			send = append(send, c)
		} else {
			logging.L().Warnf("sales mirror: day close %s cannot be sent to the main till (malformed) and is skipped", c.ID)
		}
	}
	advance := func() { _ = d.Settings.Set(ctx, salesMirrorClosePushCursorKey, last.CreatedAt+"|"+last.ID) }
	if len(send) == 0 {
		advance()
		return
	}
	raw, _ := json.Marshal(send)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(primary, "/")+"/api/sync/closes", bytes.NewReader(raw))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		advance()
	case http.StatusConflict, http.StatusForbidden:
		// Off on the main till, or this till is a satellite / not on pinned
		// TLS there: nothing to do until that changes; not an error.
	default:
		logging.L().Infof("sales mirror: closes push refused: %s — will retry", resp.Status)
	}
}
