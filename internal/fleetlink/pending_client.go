package fleetlink

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/universaltill/universal-till/internal/lantls"
	"github.com/universaltill/universal-till/internal/logging"
)

// The joining till's pending link, joiner side (ADR-0114 amendment
// 2026-11-03, ut-docs#5330 slice B2). The main till's side is pending.go.
//
// The link is a nudge, never a carry: the only thing this side acts on is a
// `pairing` frame's status. Everything else the main till could say is
// ignored, and fleetlink logs nothing on this path (the secret rides in a
// header, and no error text or URL is ever handed to a caller).

// PendingTarget is one joining-till attempt's pending link.
type PendingTarget struct {
	BaseURL    string       // the attempt's dial URL; must be https (wss), else refused without a dial
	ID, Secret string       // sent only in HeaderPairRequestID / HeaderPairRequestSecret
	HTTPClient *http.Client // pinned, HTTP/1.1 (lantls.PinnedClient.Client); copied by dialWith
}

// PendingOptions: zero values take the defaults noted.
type PendingOptions struct {
	Config           Config        // PingInterval 5 s, PeerTimeout 12 s, WriteTimeout 10 s
	BackoffMin       time.Duration // 1 s
	BackoffMax       time.Duration // 30 s
	HandshakeTimeout time.Duration // 10 s per dial
	// OnUp is called on the watch goroutine with true after each upgrade
	// and false when that connection ends. It must not block.
	OnUp func(up bool)
}

// PendingEnd is how WatchPending ended.
type PendingEnd int

// PendingEnd values.
const (
	PendingResolved PendingEnd = iota + 1 // a valid pairing frame: Status set
	PendingRefused                        // final: no redial this attempt
	PendingStopped                        // ctx ended (TTL, new pair-start, shutdown)
)

// PendingResult carries no URL, header or error text, so a caller cannot log
// one.
type PendingResult struct {
	End        PendingEnd
	Status     string    // PairingApproved | PairingDenied | PairingExpired (Resolved only)
	HTTPStatus int       // the last refused upgrade's status, 0 if none
	CloseCode  CloseCode // the last remote close code, 0 if none
}

// errPendingProtocol marks a connection end that must not be redialled.
var errPendingProtocol = errors.New("fleetlink: pending link protocol end")

// pendingLinkURL is the wss:// link URL for base; ok is false for anything
// else, so the secret never goes over plain ws.
func pendingLinkURL(base string) (string, bool) {
	u, err := linkURL(base)
	if err != nil || len(u) < 6 || u[:6] != "wss://" {
		return "", false
	}
	return u, true
}

func dialPending(ctx context.Context, url string, hc *http.Client, id, secret string) (Conn, error) {
	return dialWith(ctx, url, hc, http.Header{
		HeaderPairRequestID:     {id},
		HeaderPairRequestSecret: {secret},
	}, PendingReadLimit)
}

func (o PendingOptions) withDefaults() PendingOptions {
	d := DefaultConfig()
	if o.Config.PingInterval <= 0 {
		o.Config.PingInterval = d.PingInterval
	}
	if o.Config.PeerTimeout <= 0 {
		o.Config.PeerTimeout = d.PeerTimeout
	}
	if o.Config.WriteTimeout <= 0 {
		o.Config.WriteTimeout = d.WriteTimeout
	}
	if o.BackoffMin <= 0 {
		o.BackoffMin = time.Second
	}
	if o.BackoffMax <= 0 {
		o.BackoffMax = 30 * time.Second
	}
	if o.HandshakeTimeout <= 0 {
		o.HandshakeTimeout = 10 * time.Second
	}
	if o.OnUp == nil {
		o.OnUp = func(bool) {}
	}
	return o
}

type pendingMsg struct {
	b   []byte
	err error
}

