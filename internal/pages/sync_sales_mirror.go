package pages

// The main till's side of the read-only sales copy (ut-docs#5074, ADR-0011
// and ADR-0114 "Amendment 2026-10-26"; wire contract ut-docs
// reference/contracts/pos-lan-sales-mirror.md, design §4-§5.1):
//
//   - GET /api/sync/sales-mirror        the feed: one page of the change log
//     after the caller's cursor, as full current records; acknowledges the
//     cursor.
//   - GET /api/sync/sales-mirror/state  the state re-sync after a first
//     pull, an epoch change or a passed horizon.
//   - POST /api/sync/sales-mirror/enable|disable  the shop switch (manager),
//     which mints a new epoch on every enable.
//   - StartSalesMirrorMaintenance        prunes the change log.
//
// Ships dark: with sales_mirror.enabled off (the default) the feed answers
// 409, the maintenance loop does one settings read per tick and nothing
// else, and the change-log triggers write nothing (migration 093).

import (
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/diskspace"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// SalesMirrorFeedVersion is the feed's major version (contract 1.0.0).
const SalesMirrorFeedVersion = 1

// Feed bounds (design §5.1, §11).
const (
	salesMirrorDefaultLimit = 200
	salesMirrorMaxLimit     = 500
	salesMirrorMaxPageBytes = 1 << 20
	salesMirrorMaxInFlight  = 2
	// An acknowledgement is written when it moved by at least this many
	// records, or this long after the last write (design §4.2). A stale ack
	// only delays pruning.
	salesMirrorAckEvery    = 50
	salesMirrorAckInterval = 5 * time.Second
	salesMirrorMaintEvery  = 10 * time.Minute
)

// salesMirrorEnvKilled is the support kill switch (UT_SALES_MIRROR=off).
func salesMirrorEnvKilled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("UT_SALES_MIRROR")), "off")
}

// salesMirrorGate reports whether this till may take part in the copy at
// all: shop setting on, this till's own kill switch off, process not
// killed.
func salesMirrorGate(ctx context.Context, d *common.Deps) bool {
	if salesMirrorEnvKilled() || d.Settings == nil {
		return false
	}
	get := func(k string) string {
		v, _, _ := d.Settings.Get(ctx, k)
		return strings.TrimSpace(v)
	}
	return get(data.SalesMirrorEnabledKey) == "1" && get(data.SalesMirrorTillOffKey) != "1"
}

// salesMirrorMainGate is salesMirrorGate on a main till.
func salesMirrorMainGate(ctx context.Context, d *common.Deps) bool {
	return d.SyncPrimaryURL(ctx) == "" && salesMirrorGate(ctx, d)
}

// salesMirrorEpoch returns the shop's epoch, minting one when the copy is on
// and none exists (first enable without the switch handler, or after a
// restore dropped it — db.RestoreDropKeys).
//
// A plain read first: GetOrCreate takes the database write lock (BEGIN
// IMMEDIATE), and every served page asks for the epoch — a catch-up must
// never contend with a sale for that lock (M2 review finding 2).
func salesMirrorEpoch(ctx context.Context, d *common.Deps) (string, error) {
	if v, ok, err := d.Settings.Get(ctx, data.SalesMirrorEpochKey); err != nil {
		return "", err
	} else if ok && strings.TrimSpace(v) != "" {
		return v, nil
	}
	return d.Settings.GetOrCreate(ctx, data.SalesMirrorEpochKey, uuid.NewString())
}

func salesMirrorSource(ctx context.Context, d *common.Deps) data.MirrorSource {
	id, _ := discovery.TillID(ctx, d.Settings)
	name, _, _ := d.Settings.Get(ctx, "till.name")
	return data.MirrorSource{MainTillID: id, MainTillName: strings.TrimSpace(name)}
}

type salesMirrorError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Epoch   string `json:"epoch,omitempty"`
	Horizon *int64 `json:"horizon,omitempty"`
	HeadSeq *int64 `json:"head_seq,omitempty"`
}

func writeSalesMirrorError(w http.ResponseWriter, status int, e salesMirrorError) {
	writeJSON(w, status, map[string]any{"data": nil, "error": e})
}

