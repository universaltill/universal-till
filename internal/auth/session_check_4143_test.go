package auth

import (
	"context"
	"testing"
)

// SessionCheck (ut-docs#4143) resolves the session like ResolveNoTouch,
// from the token's hash, for work that outlives the request.
func TestSessionCheck_FollowsTheSession_4143(t *testing.T) {
	db := openAuthTestDB(t)
	seedOperator(t, db, "alice", "manager", "1122")
	svc := NewService(db)
	ctx := context.Background()
	u, token, err := svc.Login(ctx, "1122")
	if err != nil {
		t.Fatal(err)
	}
	check := svc.SessionCheck(token)
	if got, ok := check(ctx); !ok || got.ID != u.ID || got.Role != "manager" {
		t.Fatalf("live session = %+v, %v; want %s", got, ok, u.ID)
	}
	svc.Logout(ctx, token)
	if _, ok := check(ctx); ok {
		t.Fatal("check still live after Logout")
	}
	if _, ok := svc.SessionCheck("")(ctx); ok {
		t.Fatal("an empty token checked live")
	}
}
