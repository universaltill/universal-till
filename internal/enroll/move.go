package enroll

// Moving a till into another shop (ut-docs#5127 UT-1b; ut-docs
// reference/design-5127-till-approval-for-moves.md §4–§5, investigation
// §4.3). Owner decision 2026-10-28: a till that already belongs to a shop S
// and is paired with a code of a shop T made on my. MOVES into T, keeping its
// device id, instead of joining T as a second device.
//
// The flow, all of it dark until the cloud advertises it:
//
//  1. Pair asks the cloud's GET /version whether it offers pair_move
//     "approval-v1" (only while PAIR_MOVE_ENABLED is on). Only then, and only
//     on a main or standalone till that holds a live credential, it posts the
//     code with move:true, its CURRENT device id and its current token as the
//     bearer. Nothing is wiped first: the till stays in S until the very end.
//  2. The cloud answers 202 pending_approval: the move is recorded, not made.
//     The till keeps the pending block in memory (also re-sent on every
//     /v1/stores/sync as pending_moves) and shows it to managers.
//  3. A manager or admin opens the approval page and approves or declines
//     with a fresh PIN (the handler's step-up). Only then DecideMove signs the
//     cloud's own record with this till's approval key (internal/approvalkey)
//     and posts {decision, key_id, signature}. Nothing is signed or queued
//     while offline.
//  4. An approved move answers the T credential: the till adopts it under the
//     same device id, so its history follows it (the cloud merges S into T).
//
// The pending state is in memory only: a restarted till gets it back from its
// next sync, and a move nobody approves expires in the cloud (~24 h).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/approvalkey"
	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/logging"
)

// Wire names, mirroring ut-cloud's internal/paircontract (CL-2/CL-2b).
const (
	pairMoveCapability    = "approval-v1"
	pendingApprovalStatus = "pending_approval"
	moveUnauthorizedCode  = "move_unauthorized"
	moveUnavailableCode   = "move_unavailable"
	deviceExistsCode      = "device_exists"
	moveApprovalPath      = "/v1/stores/move-approvals/"

	// MaxWrongMovePINs is how many wrong PINs one pending move tolerates on
	// this till before it declines the move by itself (design §5 M5).
	MaxWrongMovePINs = 3

	maxMoveStoreID   = 200
	maxMoveNonce     = 128
	maxMoveNameRunes = 80
	// endedMoveTTL is how long an ended move's outcome stays visible.
	endedMoveTTL = 24 * time.Hour
	// pendingMoveGrace hides a pending move this long after its cloud
	// expiry by the till's own clock. Housekeeping only: the cloud decides
	// expiry (design §3), so the margin is generous.
	pendingMoveGrace = 24 * time.Hour
	// maxPendingMoves / maxEndedMoves bound the in-memory move state: a
	// till cache is budgeted, never unbounded, whatever a sync answer
	// carries. One device realistically has zero or one move waiting.
	maxPendingMoves = 8
	maxEndedMoves   = 32
)

// Move decisions, as the cloud spells them.
const (
	MoveApprove = approvalkey.Approve
	MoveDecline = approvalkey.Decline
)

// PendingMove is one move into another shop waiting for this till's
// approval. Every field comes from the cloud; ExpiresRaw is kept exactly as
// sent because it is part of the signed bytes.
type PendingMove struct {
	MoveID      string
	SourceStore string
	TargetStore string
	TargetName  string
	Nonce       string
	ExpiresAt   time.Time
	ExpiresRaw  string
}

// EndedMove is a move that ended without (or after) this till's decision,
// kept for a day so the managers see why the notice went away.
type EndedMove struct {
	MoveID     string
	TargetName string
	// Status is the cloud's (cancelled, expired) or this till's own outcome
	// (declined, moved, failed, locked).
	Status string
	At     time.Time
}

// MoveHooks are the local-data operations a move needs, wired by the pages
// package (enroll reaches no database itself).
type MoveHooks struct {
	// HasTradingHistory reports whether this till has any sale or close.
	// A till whose old shop cannot vouch for it (401 move_unauthorized)
	// only falls back to an ordinary pairing without history
	// (investigation §4.3); nil or an error counts as "has history".
	HasTradingHistory func(ctx context.Context) (bool, error)
	// ResetUploadLedger forgets which daily rollups this till uploaded, so
	// they re-key to the new shop after a move.
	ResetUploadLedger func(ctx context.Context) error
}

