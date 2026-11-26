package pages

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
)

// ut-docs#7085: a manager PIN on a sell-screen elevation prompt approves only
// the exact values the prompt showed (to as well as from), once, for the same
// session user. Any edit, replay, other user, expiry or restart re-prompts and
// applies nothing.

func bindEnv(t *testing.T) gateEnv {
	t.Helper()
	elevationApprovals.reset()
	t.Cleanup(elevationApprovals.reset)
	return newGateEnv(t)
}

// basketState is a comparable snapshot of everything these gates can change.
func (e gateEnv) basketState() string {
	b := e.dp.Engine.Basket()
	var sb strings.Builder
	for _, l := range b.Lines {
		fmt.Fprintf(&sb, "%s:%v:%d;", l.LineKey, l.Qty, l.LineDiscount.Minor())
	}
	fmt.Fprintf(&sb, "sale:%d", e.dp.Engine.SaleDiscount().Minor())
	return sb.String()
}

func (e gateEnv) noRows(t *testing.T) {
	t.Helper()
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log`); n != 0 {
		t.Fatalf("audit rows = %d, want 0", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM shrinkage_events`); n != 0 {
		t.Fatalf("shrinkage rows = %d, want 0", n)
	}
}

// refused: the prompt came back again carrying the approval-changed line.
func refused(rec *httptest.ResponseRecorder) bool {
	return isElevationPrompt(rec) && strings.Contains(rec.Body.String(), "match the request any more")
}

// promptRetry posts form as user over HTMX, asserts a prompt and returns its
// hidden fields with the manager PIN set.
func promptRetry(t *testing.T, e gateEnv, path string, form url.Values, user *auth.User) url.Values {
	t.Helper()
	rec := postFormHtmx(e.mux, path, form, user)
	if !isElevationPrompt(rec) {
		t.Fatalf("want prompt, got %d: %s", rec.Code, rec.Body.String())
	}
	h := promptHiddenFields(t, rec.Body.String())
	h.Set("override_pin", gatePIN)
	return h
}

func (e gateEnv) post(path string, f url.Values) *httptest.ResponseRecorder {
	return postFormHtmx(e.mux, path, f, e.cashier)
}

type bindGate struct {
	name  string
	path  string
	setup func(t *testing.T, e gateEnv) url.Values // seeds, returns the first post
}

func bindGates() []bindGate {
	return []bindGate{
		{"G1 line reduce", "/api/pos/line", func(t *testing.T, e gateEnv) url.Values {
			return url.Values{"key": {e.seedLine(t, 3)}, "qty": {"1"}}
		}},
		{"G2 line discount", "/api/pos/line", func(t *testing.T, e gateEnv) url.Values {
			return url.Values{"key": {e.seedLine(t, 2)}, "qty": {"2"}, "discount": {"10"}}
		}},
		{"G3 line reduce+raise", "/api/pos/line", func(t *testing.T, e gateEnv) url.Values {
			return url.Values{"key": {e.seedLine(t, 3)}, "qty": {"2"}, "discount": {"10"}}
		}},
		{"G4 sale discount", "/api/pos/discount", func(t *testing.T, e gateEnv) url.Values {
			e.seedLine(t, 2)
			return url.Values{"discount": {"5"}}
		}},
		{"G5 remove", "/api/pos/remove", func(t *testing.T, e gateEnv) url.Values {
			return url.Values{"key": {e.seedLine(t, 2)}, "reason": {"void"}}
		}},
	}
}

// AC1: prompt, then the same values with a valid PIN applies, once, audited.
func TestElevationBind7085_UnchangedRetryApplies(t *testing.T) {
	for _, g := range bindGates() {
		t.Run(g.name, func(t *testing.T) {
			e := bindEnv(t)
			form := g.setup(t, e)
			before := e.basketState()
			retry := promptRetry(t, e, g.path, form, e.cashier)
			if retry.Get("approval") == "" {
				t.Fatalf("prompt carries no approval field: %v", retry)
			}
			rec := e.post(g.path, retry)
			if rec.Code != http.StatusOK || isElevationPrompt(rec) {
				t.Fatalf("approved retry: %d prompt=%v: %s", rec.Code, isElevationPrompt(rec), rec.Body.String())
			}
			if e.basketState() == before {
				t.Fatalf("approved retry changed nothing")
			}
			want := 1
			if strings.HasPrefix(g.name, "G3") {
				want = 2 // a reduction row and a discount row
			}
			if n := e.count(t, `SELECT COUNT(*) FROM audit_log`); n != want {
				t.Fatalf("audit rows = %d, want %d", n, want)
			}
		})
	}
}

