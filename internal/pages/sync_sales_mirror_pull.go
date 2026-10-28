package pages

// The additional till's side of the read-only sales copy (ut-docs#5074 M3;
// design §5.3, §6, §7, §12; contract reference/contracts/pos-lan-sales-mirror.md):
// a pull loop that keeps this till's mirror_* copy current from the main
// till's feed, re-syncs by state when told to, and keeps the copy inside its
// window and disk budget.
//
// Ships dark. The loop's tick returns after reading two or three settings
// when this till has no main till, the shop setting is off, this till's own
// kill switch (sync.sales_mirror_off) is on, or the process has
// UT_SALES_MIRROR=off. A copied sale only ever lands in mirror_* (through
// data.SalesMirrorRepo): never sales, stock, vouchers, receipts, Z reports,
// the journal, cloud uploads or plugin events.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/diskspace"
	"github.com/universaltill/universal-till/internal/lantls"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

const (
	salesMirrorPullEvery       = 30 * time.Second
	salesMirrorPullEveryLinked = 5 * time.Minute
	// salesMirrorPagePause spaces catch-up pages (design §11 "Pacing") so a
	// first catch-up of a full window never monopolises the till.
	salesMirrorPagePause = 250 * time.Millisecond
	// salesMirrorMaxPagesPerTick bounds one tick; the next tick continues.
	salesMirrorMaxPagesPerTick = 400
	// The bounds (window prune, disk budget) run hourly and after every
	// 1,000 applied records (design §6).
	salesMirrorBoundsEvery   = time.Hour
	salesMirrorBoundsRecords = 1000
	// Below the low-disk floor the copy keeps at least this many days.
	salesMirrorFloorDays = 31
	// salesMirrorMaxBody bounds a feed response read (a page is ≤ 1 MiB of
	// records; the envelope adds little).
	salesMirrorMaxBody = 4 << 20
	// salesMirrorPullLimit is the records per page this till asks for: one
	// page is one apply transaction, and design §11 holds that at ≤ 100
	// sales so a sale on this till never waits long for the write lock.
	salesMirrorPullLimit = 100
	// salesMirrorMaxBudgetMB clamps sales_mirror.max_mb (1 TiB) so a
	// mistyped value can never overflow the byte budget into a negative one
	// that would trim the copy to its floor.
	salesMirrorMaxBudgetMB = 1 << 20
)

// Pause reasons kept in mirror_state.paused_reason.
const (
	salesMirrorPausedLowDisk     = "low_disk"
	salesMirrorPausedBudget      = "budget"
	salesMirrorPausedVersionWait = "version_wait"
)

// salesMirrorApplyMu makes every apply of the copy single-flight: they all
// move the same cursor. Today only the pull applies; the contract's reserved
// link frames would apply under it too.
var salesMirrorApplyMu sync.Mutex