// runPending is one connection's loop. It returns a status for a valid
// pairing frame; otherwise err is errPendingProtocol (final) or another
// error (retryable), with the remote close code if one was sent. It closes
// conn and joins its reader before returning.
func runPending(ctx context.Context, conn Conn, cfg Config) (status string, code CloseCode, err error) {
	rctx, cancel := context.WithCancel(ctx)
	msgs := make(chan pendingMsg, 1)
	readerDone := make(chan struct{})
	go func() {
		defer logging.RecoverAndLog("fleetlink.pendingClientRead")
		defer close(readerDone)
		for {
			b, rerr := conn.Read(rctx)
			select {
			case msgs <- pendingMsg{b, rerr}:
			case <-rctx.Done():
				return
			}
			if rerr != nil {
				return
			}
		}
	}()
	closeWith := CloseShutdown
	defer func() {
		cancel()
		conn.Close(closeWith, "")
		<-readerDone
	}()

	write := func(env Envelope) bool {
		b, eerr := Encode(env)
		if eerr != nil {
			return false
		}
		wctx, wcancel := context.WithTimeout(rctx, cfg.WriteTimeout)
		defer wcancel()
		return conn.Write(wctx, b) == nil
	}

	tick := time.NewTicker(cfg.PingInterval)
	defer tick.Stop()
	lastIn := time.Now()
	seq := 0
	for {
		select {
		case <-ctx.Done():
			return "", 0, ctx.Err()
		case <-tick.C:
			if time.Since(lastIn) > cfg.PeerTimeout {
				closeWith = CloseGone
				return "", 0, errors.New("silent")
			}
			seq++
			m, _ := newMessage("pc"+strconv.Itoa(seq), TypePing, "", nil)
			if !write(m) {
				return "", 0, errors.New("write failed")
			}
		case m := <-msgs:
			if m.err != nil {
				if ctx.Err() != nil {
					return "", 0, ctx.Err()
				}
				code = remoteCloseCode(m.err)
				switch {
				case errors.Is(m.err, errNonText), websocket.CloseStatus(m.err) == websocket.StatusMessageTooBig:
					closeWith = CloseProtocol
					return "", code, errPendingProtocol
				case code == CloseReplaced, code == CloseProtocol, code == CloseRevoked:
					return "", code, errPendingProtocol
				}
				return "", code, m.err
			}
			env, derr := Decode(m.b)
			if derr != nil {
				closeWith = CloseProtocol
				return "", 0, errPendingProtocol
			}
			lastIn = time.Now()
			if env.ReplyTo != "" {
				continue
			}
			switch env.Type {
			case TypePing:
				if len(env.ID) > maxPendingIDLen {
					continue
				}
				if pm, merr := newMessage("pc-pong", TypePong, env.ID, nil); merr == nil && !write(pm) {
					return "", 0, errors.New("write failed")
				}
			case TypePairing:
				var p PairingPayload
				if json.Unmarshal(env.Payload, &p) != nil {
					closeWith = CloseProtocol
					return "", 0, errPendingProtocol
				}
				switch p.Status {
				case PairingApproved, PairingDenied, PairingExpired:
					closeWith = CloseShutdown
					return p.Status, 0, nil
				}
				closeWith = CloseProtocol
				return "", 0, errPendingProtocol
			case TypeBye:
				return "", 0, errors.New("bye")
			}
			// pong, error and anything unknown: liveness only.
		}
	}
}

// WatchPending dials the pending link and redials it until a result, a
// final refusal or ctx's end. It runs on the caller's goroutine. Each live
// connection has one extra reader goroutine, joined before the next dial and
// before return.
func WatchPending(ctx context.Context, t PendingTarget, o PendingOptions) PendingResult {
	o = o.withDefaults()
	url, ok := pendingLinkURL(t.BaseURL)
	if !ok || t.HTTPClient == nil {
		return PendingResult{End: PendingRefused}
	}
	res := PendingResult{}
	attempt := 1
	wait := func(d time.Duration) bool {
		tm := time.NewTimer(d)
		defer tm.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-tm.C:
			return true
		}
	}
	for {
		if ctx.Err() != nil {
			res.End = PendingStopped
			return res
		}
		dctx, dcancel := context.WithTimeout(ctx, o.HandshakeTimeout)
		conn, err := dialPending(dctx, url, t.HTTPClient, t.ID, t.Secret)
		dcancel()
		if err != nil {
			if ctx.Err() != nil {
				res.End = PendingStopped
				return res
			}
			var de *DialError
			var d time.Duration
			switch {
			case errors.As(err, &de):
				res.HTTPStatus = de.Status
				if de.Status != http.StatusTooManyRequests && de.Status != http.StatusServiceUnavailable {
					res.End = PendingRefused
					return res
				}
				d = max(de.RetryAfter, fullJitter(attempt, o.BackoffMin, o.BackoffMax))
			case errors.Is(err, lantls.ErrPinMismatch):
				res.End = PendingRefused
				return res
			default:
				d = fullJitter(attempt, o.BackoffMin, o.BackoffMax)
			}
			attempt++
			if !wait(d) {
				res.End = PendingStopped
				return res
			}
			continue
		}

		o.OnUp(true)
		started := time.Now()
		status, code, rerr := runPending(ctx, conn, o.Config)
		o.OnUp(false)
		if code != 0 {
			res.CloseCode = code
		}
		switch {
		case rerr == nil:
			res.End, res.Status = PendingResolved, status
			return res
		case ctx.Err() != nil:
			res.End = PendingStopped
			return res
		case errors.Is(rerr, errPendingProtocol):
			res.End = PendingRefused
			return res
		}
		if time.Since(started) >= o.Config.PeerTimeout {
			attempt = 1
		}
		d := fullJitter(attempt, o.BackoffMin, o.BackoffMax)
		attempt++
		if !wait(d) {
			res.End = PendingStopped
			return res
		}
	}
}
