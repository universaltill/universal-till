package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/lantls"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Main-till link, main side (ADR-0114 §1/§2/§4/§8/§11, ut-docs#2734).
//
// Additional tills (and later satellites) dial GET /api/sync/link; nothing
// dials out from here. The link carries presence, hello state and coalesced
// `sync` nudges; the data itself still moves over the existing /api/sync/*
// pulls, which stay the source of truth. A replica holding the main till's
// pin dials it over pinned TLS (wss, ut-docs#4092); an unpinned one over
// plain HTTP. The replica client is #2735.

// linkAdminWatchInterval is how often the main till checks the admin
// generation counter while at least one till is linked. One single-row
// SELECT per second, only while linked.
const linkAdminWatchInterval = time.Second

// newSyncLinkHub builds the hub. adminRepo is the same instance
// registerSyncAdmin serves GET /api/sync/admin from, so the hello's admin
// cursor comes from the one generation-keyed bundle cache instead of a
// second copy of it (memory-bounded caches rule).
func newSyncLinkHub(d *common.Deps, cfg fleetlink.Config, adminRepo *data.SyncAdminRepo) *fleetlink.Hub {
	settingsRepo := data.NewSettingsRepo(d.Db)
	pluginsRepo := data.NewSyncPluginsRepo(d.Db)
	stockRepo := data.NewSyncStockRepo(data.NewPOSRepo(d.Db))
	tills := data.NewTillsRepo(d.Db)
	registers := data.NewPOSRepo(d.Db)
	var unusableNames sync.Map // till id → the unusable name last warned about
	var hub *fleetlink.Hub
	reportOf := func(tillID string) (fleetlink.Report, bool) {
		if hub == nil {
			return fleetlink.Report{}, false
		}
		return hub.Report(tillID)
	}
	hub = fleetlink.NewHub(fleetlink.HubOptions{
		Config: cfg,
		Hello: func(ctx context.Context, _ string) fleetlink.Hello {
			h := fleetlink.Hello{
				Role:         "main",
				Version:      buildinfo.Version,
				Platform:     runtime.GOOS + "/" + runtime.GOARCH,
				SyncProtocol: fleetlink.SyncProtocolLevel,
			}
			if id, err := discovery.TillID(ctx, settingsRepo); err == nil {
				h.TillID = id
			}
			// Cursors are best-effort: an empty one only makes the replica
			// pull that scope, which is always safe.
			if fp, err := adminRepo.AdminFingerprint(ctx); err == nil {
				h.Cursors.Admin = fp
			}
			if b, err := pluginsRepo.DumpActivePlugins(ctx); err == nil {
				h.Cursors.Plugins = b.Fingerprint()
			}
			if b, err := stockRepo.DumpStock(ctx); err == nil {
				h.Cursors.Stock = b.Fingerprint()
			}
			// The pin this till serves and, during a key rotation, the
			// next one (ADR-0114 §7, ut-docs#4409). A replica takes the
			// next pin only over a link already pinned to this one.
			if c := lantls.Serving(); c != nil {
				pin := c.Pin()
				h.TLSPin, h.NextPin = &pin, c.NextPin()
			}
			return h
		},
		// A linked till may poll far less (#2735's 5-min floor), but
		// table-claim TTLs read tills.last_seen_at: keep it fresh from the
		// link, at most every 30 s per till.
		OnFrame: func(tillID string) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := tills.TouchLastSeen(ctx, tillID); err != nil {
				logging.L().Warnf("sync link: touch last seen for %s: %v", tillID, err)
			}
		},
		OnReport: func(tillID string, r fleetlink.Report) {
			applyReportedTillName(tills, registers, &unusableNames, tillID, r.Name)
			confirmNextPin(lantls.Serving(), tills, reportOf, tillID, r)
		},
	})
	return hub
}