// AC2: editing any approved to value (or the target) between prompt and
// retry is refused, applies nothing and writes nothing.
func TestElevationBind7085_EditedToValueRefused(t *testing.T) {
	cases := []struct {
		name   string
		gate   int
		tamper func(t *testing.T, e gateEnv, h url.Values)
	}{
		{"G1 qty", 0, func(t *testing.T, e gateEnv, h url.Values) { h.Set("qty", "0") }},
		{"G1 reason", 0, func(t *testing.T, e gateEnv, h url.Values) { h.Set("reason", "waste") }},
		{"G1 discount", 0, func(t *testing.T, e gateEnv, h url.Values) { h.Set("discount", "50") }},
		{"G2 discount", 1, func(t *testing.T, e gateEnv, h url.Values) { h.Set("discount", "90") }},
		{"G3 qty", 2, func(t *testing.T, e gateEnv, h url.Values) { h.Set("qty", "1") }},
		{"G4 discount", 3, func(t *testing.T, e gateEnv, h url.Values) { h.Set("discount", "50") }},
		{"G5 reason", 4, func(t *testing.T, e gateEnv, h url.Values) { h.Set("reason", "waste") }},
		{"G5 key to other priced line", 4, func(t *testing.T, e gateEnv, h url.Values) {
			if _, err := e.dp.Engine.ScanQty("PLAIN", 1); err != nil {
				t.Fatal(err)
			}
			h.Set("key", e.dp.Engine.Basket().Lines[1].LineKey)
		}},
	}
	gates := bindGates()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := bindEnv(t)
			g := gates[c.gate]
			retry := promptRetry(t, e, g.path, g.setup(t, e), e.cashier)
			c.tamper(t, e, retry)
			before := e.basketState()
			rec := e.post(g.path, retry)
			if !refused(rec) {
				t.Fatalf("edited retry not refused: %d %s", rec.Code, rec.Body.String())
			}
			if e.basketState() != before {
				t.Fatalf("basket changed: %s -> %s", before, e.basketState())
			}
			e.noRows(t)
		})
	}
}

// AC3: a successful retry replayed after the from value matches again.
func TestElevationBind7085_ReplayRefused_SaleDiscount(t *testing.T) {
	e := bindEnv(t)
	e.seedLine(t, 2)
	retry := promptRetry(t, e, "/api/pos/discount", url.Values{"discount": {"5"}}, e.cashier)
	if rec := e.post("/api/pos/discount", retry); rec.Code != http.StatusOK || isElevationPrompt(rec) {
		t.Fatalf("first retry: %d", rec.Code)
	}
	if rec := e.post("/api/pos/discount", url.Values{"discount": {"0"}}); isElevationPrompt(rec) {
		t.Fatalf("lowering is free, got a prompt")
	}
	before := e.basketState()
	auditBefore := e.count(t, `SELECT COUNT(*) FROM audit_log`)
	rec := e.post("/api/pos/discount", retry)
	if !refused(rec) {
		t.Fatalf("replay not refused: %d %s", rec.Code, rec.Body.String())
	}
	if e.basketState() != before {
		t.Fatalf("replay applied")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log`); n != auditBefore {
		t.Fatalf("replay wrote audit rows: %d -> %d", auditBefore, n)
	}
}

func TestElevationBind7085_ReplayRefused_LineReduce(t *testing.T) {
	e := bindEnv(t)
	key := e.seedLine(t, 3)
	retry := promptRetry(t, e, "/api/pos/line", url.Values{"key": {key}, "qty": {"1"}}, e.cashier)
	if rec := e.post("/api/pos/line", retry); rec.Code != http.StatusOK || isElevationPrompt(rec) {
		t.Fatalf("first retry: %d", rec.Code)
	}
	if err := e.dp.Engine.UpdateLineByKey(key, 3, 0); err != nil {
		t.Fatal(err)
	}
	rec := e.post("/api/pos/line", retry)
	if !refused(rec) {
		t.Fatalf("replay not refused: %d %s", rec.Code, rec.Body.String())
	}
	if q := e.dp.Engine.Basket().Lines[0].Qty; q != 3 {
		t.Fatalf("qty = %v, want 3", q)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM shrinkage_events`); n != 1 {
		t.Fatalf("shrinkage rows = %d, want 1", n)
	}
}

