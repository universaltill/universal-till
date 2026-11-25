package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/devicepay"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/paybroker"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/readerlink"
)

// ut-docs#6538 phase 2 (ADR-0198 amendment 2026-11-25): an additional till
// offers the main's phones, through the relay.

// stubMain is an additional till's link to its main: it answers reader.list.
type stubMain struct {
	mu     sync.Mutex
	rows   []fleetlink.RelayReader
	relay  bool
	pin    string
	down   bool
	closed chan struct{}

	relays  []fleetlink.ReaderRelay // what this till asked the main to forward
	onRelay func(fleetlink.ReaderRelay)
}

func (s *stubMain) Request(_ context.Context, typ string, payload any) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if typ == fleetlink.TypeReaderRelay {
		rq, _ := payload.(fleetlink.ReaderRelay)
		s.relays = append(s.relays, rq)
		if s.onRelay != nil {
			go s.onRelay(rq)
		}
		return json.RawMessage(`{"accepted":true}`), nil
	}
	if typ != fleetlink.TypeReaderList {
		return nil, &fleetlink.RemoteError{Code: "unknown_type"}
	}
	return json.Marshal(fleetlink.ReaderListReply{Readers: s.rows})
}
func (s *stubMain) Notify(string, any) error { return nil }
func (s *stubMain) Closed() <-chan struct{}  { return s.closed }
func (s *stubMain) VerifiedPin() string      { return s.pin }
func (s *stubMain) Hello() (fleetlink.Hello, bool) {
	return fleetlink.Hello{ReaderRelay: s.relay}, true
}

func (s *stubMain) set(f func(*stubMain)) { s.mu.Lock(); f(s); s.mu.Unlock() }

// attachRelay gives the rig an additional till's relay client over a stub main.
func attachRelay(t *testing.T, e *tapEnv, rows ...fleetlink.RelayReader) *stubMain {
	t.Helper()
	return attachRelayWith(t, e, func(context.Context, string, string, []byte) ([]byte, error) { return nil, paybrokerOffline{} }, rows...)
}

// attachRelayWith is attachRelay with the broker the till's own credential calls.
func attachRelayWith(t *testing.T, e *tapEnv, broker readerlink.Broker, rows ...fleetlink.RelayReader) *stubMain {
	t.Helper()
	st := &stubMain{rows: rows, relay: true, pin: "sha256:pin", closed: make(chan struct{})}
	rc := readerlink.NewRelayClient(func() readerlink.RelayConn {
		st.mu.Lock()
		defer st.mu.Unlock()
		if st.down {
			return nil
		}
		return st
	}, nil)
	svc := readerlink.New(readerlink.Config{Phones: rc, ByRef: rc.ByRef, ByID: rc.ByID,
		Attempts: data.NewPaymentAttemptRepo(e.dp.Db),
		Broker:   broker})
	rc.Bind(svc)
	e.dp.RelayReaders, e.dp.ReaderPay = rc, svc
	refreshRelay(t, e)
	return st
}

type paybrokerOffline struct{}

func (paybrokerOffline) Error() string { return "offline" }

func refreshRelay(t *testing.T, e *tapEnv) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := e.dp.RelayReaders.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
}

func rr(ref, name, state string) fleetlink.RelayReader {
	return fleetlink.RelayReader{Ref: ref, Name: name, Provider: "stripe", State: state}
}

// notReadyNative: this device has no Tap to Pay of its own.
func notReadyNative(t *testing.T) {
	t.Helper()
	devicepay.SetBridge(&fakeTapBridge{caps: capsNeedsTerms})
}

