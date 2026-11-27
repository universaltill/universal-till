//go:build !wasip1

package plugin

import (
	"errors"
	"strings"
	"testing"
)

// ut-docs#3697: the plugin-records SDK against FakeHost, which carries the
// host's validation so plugin unit tests exercise the real contract.

func recordsHost(t *testing.T, replica bool) *FakeHost {
	t.Helper()
	h := NewFakeHost()
	h.RecordCollections = []string{"entries"}
	h.SnapshotCollections = []string{"staff", "config"}
	h.Replica = replica
	UseFakeHost(t, h)
	return h
}

func TestRecordsForwardQueuesOnAReplica(t *testing.T) {
	h := recordsHost(t, true)
	queued, err := RecordsForward("entries", "id-1", map[string]any{"a": 1})
	if err != nil || !queued {
		t.Fatalf("queued=%v err=%v, want true", queued, err)
	}
	if len(h.Outbox) != 1 || h.Outbox[0].Collection != "entries" || h.Outbox[0].ID != "id-1" || string(h.Outbox[0].Payload) != `{"a":1}` {
		t.Fatalf("outbox = %+v", h.Outbox)
	}
	if queued, _ = RecordsForward("entries", "id-1", map[string]any{"a": 1}); !queued || len(h.Outbox) != 1 {
		t.Fatalf("a repeat must be queued=true with no second row, rows %d", len(h.Outbox))
	}
}

func TestRecordsForwardOnMainIsNotAnError(t *testing.T) {
	h := recordsHost(t, false)
	queued, err := RecordsForward("entries", "id-1", map[string]any{"a": 1})
	if err != nil || queued || len(h.Outbox) != 0 {
		t.Fatalf("queued=%v err=%v rows=%d, want false/nil/0", queued, err, len(h.Outbox))
	}
}

func TestRecordsForwardValidation(t *testing.T) {
	recordsHost(t, true)
	big := map[string]string{"x": strings.Repeat("y", 64<<10)}
	for name, c := range map[string]struct {
		col, id string
		p       any
		want    *Error
	}{
		"undeclared collection": {"other", "a", map[string]int{}, ErrDenied},
		"bad collection":        {"Entries", "a", map[string]int{}, ErrInvalid},
		"bad id":                {"entries", "a b", map[string]int{}, ErrInvalid},
		"payload not object":    {"entries", "a", []int{1}, ErrInvalid},
		"payload over 64 KiB":   {"entries", "a", big, ErrInvalid},
	} {
		if _, err := RecordsForward(c.col, c.id, c.p); !errors.Is(err, c.want) {
			t.Errorf("%s: err=%v, want %v", name, err, c.want)
		}
	}
}

func TestRecordsForwardDeniedWithoutThePermission(t *testing.T) {
	h := recordsHost(t, true)
	h.Deny["records_forward"] = true
	if _, err := RecordsForward("entries", "a", map[string]int{}); !errors.Is(err, ErrDenied) {
		t.Fatalf("err=%v, want ErrDenied", err)
	}
}

func TestRecordsCallCapPerEvent(t *testing.T) {
	recordsHost(t, false)
	for i := 0; i < 64; i++ {
		if _, err := RecordsRole(); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if _, err := RecordsRole(); !errors.Is(err, ErrQuota) {
		t.Fatalf("65th records_*/snapshot_* call: err=%v, want ErrQuota", err)
	}
}

func TestRecordsRole(t *testing.T) {
	h := recordsHost(t, false)
	if r, err := RecordsRole(); err != nil || r != "main" {
		t.Fatalf("role=%q err=%v, want main", r, err)
	}
	h.Replica = true
	if r, _ := RecordsRole(); r != "replica" {
		t.Fatalf("role=%q, want replica", r)
	}
}

func TestRecordsStatus(t *testing.T) {
	h := recordsHost(t, true)
	_, _ = RecordsForward("entries", "a", map[string]int{})
	_, _ = RecordsForward("entries", "b", map[string]int{})
	_, _ = RecordsForward("entries", "c", map[string]int{})
	h.Outbox[1].Status, h.Outbox[1].Reason = "rejected", "invalid"
	st, err := RecordsStatus("entries")
	if err != nil || len(st) != 3 {
		t.Fatalf("status=%+v err=%v", st, err)
	}
	// Same order as the host: rejected first, then newest first.
	if st[0] != (RecordStatus{ID: "b", Status: "rejected", Reason: "invalid"}) ||
		st[1] != (RecordStatus{ID: "c", Status: "pending"}) || st[2] != (RecordStatus{ID: "a", Status: "pending"}) {
		t.Fatalf("status rows = %+v", st)
	}
	if _, err := RecordsStatus("other"); !errors.Is(err, ErrDenied) {
		t.Fatalf("undeclared collection: err=%v, want ErrDenied", err)
	}
}

func TestSnapshotMainSetsAnyoneGets(t *testing.T) {
	h := recordsHost(t, false)
	stored, err := SnapshotSet("staff", []map[string]string{{"id": "1"}})
	if err != nil || !stored {
		t.Fatalf("stored=%v err=%v", stored, err)
	}
	var out []map[string]string
	found, err := SnapshotGet("staff", &out)
	if err != nil || !found || len(out) != 1 || out[0]["id"] != "1" {
		t.Fatalf("found=%v err=%v out=%v", found, err, out)
	}
	if found, err = SnapshotGet("config", &out); err != nil || found {
		t.Fatalf("unset snapshot: found=%v err=%v, want false/nil", found, err)
	}
	// a replica ignores a set but still reads
	h.Replica = true
	if stored, err = SnapshotSet("staff", []int{1}); err != nil || stored {
		t.Fatalf("replica set: stored=%v err=%v, want false/nil", stored, err)
	}
	if found, _ = SnapshotGet("staff", &out); !found {
		t.Fatal("a replica reads what the pull stored")
	}
}

func TestSnapshotValidation(t *testing.T) {
	recordsHost(t, false)
	if _, err := SnapshotSet("other", map[string]int{}); !errors.Is(err, ErrDenied) {
		t.Fatalf("undeclared: err=%v", err)
	}
	if _, err := SnapshotSet("staff", map[string]string{"x": strings.Repeat("y", 64<<10)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized: err=%v, want ErrInvalid", err)
	}
	var out any
	if _, err := SnapshotGet("other", &out); !errors.Is(err, ErrDenied) {
		t.Fatalf("get undeclared: err=%v", err)
	}
}
