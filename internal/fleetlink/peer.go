package fleetlink

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// Peer is one live link: on the main till, to one paired till (Hub); on an
// additional till, to its main till (Client). The two sides differ only in
// their peerHost — what they say in hello and what they do with the frames
// only one direction carries (report → main; sync/fleet/pairing → peer).
//
// Goroutine model (ADR-0114 §8: 2 goroutines per connection): the goroutine
// that calls run is the reader; run starts exactly one writer.
// Inbound request handlers run in their own goroutines, at most MaxInFlight
// at a time. The writer is the only goroutine that writes to or closes the
// Conn, so frame order is simple: hello first, then coalesced sync flags,
// then the bounded queue, with pings in between.
type Peer struct {
	tillID string
	host   peerHost
	cfg    Config
	conn   Conn

	ctx    context.Context // cancelled once the writer has closed the Conn
	cancel context.CancelFunc

	closeOnce   sync.Once
	stop        chan struct{} // closed by shutdown: the writer wraps up
	closeCode   CloseCode
	closeReason string
	bye         string // non-empty: say bye with this reason before closing

	// Outbound queue (§8: QueueMessages / QueueBytes). Requests, replies
	// and pongs go here; sync nudges never do (dirty below).
	qmu    sync.Mutex
	queue  [][]byte
	qBytes int
	closed bool
	wake   chan struct{} // capacity 1

	dirty atomic.Uint32 // coalesced sync scopes (scopeBit), O(1) per slow peer

	idPrefix string
	seq      atomic.Uint64

	pmu     sync.Mutex
	pending map[string]chan Envelope // outbound requests awaiting reply_to
	outSem  chan struct{}            // outbound in flight
	inSem   chan struct{}            // inbound in flight
	hwg     sync.WaitGroup           // inbound handler goroutines

	lastFrame atomic.Int64 // unix nanos of the last inbound frame

	hmu   sync.Mutex
	hello *Hello

	// Set by the reader before it returns; read after run returns.
	readErr error  // why the reader stopped (carries the peer's close code)
	peerBye string // the reason in the peer's bye, if it said one
}

// peerHost is the side-specific half of a Peer: Hub on the main till,
// Client on an additional till. Every method is called on the link's reader
// or writer goroutine and must not block.
type peerHost interface {
	// helloFor builds this side's hello (the writer's first frame).
	helloFor(ctx context.Context, tillID string) Hello
	// onFrame is the throttled inbound-frame hook (nil: none).
	onFrame() (fn func(tillID string), every time.Duration)
	// handler answers inbound requests of type typ (nil: unknown_type).
	handler(typ string) RequestHandler
	// gotHello is told the peer's hello once it arrived.
	gotHello(p *Peer, h Hello)
	// gotMessage takes the one-directional notifications (report, sync,
	// fleet, pairing); a side ignores the ones it should never receive.
	gotMessage(p *Peer, env Envelope)
}

func newPeer(host peerHost, cfg Config, idPrefix, tillID string, conn Conn) *Peer {
	ctx, cancel := context.WithCancel(context.Background())
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	p := &Peer{
		tillID:   tillID,
		host:     host,
		cfg:      cfg,
		conn:     conn,
		ctx:      ctx,
		cancel:   cancel,
		stop:     make(chan struct{}),
		wake:     make(chan struct{}, 1),
		idPrefix: idPrefix + hex.EncodeToString(rnd[:]) + "-",
		pending:  map[string]chan Envelope{},
		outSem:   make(chan struct{}, cfg.MaxInFlight),
		inSem:    make(chan struct{}, cfg.MaxInFlight),
	}
	p.lastFrame.Store(time.Now().UnixNano())
	return p
}

// Hello returns the peer's hello, once it has sent one.
func (p *Peer) Hello() (Hello, bool) {
	p.hmu.Lock()
	defer p.hmu.Unlock()
	if p.hello == nil {
		return Hello{}, false
	}
	return *p.hello, true
}

func (p *Peer) nextID() string {
	return p.idPrefix + strconv.FormatUint(p.seq.Add(1), 36)
}

// shutdown asks the writer to close the link with code. First call wins.
func (p *Peer) shutdown(code CloseCode, reason, bye string) {
	p.closeOnce.Do(func() {
		p.closeCode, p.closeReason, p.bye = code, reason, bye
		close(p.stop)
	})
}

// markDirty ORs scopes into the coalesced sync flags and wakes the writer.
func (p *Peer) markDirty(mask uint32) {
	if mask == 0 {
		return
	}
	p.dirty.Or(mask)
	p.poke()
}

