package pages

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins/marketplace"
)

// Approve-to-pair (ADR-0033 part 2/3, universaltill/ut-docs#184): the
// pending-request queue + handlers behind LAN till discovery. Discovery
// itself (#183) and the approve/deny UI (#185) are separate cards — this
// is the backend surface only, verified over HTTP.

const pairingRequestTTL = 10 * time.Minute

// pairRateLimiter caps pair-request bursts per source (ADR-0033 §8's
// inbound mitigation — nothing under internal/ rate-limits today, so this
// is deliberately small and scoped to this one endpoint, not a general
// facility).
type pairRateLimiter struct {
	mu     sync.Mutex
	window time.Duration
	max    int
	hits   map[string][]time.Time
}

func newPairRateLimiter(window time.Duration, max int) *pairRateLimiter {
	return &pairRateLimiter{window: window, max: max, hits: map[string][]time.Time{}}
}

// pairRateLimiterSweepThreshold bounds worst-case memory: a source that
// hits once and is never heard from again would otherwise leak a map
// entry for the process lifetime (pruning by cutoff only ever touches
// the CURRENT caller's own entry). Once the map holds this many distinct
// sources, allow() pays for one full sweep to drop anything now stale —
// self-healing, no background goroutine/ticker needed for a v1-scoped
// limiter (ADR-0033's own consequences section calls this "not a
// general-purpose facility for v1").
const pairRateLimiterSweepThreshold = 256

// allow records a hit for source and reports whether it's within the cap.
func (l *pairRateLimiter) allow(source string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-l.window)

	if len(l.hits) >= pairRateLimiterSweepThreshold {
		for src, hits := range l.hits {
			if src == source {
				continue // handled below with its fresh hit appended
			}
			if pruneBefore(hits, cutoff) == nil {
				delete(l.hits, src)
			}
		}
	}

	kept := pruneBefore(l.hits[source], cutoff)
	if len(kept) >= l.max {
		l.hits[source] = kept
		return false
	}
	l.hits[source] = append(kept, now)
	return true
}