// SalesMirrorStatus is this till's copy state for diagnostics and the
// status endpoint. In memory: nothing is written to report it.
type SalesMirrorStatus struct {
	State       string `json:"state"` // off | killed | on | paused:<low_disk|budget|version_wait> | needs_tls | main_off | satellite
	Epoch       string `json:"epoch"`
	Cursor      int64  `json:"cursor"`
	HeadSeq     int64  `json:"head_seq"`
	Lag         int64  `json:"lag_records"`
	LastPullAt  string `json:"last_pull_at,omitempty"`
	LastApplyAt string `json:"last_apply_at,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	Rejected    int64  `json:"rejected"`
}

type salesMirrorPuller struct {
	d      *common.Deps
	repo   *data.SalesMirrorRepo
	client *http.Client
	pinned func(context.Context) bool
	now    func() time.Time
	probe  func(string) (diskspace.Usage, error)
	pause  time.Duration

	mu           sync.Mutex
	status       SalesMirrorStatus
	lastBounds   time.Time
	appliedSince int
	// quietUntil: after a "satellite" answer, kicks (the link nudges every
	// till) send nothing until then (M4 review finding 3).
	quietUntil time.Time
}

func newSalesMirrorPuller(d *common.Deps, client *http.Client) *salesMirrorPuller {
	return &salesMirrorPuller{
		d: d, repo: data.NewSalesMirrorRepo(d.Db), client: client, pinned: lantls.PeerPinned,
		now: time.Now, probe: diskspace.Probe, pause: salesMirrorPagePause,
		status: SalesMirrorStatus{State: "off"},
	}
}

// salesMirrorPullerRef holds the process's puller so the status endpoint
// reaches it. Nil until started.
var salesMirrorPullerRef struct {
	sync.Mutex
	p *salesMirrorPuller
}

// StartSalesMirrorPull runs the copy's pull loop: every 30 s, every 5 min
// while the main-till link is up, and at once on a kick of
// d.SalesMirrorPullNow (the link's nudge, M4).
// wg registers it with app.Run's drain.
func StartSalesMirrorPull(ctx context.Context, d *common.Deps, wg *sync.WaitGroup) {
	p := newSalesMirrorPuller(d, lantls.NewPeerClient(60*time.Second))
	salesMirrorPullerRef.Lock()
	salesMirrorPullerRef.p = p
	salesMirrorPullerRef.Unlock()
	if d.SalesMirrorPullNow == nil { // pages.Init makes it before the link client starts
		d.SalesMirrorPullNow = make(chan struct{}, 1)
	}
	var reeval <-chan struct{}
	if d.LinkClient != nil {
		reeval = d.LinkClient.LinkChanged()
	}
	runPullLoop(ctx, wg, d.SalesMirrorPullNow, reeval,
		syncPullInterval(d, salesMirrorPullEvery, salesMirrorPullEveryLinked), func() { p.tick(ctx) })
}

func (p *salesMirrorPuller) setStatus(fn func(*SalesMirrorStatus)) {
	p.mu.Lock()
	fn(&p.status)
	p.mu.Unlock()
}

// Status returns a copy of the current status, refreshed from the stored
// state.
func (p *salesMirrorPuller) Status(ctx context.Context) SalesMirrorStatus {
	st, _ := p.repo.State(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.status
	s.Epoch, s.Cursor, s.LastApplyAt, s.Rejected = st.Epoch, st.Cursor, st.LastApplyAt, st.Rejected
	if s.HeadSeq > s.Cursor && st.ResyncEpoch == "" {
		s.Lag = s.HeadSeq - s.Cursor
	}
	if st.PausedReason != "" && s.State == "on" {
		s.State = "paused:" + st.PausedReason
	}
	return s
}

func salesMirrorWindowDays(ctx context.Context, d *common.Deps) int {
	n := salesMirrorSettingInt(ctx, d, data.SalesMirrorWindowDaysKey, data.SalesMirrorDefaultWindowDays)
	return min(max(n, data.SalesMirrorMinWindowDays), data.SalesMirrorMaxWindowDays)
}

func localDateDaysAgo(now time.Time, days int) string {
	return now.Local().AddDate(0, 0, -days).Format("2006-01-02")
}

// tick is one pull: pages the feed (or a state re-sync) until it reaches
// the head, a refusal, an error or the per-tick page bound.
func (p *salesMirrorPuller) tick(ctx context.Context) {
	d := p.d
	get := func(k string) string {
		v, _, _ := d.Settings.Get(ctx, k)
		return strings.TrimSpace(v)
	}
	primary, bearer := get("sync.primary_url"), get("sync.bearer")
	if primary == "" || bearer == "" {
		return // the main till, or not joined: nothing to copy from
	}
	switch {
	case salesMirrorEnvKilled() || get(data.SalesMirrorTillOffKey) == "1":
		p.setStatus(func(s *SalesMirrorStatus) { s.State = "killed" })
		return
	case get(data.SalesMirrorEnabledKey) != "1":
		p.setStatus(func(s *SalesMirrorStatus) { s.State = "off" })
		return
	}
	if !p.pinned(ctx) {
		// D5: the feed is pinned TLS only; never send the bearer to be
		// refused. Pairing again gives this till the pin.
		p.setStatus(func(s *SalesMirrorStatus) { s.State = "needs_tls" })
		return
	}
	p.mu.Lock()
	quiet := p.now().Before(p.quietUntil)
	p.mu.Unlock()
	if quiet {
		return
	}
	p.setStatus(func(s *SalesMirrorStatus) { s.State = "on"; s.LastPullAt = p.now().UTC().Format(time.RFC3339) })
	if paused := p.bounds(ctx, false); paused != "" {
		return
	}
	selfID := get("sync.till_id")
	windowStart := localDateDaysAgo(p.now(), salesMirrorWindowDays(ctx, d))
	opts := func() data.MirrorApplyOpts {
		return data.MirrorApplyOpts{SelfTillID: selfID, WindowStart: windowStart, Now: p.now()}
	}
	base := strings.TrimSuffix(primary, "/")
	for page := 0; page < salesMirrorMaxPagesPerTick; page++ {
		if page > 0 && !sleepCtx(ctx, p.pause) {
			return
		}
		st, err := p.repo.State(ctx)
		if err != nil {
			p.fail(err)
			return
		}
		var more bool
		if st.Epoch == "" || st.ResyncEpoch != "" {
			more, err = p.statePage(ctx, base, bearer, st, windowStart, opts())
		} else {
			more, err = p.feedPage(ctx, base, bearer, st, opts())
		}
		if err != nil {
			if !errors.Is(err, errSalesMirrorStop) {
				p.fail(err)
			}
			return
		}
		p.mu.Lock()
		applied := p.appliedSince
		p.mu.Unlock()
		if applied >= salesMirrorBoundsRecords {
			if paused := p.bounds(ctx, true); paused != "" {
				return
			}
		}
		if !more {
			p.setStatus(func(s *SalesMirrorStatus) { s.LastError = "" })
			return
		}
	}
}

// errSalesMirrorStop ends a tick on an answer that is not an error (off on
// the main till, busy, version wait): already reflected in the status.
var errSalesMirrorStop = errors.New("sales mirror: stop this tick")

func (p *salesMirrorPuller) fail(err error) {
	logging.L().Infof("sales mirror: pull: %v — will retry", err)
	p.setStatus(func(s *SalesMirrorStatus) { s.LastError = err.Error() })
}

// salesMirrorReply is a decoded feed or state response.
type salesMirrorReply struct {
	status int
	data   json.RawMessage
	err    salesMirrorError
}

func (p *salesMirrorPuller) fetch(ctx context.Context, base, bearer, path string, q url.Values) (salesMirrorReply, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path+"?"+q.Encode(), nil)
	if err != nil {
		return salesMirrorReply{}, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := p.client.Do(req)
	if err != nil {
		return salesMirrorReply{}, fmt.Errorf("main till unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, salesMirrorMaxBody))
	if err != nil {
		return salesMirrorReply{}, fmt.Errorf("read the main till's answer: %w", err)
	}
	var env struct {
		Data  json.RawMessage   `json:"data"`
		Error *salesMirrorError `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return salesMirrorReply{status: resp.StatusCode}, fmt.Errorf("main till answered %d with no readable copy envelope (over %d bytes, or not the copy feed)", resp.StatusCode, salesMirrorMaxBody)
	}
	out := salesMirrorReply{status: resp.StatusCode, data: env.Data}
	if env.Error != nil {
		out.err = *env.Error
	}
	return out, nil
}