// confirmNextPin records a till's report of the pending next pin (ADR-0114
// §7, ut-docs#4409). The switch waits for every paired till except those
// whose latest report says they don't pin — a switch can't cut those off;
// a till that hasn't reported since this start counts as pinning. Cheap on
// the hot path: the tills are read only for a new confirmation of the
// pending pin. Failures are logged, never fail the link; the rotation then
// falls back to its RotationWindow ceiling.
func confirmNextPin(c *lantls.Cert, tills *data.TillsRepo, reportOf func(string) (fleetlink.Report, bool), tillID string, r fleetlink.Report) {
	if c == nil || r.NextPin == "" || !c.NeedsConfirm(tillID, r.NextPin) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := tills.ListTills(ctx)
	if err != nil {
		logging.L().Warnf("sync link: key rotation: list tills: %v", err)
		return
	}
	required := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.ID != tillID {
			if rep, ok := reportOf(row.ID); ok && !rep.TLSPinned {
				continue
			}
		}
		required = append(required, row.ID)
	}
	if err := c.ConfirmNext(tillID, r.NextPin, required); err != nil {
		logging.L().Warnf("sync link: key rotation: record till %s's confirmation: %v", tillID, err)
		return
	}
	if !c.NeedsConfirm(tillID, r.NextPin) {
		logging.L().Infof("sync link: till %s holds this till's next LAN TLS pin", tillID)
	}
}

// applyReportedTillName keeps the main till's tills row in step with the
// name a joined till reports for itself (ut-docs#3294): a rename on that
// till, or from the cloud, otherwise never reached the Tills page or the
// quarantine page here. Untrusted LAN input, so it passes validateTillName
// first; an empty name (an older replica) or an invalid one keeps the row.
// A name another till already uses is still applied: the row shows what the
// till is really called, as the cloud does.
// An unusable name is warned about once per till and name, not on every
// periodic report (unusableNames).
// The register enrolment created for the till (CreateRegisterForEnrolment,
// recorded on its till_enrolled audit row) follows the rename
// (ut-docs#4022), unless another register already has the name. It is found
// by id, never by the till's old name: enrolment may have suffixed it
// ("Bar (2)"), and a manager's own "Bar" must not be renamed instead.
func applyReportedTillName(tills *data.TillsRepo, registers *data.POSRepo, unusableNames *sync.Map, tillID, reported string) {
	if strings.TrimSpace(reported) == "" {
		return
	}
	name, err := validateTillName(reported)
	if err != nil {
		if prev, ok := unusableNames.Swap(tillID, reported); !ok || prev != reported {
			logging.L().Warnf("sync link: till %s reported an unusable name: %v", tillID, err)
		}
		return
	}
	unusableNames.Delete(tillID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	changed, err := tills.UpdateName(ctx, tillID, name)
	if err != nil {
		logging.L().Warnf("sync link: rename till %s: %v", tillID, err)
		return
	}
	if !changed {
		return
	}
	logging.L().Infof("sync link: till %s now reports the name %q", tillID, name)
	regID, err := registers.EnrolledRegisterID(ctx, tillID)
	if err != nil {
		logging.L().Warnf("sync link: find register of till %s: %v", tillID, err)
		return
	}
	if regID != "" {
		renameRegisterAfterTill(ctx, registers, regID, name)
	}
}

// pairingGetter is the one PairingRepo read the pending link makes (a seam
// so a test can count reads: the limiter must run before any of them).
type pairingGetter interface {
	GetByID(ctx context.Context, id string) (data.PendingPairingRow, bool, error)
}

// pendingLinkRate is the pending link's per-source budget: its own bucket,
// equal in strictness to the pair-request one (ADR-0114 amendment
// 2026-11-03), so link reconnects can never spend what the post-approval
// token fetch needs.
const (
	pendingLinkRateMax    = 5
	pendingLinkRetryAfter = "60"
)

// maxPairRequestIDLen bounds the id a pending link may present; a longer one
// is treated as unknown without a lookup (ids are UUIDs).
const maxPairRequestIDLen = 64

// unknownCommitment is compared against when the id is unknown, so that path
// does the same hash and constant-time compare as a known one (no timing
// oracle for "this id exists").
const unknownCommitment = "0000000000000000000000000000000000000000000000000000000000000000"

// registerSyncLink mounts GET /api/sync/link. Auth-middleware exempt (the
// exempt list carries it); syncTill authenticates a peer's upgrade request,
// and a request carrying the pair-request headers instead is a joining
// till's pending link (servePendingLink).
func registerSyncLink(mux *http.ServeMux, d *common.Deps) {
	registerSyncLinkWith(mux, d, data.NewTillsRepo(d.Db), data.NewPairingRepo(d.Db),
		newPairRateLimiter(time.Minute, pendingLinkRateMax))
}

func registerSyncLinkWith(mux *http.ServeMux, d *common.Deps, tills *data.TillsRepo, pairings pairingGetter, pendingLimiter *pairRateLimiter) {
	mux.HandleFunc("GET /api/sync/link", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(fleetlink.HeaderPairRequestID) != "" || r.Header.Get(fleetlink.HeaderPairRequestSecret) != "" {
			// A bearer and pairing headers together are two identities:
			// refused, never guessed at.
			if r.Header.Get("Authorization") != "" {
				refusePendingLink(w)
				return
			}
			servePendingLink(w, r, d, pairings, pendingLimiter)
			return
		}
		till, ok := syncTill(w, r, tills)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Links terminate on the main till only; a replica's tills table
		// is a synced copy with redacted bearers, so this is defence in
		// depth next to syncTill.
		if d.SyncPrimaryURL(r.Context()) != "" {
			http.Error(w, "connect to the main till", http.StatusConflict)
			return
		}
		// ut-docs#5058: a replaced main till holds no links.
		if refuseIfReplaced(w, r, d) {
			return
		}
		if d.Link == nil {
			http.Error(w, "link unavailable", http.StatusServiceUnavailable)
			return
		}
		d.Link.Serve(w, r, till.ID)
	})
}

