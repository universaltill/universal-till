package entitlement

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// ADR-0060 amendment (ut-docs#3532): the block may carry `claimed`, whether
// the store has an owner. Cached only when it is a JSON boolean; anything
// else writes nothing (the last value stands) and never rejects the block.

const claimedBase = `"plan":"local","subscription_status":"none"`

func decodeBlock(t *testing.T, body string) Block {
	t.Helper()
	var b Block
	if err := json.Unmarshal([]byte(body), &b); err != nil {
		t.Fatalf("unmarshal rejected the block %s: %v", body, err)
	}
	return b
}

func TestBlockValuesClaimed(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name, body, want string
		present          bool
	}{
		{"true", `{` + claimedBase + `,"claimed":true}`, "true", true},
		{"false", `{` + claimedBase + `,"claimed":false}`, "false", true},
		{"absent", `{` + claimedBase + `}`, "", false},
		{"null", `{` + claimedBase + `,"claimed":null}`, "", false},
		{"string", `{` + claimedBase + `,"claimed":"true"}`, "", false},
		{"number", `{` + claimedBase + `,"claimed":1}`, "", false},
		{"object", `{` + claimedBase + `,"claimed":{}}`, "", false},
	} {
		kv, err := decodeBlock(t, tc.body).Values(now)
		if err != nil {
			t.Fatalf("%s: Values: %v", tc.name, err)
		}
		got, ok := kv[KeyStoreClaimed]
		if ok != tc.present || got != tc.want {
			t.Errorf("%s: claimed = %q (present=%v), want %q (present=%v)", tc.name, got, ok, tc.want, tc.present)
		}
		for _, k := range []string{KeyPlan, KeySubscriptionStatus, KeyLastConfirmedAt, KeyCatalogMaster} {
			if _, ok := kv[k]; !ok {
				t.Errorf("%s: key %s missing from the map", tc.name, k)
			}
		}
	}
}

func TestBlockClaimedDoesNotBypassPlanValidation(t *testing.T) {
	b := decodeBlock(t, `{"plan":"gold","subscription_status":"none","claimed":true}`)
	if _, err := b.Values(time.Now()); err == nil {
		t.Fatal("an unknown plan with claimed=true was accepted")
	}
}

func TestStoreClaimedReader(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name        string
		r           Reader
		claimed, ok bool
	}{
		{"true", mapReader{KeyStoreClaimed: "true"}, true, true},
		{"false", mapReader{KeyStoreClaimed: "false"}, false, true},
		{"no row", mapReader{}, false, false},
		{"garbage", mapReader{KeyStoreClaimed: "yes"}, false, false},
		{"upper case", mapReader{KeyStoreClaimed: "TRUE"}, false, false},
		{"empty", mapReader{KeyStoreClaimed: ""}, false, false},
		{"read error", errReader{}, false, false},
	} {
		c, known := StoreClaimed(ctx, tc.r)
		if c != tc.claimed || known != tc.ok {
			t.Errorf("%s: StoreClaimed = (%v, %v), want (%v, %v)", tc.name, c, known, tc.claimed, tc.ok)
		}
	}
}

func TestReadCachedRelaysClaimed(t *testing.T) {
	ctx := context.Background()
	confirmed := time.Now().UTC().Format(time.RFC3339)
	base := func() mapReader {
		return mapReader{KeyPlan: "local", KeySubscriptionStatus: "none", KeyExpiresAt: "", KeyLastConfirmedAt: confirmed}
	}
	for _, v := range []string{"true", "false"} {
		r := base()
		r[KeyStoreClaimed] = v
		c, ok := ReadCached(ctx, r)
		if !ok || c.Claimed == nil || strconv_(*c.Claimed) != v {
			t.Fatalf("ReadCached(claimed=%s) = %+v ok=%v", v, c, ok)
		}
		wire, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		var back Cached
		if err := json.Unmarshal(wire, &back); err != nil {
			t.Fatal(err)
		}
		vals, err := back.RelayValues()
		if err != nil {
			t.Fatal(err)
		}
		if vals[KeyStoreClaimed] != v {
			t.Errorf("round trip claimed = %q, want %q", vals[KeyStoreClaimed], v)
		}
	}
	// Never told: nil, omitted on the wire, no relayed key.
	c, ok := ReadCached(ctx, base())
	if !ok || c.Claimed != nil {
		t.Fatalf("ReadCached without a row = %+v ok=%v, want Claimed nil", c, ok)
	}
	wire, _ := json.Marshal(c)
	if string(wire) != "" && containsClaimed(wire) {
		t.Errorf("nil Claimed leaked onto the wire: %s", wire)
	}
	vals, err := c.RelayValues()
	if err != nil {
		t.Fatal(err)
	}
	if _, present := vals[KeyStoreClaimed]; present {
		t.Error("RelayValues wrote claimed for a nil value")
	}
	// Garbage row reads as never told, and a read error elsewhere is unchanged.
	r := base()
	r[KeyStoreClaimed] = "maybe"
	if c, _ := ReadCached(ctx, r); c.Claimed != nil {
		t.Error("garbage row relayed as a value")
	}
}

func strconv_(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func containsClaimed(b []byte) bool {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	_, ok := m["claimed"]
	return ok
}