// writeSalesMirrorData writes the success envelope, gzip-compressed when the
// caller asks for it (a full catch-up is ~600 pages).
func writeSalesMirrorData(w http.ResponseWriter, r *http.Request, v any) {
	w.Header().Set("Content-Type", "application/json")
	body := map[string]any{"data": v, "error": nil}
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Vary", "Accept-Encoding")
		w.WriteHeader(http.StatusOK)
		gz := gzip.NewWriter(w)
		_ = json.NewEncoder(gz).Encode(body)
		_ = gz.Close()
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

// salesMirrorAcks throttles acknowledgement writes (design §4.2): at most
// one write per till per salesMirrorAckInterval unless the ack moved by
// salesMirrorAckEvery, and never one when nothing moved.
type salesMirrorAcks struct {
	mu   sync.Mutex
	last map[string]salesMirrorAckMark
}

type salesMirrorAckMark struct {
	epoch string
	seq   int64
	at    time.Time
}

func (a *salesMirrorAcks) record(ctx context.Context, repo *data.SalesMirrorRepo, tillID, epoch string, seq int64, force bool, now time.Time) {
	a.mu.Lock()
	m, ok := a.last[tillID]
	write := force || !ok || m.epoch != epoch ||
		(seq != m.seq && (seq-m.seq >= salesMirrorAckEvery || now.Sub(m.at) >= salesMirrorAckInterval))
	if write {
		a.last[tillID] = salesMirrorAckMark{epoch: epoch, seq: seq, at: now}
	}
	a.mu.Unlock()
	if !write {
		return
	}
	if err := repo.UpsertAck(ctx, tillID, epoch, seq, now); err != nil {
		logging.L().Warnf("sales mirror: record till %s's acknowledgement: %v", tillID, err)
		a.mu.Lock()
		delete(a.last, tillID)
		a.mu.Unlock()
	}
}

type salesMirrorServer struct {
	d    *common.Deps
	repo *data.SalesMirrorRepo
	// tills is the roster repo: syncTill authenticates through it.
	tills *data.TillsRepo
	sem   chan struct{}
	acks  *salesMirrorAcks
	now   func() time.Time
	// caughtUp is called when a peer's feed page reached the head
	// (more=false): the link pump (M4) resumes frames from toSeq. Nil-safe.
	caughtUp func(tillID, epoch string, toSeq int64)
	// afterRead, when set (tests only), runs between the log read and the
	// horizon re-check: where a concurrent cap prune would land.
	afterRead func()
}

func newSalesMirrorServer(d *common.Deps) *salesMirrorServer {
	return &salesMirrorServer{
		d: d, repo: data.NewSalesMirrorRepo(d.Db), tills: data.NewTillsRepo(d.Db),
		sem: make(chan struct{}, salesMirrorMaxInFlight), acks: &salesMirrorAcks{last: map[string]salesMirrorAckMark{}},
		now: time.Now,
	}
}

func registerSyncSalesMirror(mux *http.ServeMux, d *common.Deps) *salesMirrorServer {
	s := newSalesMirrorServer(d)
	mux.HandleFunc("GET /api/sync/sales-mirror", s.feed)
	mux.HandleFunc("GET /api/sync/sales-mirror/state", s.state)
	mux.HandleFunc("POST /api/sync/sales-mirror/enable", s.toggle(true))
	mux.HandleFunc("POST /api/sync/sales-mirror/disable", s.toggle(false))
	return s
}

// preflight runs the checks every feed and state request shares, in the
// contract's order. ok=false means the response is written.
func (s *salesMirrorServer) preflight(w http.ResponseWriter, r *http.Request) (data.TillRow, string, func(), bool) {
	// Pinned TLS only (D5): checked before the bearer is even looked at.
	if r.TLS == nil {
		writeSalesMirrorError(w, http.StatusForbidden, salesMirrorError{Code: "tls_required", Message: "the sales copy is served over pinned TLS only; pair this till again"})
		return data.TillRow{}, "", nil, false
	}
	till, ok := syncTill(r, s.tills)
	if !ok {
		writeSalesMirrorError(w, http.StatusUnauthorized, salesMirrorError{Code: "unauthorized", Message: "unknown till"})
		return data.TillRow{}, "", nil, false
	}
	if till.Role == data.TillRoleSatellite {
		writeSalesMirrorError(w, http.StatusForbidden, salesMirrorError{Code: "satellite", Message: "satellites hold no sales copy (ADR-0086)"})
		return data.TillRow{}, "", nil, false
	}
	if !salesMirrorMainGate(r.Context(), s.d) {
		writeSalesMirrorError(w, http.StatusConflict, salesMirrorError{Code: "sales_mirror_off", Message: "the sales copy is off"})
		return data.TillRow{}, "", nil, false
	}
	select {
	case s.sem <- struct{}{}:
	default:
		w.Header().Set("Retry-After", "5")
		writeSalesMirrorError(w, http.StatusServiceUnavailable, salesMirrorError{Code: "busy", Message: "try again shortly"})
		return data.TillRow{}, "", nil, false
	}
	release := func() { <-s.sem }
	epoch, err := salesMirrorEpoch(r.Context(), s.d)
	if err != nil {
		release()
		writeSalesMirrorError(w, http.StatusInternalServerError, salesMirrorError{Code: "server", Message: "could not read the copy epoch"})
		return data.TillRow{}, "", nil, false
	}
	return till, epoch, release, true
}

func parseMirrorLimit(v string) int {
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return salesMirrorDefaultLimit
	}
	return min(n, salesMirrorMaxLimit)
}

