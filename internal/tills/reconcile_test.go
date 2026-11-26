package tills

import (
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
)

var testNow = time.Date(2030, 1, 10, 12, 0, 0, 0, time.UTC) // fixed-date:ok pure function of its argument, no real-clock read

const stale = 24 * time.Hour

func rowByID(t *testing.T, rows []Row, id string) Row {
	t.Helper()
	for _, r := range rows {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no row with id %q in %+v", id, rows)
	return Row{}
}

func TestMerge_StateTable(t *testing.T) {
	self := SelfInfo{ID: "self", Name: "Counter", Role: RoleMain}
	recent := testNow.Add(-time.Minute).Format(time.RFC3339)
	cases := []struct {
		name      string
		src       Source
		id        string
		wantState State
	}{
		{"in both is ok",
			Source{CloudOK: true, Self: self,
				Cloud: []enroll.Device{{DeviceID: "dev-t2", TillID: "t2", Name: "Bar"}},
				Local: []data.TillRow{{ID: "t2", Name: "Bar", Role: data.TillRoleAdditional, LastSeenAt: recent}}},
			"t2", StateOK},
		{"local only while cloud ok is stale",
			Source{CloudOK: true, Self: self,
				Local: []data.TillRow{{ID: "t3", Name: "Old tablet", Role: data.TillRoleAdditional}}},
			"t3", StateLocalOnly},
		{"local only while cloud unavailable is never stale",
			Source{CloudOK: false, Self: self,
				Local: []data.TillRow{{ID: "t3", Name: "Old tablet", Role: data.TillRoleAdditional}}},
			"t3", StateOK},
		{"cloud only",
			Source{CloudOK: true, Self: self,
				Cloud: []enroll.Device{{DeviceID: "dev-t4", TillID: "t4", Name: "Garden"}}},
			"t4", StateCloudOnly},
		{"role mismatch when cloud role known",
			Source{CloudOK: true, Self: self,
				Cloud: []enroll.Device{{DeviceID: "dev-t5", TillID: "t5", Name: "Bar", Role: "main"}},
				Local: []data.TillRow{{ID: "t5", Name: "Bar", Role: data.TillRoleAdditional}}},
			"t5", StateRoleMismatch},
		{"unknown cloud role never mismatches",
			Source{CloudOK: true, Self: self,
				Cloud: []enroll.Device{{DeviceID: "dev-t5", TillID: "t5", Name: "Bar"}},
				Local: []data.TillRow{{ID: "t5", Name: "Bar", Role: data.TillRoleAdditional}}},
			"t5", StateOK},
		{"replaced old main",
			Source{CloudOK: true, Self: self, Replaced: &enroll.Replaced{By: "New main", At: "x"}},
			"self", StateReplaced},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := rowByID(t, Merge(c.src, testNow, stale), c.id)
			if got.State != c.wantState {
				t.Fatalf("state of %q = %q, want %q", c.id, got.State, c.wantState)
			}
		})
	}
}

func TestMerge_SelfIsNeverStaleAndIsFlagged(t *testing.T) {
	rows := Merge(Source{CloudOK: true, Self: SelfInfo{ID: "self", Name: "Counter", Role: RoleMain}}, testNow, stale)
	got := rowByID(t, rows, "self")
	if !got.IsSelf || got.State != StateOK {
		t.Fatalf("self row = %+v, want IsSelf and state ok even though the account list is empty", got)
	}
}

func TestMerge_SameNameDifferentIDIsNeverMerged(t *testing.T) {
	rows := Merge(Source{CloudOK: true, Self: SelfInfo{ID: "self", Name: "Counter", Role: RoleMain},
		Cloud: []enroll.Device{{DeviceID: "dev-cloud-1", TillID: "cloud-1", Name: "Bar"}},
		Local: []data.TillRow{{ID: "local-1", Name: "bar", Role: data.TillRoleAdditional}}}, testNow, stale)
	if len(rows) != 3 {
		t.Fatalf("got %d rows (%+v), want 3 (self + one local-only + one cloud-only): same name must not merge", len(rows), rows)
	}
	if !rowByID(t, rows, "local-1").SameNameOtherID || !rowByID(t, rows, "cloud-1").SameNameOtherID {
		t.Fatal("both rows with the same name and different ids must carry SameNameOtherID")
	}
}

func TestMerge_OrderIsSelfMainThenName(t *testing.T) {
	rows := Merge(Source{CloudOK: false, Self: SelfInfo{ID: "self", Name: "Zed", Role: RoleAdditional},
		Local: []data.TillRow{
			{ID: "b", Name: "beta", Role: data.TillRoleAdditional},
			{ID: "m", Name: "Zulu main", Role: RoleMain},
			{ID: "a", Name: "Alpha", Role: data.TillRoleSatellite},
		}}, testNow, stale)
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	want := []string{"self", "m", "a", "b"}
	if len(ids) != len(want) {
		t.Fatalf("order = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("order = %v, want %v", ids, want)
		}
	}
}

