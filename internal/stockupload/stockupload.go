// Package stockupload is the till side of the cloud's stock mirror (ADR-0169
// §8, ut-docs#4763). The main till of a cloud-master store masters stock
// (ADR-0036); the cloud keeps a read-only StockLevelMirror so its allocation
// engine can push `stock.set` to sales channels. Run uploads the rows whose
// quantity changed.
//
// Where the rows come from: migration 091's triggers leave one mark per
// changed inventory row (item or variant at a location) in the same
// transaction as the inventory write, each under a fresh, monotonic `seq`
// (the till's movement sequence). Nothing on the sale path calls the network,
// so a sale never waits on this and an unreachable cloud only means the marks
// wait: they are cleared only when the cloud answers them.
//
// Idempotency is the cloud's: it applies a row only when its seq is newer
// than the one it holds (within this till's stream), so a retried upload, an
// upload whose answer was lost, or two overlapping runs change nothing twice.
// The mark is removed by its exact seq, so a sale made while the request was
// in flight (a newer seq) stays owed. If the cloud answers `stale` with a
// stored seq AHEAD of the row's, this till's sequence restarted below the
// cloud's (a restore from a backup): Run rotates the stream id, owes every
// row again, and the cloud replaces its rows from the new stream. A stream
// carries its start time; the cloud takes a different stream only if it
// started later than the stored one (a delayed request of an older stream
// changes nothing), answering the older one stale/older_stream.
//
// Poison control: a row the cloud could never take is dropped before sending
// (cloud id not a UUID, bad location); a batch the cloud refuses with a 400 is
// bisected within a budget and the refused rows dropped (rows left unresolved
// when the budget runs out stay owed for the next Run); an unknown_item answer keeps
// the mark for a few delayed retries. One bad row never stops the others.
//
// Quantities are integers on the wire: on_hand_milli is thousandths (stock is
// kept to three decimals). No float is sent.
//
// Who calls Run, and when, is internal/cloudsync's: at the end of each
// check-in that reached the cloud (any tier), and, on the realtime tier, from
// a loop that runs at most every 5 seconds. Only the main till of a
// cloud-master store may (cloudsync's gate checks that before calling Run).
package stockupload

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/data"
)

// Bounds, mirrored from the cloud (ut-cloud internal/stockmirror): the cloud
// refuses a whole request that exceeds them, so the till never sends one.
const (
	// MaxRows is the most rows in one request.
	MaxRows = 500
	// maxBatchesPerRun bounds one Run (10 000 rows); more continue on the
	// next trigger.
	maxBatchesPerRun = 20
	// maxLocationLen is the cloud's location id bound.
	maxLocationLen = 64
)

// Answer statuses.
const (
	StatusApplied  = "applied"
	StatusStale    = "stale"
	StatusRejected = "rejected"
)

// Answer codes the till acts on.
const (
	// CodeUnknownItem: the cloud's live catalogue does not hold the row's
	// item or variant (yet). The mark is kept and retried a few times.
	CodeUnknownItem = "unknown_item"
	// CodeOlderStream: a stale answer because the cloud holds a NEWER stream
	// of this store; the till starts a newer one.
	CodeOlderStream = "older_stream"
	// CodeStreamStartInFuture: a 400 because the request's stream start is
	// more than a day ahead of the cloud's clock.
	CodeStreamStartInFuture = "stream_start_in_future"
)

// Drop reasons (Result.DropReasons): the log codes for marks removed unsent
// or without a stored answer.
const (
	DropBadCloudID    = "bad_cloud_id"
	DropBadLocation   = "bad_location"
	DropCloudRefused  = "cloud_refused_row"
	DropUnknownItem   = "unknown_item_gave_up"
	DropRejectedOther = "rejected_other"
)

