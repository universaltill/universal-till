package cloudsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The till's side of the one-time key delivery code (ut-docs#6960, cloud
// POST /api/v1/stores/key-delivery/redeem): the code is normalised, sent
// once with the device bearer, and every refusal is one outcome.

func TestNormalizeKeyDeliveryCode(t *testing.T) {
	const want = "ABCDEFGHJKMNPQRSTVWXYZ0123"
	for _, in := range []string{
		"abcde-fghjk-mnpqr-stvwx-yz012-3",
		"ABCDE FGHJK MNPQR STVWX YZ012 3",
		"  abcdefghjkmnpqrstvwxyz0123  ",
		"ABCDEFGHJKMNPQRSTVWXYZOl23", // O -> 0, l -> 1
	} {
		got, ok := NormalizeKeyDeliveryCode(in)
		if !ok {
			t.Errorf("%q rejected", in)
			continue
		}
		if len(got) != 26 {
			t.Errorf("%q: len %d", in, len(got))
		}
	}
	if got, _ := NormalizeKeyDeliveryCode("abcde-fghjk-mnpqr-stvwx-yz012-3"); got != want {
		t.Errorf("got %q want %q", got, want)
	}
	for _, in := range []string{"", "short", "ABCDEFGHJKMNPQRSTVWXYZ012", "ABCDEFGHJKMNPQRSTVWXYZ01234", "ABCDEFGHJKMNPQRSTVWXYZ012U", "ABCDEFGHJKMNPQRSTVWXYZ012!"} {
		if _, ok := NormalizeKeyDeliveryCode(in); ok {
			t.Errorf("%q must be rejected", in)
		}
	}
}

func TestRedeemKeyDeliveryCode_Outcomes(t *testing.T) {
	const code = "ABCDEFGHJKMNPQRSTVWXYZ0123"
	for _, c := range []struct {
		status int
		hdr    map[string]string
		want   RedeemOutcome
	}{
		{http.StatusNoContent, nil, RedeemAccepted},
		{http.StatusNotFound, nil, RedeemRefused},
		{http.StatusForbidden, nil, RedeemRefused},
		{http.StatusUnauthorized, nil, RedeemRefused},
		{http.StatusBadRequest, nil, RedeemRefused},
		{http.StatusTooManyRequests, map[string]string{"Retry-After": "30"}, RedeemRateLimited},
		{http.StatusInternalServerError, nil, RedeemUnavailable},
	} {
		var gotAuth, gotPath, gotBody string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			gotBody = b["code"]
			for k, v := range c.hdr {
				w.Header().Set(k, v)
			}
			w.WriteHeader(c.status)
		}))
		out, err := RedeemKeyDeliveryCode(context.Background(), testCfg(srv.URL), code)
		srv.Close()
		if out != c.want {
			t.Errorf("status %d: outcome %v (err %v), want %v", c.status, out, err, c.want)
		}
		if gotPath != "/v1/stores/key-delivery/redeem" || gotAuth == "" || gotBody != code {
			t.Errorf("request: path %q auth set=%v body %q", gotPath, gotAuth != "", gotBody)
		}
	}
}

func TestRedeemKeyDeliveryCode_OfflineAndBadShape(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	if out, _ := RedeemKeyDeliveryCode(context.Background(), testCfg(url), "ABCDEFGHJKMNPQRSTVWXYZ0123"); out != RedeemOffline {
		t.Fatalf("offline: %v", out)
	}
	if out, _ := RedeemKeyDeliveryCode(context.Background(), testCfg(url), "nope"); out != RedeemRefused {
		t.Fatalf("bad shape must be refused without a request: %v", out)
	}
}