// SalesMirrorPage is the feed page body (contract "the feed").
type SalesMirrorPage struct {
	FeedVersion int                 `json:"feed_version"`
	Epoch       string              `json:"epoch"`
	MainTillID  string              `json:"main_till_id"`
	FromSeq     int64               `json:"from_seq"`
	ToSeq       int64               `json:"to_seq"`
	HeadSeq     int64               `json:"head_seq"`
	Horizon     int64               `json:"horizon"`
	More        bool                `json:"more"`
	Records     []data.MirrorRecord `json:"records"`
}

func (s *salesMirrorServer) feed(w http.ResponseWriter, r *http.Request) {
	till, epoch, release, ok := s.preflight(w, r)
	if !ok {
		return
	}
	defer release()
	ctx := r.Context()
	q := r.URL.Query()
	after, err := strconv.ParseInt(q.Get("after"), 10, 64)
	if q.Get("after") == "" {
		after, err = 0, nil
	}
	if err != nil || after < 0 {
		writeSalesMirrorError(w, http.StatusBadRequest, salesMirrorError{Code: "bad_request", Message: "after must be a non-negative integer"})
		return
	}
	head, err := s.repo.LogHead(ctx)
	if err != nil {
		writeSalesMirrorError(w, http.StatusInternalServerError, salesMirrorError{Code: "server", Message: "could not read the change log"})
		return
	}
	horizon, err := s.repo.LogHorizon(ctx)
	if err != nil {
		writeSalesMirrorError(w, http.StatusInternalServerError, salesMirrorError{Code: "server", Message: "could not read the change log"})
		return
	}
	if q.Get("epoch") != epoch || after < horizon || after > head {
		writeSalesMirrorError(w, http.StatusGone, salesMirrorError{Code: "horizon_passed", Message: "re-sync by state",
			Epoch: epoch, Horizon: &horizon, HeadSeq: &head})
		return
	}
	s.acks.record(ctx, s.repo, till.ID, epoch, after, false, s.now())

	entries, err := s.repo.ReadLog(ctx, after, parseMirrorLimit(q.Get("limit")))
	if err != nil {
		writeSalesMirrorError(w, http.StatusInternalServerError, salesMirrorError{Code: "server", Message: "could not read the change log"})
		return
	}
	// Entries logged after head was read belong to the next page, so
	// to_seq never passes the head_seq this page reports (finding 7).
	for i, e := range entries {
		if e.Seq > head {
			entries = entries[:i]
			break
		}
	}
	if s.afterRead != nil {
		s.afterRead()
	}
	// A cap prune that ran between the horizon check and the read could
	// have deleted entries just above after: the page would then skip them
	// while still claiming to start at after. Re-check, and send the till
	// to a state re-sync instead (M2 review finding 3).
	if hz, err := s.repo.LogHorizon(ctx); err != nil || hz > after {
		if err == nil {
			horizon = hz
		}
		writeSalesMirrorError(w, http.StatusGone, salesMirrorError{Code: "horizon_passed", Message: "re-sync by state",
			Epoch: epoch, Horizon: &horizon, HeadSeq: &head})
		return
	}
	src := salesMirrorSource(ctx, s.d)
	recs, toSeq, err := buildMirrorRecords(ctx, s.repo, entries, after, till.ID, src)
	if err != nil {
		logging.L().Errorf("sales mirror: build page after %d for till %s: %v", after, till.ID, err)
		writeSalesMirrorError(w, http.StatusInternalServerError, salesMirrorError{Code: "server", Message: "could not build the page"})
		return
	}
	page := SalesMirrorPage{FeedVersion: SalesMirrorFeedVersion, Epoch: epoch, MainTillID: src.MainTillID,
		FromSeq: after, ToSeq: toSeq, HeadSeq: head, Horizon: horizon, More: toSeq < head, Records: recs}
	writeSalesMirrorData(w, r, page)
	if !page.More && s.caughtUp != nil {
		s.caughtUp(till.ID, epoch, toSeq)
	}
}