// Row is one on-hand row as the cloud takes it (docs: ut-cloud
// docs/api-reference.md, POST /api/v1/stores/stock/levels).
type Row struct {
	ItemID      string `json:"item_id"`
	VariantID   string `json:"variant_id,omitempty"`
	LocationID  string `json:"location_id"`
	OnHandMilli int64  `json:"on_hand_milli"`
	Seq         int64  `json:"seq"`
}

// Request is one upload call. The store id and the credential belong to the
// Poster. StreamStartedAt is when the stream began (UTC): the cloud applies a
// request of a different stream only if it started later than the stored one.
type Request struct {
	StreamID        string
	StreamStartedAt time.Time
	TillVersion     string
	Rows            []Row
}

// Answer is the cloud's verdict on one row, in request order. StoredSeq is
// set on a stale answer: the seq the cloud holds.
type Answer struct {
	Seq       int64  `json:"seq"`
	Status    string `json:"status"`
	Code      string `json:"code,omitempty"`
	StoredSeq int64  `json:"stored_seq,omitempty"`
}

// Poster sends one request. A *StopError means the cloud will not take stock
// from this till now (credential, mode, version, not the main till); any
// other error is transient. The marks stay in both cases.
type Poster func(ctx context.Context, req Request) ([]Answer, error)

// StopError is a refusal that retrying right away cannot change.
type StopError struct {
	Status int
	Code   string
}

func (e *StopError) Error() string {
	return fmt.Sprintf("stock upload not accepted (%d %s)", e.Status, e.Code)
}

// StatusError is any other non-200 answer.
type StatusError struct {
	Status int
	Code   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("stock upload answered %d %s", e.Status, e.Code)
}

// Result is what one Run did.
type Result struct {
	Sent, Applied, Stale, Rejected int
	// Dropped counts marks removed without being stored: rows the cloud can
	// never take (a cloud id that is not a UUID, a bad location id), one row
	// the cloud refused inside a refused batch, an unknown item that stayed
	// unknown. DropReasons breaks it down by code (the log line).
	Dropped     int
	DropReasons map[string]int
	// Deferred counts marks kept for a later retry (unknown_item).
	Deferred int
	// Rotated: the stream id was rotated (see the package comment).
	Rotated bool
	// Busy: another Run was in flight; this one did nothing.
	Busy bool
	// Stopped / StopStatus: the cloud's refusal code, when it refused.
	Stopped    string
	StopStatus int
}

func (r *Result) drop(reason string, n int) {
	if n == 0 {
		return
	}
	if r.DropReasons == nil {
		r.DropReasons = map[string]int{}
	}
	r.DropReasons[reason] += n
	r.Dropped += n
}

// ErrStreamConflict ends a Run (a failure: the caller backs off) when the
// cloud keeps answering that this till's stream is older than one it holds and
// this till may not rotate again yet (or already did in this Run). The marks
// are kept: an `older_stream` row is never acknowledged. Its message is the
// stable log code.
var ErrStreamConflict = errors.New("stock_upload_stream_conflict")

var running sync.Mutex

// maxBisectRequests bounds the extra requests spent finding the one row a
// refused batch (400) objects to.
const maxBisectRequests = 40

