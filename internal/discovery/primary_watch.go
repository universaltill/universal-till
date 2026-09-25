package discovery

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// Re-discovery of a replica's main till (ut-docs#2722).
//
// A replica stores its main till's address as a fixed host:port at pairing
// (sync.primary_url). When the main till comes back on a different address —
// an Android main till used to take a new random port on every launch, and
// DHCP can move any till's IP — the replica kept knocking on the dead
// address forever: no sync, no plugin convergence, no cloud check-in.
//
// PrimaryWatch counts failed contacts from the replica's pull loop. Once the
// main till has been unreachable for UnreachableThreshold ticks (or, at
// launch, was already unreachable before this process started), it browses
// the LAN over mDNS — at most once per MinBrowseInterval — for a till
// advertising the SAME till id this replica paired with. A match alone is
// not trusted: the till id is broadcast in the clear and anyone on the LAN
// can copy it. The candidate must also answer a challenge only the real main
// till can: an HMAC over a fresh nonce keyed by this replica's bearer hash,
// which the main till stored at pairing and never shares (PrimaryProof). Only
// then does sync.primary_url change. The bearer itself is never sent to a
// candidate, and a mismatch or failed proof never switches anything.

// PrimaryTillIDSettingKey holds the main till's discovery id (its
// lan_discovery.till_id, the "id=" in its mDNS TXT record) as this replica
// last proved it. The "sync." prefix keeps it per-till: admin-sync pulls
// never overwrite it (data.PerTillSettingPrefixes).
const PrimaryTillIDSettingKey = "sync.primary_till_id"

// UnreachableThreshold is how many consecutive failed contacts (30s pull
// ticks) make the main till "unreachable": 3 ticks = 90s, the same window
// the rail sync chip already uses for its offline state.
const UnreachableThreshold = 3

// tickInterval is the replica pull loop's cadence (runSyncLoop, 30s).
const tickInterval = 30 * time.Second

// MinBrowseInterval rate-limits the mDNS browse: a stranded replica looks
// for its main till at most once per this interval, never every tick.
const MinBrowseInterval = 5 * time.Minute

// browseTimeout bounds one browse, same order as the Tills page's scan.
const browseTimeout = 3 * time.Second

// maxProofAttempts bounds how many candidates one re-discovery challenges.
const maxProofAttempts = 8

// ProofPath is the main till's challenge endpoint.
const ProofPath = "/api/sync/primary-proof"

// proofLabel domain-separates the HMAC from any other use of the key.
const proofLabel = "ut-primary-proof-v1"

// ProofRequest is the challenge a replica sends. TillID is the replica's
// own sync.till_id (assigned by the main till at enrolment, not a secret);
// Nonce is 32 random bytes, hex.
type ProofRequest struct {
	TillID string `json:"till_id"`
	Nonce  string `json:"nonce"`
}

// ProofResponse is the main till's answer.
type ProofResponse struct {
	PrimaryTillID string `json:"primary_till_id"`
	Proof         string `json:"proof"`
}

// PrimaryProof computes HMAC-SHA256 keyed by the replica's bearer hash (hex,
// exactly as the main till stores it in tills.bearer_hash) over the main
// till's id, the replica's till id, the nonce and hostPort — the host:port
// the replica dialled (its URL's Host; the main till's r.Host). Both sides
// compute it; the replica derives the key from its own bearer. An observer
// learns nothing reusable: the nonce is fresh per challenge, and the key
// never leaves either till.
//
// hostPort binds the proof to an address (ut-docs#2722 review): without it,
// a LAN device copying the main till's public mDNS id could relay the
// challenge to the real main till and return its genuine proof, and the
// replica would re-point sync.primary_url — and its next bearer-carrying
// pull — at the relay. The main till only answers for a Host that is its
// own address (pages.registerPrimaryProof), so a relayed proof is always
// for the real till's address, never the relay's.
func PrimaryProof(bearerHashHex, primaryTillID, replicaTillID, nonce, hostPort string) string {
	mac := hmac.New(sha256.New, []byte(bearerHashHex))
	for _, part := range []string{proofLabel, primaryTillID, replicaTillID, nonce, hostPort} {
		mac.Write([]byte(part))
		mac.Write([]byte{'\n'})
	}
	return hex.EncodeToString(mac.Sum(nil))
}

// HashBearer is the bearer hash exactly as the main till stores it
// (tills.bearer_hash, internal/pages/sync_api.go hashBearer).
func HashBearer(bearer string) string {
	sum := sha256.Sum256([]byte(bearer))
	return hex.EncodeToString(sum[:])
}