// buildMirrorRecords turns log entries into feed records with batched reads
// (one query per table, design §11), bounded by salesMirrorMaxPageBytes. It
// returns the records and the seq the page covers up to: the last entry it
// took, so a page cut by the byte bound still covers an exact range.
// Records whose CURRENT origin is the caller are left out but covered;
// several entries for one record in a page collapse to one (its state is
// the same current state either way).
func buildMirrorRecords(ctx context.Context, repo *data.SalesMirrorRepo, entries []data.MirrorLogEntry, after int64, callerTill string, src data.MirrorSource) ([]data.MirrorRecord, int64, error) {
	var saleIDs, closeIDs []string
	var peerKeys []data.MirrorPeerCloseKey
	for _, e := range entries {
		switch e.Op {
		case data.MirrorOpSale:
			saleIDs = append(saleIDs, e.RefID)
		case data.MirrorOpClose:
			if e.OriginTillID == "" {
				closeIDs = append(closeIDs, e.RefID)
			} else {
				peerKeys = append(peerKeys, data.MirrorPeerCloseKey{TillID: e.OriginTillID, ID: e.RefID})
			}
		}
	}
	sales, err := repo.LoadMirrorSales(ctx, saleIDs, src)
	if err != nil {
		return nil, after, err
	}
	closes, err := repo.LoadMirrorCloses(ctx, closeIDs, src)
	if err != nil {
		return nil, after, err
	}
	peerCloses, err := repo.LoadMirrorPeerCloses(ctx, peerKeys)
	if err != nil {
		return nil, after, err
	}
	recs := make([]data.MirrorRecord, 0, len(entries))
	sizes := make([]int, 0, len(entries))
	index := map[string]int{} // op+id → position in recs
	total := 0
	toSeq := after
	for _, e := range entries {
		var rec data.MirrorRecord
		switch e.Op {
		case data.MirrorOpSale:
			sale := sales[e.RefID]
			switch {
			case sale == nil:
				rec = data.MirrorRecord{Seq: e.Seq, Op: e.Op, ID: e.RefID, Gone: true}
			case sale.OriginTillID == callerTill:
				toSeq = e.Seq
				continue
			default:
				rec = data.MirrorRecord{Seq: e.Seq, Op: e.Op, Sale: sale}
			}
		case data.MirrorOpClose:
			c := closes[e.RefID]
			if e.OriginTillID != "" {
				// An additional till's own close (M7), relayed from
				// peer_closes; never sent back to that till.
				if e.OriginTillID == callerTill {
					toSeq = e.Seq
					continue
				}
				c = peerCloses[data.MirrorPeerCloseKey{TillID: e.OriginTillID, ID: e.RefID}]
			}
			if c == nil {
				rec = data.MirrorRecord{Seq: e.Seq, Op: e.Op, ID: e.RefID, Gone: true}
			} else {
				rec = data.MirrorRecord{Seq: e.Seq, Op: e.Op, Close: c}
			}
		default:
			toSeq = e.Seq
			continue
		}
		raw, err := json.Marshal(rec)
		if err != nil {
			return nil, after, err
		}
		key := e.Op + "\x00" + e.RefID
		if e.Op == data.MirrorOpClose {
			// Closes are keyed by their till (M7 review F2): another till's
			// close may carry the same id.
			key += "\x00" + e.OriginTillID
		}
		prev, dup := index[key]
		grow := len(raw)
		if dup {
			grow -= sizes[prev]
		}
		if len(recs) > 0 && total+grow > salesMirrorMaxPageBytes {
			break
		}
		if dup {
			// Keep the later seq, in the earlier slot's place: the stored
			// copy compares seqs, and the state is the same.
			recs[prev], sizes[prev] = rec, len(raw)
		} else {
			index[key] = len(recs)
			recs = append(recs, rec)
			sizes = append(sizes, len(raw))
		}
		total += grow
		toSeq = e.Seq
	}
	return recs, toSeq, nil
}