func TestTapOffer_AdditionalTillOffersTapWhenRelayedPhoneReady(t *testing.T) {
	e := tapRig(t)
	ctx := context.Background()
	notReadyNative(t)
	e.joinTill(t, data.TillRoleAdditional)
	st := attachRelay(t, e, rr("rd_a", "Front phone", "ready"))

	if err := tapOfferGate(ctx, e.dp, readerSourcePOS, false, []string{"tapstripe"}); err != nil {
		t.Fatalf("a ready relayed phone must offer the tap entry: %v", err)
	}
	pm := []data.PaymentMethod{{ID: "cash"}, {ID: "tapstripe"}}
	if got := withoutUnreadyTapEntries(ctx, e.dp, pm); len(got) != 2 {
		t.Fatalf("withoutUnreadyTapEntries = %+v, want both", got)
	}

	notReady := func(name string) {
		t.Helper()
		var tErr *tapStartFailedError
		if err := tapOfferGate(ctx, e.dp, readerSourcePOS, false, []string{"tapstripe"}); !asTapStart(err, &tErr) || !tErr.NotReady {
			t.Fatalf("%s: %v, want NotReady", name, err)
		}
	}
	// A phone that is busy or not set up cannot be offered.
	st.set(func(s *stubMain) { s.rows = []fleetlink.RelayReader{rr("rd_a", "Front phone", "busy")} })
	refreshRelay(t, e)
	notReady("busy phone")
	st.set(func(s *stubMain) {
		s.rows = []fleetlink.RelayReader{rr("rd_a", "Front phone", "ready")}
		s.relay = false
	})
	refreshRelay(t, e)
	notReady("main without reader_relay (older main or phone pairing off)")
	st.set(func(s *stubMain) { s.relay = true; s.pin = "" })
	refreshRelay(t, e)
	notReady("plain HTTP link")
	st.set(func(s *stubMain) { s.pin = "sha256:pin"; s.down = true })
	notReady("link down")
	st.set(func(s *stubMain) { s.down = false })
	refreshRelay(t, e)
	if err := tapOfferGate(ctx, e.dp, readerSourcePOS, false, []string{"tapstripe"}); err != nil {
		t.Fatalf("link back up: %v", err)
	}
	// The sale offline never starts one, relay or not.
	if err := tapOfferGate(ctx, e.dp, readerSourcePOS, true, []string{"tapstripe"}); err == nil {
		t.Fatal("an offline sale must not start a tap")
	}
}

func TestTapOffer_SatelliteAndMainNeverGetRelayedRows(t *testing.T) {
	e := tapRig(t)
	ctx := context.Background()
	notReadyNative(t)
	e.joinTill(t, data.TillRoleSatellite)
	attachRelay(t, e, rr("rd_a", "Front phone", "ready"))
	if rows := relayedReaders(ctx, e.dp, "tapstripe"); len(rows) != 0 {
		t.Fatalf("a satellite got relayed rows: %+v", rows)
	}
	if tapEntryCapable(ctx, e.dp, "tapstripe", "stripe") {
		t.Fatal("a satellite offers a tap")
	}
	relayed := data.CardReader{Kind: data.CardReaderKindPhone, Transport: data.CardReaderTransportInternet, Source: relayedSource}
	for _, src := range []string{readerSourcePOS, readerSourceKiosk, readerSourceJoined} {
		if readerEligible(relayed, src) {
			t.Fatalf("a relayed row is eligible on source %q", src)
		}
	}
	if !readerEligible(relayed, readerSourceAdditional) {
		t.Fatal("a relayed row must be eligible on an additional till")
	}
}