func (p *Peer) poke() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// enqueue adds one frame to the bounded outbound queue. A full queue is
// ErrBusy at once (§8) — never a wait, never growth.
func (p *Peer) enqueue(e Envelope) error {
	b, err := Encode(e)
	if err != nil {
		return err
	}
	if int64(len(b)) > p.cfg.ReadLimit {
		return ErrTooLarge
	}
	p.qmu.Lock()
	if p.closed {
		p.qmu.Unlock()
		return ErrClosed
	}
	if len(p.queue) >= p.cfg.QueueMessages || p.qBytes+len(b) > p.cfg.QueueBytes {
		p.qmu.Unlock()
		return ErrBusy
	}
	p.queue = append(p.queue, b)
	p.qBytes += len(b)
	p.qmu.Unlock()
	p.poke()
	return nil
}

func (p *Peer) dequeue() []byte {
	p.qmu.Lock()
	defer p.qmu.Unlock()
	if len(p.queue) == 0 {
		return nil
	}
	b := p.queue[0]
	p.queue[0] = nil
	p.queue = p.queue[1:]
	p.qBytes -= len(b)
	return b
}

// Request sends a request and waits for its reply until ctx's deadline
// (RequestTimeout when ctx has none). It fails with ErrBusy at once when 8
// requests are already in flight or the queue is full, ErrTimeout at the
// deadline, *RemoteError when the peer answered with an error payload, and
// ErrClosed if the link goes away. It never blocks the inbound direction.
func (p *Peer) Request(ctx context.Context, typ string, payload any) (json.RawMessage, error) {
	select {
	case p.outSem <- struct{}{}:
	default:
		return nil, ErrBusy
	}
	defer func() { <-p.outSem }()
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.cfg.RequestTimeout)
		defer cancel()
	}
	id := p.nextID()
	env, err := newMessage(id, typ, "", payload)
	if err != nil {
		return nil, err
	}
	ch := make(chan Envelope, 1)
	p.pmu.Lock()
	p.pending[id] = ch
	p.pmu.Unlock()
	defer func() {
		p.pmu.Lock()
		delete(p.pending, id)
		p.pmu.Unlock()
	}()
	if err := p.enqueue(env); err != nil {
		return nil, err
	}
	select {
	case rep := <-ch:
		if rep.Type == TypeError {
			var ep ErrorPayload
			_ = json.Unmarshal(rep.Payload, &ep)
			return nil, &RemoteError{Code: ep.Code, Retryable: ep.Retryable}
		}
		return rep.Payload, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %s", ErrTimeout, typ)
	case <-p.stop:
		return nil, ErrClosed
	}
}

// notify queues a one-way message (no reply expected), e.g. a report.
// ErrBusy on a full queue, never a wait.
func (p *Peer) notify(typ string, payload any) error {
	m, err := newMessage(p.nextID(), typ, "", payload)
	if err != nil {
		return err
	}
	return p.enqueue(m)
}

// run drives the link until it closes. Called on the reader goroutine.
func (p *Peer) run() {
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		p.writeLoop()
	}()
	p.readLoop()
	p.shutdown(CloseNormal, "closed", "")
	<-writerDone
	p.cancel()
	p.hwg.Wait()
	p.qmu.Lock()
	p.closed, p.queue, p.qBytes = true, nil, 0
	p.qmu.Unlock()
}

func (p *Peer) readLoop() {
	var lastTouch time.Time
	onFrame, every := p.host.onFrame()
	for {
		b, err := p.conn.Read(p.ctx)
		if err != nil {
			p.readErr = err
			if errors.Is(err, errNonText) {
				p.shutdown(CloseProtocol, "text frames only", "")
			}
			return
		}
		now := time.Now()
		p.lastFrame.Store(now.UnixNano())
		if onFrame != nil && now.Sub(lastTouch) >= every {
			lastTouch = now
			onFrame(p.tillID)
		}
		env, err := Decode(b)
		if errors.Is(err, errVersion) {
			p.replyError(env.ID, CodeUnsupportedVersion, false)
			continue
		}
		if err != nil {
			logging.L().Debugf("fleetlink: till %s sent a malformed frame: %v", p.tillID, err)
			continue
		}
		if env.ReplyTo != "" {
			p.deliver(env)
			continue
		}
		if !p.dispatch(env) {
			return
		}
	}
}

// deliver hands a reply to its waiting Request; a reply nobody waits for
// (a pong, or one that lost its deadline race) is dropped.
func (p *Peer) deliver(env Envelope) {
	p.pmu.Lock()
	ch := p.pending[env.ReplyTo]
	p.pmu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- env:
	default:
	}
}

// dispatch handles one non-reply frame; false ends the link.
func (p *Peer) dispatch(env Envelope) bool {
	switch env.Type {
	case TypePing:
		if m, err := newMessage(p.nextID(), TypePong, env.ID, nil); err == nil {
			_ = p.enqueue(m) // a dropped pong is harmless: any frame is liveness
		}
	case TypePong, TypeError:
	case TypeHello:
		var h Hello
		if json.Unmarshal(env.Payload, &h) == nil {
			h.clipStrings()
			p.hmu.Lock()
			p.hello = &h
			p.hmu.Unlock()
			p.host.gotHello(p, h)
		}
	case TypeReport, TypeSync, TypeFleet, TypePairing:
		p.host.gotMessage(p, env)
	case TypeBye:
		var b ByePayload
		_ = json.Unmarshal(env.Payload, &b)
		p.peerBye = clip(b.Reason, maxReportField)
		if p.peerBye == "" {
			p.peerBye = ByeShutdown
		}
		p.shutdown(closeAfterBye, "bye", "")
		return false
	default:
		p.handleRequest(env)
	}
	return true
}

