package channelactions

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// ADR-0215 (ut-docs#6581): the wire form of the delivery actions. The vendored
// channel-order-action-v1.json predates them; the additions are listed in
// till-a-notes.md for the contract owner (reject {reason}, prep_minutes).

func TestToWire_DeliveryActions(t *testing.T) {
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC) // fixed-date:ok no real-clock check reads it
	row := func(a data.ChannelOrderAction) string {
		a.ActionID, a.ConnectionID, a.ExternalOrderID, a.BaseRevision, a.CreatedAt, a.StaffID = "a1", "c1", "o1", 3, now.Format(time.RFC3339), "u1"
		b, err := json.Marshal(ToWire(a, now))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := row(data.ChannelOrderAction{Action: "reject", RejectReason: "busy"}); !strings.Contains(got, `"action":"reject"`) || !strings.Contains(got, `"reject":{"reason":"busy"}`) {
		t.Fatalf("reject = %s", got)
	}
	if got := row(data.ChannelOrderAction{Action: "accept", PrepMinutes: 20}); !strings.Contains(got, `"prep_minutes":20`) {
		t.Fatalf("accept = %s", got)
	}
	if got := row(data.ChannelOrderAction{Action: "accept"}); strings.Contains(got, "prep_minutes") || strings.Contains(got, "reject") {
		t.Fatalf("plain accept must carry neither: %s", got)
	}
	for _, a := range []string{"preparing", "ready", "collected"} {
		if got := row(data.ChannelOrderAction{Action: a}); !strings.Contains(got, `"action":"`+a+`"`) || strings.Contains(got, "shipment") || strings.Contains(got, "cancel") {
			t.Fatalf("%s = %s", a, got)
		}
	}
	if got := row(data.ChannelOrderAction{Action: "prep_time", PrepMinutes: 30}); !strings.Contains(got, `"action":"prep_time"`) || !strings.Contains(got, `"prep_minutes":30`) {
		t.Fatalf("prep_time = %s", got)
	}
	// An existing webshop action is byte-identical (no new keys).
	if got := row(data.ChannelOrderAction{Action: "pick"}); strings.Contains(got, "reject") || strings.Contains(got, "prep") {
		t.Fatalf("pick = %s", got)
	}
}
