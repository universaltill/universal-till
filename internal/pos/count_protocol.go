package pos

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/money"
)

// CountLine is one denomination of a stored count (ut-docs#1043).
type CountLine struct {
	Denomination money.Money // one piece, in the shop currency's minor units
	Pieces       int64
	Total        money.Money // Denomination * Pieces
}

// CountReading is the tolerant read of shifts.count_protocol.
type CountReading struct {
	Recorded   bool        // false: NULL, "", "{}" or only 0-piece entries
	Lines      []CountLine // highest denomination first
	Total      money.Money // sum of Lines[].Total
	Partial    bool        // some entries were dropped, at least one line kept
	Unreadable bool        // Recorded, but nothing could be read
}

// ReadCountProtocol parses the stored {"<minor units>": pieces} blob for
// display. Unlike ValidCountProtocol (the write gate) it never rejects: a
// row written before validation, or damaged, yields what can be read and a
// Partial/Unreadable flag. Dropped keys are never echoed, so nothing stored
// reaches the page; every output is a number.
func ReadCountProtocol(raw string) CountReading {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return CountReading{}
	}
	if len(raw) > maxCountProtocolLen {
		return CountReading{Recorded: true, Unreadable: true}
	}
	var m map[string]json.RawMessage
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil || m == nil || dec.More() {
		return CountReading{Recorded: true, Unreadable: true}
	}
	type entry struct{ denom, pieces int64 }
	var ok []entry
	dropped := 0
	for k, v := range m {
		n, isDenom := parseDenomKey(k)
		if !isDenom {
			dropped++
			continue
		}
		var num json.Number
		if err := json.Unmarshal(v, &num); err != nil {
			dropped++
			continue
		}
		pieces, err := num.Int64()
		if err != nil || pieces < 0 {
			dropped++
			continue
		}
		if pieces == 0 {
			continue // the close form never stores 0; not an error
		}
		if pieces > math.MaxInt64/n {
			dropped++
			continue
		}
		ok = append(ok, entry{n, pieces})
	}
	// Highest denomination first; also makes the overflow guard below
	// order-independent of map iteration.
	sort.Slice(ok, func(i, j int) bool { return ok[i].denom > ok[j].denom })
	r := CountReading{}
	var sum int64
	for _, e := range ok {
		line := e.denom * e.pieces
		if line > math.MaxInt64-sum {
			dropped++
			continue
		}
		sum += line
		r.Lines = append(r.Lines, CountLine{
			Denomination: money.FromMinor(e.denom),
			Pieces:       e.pieces,
			Total:        money.FromMinor(e.denom).Scale(e.pieces),
		})
	}
	r.Total = money.FromMinor(sum)
	r.Recorded = len(r.Lines) > 0 || dropped > 0
	r.Unreadable = dropped > 0 && len(r.Lines) == 0
	r.Partial = dropped > 0 && len(r.Lines) > 0
	return r
}

// parseDenomKey parses a count_protocol key: a canonical positive integer
// in minor units (no sign, leading zero or whitespace; fits int64). Shared
// by the tolerant reader and the ValidCountProtocol write gate.
func parseDenomKey(k string) (int64, bool) {
	n, err := strconv.ParseInt(k, 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != k {
		return 0, false
	}
	return n, true
}