// pruneBefore returns hits after cutoff, or nil if none remain — nil
// (not an empty non-nil slice) so the caller can tell "nothing left,
// safe to delete the map entry" apart from "still tracking, just empty
// this instant."
func pruneBefore(hits []time.Time, cutoff time.Time) []time.Time {
	var kept []time.Time
	for _, t := range hits {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	return kept
}

func sourceOf(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// derivedVerificationCode is the manager/replica visual match-and-compare
// (ADR-0033 §4): first6digits(SHA-256(commitment ‖ primary_till_id)) — six
// DECIMAL digits (the ADR's wording and the manager-facing display are
// both digit-based, like a TOTP code), not the first 6 hex characters of
// the hash. The replica side (#183/#185) must derive this the same way or
// the two screens will never visually match.
//
// primaryTillID stand-in: LAN discovery (#183) — which was meant to carry
// this over the mDNS TXT record — was never actually implemented despite
// being marked done on the tracker, so there is currently no channel by
// which a replica learns this primary's id at all. This reuses the
// existing marketplace device id as the best available stable per-install
// identifier; ADR-0033 §8's outbound (impersonation) mitigation is
// therefore NOT YET actually in effect end-to-end — it only becomes real
// once #183 lands for real and the replica can compute the same code
// independently. Tracked as universaltill/ut-docs#264, not silently left
// undocumented.
func derivedVerificationCode(commitment, primaryTillID string) string {
	sum := sha256.Sum256([]byte(commitment + primaryTillID))
	code := binary.BigEndian.Uint32(sum[:4]) % 1000000
	return fmt.Sprintf("%06d", code)
}

func registerPairingAPI(mux *http.ServeMux, d *common.Deps, svc *auth.Service, tokens *enrolTokens) {
	repo := data.NewPairingRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)
	limiter := newPairRateLimiter(time.Minute, 5)

	// Replica-initiated, unauthenticated by design (ADR-0033 §8 — the
	// manager approval step is the trust boundary for this direction);
	// rate-limited per source so the queue can't be spammed unboundedly.
	mux.HandleFunc("POST /api/sync/pair-request", func(w http.ResponseWriter, r *http.Request) {
		if !limiter.allow(sourceOf(r)) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": "too many pair requests"})
			return
		}
		var in struct {
			DeviceName string `json:"device_name"`
			Commitment string `json:"commitment"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		in.DeviceName = strings.TrimSpace(in.DeviceName)
		in.Commitment = strings.TrimSpace(strings.ToLower(in.Commitment))
		if in.DeviceName == "" || len(in.Commitment) != 64 {
			http.Error(w, "device_name and a valid sha256 commitment are required", http.StatusBadRequest)
			return
		}
		id, err := repo.CreatePendingRequest(r.Context(), in.DeviceName, in.Commitment, pairingRequestTTL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"id": id}, "error": nil})
	})

	// Manager-gated (session role, same as the Tills page itself) list of
	// pending requests, each with the verification code the manager
	// visually compares against the replica's own "waiting" screen.
	mux.HandleFunc("GET /api/sync/pair-requests", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager required", http.StatusForbidden)
			return
		}
		list, err := repo.ListPending(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		primaryTillID := marketplace.DeviceIDFromConfig(&d.Cfg.Marketplace)
		type pendingOut struct {
			ID               string `json:"id"`
			DeviceName       string `json:"device_name"`
			RequestedAt      string `json:"requested_at"`
			VerificationCode string `json:"verification_code"`
		}
		out := make([]pendingOut, 0, len(list))
		for _, p := range list {
			out = append(out, pendingOut{
				ID:               p.ID,
				DeviceName:       p.DeviceName,
				RequestedAt:      p.RequestedAt,
				VerificationCode: derivedVerificationCode(p.Commitment, primaryTillID),
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  map[string]any{"pending": out},
			"error": nil,
		})
	})

	// Manager-PIN-gated (reuses refund_page.go's AuthorizeManager pattern,
	// not a new auth mechanism): approve issues a one-time enrolment
	// token via the SAME enrolTokens store the QR flow uses, and
	// associates it with the row so the possession-gated retrieval
	// endpoint below can hand it back. The token is only minted AFTER
	// repo.Approve's compare-and-swap succeeds — issuing it first and
	// approving second would leak a live, unburnt token into the
	// enrolTokens store on a lost race (concurrent approve/deny/expiry).
	mux.HandleFunc("POST /api/sync/pair-requests/{id}/approve", func(w http.ResponseWriter, r *http.Request) {
		actorID, ok := authorizePairingManager(w, r, svc)
		if !ok {
			return
		}
		id := r.PathValue("id")
		if _, exists, err := repo.GetByID(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		} else if !exists {
			http.Error(w, "pending pairing not found", http.StatusNotFound)
			return
		}
		tok := tokens.issue()
		if err := repo.Approve(r.Context(), id, tok, pairingRequestTTL); err != nil {
			tokens.consume(tok) // burn the now-orphaned token; don't leak a live credential
			if errors.Is(err, data.ErrNotPending) {
				http.Error(w, "pending pairing already resolved", http.StatusConflict)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = posRepo.InsertAudit(r.Context(), nil, actorID, "till_pairing", id, "pairing_approved",
			nil, time.Now().UTC().Format(time.RFC3339), "")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"status": "approved"}, "error": nil})
	})

	// Deny just removes the row — a later request from the same replica
	// is a brand new row, not a resurrection of the denied one.
	mux.HandleFunc("POST /api/sync/pair-requests/{id}/deny", func(w http.ResponseWriter, r *http.Request) {
		actorID, ok := authorizePairingManager(w, r, svc)
		if !ok {
			return
		}
		id := r.PathValue("id")
		if err := repo.Deny(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = posRepo.InsertAudit(r.Context(), nil, actorID, "till_pairing", id, "pairing_denied",
			nil, time.Now().UTC().Format(time.RFC3339), "")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"status": "denied"}, "error": nil})
	})

	// Replica-facing, unauthenticated but possession-gated: the token is
	// released ONLY when the presented request_secret hashes to the
	// stored commitment — never by the pending-request id alone (the
	// impersonation gap ADR-0033's review caught). A wrong/absent secret,
	// an unapproved row, or an unknown/expired id all get an identical
	// 404 so the response can't be used to enumerate pending requests.
	// The 32-byte secret makes guessing infeasible either way, but this
	// is the exact endpoint an unbounded guessing/enumeration attempt
	// would hit, and it also drives a DB read per call — rate-limit it
	// the same as the request endpoint, defence in depth either way.
	mux.HandleFunc("GET /api/sync/pair-requests/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !limiter.allow(sourceOf(r)) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": "too many requests"})
			return
		}
		secret := r.URL.Query().Get("request_secret")
		row, ok, err := repo.GetByID(r.Context(), r.PathValue("id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok || row.Status != "approved" || secret == "" || !commitmentMatches(secret, row.Commitment) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"token": row.Token}, "error": nil})
	})
}

func commitmentMatches(secret, commitment string) bool {
	sum := sha256.Sum256([]byte(secret))
	got := hex.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(commitment)) == 1
}

// authorizePairingManager mirrors refund_page.go's manager-PIN gate: with
// auth on, a manager PIN is required and its owner becomes the
// accountable actor for the audit log; UT_AUTH=off (tests, or an
// operator's explicit choice) bypasses the PIN check and falls back to
// whatever session actor (if any) is present, same precedence refund
// uses. Returns ok=false having already written the error response.
func authorizePairingManager(w http.ResponseWriter, r *http.Request, svc *auth.Service) (actorID string, ok bool) {
	actorID = getSessionUserID(r)
	if auth.Disabled(os.Getenv("UT_AUTH")) {
		return actorID, true
	}
	_ = r.ParseForm()
	approver, err := svc.AuthorizeManager(r.Context(), strings.TrimSpace(r.Form.Get("manager_pin")))
	if err != nil {
		status := http.StatusForbidden
		if errors.Is(err, auth.ErrLockedOut) {
			status = http.StatusTooManyRequests
		}
		http.Error(w, "manager PIN required", status)
		return "", false
	}
	return approver.ID, true
}