// refusal maps a non-200 answer to the status it means; it returns
// errSalesMirrorStop for an answer that ends the tick quietly.
func (p *salesMirrorPuller) refusal(r salesMirrorReply) error {
	switch {
	case r.status == http.StatusConflict:
		p.setStatus(func(s *SalesMirrorStatus) { s.State = "main_off" })
		return errSalesMirrorStop
	case r.status == http.StatusForbidden && r.err.Code == "tls_required":
		p.setStatus(func(s *SalesMirrorStatus) { s.State = "needs_tls" })
		return errSalesMirrorStop
	case r.status == http.StatusForbidden && r.err.Code == "satellite":
		// Satellites hold no copy (ADR-0086): quiet, not an error each tick.
		p.mu.Lock()
		p.status.State = "satellite"
		p.quietUntil = p.now().Add(salesMirrorPullEveryLinked)
		p.mu.Unlock()
		return errSalesMirrorStop
	case r.status == http.StatusServiceUnavailable:
		return errSalesMirrorStop
	}
	return fmt.Errorf("main till refused the copy: %d %s", r.status, r.err.Code)
}

func (p *salesMirrorPuller) checkVersion(ctx context.Context, v int) error {
	st, _ := p.repo.State(ctx)
	if v > SalesMirrorFeedVersion {
		// The main till speaks a newer feed: stop copying until this till
		// is updated (the main till upgrades first).
		if st.PausedReason != salesMirrorPausedVersionWait {
			_ = p.repo.SetPaused(ctx, salesMirrorPausedVersionWait)
		}
		return errSalesMirrorStop
	}
	if st.PausedReason == salesMirrorPausedVersionWait {
		// This till was updated (or the main till rolled back): copying
		// again, and the status says so (M3 review finding 2).
		if err := p.repo.SetPaused(ctx, ""); err != nil {
			return err
		}
		logging.L().Infof("sales mirror: the main till's feed is readable again; copying resumes")
	}
	return nil
}