// SalesMirrorStatePage is the state re-sync page body.
type SalesMirrorStatePage struct {
	FeedVersion int                 `json:"feed_version"`
	Epoch       string              `json:"epoch"`
	MainTillID  string              `json:"main_till_id"`
	ResumeSeq   int64               `json:"resume_seq"`
	NextCursor  string              `json:"next_cursor"`
	More        bool                `json:"more"`
	Records     []data.MirrorRecord `json:"records"`
}

func encodeMirrorStateCursor(k data.MirrorStateKey) string {
	return base64.RawURLEncoding.EncodeToString([]byte(k.Phase + "\x00" + k.A + "\x00" + k.B))
}

func decodeMirrorStateCursor(c string) (data.MirrorStateKey, bool) {
	if c == "" {
		return data.MirrorStateKey{Phase: "sales"}, true
	}
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return data.MirrorStateKey{}, false
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 3 || (parts[0] != "sales" && parts[0] != "closes" && parts[0] != "peer") {
		return data.MirrorStateKey{}, false
	}
	return data.MirrorStateKey{Phase: parts[0], A: parts[1], B: parts[2]}, true
}

func validMirrorDate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func (s *salesMirrorServer) state(w http.ResponseWriter, r *http.Request) {
	till, epoch, release, ok := s.preflight(w, r)
	if !ok {
		return
	}
	defer release()
	ctx := r.Context()
	q := r.URL.Query()
	fromDate := q.Get("from_date")
	key, okCursor := decodeMirrorStateCursor(q.Get("cursor"))
	if !validMirrorDate(fromDate) || !okCursor {
		writeSalesMirrorError(w, http.StatusBadRequest, salesMirrorError{Code: "bad_request", Message: "from_date (YYYY-MM-DD) and a valid cursor are required"})
		return
	}
	head, err := s.repo.LogHead(ctx)
	if err != nil {
		writeSalesMirrorError(w, http.StatusInternalServerError, salesMirrorError{Code: "server", Message: "could not read the change log"})
		return
	}
	horizon, err := s.repo.LogHorizon(ctx)
	if err != nil {
		writeSalesMirrorError(w, http.StatusInternalServerError, salesMirrorError{Code: "server", Message: "could not read the change log"})
		return
	}
	first := q.Get("cursor") == ""
	resume := head
	if !first {
		resume, err = strconv.ParseInt(q.Get("resume_seq"), 10, 64)
		if err != nil {
			resume = -1
		}
	}
	if q.Get("epoch") != epoch || resume < horizon || resume > head {
		writeSalesMirrorError(w, http.StatusGone, salesMirrorError{Code: "horizon_passed", Message: "restart the re-sync",
			Epoch: epoch, Horizon: &horizon, HeadSeq: &head})
		return
	}
	if first {
		// Hold the log above resume_seq for this till until it acknowledges
		// further (contract: the first page records the ack).
		s.acks.record(ctx, s.repo, till.ID, epoch, resume, true, s.now())
	}
	limit := parseMirrorLimit(q.Get("limit"))
	src := salesMirrorSource(ctx, s.d)
	page := SalesMirrorStatePage{FeedVersion: SalesMirrorFeedVersion, Epoch: epoch, MainTillID: src.MainTillID, ResumeSeq: resume, Records: []data.MirrorRecord{}}
	next := key
	total := 0
	switch key.Phase {
	case "sales":
		afterRow := int64(-1)
		if key.B != "" {
			if afterRow, err = strconv.ParseInt(key.B, 10, 64); err != nil {
				writeSalesMirrorError(w, http.StatusBadRequest, salesMirrorError{Code: "bad_request", Message: "a valid cursor is required"})
				return
			}
		}
		ids, dates, rowids, err := s.repo.StateSaleIDs(ctx, fromDate, key.A, afterRow, till.ID, limit)
		if err != nil {
			writeSalesMirrorError(w, http.StatusInternalServerError, salesMirrorError{Code: "server", Message: "could not read sales"})
			return
		}
		sales, err := s.repo.LoadMirrorSales(ctx, ids, src)
		if err != nil {
			writeSalesMirrorError(w, http.StatusInternalServerError, salesMirrorError{Code: "server", Message: "could not read sales"})
			return
		}
		cut := false
		for i, id := range ids {
			if sale := sales[id]; sale != nil {
				rec := data.MirrorRecord{Seq: resume, Op: data.MirrorOpSale, Sale: sale}
				raw, _ := json.Marshal(rec)
				if len(page.Records) > 0 && total+len(raw) > salesMirrorMaxPageBytes {
					cut = true
					break
				}
				total += len(raw)
				page.Records = append(page.Records, rec)
			}
			next = data.MirrorStateKey{Phase: "sales", A: dates[i], B: strconv.FormatInt(rowids[i], 10)}
		}
		if !cut && len(ids) < limit {
			next = data.MirrorStateKey{Phase: "closes"}
		}
		page.More = true
	case "closes":
		ids, created, err := s.repo.StateCloseIDs(ctx, fromDate, key.A, key.B, limit)
		if err != nil {
			writeSalesMirrorError(w, http.StatusInternalServerError, salesMirrorError{Code: "server", Message: "could not read closes"})
			return
		}
		closes, err := s.repo.LoadMirrorCloses(ctx, ids, src)
		if err != nil {
			writeSalesMirrorError(w, http.StatusInternalServerError, salesMirrorError{Code: "server", Message: "could not read closes"})
			return
		}
		cut := false
		for i, id := range ids {
			if c := closes[id]; c != nil {
				rec := data.MirrorRecord{Seq: resume, Op: data.MirrorOpClose, Close: c}
				raw, _ := json.Marshal(rec)
				if len(page.Records) > 0 && total+len(raw) > salesMirrorMaxPageBytes {
					cut = true
					break
				}
				total += len(raw)
				page.Records = append(page.Records, rec)
			}
			next = data.MirrorStateKey{Phase: "closes", A: created[i], B: id}
		}
		page.More = true
		if !cut && len(ids) < limit {
			next = data.MirrorStateKey{Phase: "peer"}
		}
	case "peer":
		// The additional tills' own closes (M7), never the caller's own.
		afterRow := int64(-1)
		if key.B != "" {
			if afterRow, err = strconv.ParseInt(key.B, 10, 64); err != nil {
				writeSalesMirrorError(w, http.StatusBadRequest, salesMirrorError{Code: "bad_request", Message: "a valid cursor is required"})
				return
			}
		}
		keys, created, rowids, err := s.repo.StatePeerCloseKeys(ctx, fromDate, key.A, afterRow, till.ID, limit)
		if err != nil {
			writeSalesMirrorError(w, http.StatusInternalServerError, salesMirrorError{Code: "server", Message: "could not read closes"})
			return
		}
		closes, err := s.repo.LoadMirrorPeerCloses(ctx, keys)
		if err != nil {
			writeSalesMirrorError(w, http.StatusInternalServerError, salesMirrorError{Code: "server", Message: "could not read closes"})
			return
		}
		cut := false
		for i, k := range keys {
			if c := closes[k]; c != nil {
				rec := data.MirrorRecord{Seq: resume, Op: data.MirrorOpClose, Close: c}
				raw, _ := json.Marshal(rec)
				if len(page.Records) > 0 && total+len(raw) > salesMirrorMaxPageBytes {
					cut = true
					break
				}
				total += len(raw)
				page.Records = append(page.Records, rec)
			}
			next = data.MirrorStateKey{Phase: "peer", A: created[i], B: strconv.FormatInt(rowids[i], 10)}
		}
		page.More = cut || len(keys) >= limit
	}
	if page.More {
		page.NextCursor = encodeMirrorStateCursor(next)
	}
	writeSalesMirrorData(w, r, page)
}

