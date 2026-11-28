package pages

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/lantls"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Approve-to-pair (ADR-0033 part 2/3, universaltill/ut-docs#184): the
// pending-request queue + handlers behind LAN till discovery. Discovery
// itself (#183) and the approve/deny UI (#185) are separate cards — this
// is the backend surface only, verified over HTTP.

const pairingRequestTTL = 10 * time.Minute

// pairingExpiryAfter is time.AfterFunc; a var so tests capture the delay and
// fire at once (ut-docs#2743).
var pairingExpiryAfter = time.AfterFunc

// publishPairingChange nudges the manager's pairing-queue streams (ut-docs#2743).
func publishPairingChange(d *common.Deps) {
	if d.PairingPush != nil {
		d.PairingPush.Publish()
	}
}

// resolvePendingLink tells the joining till's pending link (if one is open)
// how its request ended. Targeted and O(1), unlike PairingPush's fan-out.
func resolvePendingLink(d *common.Deps, id, status string) {
	if d.PendingLinks != nil {
		d.PendingLinks.Resolve(id, status)
	}
}

// schedulePairingExpiryNudge nudges once the request's TTL has passed. Expiry
// is purely time-based (no write happens), so the stream needs its own timer.
// +1s: expires_at is second-precision and the fragment reads keep rows with
// expires_at >= now, so the re-fetch at TTL+1s already excludes the row.
func schedulePairingExpiryNudge(d *common.Deps, after time.Duration) {
	if d.PairingPush == nil {
		return
	}
	push := d.PairingPush
	pairingExpiryAfter(after+time.Second, push.Publish)
}

