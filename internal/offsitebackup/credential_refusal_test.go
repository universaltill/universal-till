package offsitebackup

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/logging"
)

// ut-docs#7058: a dead device credential gets its own reason and stops the
// rounds like a refusal, instead of "other" and a 6-hourly retry.

func TestCredentialRefusalReasons(t *testing.T) {
	cases := []struct {
		name         string
		status       int
		code, reason string
		want         string
	}{
		{"unauthorized", 401, "unauthorized", "", ReasonPairAgain},
		{"token_retired", 401, "token_retired", "", ReasonPairAgain},
		{"empty code", 401, "", "", ReasonPairAgain},
		{"replaced", 401, "device_revoked", "replaced", ReasonTillReplaced},
		{"moved", 401, "device_revoked", "moved", ReasonTillMoved},
		{"removed", 401, "device_revoked", "removed", ReasonTillRemoved},
		{"revoked no reason", 401, "device_revoked", "", ReasonTillRemoved},
		{"device_mismatch", 403, "device_mismatch", "", ReasonPairAgain},
		{"service_unavailable", 403, "service_unavailable", "", ReasonServiceUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newTill(t)
			e.snapshot()
			if c.reason != "" {
				e.cloud.revokedReason = c.reason
			} else if c.code == "device_revoked" {
				e.cloud.revokedNoReason = true
			} else {
				e.cloud.authStatus, e.cloud.authCode = c.status, c.code
			}
			e.set.Set(context.Background(), data.OffsiteAttemptsKey, "2")
			e.round(false)
			if got := e.get(data.OffsiteRefusedReasonKey); got != c.want {
				t.Fatalf("refused_reason = %q, want %q", got, c.want)
			}
			next, _ := time.Parse(time.RFC3339, e.get(data.OffsiteNextAttemptAtKey))
			if next.Sub(e.now) != 24*time.Hour {
				t.Fatalf("next attempt in %s, want 24h", next.Sub(e.now))
			}
			if e.get(data.OffsiteLastErrorKey) != "" {
				t.Fatalf("last_error = %q, a credential refusal is not a failure", e.get(data.OffsiteLastErrorKey))
			}
			if e.get(data.OffsiteAttemptsKey) != "2" {
				t.Fatalf("attempts = %q, want unchanged", e.get(data.OffsiteAttemptsKey))
			}
			if e.get(data.OffsiteFailingSinceKey) == "" {
				t.Fatal("failing_since not set")
			}
			if e.get(data.OffsiteLastErrorAtKey) == "" {
				t.Fatal("last_error_at (the refusal time) not set")
			}
		})
	}
}

type approvalCloud struct{ Cloud }

func (approvalCloud) GetKey(context.Context) (KeyState, error) {
	return KeyState{}, &APIError{Status: 403, Code: CodeOwnerApprovalRequired}
}

func TestOwnerApprovalIsAFailureWithBackoff(t *testing.T) {
	e := newTill(t)
	e.snapshot()
	e.up.Cloud = approvalCloud{e.up.Cloud}
	e.round(false)
	if got := e.get(data.OffsiteLastErrorKey); got != ReasonOwnerApproval {
		t.Fatalf("last_error = %q, want %q", got, ReasonOwnerApproval)
	}
	if e.get(data.OffsiteRefusedReasonKey) != "" {
		t.Fatal("owner approval must not be a refusal")
	}
	next, _ := time.Parse(time.RFC3339, e.get(data.OffsiteNextAttemptAtKey))
	if next.Sub(e.now) != 15*time.Minute {
		t.Fatalf("first backoff = %s, want 15m", next.Sub(e.now))
	}
}

func TestCredentialRefusalWaitsUntilACheckinSucceeds(t *testing.T) {
	e := newTill(t)
	e.snapshot()
	e.cloud.authStatus, e.cloud.authCode = 401, "unauthorized"
	e.round(false)
	calls := e.cloud.authCalls
	if calls == 0 {
		t.Fatal("the first round never reached the cloud")
	}
	for _, d := range []time.Duration{time.Hour, 6 * time.Hour, 16 * time.Hour} { // +1h, +7h, +23h
		e.now = e.now.Add(d)
		e.round(false)
		if e.cloud.authCalls != calls {
			t.Fatalf("at %s the till called the cloud again with a dead credential", e.now)
		}
	}
	// A check-in older than the refusal does not clear it.
	ctx := context.Background()
	refusedAt := e.get(data.OffsiteLastErrorAtKey)
	old, _ := time.Parse(time.RFC3339, refusedAt)
	e.set.Set(ctx, enroll.KeyLastCheckinAt, old.Add(-time.Minute).Format(time.RFC3339))
	e.round(false)
	if e.cloud.authCalls != calls || e.get(data.OffsiteRefusedReasonKey) == "" {
		t.Fatal("a check-in from before the refusal cleared the wait")
	}
	// A check-in after the refusal does; the next round uploads.
	e.set.Set(ctx, enroll.KeyLastCheckinAt, e.now.Format(time.RFC3339))
	e.cloud.authStatus = 0
	e.round(false)
	if e.cloud.authCalls == calls {
		t.Fatal("the round did not call the cloud after a successful check-in")
	}
	if e.get(data.OffsiteRefusedReasonKey) != "" {
		t.Fatalf("refused_reason = %q after a successful upload", e.get(data.OffsiteRefusedReasonKey))
	}
	if e.get(data.OffsiteLastSuccessAtKey) == "" {
		t.Fatal("no upload happened after the credential worked again")
	}
}