// toggle is the shop switch on the main till (manager, sync_management).
// Every enable mints a NEW epoch in the same write, so every additional
// till re-syncs by state: sales made while the copy was off were never
// logged, and only a re-sync brings them (design §3.2, AC7). Disabling
// keeps every copy and the epoch.
func (s *salesMirrorServer) toggle(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hx := r.Header.Get("HX-Request") != ""
		if !canPerform(s.d, r, "sync_management") {
			if hx {
				renderSalesMirrorCardErr(w, r, s.d, "common.error.manager_or_admin_required")
				return
			}
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		if s.d.SyncPrimaryURL(r.Context()) != "" {
			writeSalesMirrorError(w, http.StatusConflict, salesMirrorError{Code: "not_main", Message: "the sales copy is switched on the main till"})
			return
		}
		kv := map[string]string{data.SalesMirrorEnabledKey: "0"}
		action := "sales_mirror_disabled"
		epoch, _, _ := s.d.Settings.Get(r.Context(), data.SalesMirrorEpochKey)
		was, _, _ := s.d.Settings.Get(r.Context(), data.SalesMirrorEnabledKey)
		if on && strings.TrimSpace(was) == "1" && epoch != "" {
			// Already on (a second press, a stale tab): nothing changes —
			// a new epoch would send every till into a re-sync for nothing.
			kv, action = nil, ""
		} else if on {
			epoch = uuid.NewString()
			kv = map[string]string{data.SalesMirrorEnabledKey: "1", data.SalesMirrorEpochKey: epoch}
			action = "sales_mirror_enabled"
		}
		if kv != nil {
			// settings-write:allow the shop switch is written on the main till only (an additional till is refused above), where shop settings are authoritative; the admin bundle carries it to every till
			if err := s.d.Settings.SetMany(r.Context(), kv); err != nil {
				if hx {
					logging.L().Errorf("sales mirror: switch: %v", err)
					renderSalesMirrorCardErr(w, r, s.d, "sync.error.server")
					return
				}
				common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "sales_mirror", err)
				return
			}
			_ = data.NewPOSRepo(s.d.Db).InsertAudit(r.Context(), nil, getSessionUserID(r), "setting", data.SalesMirrorEnabledKey, action,
				nil, time.Now().UTC().Format(time.RFC3339), "")
		}
		if hx {
			// The Tills page's card (M5): re-render it in place.
			renderSalesMirrorCard(w, r, s.d)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"enabled": on, "epoch": epoch}, "error": nil})
	}
}

