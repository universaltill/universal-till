package enroll

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
)

// ut-docs#6618: "Stop sending" is shop-wide (ADR-0200 decision 6). Every path
// that tells the cloud about an automatically registered till honours it.

// A stopped main till does not register a newly joined till in the cloud.
func TestVouchForReplicaRefusedWhileStoppedAndAutoRegistered(t *testing.T) {
	resetState()
	cloud := newFakeCloud(t)
	cfg := mainTillCfg(cloud.srv.URL)
	kv := newFakeKV()
	_ = kv.Set(context.Background(), keyDeviceRegistered, mainDeviceID)
	initForTest(t, cfg, kv)
	_ = kv.Set(context.Background(), KeyAutoRegistered, "true")
	_ = SetStopped(context.Background(), kv, true)
	_, err := VouchForReplica(context.Background(), cfg, kv, ReplicaRequest{TillID: "till-row-2", DeviceID: "till-replica", DeviceName: "Back office", Version: "0.23.0"})
	if !errors.Is(err, ErrRegistrationStopped) {
		t.Fatalf("err = %v, want ErrRegistrationStopped", err)
	}
	if got := cloud.lastReq(); got != nil {
		t.Fatalf("a stopped main till called the cloud: %v", got)
	}
}

// An operator-claimed shop (not auto-registered) uses the cloud on purpose:
// its replicas still register (cloud features keep working, per the notice).
func TestVouchForReplicaStillWorksStoppedWhenOperatorClaimed(t *testing.T) {
	resetState()
	cloud := newFakeCloud(t)
	cfg := mainTillCfg(cloud.srv.URL)
	kv := newFakeKV()
	_ = kv.Set(context.Background(), keyDeviceRegistered, mainDeviceID)
	initForTest(t, cfg, kv)
	_ = SetStopped(context.Background(), kv, true)
	if _, err := VouchForReplica(context.Background(), cfg, kv, ReplicaRequest{TillID: "till-row-2", DeviceID: "till-replica", DeviceName: "Back office", Version: "0.23.0"}); err != nil {
		t.Fatalf("vouch: %v", err)
	}
}

// The once-per-start approval-key POST is not made while stopped, and is made
// again once the shop resumes.
func TestApprovalKeyKickHonoursStop(t *testing.T) {
	identify(t)
	c := newReportCloud(t, 200)
	kv := newFakeKV()
	ctx := context.Background()
	_ = kv.Set(ctx, KeyAutoRegistered, "true")
	_ = SetStopped(ctx, kv, true)
	var wg sync.WaitGroup
	kickApprovalKeyReportUnlessStopped(ctx, kv, marketplace(c), &wg)
	wg.Wait()
	if n := c.hits.Load(); n != 0 {
		t.Fatalf("stopped till POSTed its approval key (%d)", n)
	}
	_ = SetStopped(ctx, kv, false)
	kickApprovalKeyReportUnlessStopped(ctx, kv, marketplace(c), &wg)
	wg.Wait()
	if n := c.hits.Load(); n != 1 {
		t.Fatalf("resumed till POSTs = %d, want 1", n)
	}
}

// A version change made while stopped is not reported after the switch is
// turned back on: the running version is remembered even when nothing may be
// sent, so the first allowed start sees no change.
func TestVersionChangeDuringStopIsNotReportedAfterResume(t *testing.T) {
	var got []string
	run := func(kv *fakeKV, version string) {
		setReleaseVersion(t, version)
		ReportVersionChange(t.Context(), &config.Config{}, kv, func(_ context.Context, from, to string) { got = append(got, from+">"+to) })
	}
	kv := readyKV(t)
	run(kv, "0.36.0")
	_ = SetStopped(t.Context(), kv, true)
	run(kv, "0.37.0")
	run(kv, "0.38.0")
	if len(got) != 0 {
		t.Fatalf("events while stopped: %v", got)
	}
	_ = SetStopped(t.Context(), kv, false)
	run(kv, "0.38.0")
	if len(got) != 0 {
		t.Fatalf("a change made while stopped was reported after resume: %v", got)
	}
	run(kv, "0.39.0")
	if len(got) != 1 || got[0] != "0.38.0>0.39.0" {
		t.Fatalf("a real change after resume: %v", got)
	}
}
