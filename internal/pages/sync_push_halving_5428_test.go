package pages

import (
	"errors"
	"fmt"
	"testing"
)

// ut-docs#5428: the pure halving helper, driven with a fake post.

func halvingBatch(n int) []journalSale {
	b := make([]journalSale, n)
	for i := range b {
		b[i].Sale.ID = fmt.Sprintf("s%03d", i+1)
	}
	return b
}

func TestPostSalesHalving_AlwaysTooLarge_HalvesToOne(t *testing.T) {
	batch := halvingBatch(50)
	var sizes []int
	sent, final, big, err := postSalesHalving(batch, 50, 0, func(sub []journalSale) error {
		sizes = append(sizes, len(sub))
		if sub[0].Sale.ID != "s001" {
			t.Fatalf("retry must keep the LEADING slice, started at %s", sub[0].Sale.ID)
		}
		return errPayloadTooLarge
	}, nil)
	if err != nil || sent != 0 || final != 1 || big == nil || big.Sale.ID != "s001" {
		t.Fatalf("sent=%d final=%d big=%v err=%v", sent, final, big, err)
	}
	want := []int{50, 25, 13, 7, 4, 2, 1}
	if fmt.Sprint(sizes) != fmt.Sprint(want) {
		t.Fatalf("sizes = %v, want %v", sizes, want)
	}
}

func TestPostSalesHalving_AcceptsAtHalf_AcceptedInOrder(t *testing.T) {
	batch := halvingBatch(50)
	var sizes []int
	var accepted [][2]string
	sent, final, big, err := postSalesHalving(batch, 50, 0, func(sub []journalSale) error {
		sizes = append(sizes, len(sub))
		if len(sub) > 25 {
			return fmt.Errorf("wrapped: %w", errPayloadTooLarge)
		}
		return nil
	}, func(sub []journalSale) error {
		accepted = append(accepted, [2]string{sub[0].Sale.ID, sub[len(sub)-1].Sale.ID})
		return nil
	})
	if err != nil || big != nil || sent != 50 || final != 25 {
		t.Fatalf("sent=%d final=%d big=%v err=%v", sent, final, big, err)
	}
	if fmt.Sprint(sizes) != "[50 25 25]" {
		t.Fatalf("sizes = %v", sizes)
	}
	if fmt.Sprint(accepted) != "[[s001 s025] [s026 s050]]" {
		t.Fatalf("accepted = %v", accepted)
	}
}

func TestPostSalesHalving_OtherErrorStopsWithoutHalving(t *testing.T) {
	boom := errors.New("500")
	calls := 0
	sent, _, big, err := postSalesHalving(halvingBatch(50), 50, 0, func([]journalSale) error {
		calls++
		return boom
	}, nil)
	if calls != 1 || sent != 0 || big != nil || !errors.Is(err, boom) {
		t.Fatalf("calls=%d sent=%d big=%v err=%v", calls, sent, big, err)
	}
}

func TestPostSalesHalving_RequestCapStopsQuietly(t *testing.T) {
	calls := 0
	sent, final, big, err := postSalesHalving(halvingBatch(10), 1, 3, func([]journalSale) error {
		calls++
		return nil
	}, nil)
	if calls != 3 || sent != 3 || final != 1 || big != nil || err != nil {
		t.Fatalf("calls=%d sent=%d final=%d big=%v err=%v", calls, sent, final, big, err)
	}
}

func TestPostSalesHalving_OnAcceptedErrorStops(t *testing.T) {
	boom := errors.New("cursor write failed")
	calls := 0
	sent, _, _, err := postSalesHalving(halvingBatch(10), 5, 0, func([]journalSale) error {
		calls++
		return nil
	}, func([]journalSale) error { return boom })
	if calls != 1 || sent != 0 || !errors.Is(err, boom) {
		t.Fatalf("calls=%d sent=%d err=%v", calls, sent, err)
	}
}