// salesMirrorSettingInt reads a positive integer setting with a default.
func salesMirrorSettingInt(ctx context.Context, d *common.Deps, key string, def int) int {
	v, _, _ := d.Settings.Get(ctx, key)
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// StartSalesMirrorMaintenance runs the main till's change-log prune
// (design §4.3) every salesMirrorMaintEvery. While the copy is off, killed,
// or this till is not the main till, a tick is at most three settings reads
// and nothing else.
func StartSalesMirrorMaintenance(ctx context.Context, d *common.Deps, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer logging.RecoverAndLog("pages.salesMirrorMaintenance")
		defer wg.Done()
		t := time.NewTicker(salesMirrorMaintEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				salesMirrorMaintenanceTick(ctx, d, time.Now(), diskspace.Probe)
			}
		}
	}()
}

// salesMirrorMaintenanceTick prunes the log once. probe measures the disk
// (caps halve below the low-disk floor).
func salesMirrorMaintenanceTick(ctx context.Context, d *common.Deps, now time.Time, probe func(string) (diskspace.Usage, error)) {
	// ut-docs#5074 M6: an adoption a promote left unfinished (one settings
	// read otherwise).
	resumeSalesMirrorAdoption(ctx, d)
	if !salesMirrorMainGate(ctx, d) {
		return
	}
	epoch, err := salesMirrorEpoch(ctx, d)
	if err != nil {
		logging.L().Warnf("sales mirror: read epoch: %v", err)
		return
	}
	maxDays := salesMirrorSettingInt(ctx, d, data.SalesMirrorLogMaxDaysKey, data.SalesMirrorDefaultLogMaxDays)
	maxRows := salesMirrorSettingInt(ctx, d, data.SalesMirrorLogMaxRowsKey, data.SalesMirrorDefaultLogMaxRows)
	if d.Cfg != nil && d.Cfg.DBPath != "" {
		if u, err := probe(d.Cfg.DBPath); err == nil && diskspace.Low(u) {
			maxDays, maxRows = max(1, maxDays/2), max(1, maxRows/2)
		}
	}
	res, err := data.NewSalesMirrorRepo(d.Db).PruneLog(ctx, data.MirrorLogPruneOpts{Epoch: epoch, MaxDays: maxDays, MaxRows: maxRows, Now: now})
	if err != nil {
		logging.L().Warnf("sales mirror: prune the change log: %v", err)
		return
	}
	for _, id := range res.CapPassed {
		logging.L().Warnf("sales mirror: log cap passed till %s's acknowledgement; it will re-sync by state", id)
	}
}