// refusePendingLink is the one refusal writer for every authentication
// failure of a pending link: byte-identical to the bearer path's refusal on
// this route, so a prober cannot tell the branches or the causes apart.
func refusePendingLink(w http.ResponseWriter) {
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// servePendingLink handles GET /api/sync/link with pair-request headers and
// no bearer (ADR-0114 amendment 2026-11-03, ut-docs#5297). The order matters:
// TLS and no-query (no database read), the per-source limit, then the
// replica/replaced/no-hub refusals (the settings reads), then id + secret in
// constant time. So an unlimited prober cannot drive settings reads, and a
// limited one learns nothing about the till's state. It logs nothing on
// refusal and never the URL, a header or an error that saw the URL.
func servePendingLink(w http.ResponseWriter, r *http.Request, d *common.Deps, pairings pairingGetter, limiter *pairRateLimiter) {
	// TLS only, and no query at all: a client that put the secret in the URL
	// fails closed whatever the parameter is called.
	if r.TLS == nil || r.URL.RawQuery != "" {
		refusePendingLink(w)
		return
	}
	if !limiter.allow(sourceOf(r)) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", pendingLinkRetryAfter)
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": "too many requests"})
		return
	}
	if d.SyncPrimaryURL(r.Context()) != "" {
		http.Error(w, "connect to the main till", http.StatusConflict)
		return
	}
	if refuseIfReplaced(w, r, d) {
		return
	}
	if d.PendingLinks == nil {
		http.Error(w, "link unavailable", http.StatusServiceUnavailable)
		return
	}
	id := r.Header.Get(fleetlink.HeaderPairRequestID)
	secret := r.Header.Get(fleetlink.HeaderPairRequestSecret)
	var (
		row data.PendingPairingRow
		ok  bool
	)
	if id != "" && len(id) <= maxPairRequestIDLen {
		var err error
		row, ok, err = pairings.GetByID(r.Context(), id)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pairings.error.server", "sync_link", err)
			return
		}
	}
	commitment := unknownCommitment
	if ok {
		commitment = row.Commitment
	}
	match := commitmentMatches(secret, commitment) // always run, known id or not
	if !ok || row.Status != "pending" || secret == "" || !match {
		refusePendingLink(w)
		return
	}
	expiresAt, err := time.Parse(time.RFC3339, row.ExpiresAt)
	if err != nil {
		expiresAt = time.Now().Add(pairingRequestTTL)
	}
	d.PendingLinks.Serve(w, r, id, expiresAt, func() string {
		// The manager may have resolved the request between the check above
		// and the link registering: say so at once.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cur, found, err := pairings.GetByID(ctx, id)
		switch {
		case err != nil:
			return ""
		case found && cur.Status == "approved":
			return fleetlink.PairingApproved
		case found:
			return ""
		case !time.Now().Before(expiresAt):
			return fleetlink.PairingExpired
		default:
			return fleetlink.PairingDenied // the row is gone before its expiry: denied
		}
	})
}

