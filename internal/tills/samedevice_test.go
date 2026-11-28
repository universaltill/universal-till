package tills

import (
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
)

var now0 = time.Date(2026, 11, 27, 18, 0, 0, 0, time.UTC)

func ago(d time.Duration) string { return now0.Add(-d).Format(time.RFC3339) }

// The owner's case: phone-till2 (live) and two ghosts. Only a shared device id
// (or the register link's superseded device) proves "the same device"; names
// never do.
func TestMarkSameDevice(t *testing.T) {
	src := Source{
		CloudOK: true,
		Cloud: []enroll.Device{
			{DeviceID: "dev-mac", TillID: "mac", Name: "mac-till"},
			{DeviceID: "dev-C", TillID: "p2", Name: "phone-till2"},
		},
		Self: SelfInfo{ID: "mac", Name: "mac-till", Role: RoleMain},
		Local: []data.TillRow{
			{ID: "p2", Name: "phone-till2", Role: "additional", LastSeenAt: ago(time.Hour), DeviceID: "dev-C"},
			{ID: "g1", Name: "phone till", Role: "additional", LastSeenAt: ago(40 * 24 * time.Hour), DeviceID: "dev-C"},
			{ID: "g2", Name: "iphone-till", Role: "additional", LastSeenAt: ago(30 * 24 * time.Hour), DeviceID: "dev-B"},
			{ID: "g3", Name: "kitchen", Role: "additional", LastSeenAt: ago(40 * 24 * time.Hour), DeviceID: "dev-K"},
		},
	}
	rows := Merge(src, now0, 24*time.Hour)
	// dev-C is held by two rows (live p2 and ghost g1): impossible through the
	// repo (unique claim) but MarkSameDevice must still pick the live one. dev-B
	// is the device the live register link says dev-C replaced.
	MarkSameDevice(rows, map[string]string{"dev-C": "dev-B"})
	by := map[string]Row{}
	for _, r := range rows {
		by[r.ID] = r
	}
	if by["g1"].SameDeviceAs != "phone-till2" {
		t.Fatalf("g1 SameDeviceAs = %q, want phone-till2 (shared device id)", by["g1"].SameDeviceAs)
	}
	if by["g2"].SameDeviceAs != "phone-till2" {
		t.Fatalf("g2 SameDeviceAs = %q, want phone-till2 (superseded device)", by["g2"].SameDeviceAs)
	}
	if by["g3"].SameDeviceAs != "" || by["p2"].SameDeviceAs != "" || by["mac"].SameDeviceAs != "" {
		t.Fatalf("only the ghosts that share a device are marked: %+v", by)
	}
}

func TestMarkSameDeviceNeverGuessesByName(t *testing.T) {
	src := Source{CloudOK: true, Cloud: []enroll.Device{{DeviceID: "d1", TillID: "a", Name: "phone"}},
		Self: SelfInfo{ID: "mac", Name: "mac", Role: RoleMain},
		Local: []data.TillRow{
			{ID: "a", Name: "phone", Role: "additional", LastSeenAt: ago(time.Hour)},
			{ID: "b", Name: "phone", Role: "additional", LastSeenAt: ago(40 * 24 * time.Hour)},
		}}
	rows := Merge(src, now0, 24*time.Hour)
	MarkSameDevice(rows, nil)
	for _, r := range rows {
		if r.SameDeviceAs != "" {
			t.Fatalf("%s marked by name only", r.ID)
		}
	}
}

// Only a live cloud-only row gets the cloud remove action, and only when the
// till has the capability; a recent local-only row never gets Remove.
func TestMergeActions(t *testing.T) {
	cloud := []enroll.Device{{DeviceID: "dev-x", TillID: "x", Name: "Phone X", Role: "replica"}}
	src := Source{CloudOK: true, Cloud: cloud, Self: SelfInfo{ID: "mac", Role: RoleMain},
		Local: []data.TillRow{{ID: "new", Name: "New", Role: "additional", EnrolledAt: ago(time.Minute)}}}
	rows := Merge(src, now0, 24*time.Hour)
	for _, r := range rows {
		if r.ID == "x" && (len(r.Actions) != 0 || r.CloudDeviceID != "dev-x") {
			t.Fatalf("cloud-only row with the capability off: %+v", r)
		}
		if r.ID == "new" && len(r.Actions) != 0 {
			t.Fatalf("a just-added till must not offer Remove: %+v", r)
		}
	}
	src.CloudRemoveOK = true
	for _, r := range Merge(src, now0, 24*time.Hour) {
		if r.ID == "x" && (len(r.Actions) != 1 || r.Actions[0] != ActionRemoveCloud) {
			t.Fatalf("cloud-only row with the capability on: %+v", r)
		}
	}
}