// exchange posts rows and, when the cloud refuses the batch with a 400,
// bisects it to find the refused row(s). Out of budget (several poison rows in
// one batch) the unresolved parts are left unsent and unanswered: they stay
// owed and the next Run continues, so a batch is always worked off. It returns the rows that were
// answered (in order, with their answers) and the sequences of rows the
// cloud refused one by one. A 400 is trusted to mean "this row" only if some
// other part of the batch was accepted: when nothing in it was, the request
// itself is wrong (store, stream, body) and the original refusal is returned
// so no mark is dropped for it.
func exchange(ctx context.Context, post Poster, req Request) (rows []Row, answers []Answer, refused []int64, err error) {
	first, perr := post(ctx, req)
	if perr == nil {
		return req.Rows, first, nil, nil
	}
	var stop *StopError
	if !errors.As(perr, &stop) || stop.Status != http.StatusBadRequest || len(req.Rows) < 2 {
		return nil, nil, nil, perr
	}
	budget := maxBisectRequests
	var bisect func(part []Row) error
	bisect = func(part []Row) error {
		r := req
		r.Rows = part
		a, err := post(ctx, r)
		if err == nil {
			rows, answers = append(rows, part...), append(answers, a...)
			return nil
		}
		var st *StopError
		if !errors.As(err, &st) || st.Status != http.StatusBadRequest {
			return err
		}
		if len(part) == 1 {
			refused = append(refused, part[0].Seq)
			return nil
		}
		if budget < 2 {
			// Out of budget: leave this part unsent (its marks stay owed and
			// the next Run continues); what was resolved so far still counts.
			return nil
		}
		budget -= 2
		mid := len(part) / 2
		if err := bisect(part[:mid]); err != nil {
			return err
		}
		return bisect(part[mid:])
	}
	mid := len(req.Rows) / 2
	budget -= 2
	if err := bisect(req.Rows[:mid]); err != nil {
		return nil, nil, nil, err
	}
	if err := bisect(req.Rows[mid:]); err != nil {
		return nil, nil, nil, err
	}
	if len(rows) == 0 {
		return nil, nil, nil, perr
	}
	return rows, answers, refused, nil
}

// Run uploads every owed row, MaxRows per request, until nothing is owed,
// the cloud refuses (Result.Stopped; marks kept), a transport or storage
// error ends it (returned; marks kept), or a bound is reached. One Run at a
// time: a second returns Busy at once. tillVersion is buildinfo.Version. The
// caller bounds Run's total time with ctx.
func Run(ctx context.Context, db *sql.DB, post Poster, tillVersion string) (Result, error) {
	var res Result
	if !running.TryLock() {
		res.Busy = true
		return res, nil
	}
	defer running.Unlock()
	repo := data.NewStockUploadRepo(db)
	stream, err := repo.Stream(ctx)
	if err != nil {
		return res, err
	}
	now := time.Now()
	var cursor int64 // marks at or below it were handled (or deferred) this run
	for batch := 0; batch < maxBatchesPerRun; batch++ {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		owed, err := repo.Pending(ctx, cursor, now, MaxRows)
		if err != nil {
			return res, err
		}
		if len(owed) == 0 {
			break
		}
		cursor = owed[len(owed)-1].Seq
		rows, dropped := wireRows(owed)
		var dropSeqs []int64
		for reason, seqs := range dropped {
			res.drop(reason, len(seqs))
			dropSeqs = append(dropSeqs, seqs...)
		}
		if len(dropSeqs) > 0 {
			if err := repo.Ack(ctx, dropSeqs); err != nil {
				return res, err
			}
		}
		if len(rows) == 0 {
			continue // the next page may hold sendable rows
		}
		sent, answers, refused, err := exchange(ctx, post, Request{
			StreamID: stream.ID, StreamStartedAt: stream.StartedAt, TillVersion: tillVersion, Rows: rows})
		var stop *StopError
		if errors.As(err, &stop) {
			res.Stopped, res.StopStatus = stop.Code, stop.Status
			if stop.Status == http.StatusBadRequest && stop.Code == CodeStreamStartInFuture {
				// This till's clock is ahead of the cloud's by more than a
				// day: restart the stream's clock at now (the next trigger
				// sends it; if the clock itself is wrong, it stays refused).
				if rerr := repo.ResetStreamStart(ctx); rerr != nil {
					return res, rerr
				}
			}
			return res, nil
		}
		if err != nil {
			return res, err
		}
		if len(answers) != len(sent) {
			return res, fmt.Errorf("stockupload: the cloud answered %d rows for %d", len(answers), len(sent))
		}
		var done, retry, ahead []int64
		for i, a := range answers {
			if a.Seq != sent[i].Seq {
				return res, errors.New("stockupload: the cloud's answer does not match the request")
			}
			switch a.Status {
			case StatusApplied:
				res.Applied++
			case StatusStale:
				res.Stale++
				if a.StoredSeq > sent[i].Seq || a.Code == CodeOlderStream {
					// Never acknowledged: the cloud did not take this
					// quantity, and the mirror still holds another stream's.
					ahead = append(ahead, a.Seq)
					continue
				}
			case StatusRejected:
				res.Rejected++
				if a.Code == CodeUnknownItem {
					retry = append(retry, a.Seq)
					continue
				}
				res.drop(DropRejectedOther, 1)
			default:
				return res, errors.New("stockupload: the cloud answered an unknown row status")
			}
			done = append(done, a.Seq)
		}
		res.Sent += len(sent)
		if len(ahead) > 0 {
			// The cloud holds a higher sequence of this stream than this till
			// has issued (a restore), or a newer stream (this till's is
			// older). A new stream, started now, owes everything again (it
			// also replaces the marks, so there is nothing to ack) - once per
			// Run and not more than once per StockUploadRotateMinInterval. If
			// the cloud still disagrees, or rotating is not allowed, the Run
			// FAILS with the marks kept (so the caller backs off): acking would
			// lose the quantities for good (ut-docs#4763 review round 2).
			allowed := false
			if !res.Rotated {
				if allowed, err = repo.RotationAllowed(ctx, time.Now()); err != nil {
					return res, err
				}
			}
			if allowed {
				if stream, err = repo.RotateStream(ctx); err != nil {
					return res, err
				}
				res.Rotated = true
				cursor = 0
				continue
			}
			if err := repo.Ack(ctx, done); err != nil { // rows the cloud did answer normally
				return res, err
			}
			return res, ErrStreamConflict
		}
		if len(refused) > 0 {
			res.drop(DropCloudRefused, len(refused))
			done = append(done, refused...)
		}
		if err := repo.Ack(ctx, done); err != nil {
			return res, err
		}
		if len(retry) > 0 {
			gaveUp, err := repo.Defer(ctx, retry, now)
			if err != nil {
				return res, err
			}
			res.Deferred += len(retry) - gaveUp
			res.drop(DropUnknownItem, gaveUp)
		}
	}
	// Nothing sendable is left (or a bound was hit): housekeeping, bounded
	// chunks so the write lock stays short.
	if ctx.Err() == nil {
		for i := 0; i < maxPurgeChunks; i++ {
			n, err := repo.PurgeOrphans(ctx, purgeChunk)
			if err != nil {
				return res, err
			}
			if n < purgeChunk {
				break
			}
		}
	}
	return res, nil
}