func (p *salesMirrorPuller) feedPage(ctx context.Context, base, bearer string, st data.MirrorState, o data.MirrorApplyOpts) (bool, error) {
	r, err := p.fetch(ctx, base, bearer, "/api/sync/sales-mirror", url.Values{
		"epoch": {st.Epoch}, "after": {strconv.FormatInt(st.Cursor, 10)}, "limit": {strconv.Itoa(salesMirrorPullLimit)},
	})
	if err != nil {
		return false, err
	}
	switch r.status {
	case http.StatusOK:
	case http.StatusGone:
		// Behind the horizon, a new epoch, or ahead of the head (the main
		// till restored a backup): re-sync by state, never by replacing this
		// till's database. The next page starts it.
		if err := p.repo.StartResync(ctx, r.err.Epoch); err != nil {
			return false, err
		}
		logging.L().Infof("sales mirror: re-syncing the copy by state (epoch %s)", r.err.Epoch)
		return true, nil
	default:
		return false, p.refusal(r)
	}
	var page SalesMirrorPage
	if err := json.Unmarshal(r.data, &page); err != nil {
		return false, fmt.Errorf("decode feed page: %w", err)
	}
	if err := p.checkVersion(ctx, page.FeedVersion); err != nil {
		return false, err
	}
	p.setStatus(func(s *SalesMirrorStatus) { s.HeadSeq = page.HeadSeq })
	if page.FromSeq != st.Cursor || page.Epoch != st.Epoch {
		return false, fmt.Errorf("feed page (%d, %d] in epoch %s does not continue cursor %d", page.FromSeq, page.ToSeq, page.Epoch, st.Cursor)
	}
	salesMirrorApplyMu.Lock()
	res, err := p.repo.ApplyFeedPage(ctx, data.MirrorFeedPage{Epoch: page.Epoch, MainTillID: page.MainTillID,
		FromSeq: page.FromSeq, ToSeq: page.ToSeq, Horizon: page.Horizon, Records: page.Records}, o)
	salesMirrorApplyMu.Unlock()
	if errors.Is(err, data.ErrMirrorCursorMoved) {
		return true, nil // another apply moved the cursor meanwhile: read it again
	}
	if err != nil {
		return false, err
	}
	p.noteApplied(res)
	return page.More, nil
}

func (p *salesMirrorPuller) statePage(ctx context.Context, base, bearer string, st data.MirrorState, windowStart string, o data.MirrorApplyOpts) (bool, error) {
	epoch, cursor, resume := st.ResyncEpoch, st.ResyncCursor, ""
	if cursor != "" {
		resume = strconv.FormatInt(st.ResyncResumeSeq, 10)
	}
	r, err := p.fetch(ctx, base, bearer, "/api/sync/sales-mirror/state", url.Values{
		"epoch": {epoch}, "from_date": {windowStart}, "cursor": {cursor}, "resume_seq": {resume},
		"limit": {strconv.Itoa(salesMirrorPullLimit)},
	})
	if err != nil {
		return false, err
	}
	switch r.status {
	case http.StatusOK:
	case http.StatusGone:
		// Our re-sync's epoch is gone (or never known): restart it with the
		// main till's current epoch.
		if r.err.Epoch == "" || (r.err.Epoch == epoch && cursor == "") {
			return false, fmt.Errorf("main till refused the re-sync without an epoch")
		}
		if err := p.repo.StartResync(ctx, r.err.Epoch); err != nil {
			return false, err
		}
		return true, nil
	default:
		return false, p.refusal(r)
	}
	var page SalesMirrorStatePage
	if err := json.Unmarshal(r.data, &page); err != nil {
		return false, fmt.Errorf("decode state page: %w", err)
	}
	if err := p.checkVersion(ctx, page.FeedVersion); err != nil {
		return false, err
	}
	p.setStatus(func(s *SalesMirrorStatus) { s.HeadSeq = page.ResumeSeq })
	salesMirrorApplyMu.Lock()
	res, err := p.repo.ApplyStatePage(ctx, cursor, data.MirrorStatePage{Epoch: page.Epoch, MainTillID: page.MainTillID,
		ResumeSeq: page.ResumeSeq, NextCursor: page.NextCursor, More: page.More, Records: page.Records}, o)
	salesMirrorApplyMu.Unlock()
	if errors.Is(err, data.ErrMirrorCursorMoved) {
		// Not the re-sync in progress (a restart raced it): start over.
		return true, p.repo.StartResync(ctx, page.Epoch)
	}
	if err != nil {
		return false, err
	}
	p.noteApplied(res)
	return true, nil // after the last page the feed continues from resume_seq
}