type pendingEntry struct {
	PendingMove
	wrongPINs int
}

var (
	movesMu    sync.Mutex
	pending    = map[string]*pendingEntry{}
	ended      = map[string]EndedMove{}
	moveHooks  MoveHooks
	moveCapMu  sync.Mutex
	moveCapFor string
	moveCapOK  bool
	moveCapAt  time.Time
	// moveCapTTL bounds how long a /version answer is trusted.
	moveCapTTL = 5 * time.Minute
	moveNow    = time.Now // overridable in tests
)

var (
	moveNonceRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	moveStoreRe = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)
)

// SetMoveHooks wires the local-data operations (pages wiring, at boot).
func SetMoveHooks(h MoveHooks) {
	movesMu.Lock()
	moveHooks = h
	movesMu.Unlock()
}

func currentMoveHooks() MoveHooks {
	movesMu.Lock()
	defer movesMu.Unlock()
	return moveHooks
}

// Errors of the move flow the handlers translate.
var (
	// ErrMoveSourceLost: the cloud no longer recognises this till's current
	// shop credential, and the till has sales or closes, so an ordinary
	// pairing would leave that history behind. Nothing changed.
	ErrMoveSourceLost = errors.New("this till's previous shop cannot vouch for it; its history would be left behind")
	// ErrMoveUnknown: no pending move with that id (it ended, or never was).
	ErrMoveUnknown = errors.New("no pending move with that id")
	// ErrMoveLocked: too many wrong PINs were entered for this move.
	ErrMoveLocked = errors.New("this move is locked after too many wrong PINs")
	// ErrMoveStale: the move was asked for a shop this till no longer
	// belongs to, or this till has no credential to answer with.
	ErrMoveStale = errors.New("the move no longer matches this till's shop")
	// ErrMoveNoKey: this till's approval key is missing or unusable.
	ErrMoveNoKey = errors.New("this till has no usable approval key")
	// ErrMoveOffline: the cloud could not be reached; nothing was decided.
	ErrMoveOffline = errors.New("the cloud could not be reached")
)

// MovePendingError is Pair's answer when the cloud recorded a move that now
// waits for this till's approval (202). It is not a failure: the till stays
// in its shop, selling as before, until a manager approves it.
type MovePendingError struct{ Move PendingMove }

func (e *MovePendingError) Error() string { return "pair: the move waits for this till's approval" }

// MoveRefusedError is a move the cloud refused (409/410/429 with a move_* or
// related code). Code is the sanitised machine code. The till is untouched.
type MoveRefusedError struct {
	Status int
	Code   string
	// UsableFrom is move_key_quarantined's time (RFC 3339), else "".
	UsableFrom string
	// FailureCode is move_failed's reason code, else "".
	FailureCode string
}

func (e *MoveRefusedError) Error() string {
	return fmt.Sprintf("move refused: %d %s", e.Status, e.Code)
}

// MoveResult is DecideMove's outcome.
type MoveResult struct {
	Decision   string
	TargetName string
	StoreID    string // the new shop after an approved move
}

// --- capability -------------------------------------------------------------

// MoveCapable reports whether the cloud behind endpoint offers moving a till
// with the till's own approval (GET /version pair_move = "approval-v1").
// Answers are cached for moveCapTTL; any failure means "no".
func MoveCapable(ctx context.Context, endpoint string) bool {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return false
	}
	moveCapMu.Lock()
	if moveCapFor == endpoint && moveNow().Sub(moveCapAt) < moveCapTTL {
		ok := moveCapOK
		moveCapMu.Unlock()
		return ok
	}
	moveCapMu.Unlock()

	ok := fetchMoveCapability(ctx, endpoint)
	moveCapMu.Lock()
	moveCapFor, moveCapOK, moveCapAt = endpoint, ok, moveNow()
	moveCapMu.Unlock()
	return ok
}