func TestCredentialRefusalProblemNamesReasonAndDays(t *testing.T) {
	e := newTill(t)
	e.snapshot()
	e.set.Set(context.Background(), data.OffsiteFailingSinceKey, e.now.Add(-163*time.Hour).Format(time.RFC3339))
	e.cloud.authStatus, e.cloud.authCode = 401, "unauthorized"
	e.round(false)
	var text string
	for _, p := range logging.OpenProblems(time.Now(), 0) {
		if p.Key == ProblemOffsiteFailed {
			text = p.Msg
		}
	}
	t.Cleanup(func() { logging.ResolveProblems(ProblemOffsiteFailed) })
	if !strings.Contains(text, "pair_again") || !strings.Contains(text, "6d 19h") || strings.Contains(text, "163h") {
		t.Fatalf("problem text = %q", text)
	}
}

func TestFailingFor(t *testing.T) {
	for d, want := range map[time.Duration]string{
		163 * time.Hour:               "6d 19h",
		47*time.Hour + 59*time.Minute: "1d 23h",
		5 * time.Hour:                 "0d 5h",
		48 * time.Hour:                "2d 0h",
	} {
		if got := failingFor(d); got != want {
			t.Errorf("failingFor(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestCredentialRefusalLogHasReasonAndStatusNotToken(t *testing.T) {
	var buf bytes.Buffer
	restore := logging.CaptureForTest(&buf)
	defer restore()
	e := newTill(t)
	e.snapshot()
	e.cloud.authStatus, e.cloud.authCode = 401, "token_retired"
	e.round(false)
	log := buf.String()
	if !strings.Contains(log, "pair_again") || !strings.Contains(log, "401") {
		t.Fatalf("log lacks the reason or status:\n%s", log)
	}
	for _, secret := range []string{"SECRET-TOKEN", e.cloud.token, "token_retired"} {
		if strings.Contains(log, secret) {
			t.Fatalf("log contains %q:\n%s", secret, log)
		}
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "t" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

var _ net.Error = timeoutErr{}

func TestClassifyKeepsOtherReasons(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&APIError{Status: 500}, ReasonServer},
		{&APIError{Status: 502}, ReasonServer},
		{&APIError{Status: 429}, ReasonRateLimited},
		{&APIError{Status: 413}, ReasonTooLarge},
		{&APIError{Status: 503, Code: "not_enabled"}, ReasonNotEnabled},
		{timeoutErr{}, ReasonNetwork},
		{&APIError{Status: 400}, ReasonOther},
		{&APIError{Status: 404}, ReasonOther},
		{&APIError{Status: 422}, ReasonOther},
		{&APIError{Status: 403, Code: CodeOwnerApprovalRequired}, ReasonOwnerApproval},
		{errors.New("x"), ReasonOther},
	}
	for _, c := range cases {
		if got := classify(c.err); got != c.want {
			t.Errorf("classify(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

func TestReplacedTillGateIsNotMain(t *testing.T) {
	e := newTill(t)
	e.snapshot()
	ctx := context.Background()
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{EndpointURL: "https://cloud.example", StoreID: "st_1", MerchantToken: "x"}}
	e.up.Gate = func(ctx context.Context, now time.Time) GateState { return CheckGate(ctx, cfg, e.set, now) }
	// A paid main till: the gate is open until the till is marked replaced,
	// so the replaced check below is what closes it (review L1).
	e.set.Set(ctx, entitlement.KeyPlan, "shop")
	e.set.Set(ctx, entitlement.KeySubscriptionStatus, entitlement.StatusActive)
	e.set.Set(ctx, entitlement.KeyLastConfirmedAt, e.now.UTC().Format(time.RFC3339))
	if got := CheckGate(ctx, cfg, e.set, e.now); got != GateOpen {
		t.Fatalf("an unreplaced paid main till's gate = %v, want GateOpen", got)
	}
	e.set.Set(ctx, "till_identity.replaced", "1")
	if got := CheckGate(ctx, cfg, e.set, e.now); got != GateNotMain {
		t.Fatalf("a replaced till's gate = %v, want GateNotMain", got)
	}
	e.round(true)
	if e.cloud.authCalls != 0 {
		t.Fatal("a replaced till called the cloud")
	}
}