// StartSyncLink runs the link's background side, joined to app.Run's drain:
// the admin-generation watch, and on shutdown a `bye` to every linked till
// before their connections close (http.Server.Shutdown does not track
// hijacked connections, so without this they would outlive the server).
func StartSyncLink(ctx context.Context, d *common.Deps, wg *sync.WaitGroup, adminRepo *data.SyncAdminRepo) {
	if d.PendingLinks != nil {
		// The joining tills' pending links are hijacked too (ADR-0114
		// amendment 2026-11-03): bye and close them on shutdown.
		wg.Add(1)
		go func() {
			defer logging.RecoverAndLog("pages.pendingLinkClose")
			defer wg.Done()
			<-ctx.Done()
			d.PendingLinks.Close()
		}()
	}
	if d.Link == nil {
		return
	}
	runLinkAdminWatch(ctx, d, wg, linkAdminWatchInterval, adminRepo)
	wg.Add(1)
	go func() {
		defer logging.RecoverAndLog("pages.syncLinkClose")
		defer wg.Done()
		<-ctx.Done()
		d.Link.Close()
	}()
}

// runLinkAdminWatch nudges `admin` whenever the admin bundle changes: it
// watches sync_admin_version, bumped by a trigger on every admin-bundle
// table, so this one watch covers every writer: the back office, a
// write-through from another till, a my. directive, the admin-bundle apply —
// users, roles and PINs included (#2731) — without a hook at each call site.
// A moved generation is then confirmed against the bundle fingerprint, so a
// per-till settings write never nudges (ut-docs#2792).
func runLinkAdminWatch(ctx context.Context, d *common.Deps, wg *sync.WaitGroup, every time.Duration, adminRepo *data.SyncAdminRepo) {
	wg.Add(1)
	go func() {
		defer logging.RecoverAndLog("pages.linkAdminWatch")
		defer wg.Done()
		t := time.NewTicker(every)
		defer t.Stop()
		var last int64
		var lastFP string
		var lastErrLog time.Time
		baseline := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if d.Link.Len() == 0 {
				baseline = false // a new link's hello carries the cursor
				continue
			}
			gen, tracked := adminRepo.AdminGeneration(ctx)
			if !tracked {
				continue
			}
			if baseline && gen == last {
				continue
			}
			// The settings trigger also bumps the generation for per-till
			// keys that never travel (ut-docs#2792): nudge only when the
			// bundle a replica would pull actually changed. On a moved
			// generation this is the same one scan the replica's pull
			// would cost anyway, and it primes the shared cache for it.
			fp, err := adminRepo.AdminFingerprint(ctx)
			if err != nil {
				// Retry next tick; a replica's own poll still converges.
				if time.Since(lastErrLog) > time.Minute {
					logging.L().Warnf("sync link: admin fingerprint for the nudge watch: %v", err)
					lastErrLog = time.Now()
				}
				continue
			}
			if baseline && fp != lastFP {
				d.NudgeLink(fleetlink.ScopeAdmin)
			}
			last, lastFP, baseline = gen, fp, true
		}
	}()
}