func TestRelayedRowsAreNeverWrittenToCardReaders(t *testing.T) {
	e := tapRigWith(t, true)
	ctx := context.Background()
	notReadyNative(t)
	e.joinTill(t, data.TillRoleAdditional)
	attachRelay(t, e, rr("rd_a", "Front phone", "ready"), rr("rd_b", "Back phone", "ready"))
	if _, err := buildReaderPicker(ctx, e.dp, "tapstripe"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := managedReaders(ctx, e.dp, data.NewCardReaderRepo(e.dp.Db), "tapstripe"); err != nil {
		t.Fatal(err)
	}
	rows, err := data.NewCardReaderRepo(e.dp.Db).ListActive(ctx)
	if err != nil || len(rows) != 0 {
		t.Fatalf("card_readers = %+v err=%v, want none (relayed rows are synthetic)", rows, err)
	}
	if n := countRows(t, e.dp, "SELECT COUNT(*) FROM card_readers"); n != 0 {
		t.Fatalf("%d card_readers rows in the table", n)
	}
}

func TestPickerShowsRelayedPhonesWithPresenceAndLastUsedDefault(t *testing.T) {
	e := tapRigWith(t, true)
	ctx := context.Background()
	notReadyNative(t)
	e.joinTill(t, data.TillRoleAdditional)
	st := attachRelay(t, e, rr("rd_a", "Front phone", "ready"), rr("rd_b", "Back phone", "ready"), rr("rd_c", "Office phone", "busy"))

	view, err := buildReaderPicker(ctx, e.dp, "tapstripe")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Rows) != 3 || view.Selectable != 2 || view.Auto {
		t.Fatalf("rows=%d selectable=%d auto=%v, want 3, 2, false", len(view.Rows), view.Selectable, view.Auto)
	}
	byID := map[string]readerPickerRow{}
	for _, r := range view.Rows {
		byID[r.ID] = r
		if r.Kind != data.CardReaderKindPhone {
			t.Fatalf("row %+v is not a phone", r)
		}
	}
	if c := byID["relay:rd_c"]; c.Selectable || c.State != "busy" || c.Reason != "tap.reason.reader_busy" {
		t.Fatalf("busy phone = %+v, want a busy row that cannot be chosen", c)
	}
	if view.DefaultID != "relay:rd_a" {
		t.Fatalf("default = %q, want the first ready phone with nothing used yet", view.DefaultID)
	}
	// Paying on rd_b makes it THIS till's default, independent of any other till.
	touchReaderLastUsed(ctx, e.dp, "relay:rd_b")
	view, _ = buildReaderPicker(ctx, e.dp, "tapstripe")
	if view.DefaultID != "relay:rd_b" || !byIDLastUsed(view, "relay:rd_b") {
		t.Fatalf("default = %q, want relay:rd_b (last used)", view.DefaultID)
	}
	// ... unless it is busy: never preselect a row that cannot be chosen.
	st.set(func(s *stubMain) {
		s.rows = []fleetlink.RelayReader{rr("rd_a", "Front phone", "ready"), rr("rd_b", "Back phone", "busy")}
	})
	refreshRelay(t, e)
	view, _ = buildReaderPicker(ctx, e.dp, "tapstripe")
	if view.DefaultID != "relay:rd_a" || !view.Auto {
		t.Fatalf("default = %q auto=%v, want relay:rd_a (the last used one is busy) and no picker", view.DefaultID, view.Auto)
	}
}

func byIDLastUsed(v readerPickerView, id string) bool {
	for _, r := range v.Rows {
		if r.ID == id {
			return r.LastUsed
		}
	}
	return false
}

func TestRelayLastUsedPersistsInTillLocalSetting(t *testing.T) {
	e := tapRig(t)
	ctx := context.Background()
	notReadyNative(t)
	e.joinTill(t, data.TillRoleAdditional)
	attachRelay(t, e, rr("rd_b", "Back phone", "ready"))
	touchReaderLastUsed(ctx, e.dp, "relay:rd_b")
	v, ok, _ := e.dp.Settings.Get(ctx, "card_readers.relay_last_ref")
	if !ok || !strings.HasPrefix(v, "rd_b|") {
		t.Fatalf("card_readers.relay_last_ref = %q ok=%v, want rd_b|<time>", v, ok)
	}
	if ref, at := relayLastUsed(ctx, e.dp); ref != "rd_b" || at == "" {
		t.Fatalf("relayLastUsed = %q, %q", ref, at)
	}
	if n := countRows(t, e.dp, "SELECT COUNT(*) FROM card_readers"); n != 0 {
		t.Fatalf("%d card_readers rows written for a relayed phone", n)
	}
}