// AC4: missing or forged approval is refused BEFORE the PIN lookup: wrong PINs
// behind it must not tick the shared lockout (5 would lock the device), and
// nothing is audited.
func TestElevationBind7085_MissingOrForgedApprovalRefusedBeforePIN(t *testing.T) {
	e := bindEnv(t)
	e.seedLine(t, 2)
	good := promptRetry(t, e, "/api/pos/discount", url.Values{"discount": {"5"}}, e.cashier)
	for i := 0; i < 6; i++ {
		bad := url.Values{}
		for k, v := range good {
			bad[k] = v
		}
		bad.Set("override_pin", "000000")
		switch i % 3 {
		case 0:
			bad.Del("approval")
		case 1:
			bad.Set("approval", "garbage")
		default:
			bad.Set("approval", good.Get("approval")+"x")
		}
		if rec := e.post("/api/pos/discount", bad); !refused(rec) {
			t.Fatalf("attempt %d not refused: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	// Missing approval with the CORRECT PIN: refused too.
	noTok := url.Values{"discount": {"5"}, "from_discount": {"0"}, "override_pin": {gatePIN}}
	if rec := e.post("/api/pos/discount", noTok); !refused(rec) {
		t.Fatalf("no approval + valid PIN not refused: %d", rec.Code)
	}
	e.noRows(t)
	// The lockout counter is untouched: a fresh prompt + right PIN still works.
	fresh := promptRetry(t, e, "/api/pos/discount", url.Values{"discount": {"5"}}, e.cashier)
	rec := e.post("/api/pos/discount", fresh)
	if rec.Code != http.StatusOK || isElevationPrompt(rec) {
		t.Fatalf("valid retry after refusals: %d %s", rec.Code, rec.Body.String())
	}
}

// AC5: an approval is for the session user it was issued to.
func TestElevationBind7085_OtherSessionUserRefused(t *testing.T) {
	e := bindEnv(t)
	e.seedLine(t, 2)
	retry := promptRetry(t, e, "/api/pos/discount", url.Values{"discount": {"5"}}, e.cashier)
	other := &auth.User{ID: "cashier-b-7085", Role: "cashier"}
	rec := postFormHtmx(e.mux, "/api/pos/discount", retry, other)
	if !refused(rec) {
		t.Fatalf("other user not refused: %d %s", rec.Code, rec.Body.String())
	}
	if e.basketState() != fmt.Sprintf("%s:2:0;sale:0", e.dp.Engine.Basket().Lines[0].LineKey) {
		t.Fatalf("discount applied: %s", e.basketState())
	}
}

// AC6: a restart (store lost) re-prompts, never applies.
func TestElevationBind7085_RestartInvalidatesApproval(t *testing.T) {
	e := bindEnv(t)
	e.seedLine(t, 2)
	retry := promptRetry(t, e, "/api/pos/discount", url.Values{"discount": {"5"}}, e.cashier)
	elevationApprovals.reset()
	if rec := e.post("/api/pos/discount", retry); !refused(rec) {
		t.Fatalf("not refused after restart: %d", rec.Code)
	}
	e.noRows(t)
}

// AC7: a wrong PIN keeps the approval usable; the right one applies once.
func TestElevationBind7085_WrongPINThenRightPIN(t *testing.T) {
	e := bindEnv(t)
	e.seedLine(t, 2)
	prompt := e.post("/api/pos/discount", url.Values{"discount": {"5"}})
	tok := promptHiddenFields(t, prompt.Body.String()).Get("approval")
	wrong := approve(t, e, "/api/pos/discount", prompt, "000000")
	if !isElevationPrompt(wrong) || refused(wrong) {
		t.Fatalf("wrong PIN should re-prompt with the PIN error: %d %s", wrong.Code, wrong.Body.String())
	}
	if got := promptHiddenFields(t, wrong.Body.String()).Get("approval"); got != tok {
		t.Fatalf("approval changed after a wrong PIN: %q -> %q", tok, got)
	}
	rec := approve(t, e, "/api/pos/discount", prompt, gatePIN)
	if rec.Code != http.StatusOK || isElevationPrompt(rec) {
		t.Fatalf("right PIN: %d %s", rec.Code, rec.Body.String())
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log`); n != 1 {
		t.Fatalf("audit rows = %d, want 1", n)
	}
}

// AC8: two prompts outstanding; approvals are not interchangeable.
func TestElevationBind7085_TwoPromptsNotInterchangeable(t *testing.T) {
	e := bindEnv(t)
	k1 := e.seedLine(t, 2)
	if _, err := e.dp.Engine.ScanQty("PLAIN", 3); err != nil {
		t.Fatal(err)
	}
	k2 := e.dp.Engine.Basket().Lines[1].LineKey
	p1 := promptRetry(t, e, "/api/pos/line", url.Values{"key": {k1}, "qty": {"2"}, "discount": {"10"}}, e.cashier)
	p2 := promptRetry(t, e, "/api/pos/line", url.Values{"key": {k2}, "qty": {"1"}}, e.cashier)
	swapped := url.Values{}
	for k, v := range p1 {
		swapped[k] = v
	}
	swapped.Set("approval", p2.Get("approval"))
	if rec := e.post("/api/pos/line", swapped); !refused(rec) {
		t.Fatalf("swapped approval not refused: %d", rec.Code)
	}
	if rec := e.post("/api/pos/line", p1); rec.Code != http.StatusOK || isElevationPrompt(rec) {
		t.Fatalf("p1: %d", rec.Code)
	}
	if rec := e.post("/api/pos/line", p2); rec.Code != http.StatusOK || isElevationPrompt(rec) {
		t.Fatalf("p2: %d", rec.Code)
	}
	b := e.dp.Engine.Basket()
	if b.Lines[0].LineDiscount.Minor() != 10 || b.Lines[0].Qty != 2 || b.Lines[1].Qty != 1 {
		t.Fatalf("each prompt must apply only its own change: %s", e.basketState())
	}
}

func TestElevationBind7085_ExpiredApprovalRefused(t *testing.T) {
	e := bindEnv(t)
	e.seedLine(t, 2)
	retry := promptRetry(t, e, "/api/pos/discount", url.Values{"discount": {"5"}}, e.cashier)
	elevationApprovals.mu.Lock()
	elevationApprovals.now = func() time.Time { return time.Now().Add(approvalTTL + time.Minute) }
	elevationApprovals.mu.Unlock()
	t.Cleanup(func() {
		elevationApprovals.mu.Lock()
		elevationApprovals.now = time.Now
		elevationApprovals.mu.Unlock()
	})
	if rec := e.post("/api/pos/discount", retry); !refused(rec) {
		t.Fatalf("expired approval not refused: %d", rec.Code)
	}
	e.noRows(t)
}

func TestElevationBind7085_StoreBoundedAndSingleUse(t *testing.T) {
	s := &approvalStore{m: map[string]approvalEntry{}, now: time.Now}
	for i := 0; i < approvalCap+44; i++ {
		if s.mint(fmt.Sprintf("b%d", i)) == "" {
			t.Fatal("mint failed")
		}
	}
	if len(s.m) > approvalCap {
		t.Fatalf("store holds %d, cap %d", len(s.m), approvalCap)
	}
	tok := s.mint("x")
	var wg sync.WaitGroup
	wins := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); wins <- s.consume(tok, "x") }()
	}
	wg.Wait()
	close(wins)
	n := 0
	for w := range wins {
		if w {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("consume winners = %d, want exactly 1", n)
	}
}
