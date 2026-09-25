package pages

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Main-till re-discovery, the HTTP side (ut-docs#2722). The mechanism itself
// — counting failed contacts, browsing, the identity match and the proof —
// is internal/discovery.PrimaryWatch; this file is the main till's proof
// endpoint, the replica's status chip, and the pull-loop glue.

// registerPrimaryProof: POST /api/sync/primary-proof, answered by a MAIN
// till. A replica that re-found it over mDNS sends its own till id and a
// fresh nonce; the main till proves it holds that till's pairing record by
// returning discovery.PrimaryProof keyed by the stored bearer hash. The
// bearer hash never leaves this till. Unauthenticated by design (the
// replica must not send its bearer to an unproven device), so: rate-limited
// per source, strict input validation, 404 for anything that isn't an
// enrolled till — and a replica never answers as a main till.
//
// The proof binds r.Host, the address the replica dialled, and this till
// answers only when that Host is its own accepting address (hostIsThisTill).
// Otherwise a LAN device copying this till's public mDNS id could relay a
// replica's challenge here with Host set to the relay's own address, get a
// proof valid for that address, and have the replica re-point its
// bearer-carrying pulls at the relay (ut-docs#2722 review).
func registerPrimaryProof(mux *http.ServeMux, d *common.Deps) {
	tills := data.NewTillsRepo(d.Db)
	settingsRepo := data.NewSettingsRepo(d.Db)
	limiter := newPairRateLimiter(time.Minute, 10)
	mux.HandleFunc("POST "+discovery.ProofPath, func(w http.ResponseWriter, r *http.Request) {
		if !limiter.allow(sourceOf(r)) {
			writeProofError(w, http.StatusTooManyRequests, "too many proof requests")
			return
		}
		var req discovery.ProofRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req); err != nil ||
			!validProofRequest(req) {
			writeProofError(w, http.StatusBadRequest, "till_id and a 32-byte hex nonce are required")
			return
		}
		if d.SyncPrimaryURL(r.Context()) != "" || !hostIsThisTill(r) {
			http.NotFound(w, r)
			return
		}
		hash, ok, err := tills.BearerHashByID(r.Context(), req.TillID)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "sync_primary_proof", err)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		primaryID, err := discovery.TillID(r.Context(), settingsRepo)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "sync_primary_proof", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": discovery.ProofResponse{
			PrimaryTillID: primaryID,
			Proof:         discovery.PrimaryProof(hash, primaryID, req.TillID, req.Nonce, r.Host),
		}, "error": nil})
	})
}

// hostLookup resolves a Host name for hostIsThisTill — a seam for tests.
var hostLookup = net.DefaultResolver.LookupIPAddr

// hostIsThisTill reports whether r.Host names the socket this request was
// accepted on: the same port, and the same IP (or a name resolving to it).
// The accepting socket (http.LocalAddrContextKey) is used rather than an
// interface scan: it is exactly the address the replica reached, and a
// relay cannot choose it. A main till behind NAT/port-forwarding (Host ≠
// its socket) therefore cannot be re-found automatically — re-pairing
// still works there.
func hostIsThisTill(r *http.Request) bool {
	la, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok || la == nil {
		return false
	}
	localHost, localPort, err := net.SplitHostPort(la.String())
	if err != nil {
		return false
	}
	localIP := net.ParseIP(localHost)
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || port != localPort || localIP == nil {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.Equal(localIP)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	addrs, err := hostLookup(ctx, host)
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if a.IP.Equal(localIP) {
			return true
		}
	}
	return false
}

// writeProofError answers the machine-to-machine proof call in the
// {data, error} envelope — a replica's pull loop reads it, never a person,
// same as /api/sync/pair-request's errors.
func writeProofError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": msg})
}

// validProofRequest: a till id of sane length and a 32-byte hex nonce.
func validProofRequest(req discovery.ProofRequest) bool {
	id := strings.TrimSpace(req.TillID)
	if id == "" || len(id) > 64 {
		return false
	}
	raw, err := hex.DecodeString(req.Nonce)
	return err == nil && len(raw) == 32
}

// registerMainTillStatus: GET /ui/main-till-status, polled from the status
// bar on every page (base.html). Empty unless this till has a main till;
// then its one connectivity chip (link_status.go, ut-docs#2742): linked,
// polling, or "Main till not reachable since <time> — selling offline" —
// a persistent chip linking to the Tills page, never a modal
// (offline-first rule). On a replica, plugin updates wait for the main
// till (ADR-0011 §7), so pending ones are named on the unreachable chip.
func registerMainTillStatus(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /ui/main-till-status", func(w http.ResponseWriter, r *http.Request) {
		renderLinkChip(replicaLinkView(r.Context(), d))(w, r)
	})
}

// primaryContactFailed is the pull loop's failure path: feeds the watch,
// logs at WARN exactly once per outage (it reaches the cloud problems list,
// ut-docs#2719) and at INFO for every repeat, and audits a re-link.
func primaryContactFailed(ctx context.Context, d *common.Deps, cause string) {
	if d.PrimaryWatch == nil {
		logging.L().Infof("sync pull: primary unreachable (%s) — will retry", cause)
		return
	}
	out := d.PrimaryWatch.ContactFailed(ctx)
	if out.Relinked {
		// Found again in the same tick (PrimaryWatch logged where): not a
		// problem worth a WARN in the cloud problems list — just audit it.
		tillID, _, _ := d.Settings.Get(ctx, "sync.till_id")
		_ = data.NewPOSRepo(d.Db).InsertAudit(ctx, nil, "system", "till", tillID, "primary_relinked",
			map[string]any{"from": out.OldURL, "to": out.NewURL}, time.Now().UTC().Format(time.RFC3339), "")
		// ADR-0114 §4: the link follows the switch — redial the new
		// address now rather than at its next re-check.
		if d.LinkClient != nil {
			d.LinkClient.Redial()
		}
		return
	}
	if out.Warn {
		since, _, _ := d.Settings.Get(ctx, "sync.last_contact_at")
		if since == "" {
			since = "never"
		}
		logging.L().WarnProblemf(discovery.MainTillProblemKey, "sync: main till unreachable (last contact %s; %s) — this till keeps selling offline and is looking for the main till on the network", since, cause)
		return
	}
	logging.L().Infof("sync pull: primary unreachable (%s) — will retry", cause)
}

// primaryContactOK is the pull loop's (and the link's) success path. The
// main till answered, so the outage problems it left in the Problems ring
// are over: resolve them, so the next heartbeat stops reporting them and
// my.'s "Attention needed" clears (ut-docs#2798).
func primaryContactOK(ctx context.Context, d *common.Deps) {
	if d.PrimaryWatch != nil {
		d.PrimaryWatch.ContactOK(ctx)
	}
	if n := logging.ResolveProblems(discovery.MainTillProblemKey); n > 0 {
		logging.L().Infof("sync: main till reachable again — %d earlier main-till problem(s) resolved", n)
	}
}
