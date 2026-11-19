package devicepay

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type fakeRemote struct {
	ref    string
	req    Request
	calls  atomic.Int32
	answer Result
}

func (f *fakeRemote) CollectRemote(_ context.Context, ref string, req Request) Result {
	f.calls.Add(1)
	f.ref, f.req = ref, req
	return f.answer
}

type countingBridge struct{ collects atomic.Int32 }

func (b *countingBridge) Capabilities() string { return "{}" }
func (b *countingBridge) Collect(string) string {
	b.collects.Add(1)
	return `{"status":"declined"}`
}
func (b *countingBridge) Cancel(string)               {}
func (b *countingBridge) Setup(string) string         { return `{"ok":true}` }
func (b *countingBridge) ShowEducation(string) string { return `{"ok":true}` }

func goodReq() Request {
	return Request{Provider: ProviderStripe, AmountMinor: 500, Currency: "gbp", IdempotencyKey: "att_1:1:1"}
}

func TestRemoteCollectRoutesByReaderInContext(t *testing.T) {
	rem := &fakeRemote{answer: Result{Status: StatusApproved, ProviderTxnID: "pi_1"}}
	SetRemote(rem)
	t.Cleanup(func() { SetRemote(nil); SetBridge(nil) })
	nb := &countingBridge{}
	SetBridge(nb)
	ctx, cancel := context.WithTimeout(WithReader(context.Background(), "rd_abc"), time.Minute)
	defer cancel()
	res := Collect(ctx, goodReq())
	if res.Status != StatusApproved || res.ProviderTxnID != "pi_1" {
		t.Fatalf("result = %+v, want the remote collector's", res)
	}
	if rem.ref != "rd_abc" || rem.req.IdempotencyKey != "att_1:1:1" || rem.req.DeadlineUnixMs == 0 {
		t.Fatalf("remote saw ref=%q req=%+v (deadline must be filled from ctx)", rem.ref, rem.req)
	}
	if nb.collects.Load() != 0 {
		t.Fatal("the native bridge was used for a phone reader tender")
	}
}

func TestWithoutAReaderTheRemoteIsNeverUsed(t *testing.T) {
	rem := &fakeRemote{}
	SetRemote(rem)
	t.Cleanup(func() { SetRemote(nil); SetBridge(nil) })
	nb := &countingBridge{}
	SetBridge(nb)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	Collect(ctx, goodReq())
	if rem.calls.Load() != 0 || nb.collects.Load() != 1 {
		t.Fatalf("remote calls=%d native collects=%d, want 0 and 1", rem.calls.Load(), nb.collects.Load())
	}
}

func TestRemoteCollectWithNoRemoteIsNotStarted(t *testing.T) {
	SetRemote(nil)
	nb := &countingBridge{}
	SetBridge(nb)
	t.Cleanup(func() { SetBridge(nil) })
	ctx, cancel := context.WithTimeout(WithReader(context.Background(), "rd_abc"), time.Minute)
	defer cancel()
	res := Collect(ctx, goodReq())
	if res.Status != StatusDeclined || res.Reason != ReasonReaderUnreachable {
		t.Fatalf("result = %+v, want declined/reader_unreachable (nothing was started)", res)
	}
	if nb.collects.Load() != 0 {
		t.Fatal("a phone reader tender fell back to the native bridge")
	}
}

func TestRemoteCollectStillNeedsAValidRequestAndDeadline(t *testing.T) {
	rem := &fakeRemote{answer: Result{Status: StatusApproved, ProviderTxnID: "pi_1"}}
	SetRemote(rem)
	t.Cleanup(func() { SetRemote(nil) })
	if res := Collect(WithReader(context.Background(), "rd_abc"), goodReq()); res.Reason != ReasonBadRequest {
		t.Fatalf("no deadline: %+v", res)
	}
	bad := goodReq()
	bad.AmountMinor = 0
	ctx, cancel := context.WithTimeout(WithReader(context.Background(), "rd_abc"), time.Minute)
	defer cancel()
	if res := Collect(ctx, bad); res.Reason != ReasonBadRequest {
		t.Fatalf("zero amount: %+v", res)
	}
	if rem.calls.Load() != 0 {
		t.Fatal("an invalid request reached the remote collector")
	}
}
