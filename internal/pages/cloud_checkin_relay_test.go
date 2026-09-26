package pages

import (
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#2893 (ADR-0117 §4): the main till's cloud link relays "check in
// with the cloud now" over the ADR-0114 LAN link; a linked replica kicks its
// own cloudsync loop (d.CloudSyncNow), single-flight.
func TestCloudCheckinRelay_ReachesALinkedReplicasCheckInLoop(t *testing.T) {
	f := newSyncLinkFixture(t)
	tillID := f.enrol(t, "Till 2", "token-abc")
	replica := linkReplica(t, f.srv.URL, tillID, fastLinkClientOptions())
	replica.CloudSyncNow = make(chan struct{}, 1)
	pulls := runReplica(t, replica, time.Hour, time.Hour)
	if !waitFor(t, 3*time.Second, func() bool {
		p := f.dp.Link.Peer(tillID)
		if p == nil || !replica.LinkClient.Linked() || pulls.Load() < 1 {
			return false
		}
		_, ok := p.Hello()
		return ok
	}) {
		t.Fatal("never linked")
	}
	select {
	case <-replica.CloudSyncNow:
		t.Fatal("the replica was kicked before any relay")
	default:
	}

	relayCloudCheckinToReplicas(f.dp)([]string{"entitlement"}, 3)
	select {
	case <-replica.CloudSyncNow:
	case <-time.After(2 * time.Second):
		t.Fatal("the relay never kicked the replica's cloud check-in")
	}
}

// Not linked (no hub, or no replicas on it): the relay is a no-op.
func TestCloudCheckinRelay_NoLinkIsANoOp(t *testing.T) {
	relayCloudCheckinToReplicas(&common.Deps{})([]string{"update"}, 1)
	f := newSyncLinkFixture(t)
	relayCloudCheckinToReplicas(f.dp)([]string{"update"}, 1)
}

// The check-in loop tells the cloud link when each check-in starts (so a
// nudge is relayed after the check-in it caused) — nil-safe when no cloud
// link was built.
func TestCloudLinkHooks_WireBeforeAndAfterTick(t *testing.T) {
	d := &common.Deps{CloudSyncNow: make(chan struct{}, 1)}
	hooks := buildCloudHooks(d, nil)
	wireCloudLinkHooks(d, &hooks)
	if hooks.Kick == nil || hooks.BeforeTick == nil || hooks.AfterTick == nil {
		t.Fatalf("hooks not wired: kick=%v before=%v after=%v", hooks.Kick != nil, hooks.BeforeTick != nil, hooks.AfterTick != nil)
	}
	hooks.BeforeTick()
	hooks.AfterTick(t.Context(), true, nil)
}
