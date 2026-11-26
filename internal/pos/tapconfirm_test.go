package pos

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/paybroker"
)

// ut-docs#6455 (ADR-0198 §3.10): core never trusts a plugin's "approved". It
// asks the broker for the PaymentIntent's real state and compares amount,
// currency and key to the attempt row it wrote before the tap.

type fakeAttempts struct {
	row     data.PaymentAttempt
	have    bool
	marks   []string
	markErr error
	upd     data.AttemptUpdate
}

func (f *fakeAttempts) Get(context.Context, string) (data.PaymentAttempt, bool, error) {
	return f.row, f.have, nil
}
func (f *fakeAttempts) Mark(_ context.Context, _ string, state string, u data.AttemptUpdate) error {
	f.marks = append(f.marks, state)
	f.upd = u
	return f.markErr
}

func openAttempt() *fakeAttempts {
	return &fakeAttempts{have: true, row: data.PaymentAttempt{Key: "att-1", Provider: "stripe", AmountMinor: 1250, Currency: "gbp", State: data.AttemptPending}}
}

func broker(t *testing.T, status int, reply string) (*[]string, func()) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, r.URL.Path+" "+string(b))
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	paybroker.SetTargetResolver(func(context.Context) (paybroker.Target, bool) {
		return paybroker.Target{BaseURL: srv.URL, Bearer: "cred"}, true
	})
	return &seen, func() { srv.Close(); paybroker.SetTargetResolver(nil) }
}

func wantPending(t *testing.T, err error, reason string) {
	t.Helper()
	var pe *PaymentPendingError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want a *PaymentPendingError(%s)", err, reason)
	}
	if pe.Reason != reason {
		t.Fatalf("reason = %q, want %q", pe.Reason, reason)
	}
}

func TestConfirmTap_Table(t *testing.T) {
	const ok = `"status":"succeeded","amount_minor":1250,"amount_received_minor":1250,"currency":"gbp"`
	cases := []struct {
		name, reply string
		httpStatus  int
		reason      string // "" = approved
	}{
		{"succeeded and equal", `{"payment_intent_id":"pi_1",` + ok + `,"card":{"brand":"visa","last4":"4242"}}`, 200, ""},
		{"currency case ignored", `{"payment_intent_id":"pi_1","status":"succeeded","amount_received_minor":1250,"currency":"GBP"}`, 200, ""},
		{"amount short", `{"payment_intent_id":"pi_1","status":"succeeded","amount_received_minor":1249,"currency":"gbp"}`, 200, PendingAmountMismatch},
		{"amount over", `{"payment_intent_id":"pi_1","status":"succeeded","amount_received_minor":1251,"currency":"gbp"}`, 200, PendingAmountMismatch},
		{"currency differs", `{"payment_intent_id":"pi_1","status":"succeeded","amount_received_minor":1250,"currency":"eur"}`, 200, PendingAmountMismatch},
		{"no amount reported", `{"payment_intent_id":"pi_1","status":"succeeded","currency":"gbp"}`, 200, PendingAmountMismatch},
		{"key echo differs", `{"payment_intent_id":"pi_1","idempotency_key":"other",` + ok + `}`, 200, PendingNotConfirmed},
		{"key echo equal", `{"payment_intent_id":"pi_1","idempotency_key":"att-1",` + ok + `}`, 200, ""},
		{"processing", `{"status":"processing","amount_received_minor":0,"currency":"gbp"}`, 200, PendingProcessing},
		{"unknown from the cloud", `{"status":"unknown","retry_after_s":30}`, 200, PendingProcessing},
		{"requires_capture", `{"status":"requires_capture","amount_received_minor":0,"currency":"gbp"}`, 200, PendingRequiresCapture},
		{"not paid", `{"status":"requires_payment_method","amount_received_minor":0,"currency":"gbp"}`, 200, PendingNotConfirmed},
		{"canceled", `{"status":"canceled"}`, 200, PendingNotConfirmed},
		{"empty status", `{}`, 200, PendingNotConfirmed},
		{"cloud 503", `{"error":{"code":"provider_unavailable"}}`, 503, PendingLookupFailed},
		{"cloud 404", `{"error":{"code":"payment_not_found"}}`, 404, PendingLookupFailed},
		{"garbage", `<html>`, 200, PendingLookupFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, stop := broker(t, c.httpStatus, c.reply)
			defer stop()
			f := openAttempt()
			got, err := ConfirmTap(context.Background(), f, "att-1")
			if c.reason == "" {
				if err != nil {
					t.Fatalf("err = %v, want approval", err)
				}
				if len(f.marks) != 1 || f.marks[0] != data.AttemptApproved {
					t.Fatalf("marks = %v, want [approved]", f.marks)
				}
				if got.PIID != "pi_1" {
					t.Fatalf("PIID = %q", got.PIID)
				}
				return
			}
			wantPending(t, err, c.reason)
			if len(f.marks) != 1 || f.marks[0] != data.AttemptUnknown || f.upd.Reason != c.reason {
				t.Fatalf("marks = %v reason %q, want [unknown] with the reason", f.marks, f.upd.Reason)
			}
		})
	}
}

// PI, brand and last4 come from the broker's answer only.
func TestConfirmTap_RecordsPIAndCardFromTheBrokerAnswerOnly(t *testing.T) {
	_, stop := broker(t, 200, `{"payment_intent_id":"pi_real","status":"succeeded","amount_received_minor":1250,"currency":"gbp","card":{"brand":"mastercard","last4":"0005"}}`)
	defer stop()
	f := openAttempt()
	f.row.PIID, f.row.CardBrand, f.row.Last4 = "", "plugin-said-amex", "9999"
	got, err := ConfirmTap(context.Background(), f, "att-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.PIID != "pi_real" || got.CardBrand != "mastercard" || got.Last4 != "0005" {
		t.Fatalf("confirmation = %+v", got)
	}
	if f.upd.PIID != "pi_real" || f.upd.CardBrand != "mastercard" || f.upd.Last4 != "0005" {
		t.Fatalf("row update = %+v", f.upd)
	}
}