func (p *salesMirrorPuller) noteApplied(res data.MirrorApplyResult) {
	if res.Rejected > 0 {
		logging.L().Warnf("sales mirror: %d record(s) from the main till could not be stored and were skipped", res.Rejected)
	}
	p.mu.Lock()
	p.appliedSince += res.Applied
	p.mu.Unlock()
}

// bounds keeps the copy inside its window and disk budget (design §6):
// hourly, or when force (after 1,000 applied records). It returns the pause
// reason now in force ("" = copying).
func (p *salesMirrorPuller) bounds(ctx context.Context, force bool) string {
	p.mu.Lock()
	due := force || p.lastBounds.IsZero() || p.now().Sub(p.lastBounds) >= salesMirrorBoundsEvery
	if due {
		p.lastBounds, p.appliedSince = p.now(), 0
	}
	p.mu.Unlock()
	st, _ := p.repo.State(ctx)
	if st.PausedReason == salesMirrorPausedVersionWait {
		return "" // a feed page decides (checkVersion), every page
	}
	if !due {
		return st.PausedReason
	}
	d := p.d
	now := p.now()
	window := salesMirrorWindowDays(ctx, d)
	closesDays := max(window, data.SalesMirrorMaxWindowDays)
	if _, err := p.repo.PruneCopyBefore(ctx, localDateDaysAgo(now, window), localDateDaysAgo(now, closesDays)); err != nil {
		logging.L().Warnf("sales mirror: trim the copy to its window: %v", err)
	}
	floorDate := localDateDaysAgo(now, salesMirrorFloorDays)
	var usage *diskspace.Usage
	if d.Cfg != nil && d.Cfg.DBPath != "" {
		if u, err := p.probe(d.Cfg.DBPath); err == nil {
			usage = &u
		}
	}
	reason := ""
	switch {
	case usage != nil && diskspace.Low(*usage):
		// Off below the floor: no new rows, keep only the floor's days.
		if _, err := p.repo.PruneCopyBefore(ctx, floorDate, floorDate); err != nil {
			logging.L().Warnf("sales mirror: trim the copy on low disk: %v", err)
		}
		reason = salesMirrorPausedLowDisk
	default:
		budget := int64(min(salesMirrorSettingInt(ctx, d, data.SalesMirrorMaxMBKey, data.SalesMirrorDefaultMaxMB), salesMirrorMaxBudgetMB)) << 20
		for range 4000 {
			stats, err := p.repo.CopyStats(ctx)
			if err != nil {
				break
			}
			b := budget
			if usage != nil {
				if share := (int64(usage.Free) + stats.BytesEstimate()) / 5; share < b {
					b = share
				}
			}
			if stats.BytesEstimate() <= b {
				break
			}
			// Over budget: drop the oldest whole day, never below the floor.
			if stats.OldestLocalDate == "" || stats.OldestLocalDate >= floorDate {
				reason = salesMirrorPausedBudget
				break
			}
			next, err := time.Parse("2006-01-02", stats.OldestLocalDate)
			if err != nil {
				break
			}
			if _, err := p.repo.PruneCopyBefore(ctx, next.AddDate(0, 0, 1).Format("2006-01-02"), ""); err != nil {
				break
			}
		}
	}
	if reason != st.PausedReason {
		if err := p.repo.SetPaused(ctx, reason); err != nil {
			logging.L().Warnf("sales mirror: record the pause: %v", err)
		}
		if reason != "" {
			logging.L().Warnf("sales mirror: copy paused (%s); no sale is affected", reason)
		}
	}
	return reason
}

// salesMirrorCopyStatus is this till's copy for the status route: nil on a
// till that has never run the pull loop.
func salesMirrorCopyStatus(ctx context.Context, d *common.Deps) map[string]any {
	salesMirrorPullerRef.Lock()
	p := salesMirrorPullerRef.p
	salesMirrorPullerRef.Unlock()
	if p == nil {
		return nil
	}
	stats, _ := data.NewSalesMirrorRepo(d.Db).CopyStats(ctx)
	return map[string]any{
		"status": p.Status(ctx), "sales": stats.Sales, "closes": stats.Closes,
		"bytes_estimate": stats.BytesEstimate(), "oldest_local_date": stats.OldestLocalDate,
	}
}
