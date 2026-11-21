package app

import (
	"context"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#6795 (AC1): a tap attempt still pending at boot lost its collect
// with the last process; it becomes unknown/till_restarted, no broker needed.
func TestMarkOrphanedTapAttempts_FlipsPending(t *testing.T) {
	sqlDB := openBackfillBootDB(t)
	ctx := context.Background()
	repo := data.NewPaymentAttemptRepo(sqlDB)
	if _, err := repo.Begin(ctx, data.PaymentAttempt{Key: "k1", Provider: "stripe", AmountMinor: 500, Currency: "gbp", Deadline: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	log := &recordingLog{}
	markOrphanedTapAttempts(ctx, sqlDB, time.Now(), log)
	a, ok, err := repo.Get(ctx, "k1")
	if err != nil || !ok {
		t.Fatalf("get: %v %v", ok, err)
	}
	if a.State != data.AttemptUnknown || a.Reason != data.AttemptReasonTillRestarted {
		t.Fatalf("attempt = %s/%q, want unknown/till_restarted", a.State, a.Reason)
	}
	if len(log.infos) != 1 || len(log.errors) != 0 {
		t.Fatalf("log infos=%v errors=%v", log.infos, log.errors)
	}
	markOrphanedTapAttempts(ctx, sqlDB, time.Now(), log) // idempotent, silent
	if len(log.infos) != 1 {
		t.Fatalf("second run logged: %v", log.infos)
	}
}

func TestMarkOrphanedTapAttempts_DBErrorDoesNotPanic(t *testing.T) {
	sqlDB := openBackfillBootDB(t)
	_ = sqlDB.Close()
	log := &recordingLog{}
	markOrphanedTapAttempts(context.Background(), sqlDB, time.Now(), log)
	if len(log.errors) != 1 {
		t.Fatalf("errors = %v, want one", log.errors)
	}
}