// SalesMirrorMainStatus is the main till's side of the copy for diagnostics
// (design §6 "Diagnostics"): the log and every till's acknowledgement.
type SalesMirrorMainStatus struct {
	State   string           `json:"state"` // off | killed | on | not_main
	Epoch   string           `json:"epoch"`
	HeadSeq int64            `json:"head_seq"`
	Horizon int64            `json:"horizon"`
	LogRows int64            `json:"log_rows"`
	Acks    []SalesMirrorAck `json:"acks"`
}

// SalesMirrorAck is one additional till's acknowledgement as shown.
type SalesMirrorAck struct {
	TillID   string `json:"till_id"`
	Epoch    string `json:"epoch"`
	AckedSeq int64  `json:"acked_seq"`
	AckedAt  string `json:"acked_at"`
	Behind   int64  `json:"behind"`
}

func salesMirrorMainStatus(ctx context.Context, d *common.Deps) SalesMirrorMainStatus {
	st := SalesMirrorMainStatus{State: "on", Acks: []SalesMirrorAck{}}
	switch {
	case d.SyncPrimaryURL(ctx) != "":
		st.State = "not_main"
		return st
	case salesMirrorEnvKilled():
		st.State = "killed"
	case !salesMirrorGate(ctx, d):
		st.State = "off"
	}
	repo := data.NewSalesMirrorRepo(d.Db)
	st.Epoch, _, _ = d.Settings.Get(ctx, data.SalesMirrorEpochKey)
	st.HeadSeq, _ = repo.LogHead(ctx)
	st.Horizon, _ = repo.LogHorizon(ctx)
	st.LogRows, _ = repo.LogCount(ctx)
	acks, _ := repo.Acks(ctx)
	for _, a := range acks {
		behind := int64(0)
		if a.Epoch == st.Epoch && st.HeadSeq > a.AckedSeq {
			behind = st.HeadSeq - a.AckedSeq
		}
		st.Acks = append(st.Acks, SalesMirrorAck{TillID: a.TillID, Epoch: a.Epoch, AckedSeq: a.AckedSeq, AckedAt: a.AckedAt, Behind: behind})
	}
	return st
}

// registerSalesMirrorStatus serves this till's side of the copy to a
// manager: GET /api/sync/sales-mirror/status (a local route, never a peer
// one; read-only, writes nothing).
func registerSalesMirrorStatus(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /api/sync/sales-mirror/status", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "sync_management") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"main": salesMirrorMainStatus(r.Context(), d),
			"copy": salesMirrorCopyStatus(r.Context(), d),
		}, "error": nil})
	})
}