// Anything in the card block that is not a plain brand and four digits is
// dropped, never stored (ADR-0127).
func TestConfirmTap_RefusesToStoreAnythingButBrandAndLast4(t *testing.T) {
	_, stop := broker(t, 200, `{"payment_intent_id":"pi_1","status":"succeeded","amount_received_minor":1250,"currency":"gbp","card":{"brand":"visa","last4":"4242424242424242"}}`)
	defer stop()
	f := openAttempt()
	got, err := ConfirmTap(context.Background(), f, "att-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Last4 != "" || f.upd.Last4 != "" {
		t.Fatalf("a long digit string was kept as last4: %+v", got)
	}
}

// The request is built from the attempt row, never from the caller, and names
// EXACTLY ONE identifier (the cloud refuses both): the PaymentIntent once the
// row has it, else the key. It goes out as core (session_status).
func TestConfirmTap_AsksByTheRowsKeyAndPI(t *testing.T) {
	seen, stop := broker(t, 200, `{"payment_intent_id":"pi_1","status":"succeeded","amount_received_minor":1250,"currency":"gbp"}`)
	defer stop()
	f := openAttempt()
	f.row.PIID = "pi_1"
	if _, err := ConfirmTap(context.Background(), f, "att-1"); err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 1 || !strings.HasPrefix((*seen)[0], "/v1/payments/stripe/session_status ") ||
		strings.Contains((*seen)[0], `idempotency_key`) || !strings.Contains((*seen)[0], `"payment_intent_id":"pi_1"`) {
		t.Fatalf("broker request = %v", *seen)
	}
	// A different PI in the answer than the one on the row is not this payment.
	_, stop2 := broker(t, 200, `{"payment_intent_id":"pi_OTHER","status":"succeeded","amount_received_minor":1250,"currency":"gbp"}`)
	defer stop2()
	f2 := openAttempt()
	f2.row.PIID = "pi_1"
	_, err := ConfirmTap(context.Background(), f2, "att-1")
	wantPending(t, err, PendingNotConfirmed)
}

func TestConfirmTap_NoRowIsNotAnApproval(t *testing.T) {
	seen, stop := broker(t, 200, `{"status":"succeeded","amount_received_minor":1250,"currency":"gbp"}`)
	defer stop()
	f := &fakeAttempts{}
	_, err := ConfirmTap(context.Background(), f, "att-1")
	wantPending(t, err, PendingNotConfirmed)
	if len(*seen) != 0 || len(f.marks) != 0 {
		t.Fatalf("with no attempt row the broker was asked %v / marked %v", *seen, f.marks)
	}
}

func TestConfirmTap_ContextCancelledIsPendingNotDeclined(t *testing.T) {
	_, stop := broker(t, 200, `{"status":"succeeded","amount_received_minor":1250,"currency":"gbp"}`)
	defer stop()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := openAttempt()
	_, err := ConfirmTap(ctx, f, "att-1")
	wantPending(t, err, PendingContextCancelled)
}

// A settled attempt refuses a second approve: the replay is pending, and the
// broker is not even asked.
func TestConfirmTap_SettledAttemptRefusesASecondApprove(t *testing.T) {
	seen, stop := broker(t, 200, `{"payment_intent_id":"pi_1","status":"succeeded","amount_received_minor":1250,"currency":"gbp"}`)
	defer stop()
	f := openAttempt()
	f.row.State = data.AttemptApproved
	_, err := ConfirmTap(context.Background(), f, "att-1")
	wantPending(t, err, PendingAlreadySettled)
	if len(*seen) != 0 || len(f.marks) != 0 {
		t.Fatalf("settled attempt: asked %v marked %v", *seen, f.marks)
	}
	// And if the row settled between the ask and the mark, still no approval.
	f2 := openAttempt()
	f2.markErr = data.ErrAttemptSettled
	_, err = ConfirmTap(context.Background(), f2, "att-1")
	wantPending(t, err, PendingAlreadySettled)
}

// The plugin's own answer is advisory: ApprovedByPluginOnly has no effect.
func TestConfirmTap_BrokerNotEnrolledIsPending(t *testing.T) {
	paybroker.SetTargetResolver(nil)
	f := openAttempt()
	_, err := ConfirmTap(context.Background(), f, "att-1")
	wantPending(t, err, PendingLookupFailed)
}

func TestBrokerRef_ExactlyOneIdentifier(t *testing.T) {
	seen, stop := broker(t, 200, `{"payment_intent_id":"pi_1","status":"requires_payment_method","currency":"gbp"}`)
	defer stop()
	f := openAttempt()
	if _, err := TapBrokerStatus(context.Background(), f.row); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains((*seen)[0], `"idempotency_key":"att-1"`) || strings.Contains((*seen)[0], "payment_intent_id") {
		t.Fatalf("no PI known: %v, want the key only", *seen)
	}
	f.row.PIID = "pi_1"
	if err := TapBrokerCancel(context.Background(), f.row); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains((*seen)[1], `"payment_intent_id":"pi_1"`) || strings.Contains((*seen)[1], "idempotency_key") {
		t.Fatalf("PI known: %v, want the PI only", *seen)
	}
}
