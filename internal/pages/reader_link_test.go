package pages

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/devicepay"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/lantls"
	"github.com/universaltill/universal-till/internal/paybroker"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/readerlink"
)

// ut-docs#6538 R1b (ADR-0198 §6.4/§6.5, design-batch2 §5/§6): a Go fake phone
// over the REAL pinned-TLS reader link drives collect -> result -> confirm
// end to end against the real payment gate, hub and broker client.

// opBroker is the cloud broker, routing by op.
type opBroker struct {
	srv *httptest.Server
	mu  sync.Mutex

	create  string // answer to session_create ("" = provider unavailable)
	status  string // answer to session_status ("" = provider unavailable)
	token   string
	calls   []string
	cancels int
}

func newOpBroker(t *testing.T) *opBroker {
	t.Helper()
	b := &opBroker{
		create: `{"payment_intent_id":"pi_r1b","client_secret":"pi_r1b_secret_TOPSECRET","status":"requires_payment_method","location_id":"tml_shop"}`,
		status: piStatus("requires_payment_method", 0),
		token:  `{"secret":"pst_TOKENSECRET","location_id":"tml_shop"}`,
	}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		op := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		b.mu.Lock()
		b.calls = append(b.calls, op+" "+string(body))
		var ans string
		switch op {
		case "session_create":
			ans = b.create
		case "session_status":
			ans = b.status
		case "connection_token":
			ans = b.token
		case "session_cancel":
			b.cancels++
			ans = `{"status":"canceled"}`
		}
		b.mu.Unlock()
		if ans == "" {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"code":"provider_unavailable"}}`)
			return
		}
		_, _ = io.WriteString(w, ans)
	}))
	paybroker.SetTargetResolver(func(context.Context) (paybroker.Target, bool) {
		return paybroker.Target{BaseURL: b.srv.URL, Bearer: "cred"}, true
	})
	t.Cleanup(func() { b.srv.Close(); paybroker.SetTargetResolver(nil) })
	return b
}

func (b *opBroker) set(f func(b *opBroker)) { b.mu.Lock(); f(b); b.mu.Unlock() }
func (b *opBroker) count(op string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, c := range b.calls {
		if strings.HasPrefix(c, op+" ") {
			n++
		}
	}
	return n
}
func (b *opBroker) all() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.calls, "\n")
}

type phoneRig struct {
	*tapEnv
	ob       *opBroker
	srv      *httptest.Server
	cert     *lantls.Cert
	svc      *readerlink.Service
	readerID string
	ref      string
	bearer   string
	mu       sync.Mutex
	collects []devicepay.Result
}

const rigRef = "rd_0a1b2c3d4e5f"

func newPhoneRig(t *testing.T) *phoneRig {
	t.Helper()
	e := tapRigWith(t, true)
	g := &phoneRig{tapEnv: e, ob: newOpBroker(t), ref: rigRef, bearer: "reader-bearer-r1b"}
	cert, err := lantls.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g.cert = cert
	t.Cleanup(lantls.SetServing(cert))
	resetBearerFailures(t)
	if err := e.dp.Settings.Set(context.Background(), settingPhoneReaderPairing, "on"); err != nil {
		t.Fatal(err)
	}
	adminMux := http.NewServeMux()
	adminRepo := registerSyncAdmin(adminMux, e.dp)
	cfg := fleetlink.DefaultConfig()
	cfg.PingInterval = 100 * time.Millisecond
	cfg.RequestTimeout = 3 * time.Second
	e.dp.Link = newSyncLinkHub(e.dp, cfg, adminRepo)
	t.Cleanup(e.dp.Link.Close)
	g.svc = wireReaderLink(e.dp)
	t.Cleanup(func() { devicepay.SetRemote(nil) })
	registerSyncAPI(e.mux, e.dp)
	registerSyncLink(e.mux, e.dp)
	registerReaderManagement(e.mux, e.dp)

	c, err := data.NewCardReaderRepo(e.dp.Db).CreatePaired(context.Background(),
		data.CardReader{Name: "Counter phone", MethodKey: "tapstripe", PluginID: "p.tap", ReaderRef: g.ref, Kind: data.CardReaderKindPhone},
		data.NewReaderCredential{BearerHash: hashBearer(g.bearer), PinSPKI: cert.Pin()})
	if err != nil {
		t.Fatal(err)
	}
	g.readerID = c.ID

	g.srv = httptest.NewUnstartedServer(e.mux)
	g.srv.TLS = cert.TLSConfig()
	g.srv.Config.ConnContext = lantls.ConnContext(cert)
	g.srv.StartTLS()
	t.Cleanup(g.srv.Close)

	// What tap_to_pay_request does for the plugin: the attempt row first, then
	// the collect on the reader the binding names.
	e.plugin.onAuth = func(ctx context.Context, ev plugins.Event) {
		b, ok := paybroker.BindingFrom(ctx)
		if !ok {
			return
		}
		e.mu.Lock()
		e.bindings = append(e.bindings, b)
		e.mu.Unlock()
		res, _ := plugins.CollectTap(ctx, e.dp.Db, b)
		g.mu.Lock()
		g.collects = append(g.collects, res)
		g.mu.Unlock()
	}
	return g
}

func (g *phoneRig) lastCollect(t *testing.T) devicepay.Result {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.collects) == 0 {
		t.Fatal("the plugin never collected")
	}
	return g.collects[len(g.collects)-1]
}

type fakePhone struct {
	c   *websocket.Conn
	log []fleetlink.Envelope
}

// pinnedDial is the phone's TLS: no CA, no names, the SPKI pin only. A
// certificate with another pin is refused before a byte (the bearer) is sent.
func pinnedClient(pin string) *http.Client {
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true, // verification is the pin check below, by design
		MinVersion:         tls.VersionTLS13,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("no certificate")
			}
			cert, err := x509.ParseCertificate(raw[0])
			if err != nil {
				return err
			}
			if lantls.PinOf(cert) != pin {
				return lantls.ErrPinMismatch
			}
			return nil
		},
	}}}
}

func (g *phoneRig) dial(bearer, pin string) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return websocket.Dial(ctx, "wss"+strings.TrimPrefix(g.srv.URL, "https")+"/api/sync/link", &websocket.DialOptions{
		HTTPClient: pinnedClient(pin),
		HTTPHeader: http.Header{"Authorization": {"Bearer " + bearer}},
	})
}

func (g *phoneRig) connect(t *testing.T, state string) *fakePhone {
	t.Helper()
	c, _, err := g.dial(g.bearer, g.cert.Pin())
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	c.SetReadLimit(1 << 20)
	t.Cleanup(func() { _ = c.CloseNow() })
	p := &fakePhone{c: c}
	if hello := p.next(t); hello.Type != fleetlink.TypeHello {
		t.Fatalf("first frame = %q, want hello", hello.Type)
	}
	if state != "" {
		p.send(t, fleetlink.TypeReaderStatus, "", fleetlink.ReaderStatus{State: state})
		deadline := time.Now().Add(3 * time.Second)
		for {
			if s, ok := g.dp.Link.ReaderState(g.readerID); ok && s.State == state {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("reader state never reached the main")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	return p
}

func (p *fakePhone) next(t *testing.T) fleetlink.Envelope {
	t.Helper()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		_, b, err := p.c.Read(ctx)
		cancel()
		if err != nil {
			t.Fatalf("phone read: %v", err)
		}
		e, err := fleetlink.Decode(b)
		if err != nil {
			t.Fatalf("undecodable frame %s", b)
		}
		if e.Type == fleetlink.TypePing {
			continue
		}
		p.log = append(p.log, e)
		return e
	}
}

func (p *fakePhone) send(t *testing.T, typ, replyTo string, payload any) {
	t.Helper()
	raw, _ := json.Marshal(payload)
	b, _ := fleetlink.Encode(fleetlink.Envelope{ID: fmt.Sprintf("ph-%d", time.Now().UnixNano()), Type: typ, ReplyTo: replyTo, Payload: raw})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := p.c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("phone write: %v", err)
	}
}

func (p *fakePhone) closed(d time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	for {
		if _, _, err := p.c.Read(ctx); err != nil {
			return ctx.Err() == nil
		}
	}
}

// tenderAsync starts the 120.00 tap tender (the cashier's request blocks while
// the phone collects) and returns the response channel.
func (g *phoneRig) tenderAsync(t *testing.T) <-chan *httptest.ResponseRecorder {
	t.Helper()
	ch := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(tenderBody(leg("tapstripe", 120, g.readerRowID(t)))))
		req.Header.Set("Content-Type", "application/json")
		ch <- g.serveAs(g.cashier, req)
	}()
	return ch
}

func (g *phoneRig) readerRowID(t *testing.T) string { return g.readerID }

func (g *phoneRig) serveAs(u auth.User, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	g.mux.ServeHTTP(rec, auth.WithUser(req, u))
	return rec
}

func waitRes(t *testing.T, ch <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("the tender never returned")
	}
	return nil
}

// phoneCollects answers the main's collect like a healthy phone and returns it.
func (p *fakePhone) acceptCollect(t *testing.T) fleetlink.ReaderCollect {
	t.Helper()
	f := p.next(t)
	if f.Type != fleetlink.TypeReaderCollect {
		t.Fatalf("phone saw %q, want reader.collect", f.Type)
	}
	var c fleetlink.ReaderCollect
	_ = json.Unmarshal(f.Payload, &c)
	p.send(t, fleetlink.TypeReaderCollect, f.ID, fleetlink.ReaderCollectAck{Accepted: true})
	return c
}

func TestFakePhoneDrivesCollectResultConfirmEndToEnd(t *testing.T) {
	g := newPhoneRig(t)
	g.ob.set(func(b *opBroker) { b.status = strings.Replace(piStatus("succeeded", 120), "pi_k1b", "pi_r1b", 1) })
	ph := g.connect(t, fleetlink.ReaderReady)

	// The phone asks for a connection token first (the SDK connects).
	ph.send(t, fleetlink.TypeReaderToken, "", map[string]any{})
	tok := ph.next(t)
	var rep fleetlink.ReaderTokenReply
	_ = json.Unmarshal(tok.Payload, &rep)
	if tok.Type != fleetlink.TypeReaderToken || rep.Secret != "pst_TOKENSECRET" || rep.LocationID != "tml_shop" {
		t.Fatalf("token reply = %+v %s", tok, tok.Payload)
	}

	done := g.tenderAsync(t)
	c := ph.acceptCollect(t)
	if c.AmountMinor != 120 || c.Currency == "" || !strings.EqualFold(c.Currency, "gbp") || c.Provider != "stripe" || c.ReaderID != g.ref {
		t.Fatalf("collect frame = %+v", c)
	}
	if c.ClientSecret != "pi_r1b_secret_TOPSECRET" || c.LocationID != "tml_shop" || c.IdempotencyKey != g.lastBinding(t).AttemptKey {
		t.Fatalf("collect frame must come from the main's attempt and the broker: %+v", c)
	}
	// The sale is not recorded on the phone's word: still open while the tap runs.
	if g.sales(t) != 0 {
		t.Fatal("the sale was recorded before the phone reported anything")
	}
	ph.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: c.IdempotencyKey, Status: fleetlink.ResultApproved})
	rec := waitRes(t, done)
	if g.sales(t) != 1 {
		t.Fatalf("a broker-confirmed phone payment completes the sale: %d %s", rec.Code, rec.Body.String())
	}
	if a := g.attempt(t); a.State != data.AttemptApproved || a.PIID != "pi_r1b" || a.CardBrand != "visa" || a.Last4 != "4242" {
		t.Fatalf("attempt = %+v, want approved with the PaymentIntent, brand and last four from the broker", a)
	}
	if g.ob.count("session_create") != 1 || g.ob.count("session_status") == 0 {
		t.Fatalf("main must create in-process and confirm with the broker itself:\n%s", g.ob.all())
	}
	// No card data, secret or token anywhere it must not be.
	for _, f := range ph.log {
		if f.Type != fleetlink.TypeReaderCollect && f.Type != fleetlink.TypeReaderToken && strings.Contains(string(f.Payload), "SECRET") {
			t.Fatalf("a secret rode a %q frame", f.Type)
		}
	}
	if strings.Contains(rec.Body.String(), "TOPSECRET") || strings.Contains(rec.Body.String(), "TOKENSECRET") {
		t.Fatal("a secret reached the cashier's response")
	}
}

func TestReaderLinkRequiresPinnedTLS(t *testing.T) {
	g := newPhoneRig(t)
	// Plain HTTP: the reader bearer is never honoured.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	plain := httptest.NewServer(g.mux)
	defer plain.Close()
	_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(plain.URL, "http")+"/api/sync/link",
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + g.bearer}}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reader link over plain HTTP: err=%v resp=%v, want 401", err, resp)
	}
	// A certificate with another pin: the phone refuses before sending the bearer.
	if _, _, err := g.dial(g.bearer, "00"+g.cert.Pin()[2:]); err == nil {
		t.Fatal("the phone accepted a certificate that does not match its pin")
	}
	// A bearer nobody enrolled.
	if _, resp, err := g.dial("never-enrolled", g.cert.Pin()); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("rogue bearer over pinned TLS: err=%v resp=%v, want 401", err, resp)
	}
	// The real one connects as a reader peer, never as a till.
	g.connect(t, fleetlink.ReaderReady)
	if g.dp.Link.ReaderPeer(g.readerID) == nil || len(g.dp.Link.Peers()) != 0 {
		t.Fatalf("reader peer=%v tills=%v: a reader must not become a till peer", g.dp.Link.ReaderPeer(g.readerID), g.dp.Link.Peers())
	}
}

func TestPhoneSucceededNotConfirmedByBrokerIsPending(t *testing.T) {
	g := newPhoneRig(t)
	g.ob.set(func(b *opBroker) { b.status = piStatus("processing", 0) })
	ph := g.connect(t, fleetlink.ReaderReady)
	done := g.tenderAsync(t)
	c := ph.acceptCollect(t)
	ph.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: c.IdempotencyKey, Status: fleetlink.ResultApproved})
	rec := waitRes(t, done)
	if g.sales(t) != 0 || g.dp.Engine.TenderLock() == "" {
		t.Fatalf("a phone 'approved' the broker does not confirm must not sell (sales=%d lock=%q)", g.sales(t), g.dp.Engine.TenderLock())
	}
	g.wantText(t, rec, "tap.pending.title", "tap.check_again")
	if strings.Contains(strings.ToLower(rec.Body.String()), "declin") {
		t.Fatalf("pending is never 'declined': %s", rec.Body.String())
	}
	if a := g.attempt(t); a.State != data.AttemptUnknown {
		t.Fatalf("attempt = %+v, want unknown", a)
	}
}

func TestBrokerDownIsPendingNeverDeclined(t *testing.T) {
	g := newPhoneRig(t)
	g.ob.set(func(b *opBroker) { b.status = "" })
	ph := g.connect(t, fleetlink.ReaderReady)
	done := g.tenderAsync(t)
	c := ph.acceptCollect(t)
	ph.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: c.IdempotencyKey, Status: fleetlink.ResultDeclined, DeclineCode: "card_declined"})
	rec := waitRes(t, done)
	if g.sales(t) != 0 || g.dp.Engine.TenderLock() == "" {
		t.Fatalf("broker down must lock, not sell and not unlock (sales=%d lock=%q)", g.sales(t), g.dp.Engine.TenderLock())
	}
	if res := g.lastCollect(t); res.Status == devicepay.StatusDeclined {
		t.Fatalf("the phone's decline became a declined with the broker unreachable: %+v", res)
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "declin") {
		t.Fatalf("pending is never 'declined': %s", rec.Body.String())
	}
}

func TestAmountMismatchReaderResultIsPending(t *testing.T) {
	g := newPhoneRig(t)
	g.ob.set(func(b *opBroker) { b.status = piStatus("succeeded", 100) }) // 1.00 charged for a 1.20 sale
	ph := g.connect(t, fleetlink.ReaderReady)
	done := g.tenderAsync(t)
	c := ph.acceptCollect(t)
	ph.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: c.IdempotencyKey, Status: fleetlink.ResultApproved})
	waitRes(t, done)
	if g.sales(t) != 0 || g.dp.Engine.TenderLock() == "" {
		t.Fatalf("an amount mismatch must lock, not sell (sales=%d lock=%q)", g.sales(t), g.dp.Engine.TenderLock())
	}
	if a := g.attempt(t); a.State != data.AttemptUnknown || a.Reason != "amount_mismatch" {
		t.Fatalf("attempt = %+v, want unknown/amount_mismatch", a)
	}
}

func TestLinkDropMidCollectKeepsPendingAndSurvivesRestart(t *testing.T) {
	g := newPhoneRig(t)
	ph := g.connect(t, fleetlink.ReaderReady)
	done := g.tenderAsync(t)
	ph.acceptCollect(t)
	_ = ph.c.CloseNow() // the Wi-Fi goes mid-tap
	rec := waitRes(t, done)
	if g.sales(t) != 0 || g.dp.Engine.TenderLock() == "" {
		t.Fatalf("a link drop mid-collect must lock the tender (sales=%d lock=%q)", g.sales(t), g.dp.Engine.TenderLock())
	}
	if res := g.lastCollect(t); res.Status != devicepay.StatusPending {
		t.Fatalf("collect result = %+v, want pending", res)
	}
	g.wantText(t, rec, "tap.pending.title", "tap.pending.lan")
	if g.ob.cancels != 0 {
		t.Fatal("a possibly-charged PaymentIntent was cancelled on a link drop")
	}
	// A restart keeps the row (a new repo on the same database finds it).
	key := g.lastBinding(t).AttemptKey
	a, ok, err := data.NewPaymentAttemptRepo(g.dp.Db).Get(context.Background(), key)
	if err != nil || !ok || (a.State != data.AttemptUnknown && a.State != data.AttemptPending) {
		t.Fatalf("attempt after the drop = %+v ok=%v err=%v, want a surviving open row", a, ok, err)
	}
}

func TestLostContactWordingNeverDeclined(t *testing.T) {
	text := strings.ToLower(enText(t, "tap.pending.lan"))
	if strings.Contains(text, "declin") || !strings.Contains(text, "charged") {
		t.Fatalf("tap.pending.lan = %q: must warn the customer may have been charged and never say declined", text)
	}
	for _, k := range []string{"tap.remote.waiting", "tap.remote.on_phone", "tap.cancelling"} {
		if strings.Contains(strings.ToLower(enText(t, k)), "declin") {
			t.Fatalf("%s says declined", k)
		}
	}
}

func TestRevokeClosesLinkImmediately(t *testing.T) {
	g := newPhoneRig(t)
	ph := g.connect(t, fleetlink.ReaderReady)
	if err := data.NewCardReaderCredentialRepo(g.dp.Db).Revoke(context.Background(), g.readerID); err != nil {
		t.Fatal(err)
	}
	disconnectReaderLink(g.dp, g.readerID)
	if !ph.closed(2 * time.Second) {
		t.Fatal("the phone's link stayed open after revoke")
	}
	if g.dp.Link.ReaderPeer(g.readerID) != nil {
		t.Fatal("a revoked reader is still registered")
	}
	if _, resp, err := g.dial(g.bearer, g.cert.Pin()); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a revoked bearer re-linked: err=%v resp=%v", err, resp)
	}
}

func TestRevokeMidCollectEndsPendingNotDeclined(t *testing.T) {
	g := newPhoneRig(t)
	ph := g.connect(t, fleetlink.ReaderReady)
	done := g.tenderAsync(t)
	ph.acceptCollect(t)
	disconnectReaderLink(g.dp, g.readerID)
	waitRes(t, done)
	if g.sales(t) != 0 || g.dp.Engine.TenderLock() == "" {
		t.Fatalf("revoke mid-collect must lock the tender (sales=%d lock=%q)", g.sales(t), g.dp.Engine.TenderLock())
	}
}

func TestRogueEnrolledPhoneCannotFinishAnotherReadersCollect(t *testing.T) {
	g := newPhoneRig(t)
	ph := g.connect(t, fleetlink.ReaderReady)
	// A second enrolled reader (or a till) tries to settle the first one's key.
	other, err := data.NewCardReaderRepo(g.dp.Db).CreatePaired(context.Background(),
		data.CardReader{Name: "Other phone", MethodKey: "tapstripe", PluginID: "p.tap", ReaderRef: "rd_ffffffffffff", Kind: data.CardReaderKindPhone},
		data.NewReaderCredential{BearerHash: hashBearer("other-bearer"), PinSPKI: g.cert.Pin()})
	if err != nil {
		t.Fatal(err)
	}
	oc, _, err := g.dial("other-bearer", g.cert.Pin())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = oc.CloseNow() })
	rogue := &fakePhone{c: oc}
	rogue.next(t)
	_ = other
	tillBearer := "till-bearer-r1b"
	if _, err := data.NewTillsRepo(g.dp.Db).InsertTill(context.Background(), "Till 2", hashBearer(tillBearer)); err != nil {
		t.Fatal(err)
	}
	tc, _, err := g.dial(tillBearer, g.cert.Pin())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tc.CloseNow() })
	till := &fakePhone{c: tc}
	till.next(t)

	g.ob.set(func(b *opBroker) { b.status = piStatus("processing", 0) })
	done := g.tenderAsync(t)
	c := ph.acceptCollect(t)
	for _, p := range []*fakePhone{rogue, till} {
		p.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: c.IdempotencyKey, Status: fleetlink.ResultApproved})
	}
	select {
	case <-done:
		t.Fatal("another link's result ended the collect")
	case <-time.After(300 * time.Millisecond):
	}
	ph.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: c.IdempotencyKey, Status: fleetlink.ResultCancelled})
	waitRes(t, done)
}

func TestReadersListShowsPresenceFromTheLink(t *testing.T) {
	g := newPhoneRig(t)
	get := func() string {
		rec := g.do(t, g.manager, http.MethodGet, "/ui/tills/readers", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("readers list = %d %s", rec.Code, rec.Body.String())
		}
		return bodyText(rec)
	}
	if body := get(); !strings.Contains(body, enText(t, "tap.reader.status.offline")) {
		t.Fatalf("an unlinked reader must read Not connected: %s", body)
	}
	ph := g.connect(t, fleetlink.ReaderReady)
	if body := get(); !strings.Contains(body, enText(t, "tap.reader.status.ready")) || strings.Contains(body, enText(t, "tap.reader.status.offline")) {
		t.Fatalf("a ready reader must read Ready: %s", body)
	}
	ph.send(t, fleetlink.TypeReaderStatus, "", fleetlink.ReaderStatus{State: fleetlink.ReaderNeedsSetup, Reason: "reader_needs_setup"})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s, _ := g.dp.Link.ReaderState(g.readerID); s.State == fleetlink.ReaderNeedsSetup {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if body := get(); !strings.Contains(body, enText(t, "tap.reader.status.needs_setup")) {
		t.Fatalf("needs_setup must show: %s", body)
	}
}

func TestRemoteProgressFragmentFollowsThePhone(t *testing.T) {
	g := newPhoneRig(t)
	ph := g.connect(t, fleetlink.ReaderReady)
	frag := func() *httptest.ResponseRecorder {
		return g.do(t, g.cashier, http.MethodGet, "/ui/pay/tap/remote", nil)
	}
	if rec := frag(); rec.Code != http.StatusOK || strings.Contains(bodyText(rec), enText(t, "tap.remote.waiting")) {
		t.Fatalf("no collect running: %d %s, want the quiet poll starter", rec.Code, rec.Body.String())
	}
	done := g.tenderAsync(t)
	f := ph.next(t)
	var rec *httptest.ResponseRecorder
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rec = frag(); strings.Contains(bodyText(rec), enText(t, "tap.remote.waiting")) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	g.wantText(t, rec, "tap.remote.waiting")
	if !strings.Contains(bodyText(rec), "Counter phone") {
		t.Fatalf("the screen must name the reader: %s", rec.Body.String())
	}
	ph.send(t, fleetlink.TypeReaderCollect, f.ID, fleetlink.ReaderCollectAck{Accepted: true})
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rec = frag(); strings.Contains(bodyText(rec), enText(t, "tap.remote.on_phone")) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	g.wantText(t, rec, "tap.remote.on_phone", "tap.cancel")
	var c fleetlink.ReaderCollect
	_ = json.Unmarshal(f.Payload, &c)
	ph.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: c.IdempotencyKey, Status: fleetlink.ResultCancelled})
	waitRes(t, done)
	if rec := frag(); strings.Contains(bodyText(rec), enText(t, "tap.remote.on_phone")) {
		t.Fatalf("after the collect the progress is still shown: %s", rec.Body.String())
	}
}

func TestCashierCancelStopsTheCollectAndTheReader(t *testing.T) {
	g := newPhoneRig(t)
	ph := g.connect(t, fleetlink.ReaderReady)
	done := g.tenderAsync(t)
	ph.acceptCollect(t)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p, ok := g.svc.Progress(); ok && p.Phase == readerlink.PhaseOnPhone {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	rec := g.do(t, g.cashier, http.MethodPost, "/pay/tap/remote/cancel", url.Values{})
	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
	}
	if f := ph.next(t); f.Type != fleetlink.TypeReaderCancel {
		t.Fatalf("phone saw %q, want reader.cancel", f.Type)
	}
	waitRes(t, done)
	if g.ob.cancels != 1 {
		t.Fatalf("session_cancel ran %d times, want 1", g.ob.cancels)
	}
	if g.sales(t) != 0 {
		t.Fatal("a cancelled payment sold goods")
	}
}

func TestReaderTokenNeverOverHTTP(t *testing.T) {
	g := newPhoneRig(t)
	for _, p := range []string{"/api/sync/connection-token", "/api/pos/connection-token", "/api/sync/reader/token", "/pay/tap/token"} {
		for _, m := range []string{http.MethodGet, http.MethodPost} {
			req := httptest.NewRequest(m, p, nil)
			req.Header.Set("Authorization", "Bearer "+g.bearer)
			rec := httptest.NewRecorder()
			g.mux.ServeHTTP(rec, req)
			if rec.Code == http.StatusOK {
				t.Fatalf("%s %s answered 200: a connection token must travel only on the reader link", m, p)
			}
		}
	}
	if strings.Contains(g.ob.all(), "connection_token") {
		t.Fatal("something asked the broker for a token over HTTP")
	}
}

// ux A6 / review: a phone that answers busy shows the reader-busy wording
// ("Reader is busy. Nothing was charged."), not the generic start failure.
func TestPhoneBusyAnswerShowsTheReaderBusyWording(t *testing.T) {
	g := newPhoneRig(t)
	g.ob.set(func(b *opBroker) { b.status = piStatus("canceled", 120) }) // the main cancelled the unused PaymentIntent
	// What a real tap plugin prints for a collect that never started.
	g.plugin.answer = func(int) string {
		g.mu.Lock()
		defer g.mu.Unlock()
		if n := len(g.collects); n > 0 && g.collects[n-1].DeclineCode == "start_failed" {
			return "START_FAILED"
		}
		return ""
	}
	ph := g.connect(t, fleetlink.ReaderReady)
	done := g.tenderAsync(t)
	f := ph.next(t)
	if f.Type != fleetlink.TypeReaderCollect {
		t.Fatalf("phone saw %q, want reader.collect", f.Type)
	}
	ph.send(t, fleetlink.TypeError, f.ID, fleetlink.ErrorPayload{Code: fleetlink.CodeReaderBusy, Retryable: true})
	rec := waitRes(t, done)
	if g.sales(t) != 0 || g.dp.Engine.TenderLock() != "" {
		t.Fatalf("a busy phone moves no money and locks nothing: lock=%q", g.dp.Engine.TenderLock())
	}
	g.wantText(t, rec, "tap.reader.busy_result")
	if strings.Contains(bodyText(rec), enText(t, "tap.start_failed")) {
		t.Fatalf("busy must not show the generic start failure: %s", rec.Body.String())
	}
}