// WatchSettings is the settings surface PrimaryWatch needs — satisfied by
// both *data.SettingsRepo and *settings.Store.
type WatchSettings interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key, value string) error
}

// FailureOutcome tells the pull loop what a failed contact led to.
type FailureOutcome struct {
	// Warn is true exactly once per outage: the contact that made the main
	// till "unreachable". The caller logs that one at WARN (it reaches the
	// cloud problems list, ut-docs#2719) and keeps the repeats at INFO.
	Warn bool
	// Relinked is true when re-discovery found the main till at NewURL and
	// sync.primary_url now points there.
	Relinked bool
	OldURL   string
	NewURL   string
}

// PrimaryWatch tracks a replica's contact with its main till. Safe for
// concurrent use: the pull loop records contacts while the status chip
// handler reads Unreachable.
type PrimaryWatch struct {
	settings WatchSettings
	client   *http.Client

	// Seams for tests.
	browse BrowseFunc
	now    func() time.Time

	mu            sync.Mutex
	failures      int
	warned        bool
	lastBrowse    time.Time
	backfillTried bool
}

// BrowseFunc finds main tills on the LAN — Browse in production; a fake LAN
// in tests.
type BrowseFunc func(ctx context.Context, timeout time.Duration) ([]Candidate, error)

// NewPrimaryWatch builds a watch over settings that looks for a moved main
// till with browse (production passes Browse).
func NewPrimaryWatch(settings WatchSettings, browse BrowseFunc) *PrimaryWatch {
	return &PrimaryWatch{
		settings: settings,
		client: &http.Client{
			Timeout: 5 * time.Second,
			// A candidate must answer itself; a redirect could bounce the
			// challenge somewhere else entirely.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		browse: browse,
		now:    time.Now,
	}
}

func (w *PrimaryWatch) get(ctx context.Context, key string) string {
	v, _, _ := w.settings.Get(ctx, key)
	return strings.TrimSpace(v)
}

// ContactOK records a successful round trip to the main till. It re-arms
// the one-time warning, and — once per process, for replicas paired before
// ut-docs#2722 — learns the main till's id through the same proof, so a
// later re-discovery has an identity to match on.
func (w *PrimaryWatch) ContactOK(ctx context.Context) {
	w.mu.Lock()
	w.failures = 0
	w.warned = false
	learn := !w.backfillTried && w.get(ctx, PrimaryTillIDSettingKey) == ""
	if learn {
		w.backfillTried = true
	}
	w.mu.Unlock()
	if !learn {
		return
	}
	primaryURL := w.get(ctx, "sync.primary_url")
	id, err := w.challenge(ctx, primaryURL, "")
	if err != nil {
		logging.L().Infof("sync: could not learn the main till's id from %s (%v) — re-discovery will fall back to the synced discovery id", primaryURL, err)
		return
	}
	_ = w.settings.Set(ctx, PrimaryTillIDSettingKey, id)
}

// ContactFailed records a failed contact (the main till did not answer).
// Past the threshold it reports the one-time warning and, rate-limited,
// tries to re-find the main till on the LAN.
func (w *PrimaryWatch) ContactFailed(ctx context.Context) FailureOutcome {
	w.mu.Lock()
	w.failures++
	unreachable := w.unreachableLocked(ctx)
	var out FailureOutcome
	if unreachable && !w.warned {
		w.warned = true
		out.Warn = true
	}
	now := w.now()
	browse := unreachable && (w.lastBrowse.IsZero() || now.Sub(w.lastBrowse) >= MinBrowseInterval)
	if browse {
		w.lastBrowse = now
	}
	w.mu.Unlock()

	if !browse {
		return out
	}
	oldURL, newURL, err := w.rediscover(ctx)
	if err != nil {
		logging.L().Infof("sync: main till not found on this network (%v) — still selling offline, will look again in %s", err, MinBrowseInterval)
		return out
	}
	w.mu.Lock()
	w.failures = 0
	w.warned = false
	w.mu.Unlock()
	out.Relinked, out.OldURL, out.NewURL = true, oldURL, newURL
	return out
}

// Unreachable reports whether the main till counts as unreachable right now
// and, if so, since when (sync.last_contact_at, RFC 3339; "" if this
// replica never reached it).
func (w *PrimaryWatch) Unreachable(ctx context.Context) (since string, unreachable bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.unreachableLocked(ctx) {
		return "", false
	}
	return w.get(ctx, "sync.last_contact_at"), true
}

// unreachableLocked: UnreachableThreshold consecutive failures, or at least
// one failure when the last contact is already older than that window (the
// main till was gone before this process started — no need to wait out
// three more ticks after a restart).
func (w *PrimaryWatch) unreachableLocked(ctx context.Context) bool {
	if w.failures >= UnreachableThreshold {
		return true
	}
	if w.failures == 0 {
		return false
	}
	last, err := time.Parse(time.RFC3339, w.get(ctx, "sync.last_contact_at"))
	return err != nil || w.now().Sub(last) >= UnreachableThreshold*tickInterval
}

var errNoMatch = errors.New("no till advertising this shop's main till id proved it holds this till's pairing")

// rediscover browses for the main till and switches sync.primary_url to the
// first candidate that matches its identity AND passes the proof.
func (w *PrimaryWatch) rediscover(ctx context.Context) (oldURL, newURL string, err error) {
	oldURL = w.get(ctx, "sync.primary_url")
	if w.get(ctx, "sync.bearer") == "" || w.get(ctx, "sync.till_id") == "" {
		return oldURL, "", errors.New("this till has no pairing to prove")
	}
	expected := w.get(ctx, PrimaryTillIDSettingKey)
	strict := expected != ""
	if !strict {
		// Pre-#2722 replica: no proven id yet. On a replica the discovery
		// id is the MAIN till's (the join snapshot copied it, and every
		// admin pull re-syncs it — lan_discovery.* is not per-till). A hint
		// only: matching candidates go first, and the proof decides.
		expected = w.get(ctx, TillIDSettingKey)
	}
	cands, err := w.browse(ctx, browseTimeout)
	if err != nil && len(cands) == 0 {
		return oldURL, "", err
	}
	ordered := make([]Candidate, 0, len(cands))
	var others []Candidate
	for _, c := range cands {
		if strings.TrimSuffix(c.BaseURL, "/") == strings.TrimSuffix(oldURL, "/") {
			continue // the address that is already failing
		}
		switch {
		case c.TillID == expected && expected != "":
			ordered = append(ordered, c)
		case !strict:
			others = append(others, c)
		}
	}
	ordered = append(ordered, others...)
	for i, c := range ordered {
		if i >= maxProofAttempts {
			break
		}
		id, perr := w.challenge(ctx, c.BaseURL, c.TillID)
		if perr != nil {
			if c.TillID == expected {
				// Claims to be our main till but can't prove it: a stale
				// record, a re-installed main till (new pairing needed) — or
				// a spoof. Never switch; say so where an operator can see it.
				logging.L().Warnf("sync: %s advertises this shop's main till id but did not prove it holds this till's pairing (%v) — not switching", c.BaseURL, perr)
			}
			continue
		}
		if err := w.settings.Set(ctx, "sync.primary_url", c.BaseURL); err != nil {
			return oldURL, "", err
		}
		_ = w.settings.Set(ctx, PrimaryTillIDSettingKey, id)
		logging.L().Infof("sync: main till found again at %s (was %s, main till id %s) — pairing kept, syncing resumes", c.BaseURL, oldURL, id)
		return oldURL, c.BaseURL, nil
	}
	return oldURL, "", errNoMatch
}

// challenge asks baseURL to prove it is this replica's main till. wantID,
// when set, must equal the id the candidate answers with (the id it
// advertised). Returns the proven main till id.
func (w *PrimaryWatch) challenge(ctx context.Context, baseURL, wantID string) (string, error) {
	bearer, tillID := w.get(ctx, "sync.bearer"), w.get(ctx, "sync.till_id")
	if baseURL == "" || bearer == "" || tillID == "" {
		return "", errors.New("no pairing to prove")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	nonce := hex.EncodeToString(raw)
	body, _ := json.Marshal(ProofRequest{TillID: tillID, Nonce: nonce})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(baseURL, "/")+ProofPath, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	// The address this challenge is actually dialled at — exactly what the
	// client sends as Host, so the main till HMACs the same bytes.
	dialled := req.URL.Host
	if dialled == "" {
		return "", errors.New("candidate URL has no host")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("proof refused: " + resp.Status)
	}
	var out struct {
		Data ProofResponse `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&out); err != nil {
		return "", err
	}
	id := strings.TrimSpace(out.Data.PrimaryTillID)
	if id == "" || (wantID != "" && id != wantID) {
		return "", errors.New("proof names a different till")
	}
	want := PrimaryProof(HashBearer(bearer), id, tillID, nonce, dialled)
	got, err := hex.DecodeString(out.Data.Proof)
	wantRaw, _ := hex.DecodeString(want)
	if err != nil || !hmac.Equal(got, wantRaw) {
		return "", errors.New("proof does not match this till's pairing")
	}
	return id, nil
}
