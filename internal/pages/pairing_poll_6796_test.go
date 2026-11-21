package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#6796: secret x state x role for GET /api/sync/pair-requests/{id}.
func TestPairPoll_SecretStateRoleMatrix(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	repo := data.NewPairingRepo(dp.Db)
	ctx := context.Background()
	const secret = "matrix-secret"

	// mk creates a row in the named state for the role and returns its id.
	mk := func(t *testing.T, role, state string) string {
		t.Helper()
		ttl := 10 * time.Minute
		if state == "expired" {
			ttl = -time.Minute
		}
		id, err := repo.CreatePendingRequestWithRole(ctx, "Dev", commitOf(secret), role, "", ttl)
		if err != nil {
			t.Fatal(err)
		}
		switch state {
		case "approved":
			if err := repo.ApproveWithRole(ctx, id, "tok-"+role, "", time.Minute); err != nil {
				t.Fatal(err)
			}
		case "denied", "denied+expired":
			if err := repo.Deny(ctx, id); err != nil {
				t.Fatal(err)
			}
		case "approved+expired":
			if err := repo.ApproveWithRole(ctx, id, "tok-"+role, "", -time.Minute); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	get := func(id, mode string) *httptest.ResponseRecorder {
		url := "/api/sync/pair-requests/" + id
		req := httptest.NewRequest(http.MethodGet, url, nil)
		switch mode {
		case "wrong":
			req.Header.Set("X-Pair-Request-Secret", "nope")
			req.URL.RawQuery = "request_secret=nope"
		case "right":
			req.URL.RawQuery = "request_secret=" + secret
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// R1.1: no/wrong secret -> the identical 404 whatever the row.
	var want404 string
	for _, role := range []string{"additional", data.TillRoleCardReader} {
		for _, state := range []string{"pending", "approved", "denied", "expired"} {
			for _, mode := range []string{"absent", "wrong"} {
				rec := get(mk(t, role, state), mode)
				if rec.Code != http.StatusNotFound {
					t.Fatalf("%s/%s/%s: want 404, got %d", role, state, mode, rec.Code)
				}
				if want404 == "" {
					want404 = rec.Body.String()
				}
				if rec.Body.String() != want404 {
					t.Fatalf("%s/%s/%s: body differs: %q vs %q", role, state, mode, rec.Body.String(), want404)
				}
			}
		}
	}
	if rec := get("no-such-id", "right"); rec.Code != 404 || rec.Body.String() != want404 {
		t.Fatalf("unknown id: %d %q", rec.Code, rec.Body.String())
	}

	// Tills: unchanged -- only approved answers, everything else 404.
	for _, state := range []string{"pending", "denied", "expired", "approved+expired"} {
		if rec := get(mk(t, "additional", state), "right"); rec.Code != 404 {
			t.Fatalf("till %s: want 404, got %d", state, rec.Code)
		}
	}
	if rec := get(mk(t, "additional", "approved"), "right"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "tok-additional") {
		t.Fatalf("till approved: %d %s", rec.Code, rec.Body.String())
	}

	// Reader with the secret.
	if rec := get(mk(t, data.TillRoleCardReader, "pending"), "right"); rec.Code != 404 {
		t.Fatalf("reader pending: want 404, got %d", rec.Code)
	}
	for state, code := range map[string]string{"denied": "pairing_denied", "expired": "pairing_expired", "denied+expired": "pairing_denied"} {
		st := state
		if st == "denied+expired" {
			st = "denied"
		}
		id := mk(t, data.TillRoleCardReader, st)
		if state == "denied+expired" {
			// let the tombstone's TTL lapse: re-deny a row created already expired
			id = mk(t, data.TillRoleCardReader, "expired")
			if err := repo.Deny(ctx, id); err != nil {
				t.Fatal(err)
			}
		}
		rec := get(id, "right")
		if rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), `"error":"`+code+`"`) {
			t.Fatalf("reader %s: want 410 %s, got %d %s", state, code, rec.Code, rec.Body.String())
		}
	}
	// Expired-but-approved reader never consumes the approval.
	id := mk(t, data.TillRoleCardReader, "approved+expired")
	if rec := get(id, "right"); rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), "pairing_expired") {
		t.Fatalf("reader approved+expired: %d %s", rec.Code, rec.Body.String())
	}
	if _, _, ok, _ := repo.GetByIDForPoll(ctx, id); !ok {
		t.Fatal("expired approved reader row must not be consumed")
	}
}

func TestApprovePairRequest_DeniedReaderRowIs409(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	repo := data.NewPairingRepo(dp.Db)
	id, _ := repo.CreatePendingRequestWithRole(context.Background(), "Phone", commitOf("s"), data.TillRoleCardReader, "", time.Minute)
	if err := repo.Deny(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/sync/pair-requests/"+id+"/approve", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d %s", rec.Code, rec.Body.String())
	}
}
