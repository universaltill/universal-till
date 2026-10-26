package cloudsync

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
)

// ut-docs#5058 (design C6/T2): a replaced till's 401 device_revoked carries
// data {reason: "replaced", replaced_at, replaced_by}. The till keeps it and
// records itself replaced on the FIRST such answer — not after the 3-strike
// lock-out — because it must stop acting as main till at once.

const replacedBody = `{"data":{"reason":"replaced","replaced_at":"2026-10-26T10:00:00Z","replaced_by":"Counter 2"},` +
	`"error":{"code":"device_revoked","message":"no"}}`

func TestPost401KeepsTheReplacedSignal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(replacedBody))
	}))
	defer srv.Close()
	_, err := post(context.Background(), testCfg(srv.URL), "/v1/stores/sync", []byte("{}"))
	var se *statusError
	if !errors.As(err, &se) || se.Replaced == nil || se.Replaced.By != "Counter 2" || se.Replaced.At != "2026-10-26T10:00:00Z" {
		t.Fatalf("err = %#v, want a 401 carrying the replaced signal", err)
	}
}

func TestCheckin401KeepsTheReplacedSignal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(replacedBody))
	}))
	defer srv.Close()
	_, _, err := getCheckin(context.Background(), srv.URL, "store-1", "tok-1", false, 0)
	var se *statusError
	if !errors.As(err, &se) || se.Code != "device_revoked" || se.Replaced == nil || se.Replaced.By != "Counter 2" {
		t.Fatalf("err = %#v, want device_revoked with the replaced signal", err)
	}
}

// A plain removal (no data, or another reason) is not a replacement.
func TestRemovedNotReplacedCarriesNoSignal(t *testing.T) {
	for _, body := range []string{
		unauthorizedBody("device_revoked"),
		`{"data":{"reason":"removed"},"error":{"code":"device_revoked"}}`,
		`{"data":{"reason":"replaced","replaced_by":"X"},"error":{"code":"token_retired"}}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(body))
		}))
		_, err := post(context.Background(), testCfg(srv.URL), "/v1/stores/sync", []byte("{}"))
		srv.Close()
		var se *statusError
		if !errors.As(err, &se) || se.Replaced != nil {
			t.Fatalf("body %s: Replaced = %+v, want nil", body, se.Replaced)
		}
	}
}

func TestTickRecordsReplacedOnTheFirstAnswer(t *testing.T) {
	resetTillAuth(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(replacedBody))
	}))
	defer srv.Close()
	settingsDB := testDB(t)
	_, err := tick(context.Background(), testCfg(srv.URL), settingsDB, Hooks{})
	if err == nil {
		t.Fatal("tick succeeded against a cloud that replaced this till")
	}
	st, ok := enroll.ReplacedState(context.Background(), data.NewSettingsRepo(settingsDB))
	if !ok || st.By != "Counter 2" {
		t.Fatalf("after one replaced answer: state=%+v ok=%v, want recorded at once", st, ok)
	}
}