func (p *Peer) handleRequest(env Envelope) {
	h := p.host.handler(env.Type)
	if h == nil {
		p.replyError(env.ID, CodeUnknownType, false)
		return
	}
	select {
	case p.inSem <- struct{}{}:
	default:
		p.replyError(env.ID, CodeBusy, true)
		return
	}
	p.hwg.Add(1)
	go func() {
		defer p.hwg.Done()
		defer func() { <-p.inSem }()
		ctx, cancel := context.WithTimeout(p.ctx, p.cfg.RequestTimeout)
		defer cancel()
		res, err := h(ctx, p.tillID, env.Payload)
		if err != nil {
			var re *RemoteError
			switch {
			case errors.As(err, &re):
				p.replyError(env.ID, re.Code, re.Retryable)
			case ctx.Err() != nil:
				p.replyError(env.ID, CodeTimeout, true)
			default:
				logging.L().Warnf("fleetlink: %s handler for till %s: %v", env.Type, p.tillID, err)
				p.replyError(env.ID, CodeInternal, true)
			}
			return
		}
		m, err := newMessage(p.nextID(), env.Type, env.ID, res)
		if err != nil {
			p.replyError(env.ID, CodeInternal, false)
			return
		}
		if err := p.enqueue(m); err != nil {
			// The requester fails at its own deadline (§2).
			logging.L().Debugf("fleetlink: reply to till %s dropped: %v", p.tillID, err)
		}
	}()
}

func (p *Peer) replyError(replyTo, code string, retryable bool) {
	if replyTo == "" {
		return
	}
	m, err := newMessage(p.nextID(), TypeError, replyTo, ErrorPayload{Code: code, Retryable: retryable})
	if err == nil {
		_ = p.enqueue(m)
	}
}

// writeLoop is the link's only writer and the only place that closes the
// Conn.
func (p *Peer) writeLoop() {
	defer func() { p.conn.Close(p.closeCode, p.closeReason) }()

	if !p.writeMsg(TypeHello, p.host.helloFor(p.ctx, p.tillID)) {
		return
	}

	ping := time.NewTicker(p.cfg.PingInterval)
	defer ping.Stop()
	check := time.NewTicker(max(p.cfg.PeerTimeout/6, time.Millisecond))
	defer check.Stop()
	for {
		select {
		case <-p.stop:
			if p.bye != "" {
				p.writeBye()
			}
			return
		case <-p.wake:
		case <-ping.C:
			if !p.writeMsg(TypePing, nil) {
				return
			}
		case <-check.C:
			if time.Since(time.Unix(0, p.lastFrame.Load())) > p.cfg.PeerTimeout {
				p.shutdown(CloseGone, "no frame within timeout", "")
			}
			continue
		}
		if !p.flush() {
			return
		}
	}
}

// flush writes the coalesced sync flags, then drains the queue. False when
// a write failed (the link is being closed).
func (p *Peer) flush() bool {
	if mask := p.dirty.Swap(0); mask != 0 {
		if !p.writeMsg(TypeSync, SyncPayload{Scopes: scopesOf(mask)}) {
			return false
		}
	}
	for {
		select {
		case <-p.stop:
			return true // the loop's stop arm finishes up
		default:
		}
		b := p.dequeue()
		if b == nil {
			return true
		}
		if !p.write(b) {
			return false
		}
	}
}

func (p *Peer) writeMsg(typ string, payload any) bool {
	m, err := newMessage(p.nextID(), typ, "", payload)
	if err != nil {
		return true
	}
	b, err := Encode(m)
	if err != nil {
		return true
	}
	return p.write(b)
}

// write sends one frame within WriteTimeout (§8); a peer that can't take
// it is disconnected and resyncs via hello when it redials.
func (p *Peer) write(b []byte) bool {
	ctx, cancel := context.WithTimeout(p.ctx, p.cfg.WriteTimeout)
	defer cancel()
	if err := p.conn.Write(ctx, b); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			p.shutdown(CloseSlow, "write timeout", "")
		} else {
			p.shutdown(CloseNormal, "write failed", "")
		}
		return false
	}
	return true
}

func (p *Peer) writeBye() {
	m, err := newMessage(p.nextID(), TypeBye, "", ByePayload{Reason: p.bye})
	if err != nil {
		return
	}
	b, err := Encode(m)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(p.ctx, min(p.cfg.WriteTimeout, time.Second))
	defer cancel()
	_ = p.conn.Write(ctx, b)
}