// pairRateLimiter caps pair-request bursts per source (ADR-0033 §8's
// inbound mitigation — nothing under internal/ rate-limits today, so this
// is deliberately small and scoped to this one endpoint, not a general
// facility).
type pairRateLimiter struct {
	mu     sync.Mutex
	window time.Duration
	max    int
	hits   map[string][]time.Time
	clock  func() time.Time // nil = time.Now
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
	if l.clock != nil {
		now = l.clock()
	}
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
// the hash. The replica side (#185) must derive this the same way or the
// two screens will never visually match.
//
// primaryTillID now comes from discovery.TillID — the same settings-backed,
// get-or-create id LAN discovery's Advertiser publishes in its mDNS TXT
// record (ADR-0033 part 1, ut-docs#264). A replica that learns this
// primary's id off mDNS and this handler computing the code server-side
// now agree on the identical value, so ADR-0033 §8's outbound
// (impersonation) mitigation is actually in effect end-to-end.
//
// pin is the main till's LAN TLS pin as seen on the pair request's
// connection (ADR-0114 §7 amends ADR-0033 §4, ut-docs#4091): the main till
// uses the pin it served, the joining till the pin it saw. A MITM's
// certificate gives the two screens different codes, which the manager's
// visual compare catches. "" (plain HTTP: a main till without TLS, or an
// older joining till) is the original formula, byte for byte.
func derivedVerificationCode(commitment, primaryTillID, pin string) string {
	sum := sha256.Sum256([]byte(commitment + primaryTillID + pin))
	code := binary.BigEndian.Uint32(sum[:4]) % 1000000
	return fmt.Sprintf("%06d", code)
}

// The phone's approval poll (GET pair-requests/{id}) is not a pair request: it
// repeats every ~2 s for as long as the manager takes to type a PIN
// (ut-docs#6538 F1). It has its own limits, never counted against the
// request limiter: one poll per pairPollGap per (source, request), and a
// generous per-source cap that still stops id enumeration.
const (
	pairPollGap       = 1500 * time.Millisecond // phone cadence is 2 s; slack for jitter
	pairPollRetrySecs = "2"
	pairPollSourceMax = 120 // per source per minute, across all ids
)

// pairPollNow is the poll limiters' clock; a var so tests simulate minutes.
var pairPollNow = time.Now

// validRequesterDeviceID accepts "" (an older till) or a short single-line
// id of letters, digits and dashes (the till's marketplace.device_id).
func validRequesterDeviceID(id string) bool {
	if id == "" {
		return true
	}
	if len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

func registerPairingAPI(mux *http.ServeMux, d *common.Deps, svc *auth.Service, tokens *enrolTokens) {
	repo := data.NewPairingRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)
	tillsRepo := data.NewTillsRepo(d.Db)
	limiter := newPairRateLimiter(time.Minute, 5)
	// The approval poll's own limiters (see pairPollGap); never the request one.
	pollClock := func() time.Time { return pairPollNow() }
	pollEach := &pairRateLimiter{window: pairPollGap, max: 1, hits: map[string][]time.Time{}, clock: pollClock}
	pollSource := &pairRateLimiter{window: time.Minute, max: pairPollSourceMax, hits: map[string][]time.Time{}, clock: pollClock}

	// Replica-initiated, unauthenticated by design (ADR-0033 §8 — the
	// manager approval step is the trust boundary for this direction);
	// rate-limited per source so the queue can't be spammed unboundedly.
	mux.HandleFunc("POST /api/sync/pair-request", func(w http.ResponseWriter, r *http.Request) {
		// ut-docs#6538 (ADR-0198 §6.2): a card reader asks over TLS only, and
		// only a main/standalone till takes one (additional tills get no
		// reader in v1, O-3). Checked before anything else, so nothing is
		// stored and no database is read for a plain-HTTP attempt.
		wantsReader := false
		if r.TLS == nil && readerRoleAsked(r) {
			writeReaderError(w, http.StatusBadRequest, readerErrTLSRequired)
			return
		}
		// ut-docs#4411: plain HTTP closes once every till is pinned.
		if refusePlainSyncWhenClosed(w, r, tillsRepo) {
			return
		}
		// ut-docs#5058: a replaced main till takes no new tills.
		if refuseIfReplaced(w, r, d) {
			return
		}
		if !limiter.allow(sourceOf(r)) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": "too many pair requests"})
			return
		}
		var in struct {
			DeviceName string `json:"device_name"`
			Commitment string `json:"commitment"`
			// ut-docs#2781: the role the till asks to join as; absent
			// (an older till) means an additional till.
			Role string `json:"role"`
			// ut-docs#7097 (AC16): the requester's own device id; a retry from
			// the same device replaces its older pending request. Absent on an
			// older till: nothing is replaced.
			DeviceID string `json:"device_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		in.DeviceName = strings.TrimSpace(in.DeviceName)
		in.Commitment = strings.TrimSpace(strings.ToLower(in.Commitment))
		in.DeviceID = strings.TrimSpace(in.DeviceID)
		if !validRequesterDeviceID(in.DeviceID) {
			http.Error(w, "device_id is not valid", http.StatusBadRequest)
			return
		}
		if in.DeviceName == "" || len(in.Commitment) != 64 {
			http.Error(w, "device_name and a valid sha256 commitment are required", http.StatusBadRequest)
			return
		}
		role, ok := tillRoleFromForm(in.Role)
		if strings.TrimSpace(in.Role) == data.TillRoleCardReader {
			role, ok, wantsReader = data.TillRoleCardReader, true, true
		}
		if !ok {
			http.Error(w, "role must be additional, satellite or card_reader", http.StatusBadRequest)
			return
		}
		if wantsReader && !phoneReaderPairingOn(r.Context(), d) {
			writeReaderError(w, http.StatusForbidden, readerErrPairingOff)
			return
		}
		if wantsReader && d.SyncPrimaryURL(r.Context()) != "" {
			http.Error(w, "card readers pair to the main till", http.StatusConflict)
			return
		}
		// ut-docs#5862, #5875: the name shows on the manager's approval card
		// and becomes the till's name (one line, at most maxTillNameRunes);
		// no pending row for a refused one.
		if key := tillNameRefusalKey(in.DeviceName); key != "" {
			common.LocalizedError(w, r, http.StatusBadRequest, key)
			return
		}
		// The pin this till served on THIS connection ("" over plain
		// HTTP) — what the joining till saw, unless something in between
		// presented its own certificate (ut-docs#4091).
		id, err := repo.CreatePendingRequestForDevice(r.Context(), in.DeviceName, in.Commitment, role,
			lantls.ServedPin(r.Context()), in.DeviceID, pairingRequestTTL)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pairings.error.server", "pairing_api", err)
			return
		}
		publishPairingChange(d)
		schedulePairingExpiryNudge(d, pairingRequestTTL)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"id": id, "pending_link": true}, "error": nil})
	})

	// Manager-gated (session role, same as the Tills page itself) list of
	// pending requests, each with the verification code the manager
	// visually compares against the replica's own "waiting" screen.
	mux.HandleFunc("GET /api/sync/pair-requests", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "sync_management") {
			http.Error(w, "manager required", http.StatusForbidden)
			return
		}
		list, err := repo.ListPending(r.Context())
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pairings.error.server", "pairing_api", err)
			return
		}
		primaryTillID, err := discovery.TillID(r.Context(), data.NewSettingsRepo(d.Db))
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pairings.error.server", "pairing_api", err)
			return
		}
		type pendingOut struct {
			ID               string `json:"id"`
			DeviceName       string `json:"device_name"`
			RequestedAt      string `json:"requested_at"`
			VerificationCode string `json:"verification_code"`
			RequestedRole    string `json:"requested_role"` // ut-docs#2781
		}
		out := make([]pendingOut, 0, len(list))
		for _, p := range list {
			out = append(out, pendingOut{
				ID:               p.ID,
				DeviceName:       p.DeviceName,
				RequestedAt:      p.RequestedAt,
				VerificationCode: derivedVerificationCode(p.Commitment, primaryTillID, p.ServedPin),
				RequestedRole:    p.RequestedRole,
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
		// ut-docs#2781: the manager's final role for the till (the card's
		// role select). Absent keeps the role the till asked for; it is
		// what /api/sync/enroll then enrols the till as (RoleForToken).
		_ = r.ParseForm()
		finalRole := strings.TrimSpace(r.Form.Get("role"))
		if finalRole != "" && !data.ValidPairingRole(finalRole) {
			http.Error(w, "role must be additional, satellite or card_reader", http.StatusBadRequest)
			return
		}
		pending, exists, err := repo.GetByID(r.Context(), id)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pairings.error.server", "pairing_api", err)
			return
		} else if !exists {
			http.Error(w, "pending pairing not found", http.StatusNotFound)
			return
		}
		if pending.Status != "pending" { // a denied tombstone (ut-docs#6796)
			http.Error(w, "pending pairing already resolved", http.StatusConflict)
			return
		}
		// ut-docs#6538: the role is fixed by the request, both ways.
		isReader := pending.RequestedRole == data.TillRoleCardReader
		if finalRole != "" && (finalRole == data.TillRoleCardReader) != isReader {
			writeReaderError(w, http.StatusConflict, readerErrRoleMismatch)
			return
		}
		// ut-docs#7098: "this is the same device as that old entry". The manager's
		// choice (replace_of) is validated now, while they look at it, and again
		// at enrolment; it is stored with this approval only.
		replaceOf := strings.TrimSpace(r.Form.Get("replace_of"))
		if replaceOf != "" {
			if isReader {
				http.Error(w, "a card reader request cannot replace a till", http.StatusBadRequest)
				return
			}
			wantRole := finalRole
			if wantRole == "" {
				wantRole = pending.RequestedRole
			}
			if status, key := validateReplaceChoice(r.Context(), d, replaceOf, pending.DeviceName, wantRole); key != "" {
				common.LocalizedError(w, r, status, key)
				return
			}
		}
		tok := ""
		if isReader {
			// A reader gets no enrolment token (nothing a till could
			// redeem); its identity is created when the phone collects the
			// approval. Refuse now what would fail then, while the manager
			// is looking: no TLS pin, no reader-capable method, no room.
			if pending.ServedPin == "" {
				writeReaderError(w, http.StatusBadRequest, readerErrTLSRequired)
				return
			}
			if _, _, merr := resolveReaderMethod(r.Context(), d); merr != nil {
				writeReaderError(w, http.StatusConflict, readerErrNoMethod)
				return
			}
			if active, lerr := data.NewCardReaderRepo(d.Db).ListActive(r.Context()); lerr == nil && len(active) >= data.MaxActiveCardReaders {
				writeReaderError(w, http.StatusConflict, readerErrLimit)
				return
			}
		} else {
			tok = tokens.issue()
		}
		if err := repo.ApproveWithReplace(r.Context(), id, tok, finalRole, replaceOf, pairingRequestTTL); err != nil {
			if tok != "" {
				tokens.consume(tok) // burn the now-orphaned token; don't leak a live credential
			}
			if errors.Is(err, data.ErrRoleMismatch) {
				writeReaderError(w, http.StatusConflict, readerErrRoleMismatch)
				return
			}
			if errors.Is(err, data.ErrNotPending) {
				http.Error(w, "pending pairing already resolved", http.StatusConflict)
				return
			}
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pairings.error.server", "pairing_api", err)
			return
		}
		publishPairingChange(d)
		resolvePendingLink(d, id, fleetlink.PairingApproved)
		var approvedPayload any
		if finalRole != "" {
			approvedPayload = map[string]any{"role": finalRole}
		}
		_ = posRepo.InsertAudit(r.Context(), nil, actorID, "till_pairing", id, "pairing_approved",
			approvedPayload, time.Now().UTC().Format(time.RFC3339), "")
		// No HX-Refresh (ut-docs#2904): pending_pairings.html's forms
		// refresh only the Tills page's pairing card on a 2xx
		// (data-after-request "ok refresh-region"); the Enrolled Tills
		// table only changes once the replica actually enrols, and it
		// re-renders itself on a main till. tills-changed makes the nav
		// sync-chip dot and the pairing notice (30s polls) re-fetch now, as
		// the reload used to. JSON contract unchanged (#184).
		w.Header().Set("HX-Trigger", "tills-changed")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"status": "approved"}, "error": nil})
	})

	// Deny removes a till request's row; a card_reader request keeps a denied
	// tombstone until expiry so the phone can be told (ut-docs#6796). Either
	// way a later request from the same device is a brand new row.
	mux.HandleFunc("POST /api/sync/pair-requests/{id}/deny", func(w http.ResponseWriter, r *http.Request) {
		actorID, ok := authorizePairingManager(w, r, svc)
		if !ok {
			return
		}
		id := r.PathValue("id")
		if err := repo.Deny(r.Context(), id); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pairings.error.server", "pairing_api", err)
			return
		}
		publishPairingChange(d)
		resolvePendingLink(d, id, fleetlink.PairingDenied)
		_ = posRepo.InsertAudit(r.Context(), nil, actorID, "till_pairing", id, "pairing_denied",
			nil, time.Now().UTC().Format(time.RFC3339), "")
		w.Header().Set("HX-Trigger", "tills-changed") // see approve above
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"status": "denied"}, "error": nil})
	})

	// Replica-facing, unauthenticated but possession-gated: the token is
	// released ONLY when the presented request_secret hashes to the
	// stored commitment — never by the pending-request id alone (the
	// impersonation gap ADR-0033's review caught). A wrong/absent secret,
	// an unapproved row, or an unknown/expired id all get an identical
	// 404 so the response can't be used to enumerate pending requests.
	// ut-docs#6796: ONLY to the secret holder, a card_reader request that was
	// denied or expired answers 410 {"error":"pairing_denied"|"pairing_expired"}
	// (denied wins); a pending one still 404s (keep waiting). Till requests
	// keep today's answers byte for byte.
	// The 32-byte secret makes guessing infeasible either way, but this
	// is the exact endpoint an unbounded guessing/enumeration attempt
	// would hit, and it also drives a DB read per call — rate-limit it
	// (own limits, above), defence in depth either way.
	mux.HandleFunc("GET /api/sync/pair-requests/{id}", func(w http.ResponseWriter, r *http.Request) {
		// ut-docs#4411: the token never leaves over plain HTTP once closed.
		if refusePlainSyncWhenClosed(w, r, tillsRepo) {
			return
		}
		// Not the pair-request limiter (ut-docs#6538 F1): the phone polls
		// every 2 s, that limiter allows 5 a minute. 429 + Retry-After tells
		// the phone to wait, not that it was refused.
		if !pollSource.allow(sourceOf(r)) || !pollEach.allow(sourceOf(r)+"|"+r.PathValue("id")) {
			w.Header().Set("Retry-After", pairPollRetrySecs)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": "too many requests"})
			return
		}
		// The secret travels in a header (ADR-0114 amendment 2026-11-03,
		// ut-docs#5297); the query form stays for joining tills older than
		// that, until the removal card ships.
		secret := r.Header.Get(fleetlink.HeaderPairRequestSecret)
		if secret == "" {
			secret = r.URL.Query().Get("request_secret")
		}
		row, expired, ok, err := repo.GetByIDForPoll(r.Context(), r.PathValue("id"))
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pairings.error.server", "pairing_api", err)
			return
		}
		commitment := unknownCommitment
		if ok {
			commitment = row.Commitment
		}
		match := commitmentMatches(secret, commitment) // always run, known id or not
		if !ok || secret == "" || !match {
			http.NotFound(w, r)
			return
		}
		if row.RequestedRole != data.TillRoleCardReader {
			if expired || row.Status != "approved" {
				http.NotFound(w, r)
				return
			}
		} else {
			switch {
			case row.Status == "denied":
				writeReaderError(w, http.StatusGone, readerErrPairingDenied)
			case expired:
				writeReaderError(w, http.StatusGone, readerErrPairingExpired)
			case row.Status != "approved":
				http.NotFound(w, r)
			default:
				completeReaderPairing(w, r, d, repo, row)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"token": row.Token}, "error": nil})
	})
}

// readerRoleAsked peeks at a pair request's role without consuming the body,
// so a plain-HTTP reader request is refused before any real handling.
func readerRoleAsked(r *http.Request) bool {
	b, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 64<<10))
	if err != nil {
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	var peek struct {
		Role string `json:"role"`
	}
	_ = json.Unmarshal(b, &peek)
	return strings.TrimSpace(peek.Role) == data.TillRoleCardReader
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
// validateReplaceChoice checks a manager's "replace this old entry" choice
// (ut-docs#7098) for a request named newName that will enrol as role: the
// target must be a till row, not live right now (replaceLiveWindow), of the
// SAME role (a replace never changes it), and newName must be free or the
// target's own. It returns an HTTP status and i18n key on refusal, "" key when
// the choice is fine. Used at approval and again at enrolment.
func validateReplaceChoice(ctx context.Context, d *common.Deps, tillID, newName, role string) (int, string) {
	repo := data.NewTillsRepo(d.Db)
	list, err := repo.ListTills(ctx)
	if err != nil {
		return http.StatusInternalServerError, "pairings.error.server"
	}
	var target *data.TillRow
	for i := range list {
		if list[i].ID == tillID {
			target = &list[i]
		}
	}
	if target == nil {
		return http.StatusNotFound, "tills.pending.replace_gone"
	}
	if target.SeenWithin(time.Now(), replaceLiveWindow) {
		return http.StatusConflict, "tills.pending.replace_live"
	}
	if role != "" && role != target.Role {
		return http.StatusConflict, "tills.pending.replace_role"
	}
	if taken, terr := repo.NameTakenExcept(ctx, newName, tillID); terr != nil {
		return http.StatusInternalServerError, "pairings.error.server"
	} else if taken || strings.EqualFold(strings.TrimSpace(newName), tillNameOrDefault(ctx, d, httpx.DefaultLocale())) {
		return http.StatusUnprocessableEntity, "sync.error.name_taken"
	}
	return 0, ""
}

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
