package lantls

import (
	"context"
	"testing"
	"time"
)

// ut-docs#5198: the main till asks a replica to rotate its sync bearer with
// a response header; the peer transport turns that into one background call
// to the rotation hook, never on the request's own path.

// rotateHook counts calls and parks each one until released.
type rotateHook struct {
	calls   chan struct{}
	release chan struct{}
}

func installRotateHook(t *testing.T) *rotateHook {
	t.Helper()
	h := &rotateHook{calls: make(chan struct{}, 16), release: make(chan struct{})}
	t.Cleanup(SetBearerRotate(func(context.Context) {
		h.calls <- struct{}{}
		<-h.release
	}))
	prev := bearerRotateRetryEvery
	t.Cleanup(func() { bearerRotateRetryEvery = prev })
	return h
}

func (h *rotateHook) waitCall(t *testing.T) {
	t.Helper()
	select {
	case <-h.calls:
	case <-time.After(3 * time.Second):
		t.Fatal("the rotation hook was not called")
	}
}

func (h *rotateHook) expectNoCall(t *testing.T, why string) {
	t.Helper()
	select {
	case <-h.calls:
		t.Fatalf("the rotation hook was called: %s", why)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestPeerTransport_RotateHeaderFiresHookAsyncOnce(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		name := "plain"
		if pinned {
			name = "pinned"
		}
		t.Run(name, func(t *testing.T) {
			main := newPeerTill(t)
			main.rotateHdr.Store(true)
			pin := ""
			if pinned {
				pin = main.cert.Pin()
			}
			usePeer(t, main.base, pin)
			h := installRotateHook(t)
			bearerRotateRetryEvery = time.Hour

			// The hook blocks, yet the request returns at once and a second
			// one is not held up behind it.
			done := make(chan error, 2)
			go func() { _, err := peerGet(t, main.base+"/a"); done <- err }()
			h.waitCall(t)
			go func() { _, err := peerGet(t, main.base+"/b"); done <- err }()
			for i := 0; i < 2; i++ {
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("a request waited for the rotation hook")
				}
			}
			h.expectNoCall(t, "a rotation is already in flight")

			// Once it finishes, the retry interval still holds the next one off.
			close(h.release)
			time.Sleep(50 * time.Millisecond)
			if _, err := peerGet(t, main.base+"/c"); err != nil {
				t.Fatal(err)
			}
			h.expectNoCall(t, "inside the retry interval")

			bearerRotateRetryEvery = 0
			if _, err := peerGet(t, main.base+"/d"); err != nil {
				t.Fatal(err)
			}
			h.waitCall(t)
		})
	}
}

func TestPeerTransport_RotateHeaderIgnoredForOtherHostsAndTheRotatePath(t *testing.T) {
	main := newPeerTill(t)
	main.rotateHdr.Store(true)
	other := newPeerTill(t)
	other.rotateHdr.Store(true)
	usePeer(t, main.base, "")
	h := installRotateHook(t)
	bearerRotateRetryEvery = 0

	if _, err := peerGet(t, other.base+"/"); err != nil {
		t.Fatal(err)
	}
	h.expectNoCall(t, "the header came from a host that is not the main till")
	if _, err := peerGet(t, main.base+BearerRotatePath); err != nil {
		t.Fatal(err)
	}
	h.expectNoCall(t, "the header came on the rotate call itself")

	// No header, no call.
	main.rotateHdr.Store(false)
	if _, err := peerGet(t, main.base+"/"); err != nil {
		t.Fatal(err)
	}
	h.expectNoCall(t, "the answer carried no header")
}