func TestRelayedRowWithSameNameAsThisTillIsHidden(t *testing.T) {
	e := tapRig(t)
	ctx := context.Background()
	notReadyNative(t)
	e.joinTill(t, data.TillRoleAdditional)
	if err := e.dp.Settings.Set(ctx, "sync.till_name", "Counter Phone"); err != nil {
		t.Fatal(err)
	}
	attachRelay(t, e, rr("rd_self", "  counter phone ", "ready"), rr("rd_b", "Back phone", "ready"))
	rows := relayedReaders(ctx, e.dp, "tapstripe")
	if len(rows) != 1 || rows[0].ReaderRef != "rd_b" {
		t.Fatalf("rows = %+v, want only rd_b (a phone named like this till is hidden)", rows)
	}
}

func TestResolveReaderLegsPicksARelayedPhoneAndNeverAnUnofferedOne(t *testing.T) {
	e := tapRigWith(t, true)
	ctx := context.Background()
	notReadyNative(t)
	e.joinTill(t, data.TillRoleAdditional)
	attachRelay(t, e, rr("rd_a", "Front phone", "ready"), rr("rd_b", "Back phone", "ready"))

	pay := []pos.PaymentInput{{MethodID: "tapstripe", ReaderRowID: "relay:rd_b"}}
	release, err := resolveReaderLegs(ctx, e.dp, pay, readerSourcePOS)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if pay[0].ReaderRef != "rd_b" || pay[0].ReaderKind != data.CardReaderKindPhone || pay[0].ReaderRowID != "relay:rd_b" {
		t.Fatalf("leg = %+v, want rd_b on kind phone", pay[0])
	}
	// A crafted id the main did not list is refused before any authorize.
	bad := []pos.PaymentInput{{MethodID: "tapstripe", ReaderRowID: "relay:rd_zzz"}}
	if rel, err := resolveReaderLegs(ctx, e.dp, bad, readerSourcePOS); err == nil {
		rel()
		t.Fatal("a relayed phone that is not in the directory was accepted")
	} else if ce, ok := err.(*cardReaderError); !ok || ce.Code != readerCodeInvalid {
		t.Fatalf("err = %v, want card_reader.invalid", err)
	}
	// Two ready phones and no choice: the cashier must pick.
	none := []pos.PaymentInput{{MethodID: "tapstripe"}}
	if rel, err := resolveReaderLegs(ctx, e.dp, none, readerSourcePOS); err == nil {
		rel()
		t.Fatal("two phones and no choice must ask which reader")
	} else if ce, ok := err.(*cardReaderError); !ok || ce.Code != readerCodeRequired {
		t.Fatalf("err = %v, want reader_required", err)
	}
}

// G11 (ut-docs#6538 phase 2): the words a cashier reads when a phone reader
// could not start, was busy, or went away. A nothing-started failure says
// nothing was charged; a lost link says the customer MAY have been charged;
// nothing here ever says a card was declined (it was never read).
func TestReaderReasonsNeverSayDeclined(t *testing.T) {
	raw, err := os.ReadFile("web/locales/en.json")
	if err != nil {
		t.Fatal(err)
	}
	var en map[string]string
	if err := json.Unmarshal(raw, &en); err != nil {
		t.Fatal(err)
	}
	nothingCharged := []string{"tap.start_failed", "tap.reader.busy_result", "tap.start_failed.not_ready", "tap.start_failed.setup_again"}
	pickerReasons := []string{"tap.reason.reader_busy", "tap.reason.reader_unreachable", "tap.reason.reader_needs_setup"}
	for _, k := range append(append([]string{"tap.pending.lan", "tap.remote.waiting", "tap.remote.on_phone", "tap.settings.reason.busy"}, nothingCharged...), pickerReasons...) {
		v, ok := en[k]
		if !ok {
			t.Errorf("%s is missing from en.json", k)
			continue
		}
		if strings.Contains(strings.ToLower(v), "declin") {
			t.Errorf("%s = %q says declined for something that never reached a card", k, v)
		}
	}
	for _, k := range nothingCharged {
		if !strings.Contains(en[k], "Nothing was charged") {
			t.Errorf("%s = %q must say Nothing was charged", k, en[k])
		}
	}
	if !strings.Contains(en["tap.pending.lan"], "may have been charged") {
		t.Errorf("tap.pending.lan = %q must say the customer may have been charged", en["tap.pending.lan"])
	}
	for _, k := range pickerReasons {
		if strings.Contains(strings.ToLower(en[k]), "charged") {
			t.Errorf("%s = %q: a picker reason is before any payment, it must not talk about charging", k, en[k])
		}
	}
}