const (
	purgeChunk     = 1000
	maxPurgeChunks = 10
)

// wireRows converts owed rows to the wire and separates, by reason, the
// sequences of rows the cloud could never take (a cloud id that is not a
// UUID, a bad location id), which are dropped rather than retried forever;
// the row's next change re-marks it.
func wireRows(owed []data.StockUploadRow) (rows []Row, drop map[string][]int64) {
	drop = map[string][]int64{}
	for _, o := range owed {
		switch {
		case !validLocation(o.LocationID):
			drop[DropBadLocation] = append(drop[DropBadLocation], o.Seq)
		case !validCloudID(o.ItemCloudID) || (o.VariantCloudID != "" && !validCloudID(o.VariantCloudID)):
			drop[DropBadCloudID] = append(drop[DropBadCloudID], o.Seq)
		default:
			rows = append(rows, Row{ItemID: o.ItemCloudID, VariantID: o.VariantCloudID, LocationID: o.LocationID, OnHandMilli: o.OnHandMilli, Seq: o.Seq})
		}
	}
	return rows, drop
}

// validCloudID: the cloud parses ids as non-nil UUIDs.
func validCloudID(s string) bool {
	u, err := uuid.Parse(s)
	return err == nil && u != uuid.Nil
}

func validLocation(s string) bool {
	if s == "" || len(s) > maxLocationLen || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
