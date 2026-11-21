package pages

import (
	"testing"

	"github.com/coder/websocket"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fleetlink"
)

// ut-docs#6796: a card_reader request denied between the link handler's check
// and the link registering leaves a `denied` tombstone (not a deleted row).
// The registration recheck must answer PairingDenied for it at once, not leave
// the link open as though the row were still pending.
func TestPendingLink_RecheckSeesDeniedReaderTombstone(t *testing.T) {
	t.Run("scripted_denied_row", func(t *testing.T) {
		f := newPendingLinkFixture(t, fixtureOpts{})
		id := f.newRequest(t, pendingTestSecret)
		f.counted.script = func(n int32, row data.PendingPairingRow, ok bool, err error) (data.PendingPairingRow, bool, error) {
			if n == 2 {
				row.Status = "denied"
				return row, true, nil
			}
			return row, ok, err
		}
		c, _, err := f.dial(t, id, pendingTestSecret)
		if err != nil {
			t.Fatal(err)
		}
		if st, code := readPairing(t, c); st != fleetlink.PairingDenied || code != websocket.StatusNormalClosure {
			t.Fatalf("got %q close %d, want denied + 1000", st, code)
		}
	})

	// The same race through the real repository: a card_reader row is denied
	// for real (Deny writes the tombstone) just before the recheck reads it.
	t.Run("real_reader_tombstone", func(t *testing.T) {
		f := newPendingLinkFixture(t, fixtureOpts{})
		id, err := f.repo.CreatePendingRequestWithRole(t.Context(), "Phone", commitmentOf(pendingTestSecret), data.TillRoleCardReader, "", pairingRequestTTL)
		if err != nil {
			t.Fatal(err)
		}
		f.counted.script = func(n int32, row data.PendingPairingRow, ok bool, err error) (data.PendingPairingRow, bool, error) {
			if n != 2 {
				return row, ok, err
			}
			if derr := f.repo.Deny(t.Context(), id); derr != nil {
				t.Errorf("deny: %v", derr)
			}
			cur, found, gerr := f.repo.GetByID(t.Context(), id)
			if !found || cur.Status != "denied" {
				t.Errorf("tombstone not readable by GetByID: found=%v status=%q err=%v", found, cur.Status, gerr)
			}
			return cur, found, gerr
		}
		c, _, err := f.dial(t, id, pendingTestSecret)
		if err != nil {
			t.Fatal(err)
		}
		if st, code := readPairing(t, c); st != fleetlink.PairingDenied || code != websocket.StatusNormalClosure {
			t.Fatalf("got %q close %d, want denied + 1000", st, code)
		}
	})
}