func fetchMoveCapability(ctx context.Context, endpoint string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, WebBase(endpoint)+"/version", nil)
	if err != nil {
		return false
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return false
	}
	var v struct {
		PairMove string `json:"pair_move"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&v) != nil {
		return false
	}
	return v.PairMove == pairMoveCapability
}

// moveCandidate is the identity a move proves itself with: a main or
// standalone till with an endpoint, a store, a device id and a token of its
// own. ok is false otherwise.
func moveCandidate(cfg *config.Config) (m config.MarketplaceConfig, ok bool) {
	m = Effective(cfg).Marketplace
	if !CredentialsComplete(m) || strings.TrimSpace(m.DeviceID) == "" {
		return m, false
	}
	return m, true
}

// PairWouldMove reports whether pairing this till now would ask the cloud to
// move it (with its history) rather than replace its connection: the cloud
// offers moves and this main or standalone till holds a live credential. The
// Settings confirmation uses it to say what will happen.
func PairWouldMove(ctx context.Context, cfg *config.Config, kv Settings) bool {
	if isReplica(ctx, kv) {
		return false
	}
	m, ok := moveCandidate(cfg)
	return ok && MoveCapable(ctx, m.EndpointURL)
}

// --- Pair's move leg --------------------------------------------------------

// moveOutcome is what pairMove decided.
type moveOutcome int

const (
	moveFallBack moveOutcome = iota // pair the ordinary way (fresh device id)
	moveDone                        // answered; Pair returns status, err
)

// pairMove is Pair's move attempt; the caller holds the attempt slot and has
// checked the replica and env-pin refusals. It never wipes the identity: a
// refusal leaves the till in its shop exactly as it was.
func pairMove(ctx context.Context, cfg *config.Config, kv Settings, code string) (moveOutcome, error) {
	m, ok := moveCandidate(cfg)
	if !ok || !MoveCapable(ctx, m.EndpointURL) {
		return moveFallBack, nil
	}
	log := logging.L()
	a, err := postPairMove(ctx, m, code, DeviceName(ctx, kv))
	var refused *MoveRefusedError
	switch {
	case err == nil && a.pending != nil:
		recordPending(*a.pending)
		logging.ResolveProblems(ProblemKeyPairFailed)
		log.Infof("enrolment: the move of device %s into store %s waits for this till's approval (ut-docs#5127)", m.DeviceID, a.pending.TargetStore)
		return moveDone, &MovePendingError{Move: *a.pending}
	case err == nil:
		// A cloud that moved at once (no approval step): adopt it.
		if a.deviceID != "" && a.deviceID != m.DeviceID {
			return moveDone, fmt.Errorf("%w: moved under another device id", errPairAnswerRejected)
		}
		adoptMoved(ctx, kv, m.DeviceID, a.storeID, a.merchantID, a.token)
		logging.ResolveProblems(ProblemKeyPairFailed)
		log.Infof("enrolment: this till moved into store %s as device %s (ut-docs#5127)", a.storeID, m.DeviceID)
		kickApprovalKeyReport(context.WithoutCancel(ctx), Effective(cfg).Marketplace, nil)
		return moveDone, nil
	case errors.As(err, &refused) && (refused.Code == moveUnavailableCode || refused.Code == deviceExistsCode):
		// The cloud turned moves off since /version said yes, or the code
		// is for this till's own shop: today's re-pair. The code is unspent.
		return moveFallBack, nil
	case errors.As(err, &refused) && refused.Code == moveUnauthorizedCode:
		// The old shop no longer vouches for this till (revoked, retired,
		// lost). Pair afresh only when nothing would be left behind.
		if tillHasHistory(ctx) {
			log.WarnProblemf(ProblemKeyPairFailed, "enrolment: this till's previous shop no longer vouches for it and it has sales or closes; not pairing it as a new device (ut-docs#5127)")
			return moveDone, ErrMoveSourceLost
		}
		return moveFallBack, nil
	default:
		log.WarnProblemf(ProblemKeyPairFailed, "enrolment: moving this till into another shop failed: %v (ut-docs#5127)", err)
		return moveDone, err
	}
}

func tillHasHistory(ctx context.Context) bool {
	h := currentMoveHooks().HasTradingHistory
	if h == nil {
		return true
	}
	has, err := h(ctx)
	if err != nil {
		logging.L().Warnf("enrolment: could not check this till's sales history; treating it as having some: %v", err)
		return true
	}
	return has
}

type moveAnswer struct {
	storeID, merchantID, token, deviceID string
	pending                              *PendingMove
}

// postPairMove posts the code with move:true, this till's current device id,
// and its current token as the bearer (the cloud's proof that the till holds
// S's credential). The token and the code never reach an error or a log.
func postPairMove(ctx context.Context, m config.MarketplaceConfig, code, deviceName string) (moveAnswer, error) {
	body := map[string]any{"code": code, "device_id": m.DeviceID, "device_name": deviceName,
		"version": buildinfo.Version, "move": true}
	if ak := approvalkey.Shared().Report(m.DeviceID, ""); ak != nil {
		body["approval_key"] = ak
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return moveAnswer{}, err
	}
	status, raw, err := postWithBearer(ctx, strings.TrimRight(m.EndpointURL, "/")+"/v1/stores/pair", m.MerchantToken, payload)
	if err != nil {
		return moveAnswer{}, fmt.Errorf("pair request: %w", err)
	}
	switch status {
	case http.StatusAccepted:
		var acc struct {
			Data struct {
				Status   string          `json:"status"`
				Approval json.RawMessage `json:"approval"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &acc) != nil || acc.Data.Status != pendingApprovalStatus {
			return moveAnswer{}, fmt.Errorf("%w: unreadable pending move", errPairAnswerRejected)
		}
		p, ok := parsePendingMove(acc.Data.Approval)
		if !ok || p.SourceStore != m.StoreID {
			return moveAnswer{}, fmt.Errorf("%w: unreadable pending move", errPairAnswerRejected)
		}
		return moveAnswer{pending: &p}, nil
	case http.StatusOK:
		return parseMovedCredential(raw)
	default:
		return moveAnswer{}, moveRefusal(status, raw)
	}
}