// The whole path on an additional till, through the real tender gate and the
// real broker client: the till creates the PaymentIntent with its OWN credential
// (CallerCore), asks its main to forward the collect, takes the phone's word as
// advice and records the sale only when session_status says succeeded for the
// exact amount.
func TestAdditionalTillRelayCreatesPIWithOwnCredentialAndConfirmsWithSessionStatus(t *testing.T) {
	e := tapRigWith(t, true)
	ob := newOpBroker(t) // after the rig: this broker answers the till's own broker calls
	ob.set(func(b *opBroker) { b.status = strings.Replace(piStatus("succeeded", 120), "pi_k1b", "pi_r1b", 1) })
	notReadyNative(t)
	e.joinTill(t, data.TillRoleAdditional)
	st := attachRelayWith(t, e, func(ctx context.Context, provider, op string, body []byte) ([]byte, error) {
		return paybroker.Call(ctx, paybroker.CallerCore, provider, op, body)
	}, rr("rd_b", "Back phone", "ready"))
	rc := e.dp.RelayReaders
	st.set(func(s *stubMain) {
		s.onRelay = func(rq fleetlink.ReaderRelay) {
			// The main's verdict frame, as the client link delivers it.
			time.Sleep(30 * time.Millisecond)
			rc.OnResult(s, fleetlink.ReaderResult{IdempotencyKey: rq.IdempotencyKey, Status: fleetlink.ResultApproved}, true)
		}
	})
	devicepay.SetRemote(e.dp.ReaderPay)
	t.Cleanup(func() { devicepay.SetRemote(nil) })
	e.plugin.onAuth = func(ctx context.Context, ev plugins.Event) {
		b, ok := paybroker.BindingFrom(ctx)
		if !ok {
			return
		}
		e.mu.Lock()
		e.bindings = append(e.bindings, b)
		e.mu.Unlock()
		_, _ = plugins.CollectTap(ctx, e.dp.Db, b)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(tenderBody(leg("tapstripe", 120, "relay:rd_b"))))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, auth.WithUser(req, e.cashier))
	if e.sales(t) != 1 {
		t.Fatalf("a broker-confirmed relayed payment completes the sale: %d %s", rec.Code, rec.Body.String())
	}
	if a := e.attempt(t); a.State != data.AttemptApproved || a.PIID != "pi_r1b" || a.AmountMinor != 120 {
		t.Fatalf("attempt = %+v, want approved pi_r1b for 120", a)
	}
	if ob.count("session_create") != 1 || ob.count("session_status") == 0 {
		t.Fatalf("this till must create the PaymentIntent and confirm it itself:\n%s", ob.all())
	}
	st.mu.Lock()
	relays := append([]fleetlink.ReaderRelay(nil), st.relays...)
	st.mu.Unlock()
	if len(relays) != 1 || relays[0].ReaderRef != "rd_b" || relays[0].AmountMinor != 120 || relays[0].Provider != "stripe" ||
		relays[0].ClientSecret != "pi_r1b_secret_TOPSECRET" || relays[0].IdempotencyKey != e.attempt(t).Key {
		t.Fatalf("relay frame = %+v", relays)
	}
	if strings.Contains(rec.Body.String(), "TOPSECRET") {
		t.Fatal("the PaymentIntent secret reached the cashier's response")
	}
	// The last used phone is this till's own setting now.
	if ref, _ := relayLastUsed(context.Background(), e.dp); ref != "rd_b" {
		t.Fatalf("relay_last_ref = %q, want rd_b", ref)
	}
}