func TestMerge_StaleRowOffersRemoveAndKnowsAge(t *testing.T) {
	old := testNow.Add(-3 * 24 * time.Hour).Format(time.RFC3339)
	rows := Merge(Source{CloudOK: true, Self: SelfInfo{ID: "self", Name: "C", Role: RoleMain},
		Local: []data.TillRow{{ID: "t3", Name: "Old", Role: data.TillRoleAdditional, LastSeenAt: old}}}, testNow, stale)
	got := rowByID(t, rows, "t3")
	if !got.LongUnseen {
		t.Fatalf("a till last seen 3 days ago with stale-after 24h must be LongUnseen: %+v", got)
	}
	if len(got.Actions) != 1 || got.Actions[0] != ActionRemove {
		t.Fatalf("actions = %v, want [remove]", got.Actions)
	}
}

func TestMerge_ReplacedStateOnSelfOnly(t *testing.T) {
	rows := Merge(Source{CloudOK: true, Self: SelfInfo{ID: "self", Name: "C", Role: RoleMain},
		Replaced: &enroll.Replaced{By: "New", At: "t"},
		Local:    []data.TillRow{{ID: "t2", Name: "Bar", Role: data.TillRoleAdditional}},
		Cloud:    []enroll.Device{{DeviceID: "dev-t2", TillID: "t2", Name: "Bar"}}}, testNow, stale)
	if got := rowByID(t, rows, "self"); got.State != StateReplaced || got.ReplacedBy != "New" {
		t.Fatalf("self = %+v, want replaced by New", got)
	}
	if got := rowByID(t, rows, "t2"); got.State != StateOK {
		t.Fatalf("t2 = %+v, want ok", got)
	}
}

func TestMerge_BoundedAndEmpty(t *testing.T) {
	if rows := Merge(Source{}, testNow, stale); len(rows) != 0 {
		t.Fatalf("empty source (no self id) gave %d rows, want 0", len(rows))
	}
	var cloud []enroll.Device
	for i := 0; i < MaxRows+50; i++ {
		cloud = append(cloud, enroll.Device{TillID: string(rune('a'+i%26)) + string(rune('A'+i/26%26)) + string(rune('0'+i/676)), Name: "n"})
	}
	if rows := Merge(Source{CloudOK: true, Cloud: cloud}, testNow, stale); len(rows) > MaxRows {
		t.Fatalf("got %d rows, want at most %d", len(rows), MaxRows)
	}
}

// The account's device id is the till's marketplace identity ("till-<uuid>"),
// never the local till id; the join is on till_id. Roles use the account's
// words (primary/replica). Regression for the review of ut-docs#7057: joining
// on the device id marked every till stale and the bulk remove deleted them.
func TestMerge_JoinsOnTillIDNotDeviceID_AccountRoleWords(t *testing.T) {
	rows := Merge(Source{CloudOK: true,
		Self:  SelfInfo{ID: "main-1", Name: "Main", Role: RoleMain},
		Local: []data.TillRow{{ID: "t2", Name: "Bar", Role: data.TillRoleAdditional}},
		Cloud: []enroll.Device{
			{DeviceID: "till-aaa", TillID: "main-1", Name: "Main", Role: "primary"},
			{DeviceID: "till-bbb", TillID: "t2", Name: "Bar", Role: "replica"},
		}}, testNow, stale)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (no cloud-only strangers): %+v", len(rows), rows)
	}
	for _, r := range rows {
		if r.State != StateOK || !r.InCloud || !r.InLocal {
			t.Errorf("row %s: state %s inCloud=%v inLocal=%v, want ok in both", r.ID, r.State, r.InCloud, r.InLocal)
		}
	}
}

// A cloud row that never reported a till_id cannot be joined, so nothing is
// proven absent: no local till is marked stale while one exists.
func TestMerge_CloudRowWithoutTillIDNeverMakesAStaleMark(t *testing.T) {
	rows := Merge(Source{CloudOK: true,
		Self:  SelfInfo{ID: "main-1", Name: "Main", Role: RoleMain},
		Local: []data.TillRow{{ID: "t2", Name: "Bar", Role: data.TillRoleAdditional}},
		Cloud: []enroll.Device{{DeviceID: "till-old", Name: "Bar"}}}, testNow, stale)
	for _, r := range rows {
		if r.State == StateLocalOnly || r.State == StateCloudOnly {
			t.Errorf("row %s marked %s from an ambiguous list", r.ID, r.State)
		}
	}
}