// parseMovedCredential reads a moved credential (a pair 200 or an approve
// 200): store, merchant, device and a well-formed token.
func parseMovedCredential(raw []byte) (moveAnswer, error) {
	var ok struct {
		Data struct {
			StoreID     string `json:"store_id"`
			MerchantID  string `json:"merchant_id"`
			DeviceID    string `json:"device_id"`
			DeviceToken string `json:"device_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &ok); err != nil {
		return moveAnswer{}, fmt.Errorf("%w: not valid JSON", errPairAnswerRejected)
	}
	d := ok.Data
	switch {
	case strings.TrimSpace(d.StoreID) == "":
		return moveAnswer{}, fmt.Errorf("%w: no store", errPairAnswerRejected)
	case strings.TrimSpace(d.MerchantID) == "":
		return moveAnswer{}, fmt.Errorf("%w: no merchant", errPairAnswerRejected)
	case !validRotatedToken(d.DeviceToken):
		return moveAnswer{}, fmt.Errorf("%w: malformed credential (length %d)", errPairAnswerRejected, len(d.DeviceToken))
	}
	return moveAnswer{storeID: d.StoreID, merchantID: d.MerchantID, token: d.DeviceToken, deviceID: strings.TrimSpace(d.DeviceID)}, nil
}

// moveRefusal turns a non-2xx answer into a *MoveRefusedError carrying only
// the sanitised code (and the two data fields the till shows).
func moveRefusal(status int, raw []byte) error {
	var fail struct {
		Data struct {
			UsableFrom  string `json:"usable_from"`
			FailureCode string `json:"failure_code"`
		} `json:"data"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &fail)
	code := fail.Error.Code
	if !redeemErrCodeRe.MatchString(code) {
		code = ""
	}
	e := &MoveRefusedError{Status: status, Code: code}
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(fail.Data.UsableFrom)); err == nil {
		e.UsableFrom = t.UTC().Format(time.RFC3339)
	}
	if redeemErrCodeRe.MatchString(fail.Data.FailureCode) {
		e.FailureCode = fail.Data.FailureCode
	}
	return e
}

// postWithBearer POSTs JSON with the till's bearer and returns the status
// and a bounded body. The token never enters an error.
func postWithBearer(ctx context.Context, url, token string, payload []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	return resp.StatusCode, raw, nil
}

// adoptMoved makes the moved credential this till's identity, keeping its
// device id (investigation §4.3), and forgets the rollup upload ledger so
// the days re-key to the new shop. Caller holds the attempt slot.
func adoptMoved(ctx context.Context, kv Settings, deviceID, storeID, merchantID, token string) {
	resetIdentityForPair(ctx, kv, false, deviceID)
	adoptPaired(ctx, kv, false, deviceID, storeID, merchantID, token)
	if reset := currentMoveHooks().ResetUploadLedger; reset != nil {
		if err := reset(ctx); err != nil {
			logging.L().Warnf("enrolment: after the move, the sales rollup ledger was not reset (days may need a manual re-send): %v", err)
		}
	}
}

// --- pending state ----------------------------------------------------------

// parsePendingMove validates one pending block from the cloud; ok is false
// for anything malformed (it is then ignored, never shown or signed).
func parsePendingMove(raw json.RawMessage) (PendingMove, bool) {
	var b struct {
		MoveID      string `json:"move_id"`
		SourceStore string `json:"source_store"`
		TargetStore string `json:"target_store"`
		TargetName  string `json:"target_name"`
		Nonce       string `json:"nonce"`
		ExpiresAt   string `json:"expires_at"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &b) != nil {
		return PendingMove{}, false
	}
	id, err := uuid.Parse(b.MoveID)
	if err != nil || id.String() != strings.ToLower(b.MoveID) {
		return PendingMove{}, false
	}
	for _, s := range []string{b.SourceStore, b.TargetStore} {
		if s == "" || len(s) > maxMoveStoreID || !moveStoreRe.MatchString(s) {
			return PendingMove{}, false
		}
	}
	if b.Nonce == "" || len(b.Nonce) > maxMoveNonce || !moveNonceRe.MatchString(b.Nonce) {
		return PendingMove{}, false
	}
	exp, err := time.Parse(time.RFC3339, b.ExpiresAt)
	if err != nil || len(b.ExpiresAt) > 40 || strings.ContainsAny(b.ExpiresAt, "\n\r") {
		return PendingMove{}, false
	}
	return PendingMove{
		MoveID:      b.MoveID,
		SourceStore: b.SourceStore,
		TargetStore: b.TargetStore,
		TargetName:  cleanMoveName(b.TargetName),
		Nonce:       b.Nonce,
		ExpiresAt:   exp.UTC(),
		ExpiresRaw:  b.ExpiresAt,
	}, true
}

// cleanMoveName drops control and bidi-override characters from a
// cloud-supplied shop name and bounds it.
func cleanMoveName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			continue
		}
		b.WriteRune(r)
	}
	return clampText(b.String(), maxMoveNameRunes)
}

func recordPending(p PendingMove) {
	movesMu.Lock()
	defer movesMu.Unlock()
	if e, ok := ended[p.MoveID]; ok && e.Status == "locked" {
		// Locked after too many wrong PINs while the decline could not
		// reach the cloud: it stays refused here until it expires.
		return
	}
	if e, ok := pending[p.MoveID]; ok {
		// A move id names one cloud record: its source, target, nonce and
		// expiry never change. The approval page showed the first-seen
		// record and the signature covers exactly that, so a later block
		// may only refresh the display name — never what gets signed.
		if e.SourceStore != p.SourceStore || e.TargetStore != p.TargetStore || e.Nonce != p.Nonce || e.ExpiresRaw != p.ExpiresRaw {
			logging.L().Warnf("enrolment: a sync changed the record of pending move %s; keeping the one first shown (ut-docs#5127)", p.MoveID)
			return
		}
		e.TargetName = p.TargetName
		return
	}
	if len(pending) >= maxPendingMoves {
		return
	}
	pending[p.MoveID] = &pendingEntry{PendingMove: p}
	delete(ended, p.MoveID)
}

func endMove(id, targetName, status string) {
	movesMu.Lock()
	defer movesMu.Unlock()
	if e, ok := pending[id]; ok && targetName == "" {
		targetName = e.TargetName
	}
	delete(pending, id)
	now := moveNow()
	// Bounded: drop the oldest outcomes once the cap is reached.
	for len(ended) >= maxEndedMoves {
		oldest, oldestAt := "", now
		for k, e := range ended {
			if oldest == "" || e.At.Before(oldestAt) {
				oldest, oldestAt = k, e.At
			}
		}
		delete(ended, oldest)
	}
	ended[id] = EndedMove{MoveID: id, TargetName: targetName, Status: status, At: now}
}

// ApplySyncMoves takes a /v1/stores/sync answer's pending_moves and
// ended_moves (the 200 and the 402). pendingRaw nil means the field was
// absent (the feature is off, or an older cloud): the till's state is left
// as it is. Present, it is the whole list for this device.
func ApplySyncMoves(pendingRaw, endedRaw json.RawMessage) {
	// Ended first, while the pending entry still has the shop's name.
	if len(endedRaw) > 0 && string(endedRaw) != "null" {
		var list []struct {
			MoveID string `json:"move_id"`
			Status string `json:"status"`
		}
		if json.Unmarshal(endedRaw, &list) == nil {
			if len(list) > maxEndedMoves {
				list = list[:maxEndedMoves]
			}
			for _, e := range list {
				if _, err := uuid.Parse(e.MoveID); err != nil {
					continue
				}
				switch e.Status {
				case "cancelled", "expired":
				default:
					continue
				}
				movesMu.Lock()
				_, known := pending[e.MoveID]
				_, seen := ended[e.MoveID]
				movesMu.Unlock()
				if known || !seen {
					// Only moves this till knew, or a first report of
					// one that ended while it was off: never re-open
					// an outcome already shown.
					endMove(e.MoveID, "", e.Status)
				}
			}
		}
	}
	if len(pendingRaw) > 0 && string(pendingRaw) != "null" {
		var blocks []json.RawMessage
		if json.Unmarshal(pendingRaw, &blocks) == nil {
			keep := map[string]PendingMove{}
			for _, b := range blocks {
				if len(keep) >= maxPendingMoves {
					break
				}
				if p, ok := parsePendingMove(b); ok {
					keep[p.MoveID] = p
				}
			}
			movesMu.Lock()
			for id := range pending {
				if _, ok := keep[id]; !ok {
					delete(pending, id)
				}
			}
			movesMu.Unlock()
			for _, p := range keep {
				recordPending(p)
			}
		}
	}
}

// PendingMoves is the moves waiting for this till's approval, soonest expiry
// first: only moves out of the shop this till belongs to now, and not long
// past their expiry.
func PendingMoves(cfg *config.Config) []PendingMove {
	store := Effective(cfg).Marketplace.StoreID
	now := moveNow()
	movesMu.Lock()
	out := make([]PendingMove, 0, len(pending))
	for _, e := range pending {
		if e.SourceStore != store || now.After(e.ExpiresAt.Add(pendingMoveGrace)) {
			continue
		}
		out = append(out, e.PendingMove)
	}
	movesMu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if !out[i].ExpiresAt.Equal(out[j].ExpiresAt) {
			return out[i].ExpiresAt.Before(out[j].ExpiresAt)
		}
		return out[i].MoveID < out[j].MoveID
	})
	return out
}

// PendingMoveByID is one of PendingMoves.
func PendingMoveByID(cfg *config.Config, id string) (PendingMove, bool) {
	for _, p := range PendingMoves(cfg) {
		if p.MoveID == id {
			return p, true
		}
	}
	return PendingMove{}, false
}

// EndedMoves is the moves that ended within the last day, newest first.
func EndedMoves() []EndedMove {
	now := moveNow()
	movesMu.Lock()
	out := make([]EndedMove, 0, len(ended))
	for id, e := range ended {
		if now.Sub(e.At) > endedMoveTTL {
			delete(ended, id)
			continue
		}
		out = append(out, e)
	}
	movesMu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		return out[i].MoveID < out[j].MoveID
	})
	return out
}

// EndedMoveByID is one of EndedMoves.
func EndedMoveByID(id string) (EndedMove, bool) {
	for _, e := range EndedMoves() {
		if e.MoveID == id {
			return e, true
		}
	}
	return EndedMove{}, false
}

// RecordWrongMovePIN counts a wrong PIN against a pending move and reports
// whether it has now reached MaxWrongMovePINs.
func RecordWrongMovePIN(id string) bool {
	movesMu.Lock()
	defer movesMu.Unlock()
	e, ok := pending[id]
	if !ok {
		return false
	}
	e.wrongPINs++
	return e.wrongPINs >= MaxWrongMovePINs
}

// LockMove ends a pending move locally after too many wrong PINs, declining
// it in the cloud when it can (a decline needs no PIN: it only refuses).
// Offline, the move just stays refused on this till until it expires.
func LockMove(ctx context.Context, cfg *config.Config, kv Settings, id string) {
	if _, err := DecideMove(ctx, cfg, kv, id, MoveDecline); err != nil {
		logging.L().Warnf("enrolment: the locked move %s was not declined in the cloud (it expires there): %v", id, err)
	}
	if _, still := PendingMoveByID(cfg, id); still {
		endMove(id, "", "locked")
	} else if e, ok := EndedMoveByID(id); ok && e.Status == "declined" {
		endMove(id, e.TargetName, "locked")
	}
}

// --- the decision -----------------------------------------------------------

// DecideMove signs this till's decision on a pending move and sends it.
// The caller has already checked a fresh admin or manager PIN (approve and
// decline alike, except LockMove's decline). Nothing is signed unless it can
// be sent at once; offline returns ErrMoveOffline and keeps the move.
func DecideMove(ctx context.Context, cfg *config.Config, kv Settings, id, decision string) (MoveResult, error) {
	if decision != MoveApprove && decision != MoveDecline {
		return MoveResult{}, fmt.Errorf("move decision must be approve or decline")
	}
	p, ok := PendingMoveByID(cfg, id)
	if !ok {
		return MoveResult{}, ErrMoveUnknown
	}
	if isReplica(ctx, kv) {
		return MoveResult{}, ErrMoveStale
	}
	if !acquireAttempt(ctx) {
		return MoveResult{}, fmt.Errorf("%w: %w", ErrAttemptBusy, ctx.Err())
	}
	defer releaseAttempt()
	m, ok := moveCandidate(cfg)
	if !ok || m.StoreID != p.SourceStore {
		return MoveResult{}, ErrMoveStale
	}
	key := approvalkey.Shared()
	kid, ok := key.KeyID()
	if !ok {
		return MoveResult{}, ErrMoveNoKey
	}
	sig, err := key.SignApproval(approvalkey.Approval{
		Decision: decision, MoveID: p.MoveID, DeviceID: m.DeviceID,
		SourceStore: p.SourceStore, TargetStore: p.TargetStore,
		Nonce: p.Nonce, ExpiresAt: p.ExpiresRaw,
	})
	if err != nil {
		logging.L().Warnf("enrolment: could not sign the move decision: %v", err)
		return MoveResult{}, ErrMoveNoKey
	}
	payload, _ := json.Marshal(map[string]string{"decision": decision, "key_id": kid, "signature": sig})
	status, raw, err := postWithBearer(ctx, strings.TrimRight(m.EndpointURL, "/")+moveApprovalPath+p.MoveID, m.MerchantToken, payload)
	if err != nil {
		logging.L().Warnf("enrolment: the move decision did not reach the cloud: %v", err)
		return MoveResult{}, fmt.Errorf("%w: %w", ErrMoveOffline, err)
	}
	res := MoveResult{Decision: decision, TargetName: p.TargetName}
	if status != http.StatusOK {
		e := moveRefusal(status, raw)
		var r *MoveRefusedError
		if errors.As(e, &r) && moveTerminal(r.Code) {
			endMove(p.MoveID, p.TargetName, strings.TrimPrefix(r.Code, "move_"))
		}
		logging.L().Warnf("enrolment: the cloud refused the move decision: %v (ut-docs#5127)", e)
		return res, e
	}
	if decision == MoveDecline {
		endMove(p.MoveID, p.TargetName, "declined")
		logging.L().Infof("enrolment: this till declined the move into store %s (ut-docs#5127)", p.TargetStore)
		return res, nil
	}
	a, err := parseMovedCredential(raw)
	if err == nil && a.deviceID != "" && a.deviceID != m.DeviceID {
		err = fmt.Errorf("%w: moved under another device id", errPairAnswerRejected)
	}
	if err != nil {
		// Approved in the cloud but unusable here: keep the move, so a retry
		// (the cloud's lost-answer rule) can fetch the credential again.
		logging.L().Warnf("enrolment: the approved move's answer was not usable: %v", err)
		return res, err
	}
	adoptMoved(ctx, kv, m.DeviceID, a.storeID, a.merchantID, a.token)
	endMove(p.MoveID, p.TargetName, "moved")
	res.StoreID = a.storeID
	logging.ResolveProblems(ProblemKeyPairFailed)
	logging.L().Infof("enrolment: this till moved into store %s as device %s (ut-docs#5127)", a.storeID, m.DeviceID)
	kickApprovalKeyReport(context.WithoutCancel(ctx), Effective(cfg).Marketplace, nil)
	return res, nil
}

// moveTerminal: the move can never be decided again, so the till drops it.
func moveTerminal(code string) bool {
	switch code {
	case "move_cancelled", "move_declined", "move_failed", "move_done", "move_expired", moveUnavailableCode:
		return true
	}
	return false
}
