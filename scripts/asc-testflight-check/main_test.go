package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeASC is a stubbed App Store Connect API holding one app, one build
// and a set of beta groups.
type fakeASC struct {
	t          *testing.T
	states     []string // processingState[/internalBuildState] per poll of /v1/builds; last repeats; "" = not listed yet
	polls      int
	requests   int
	failFirst  int // the first N requests answer 503
	failPoll   int // poll number (1-based) of /v1/builds that answers 503 on every retry
	failed     int
	omitMeta   bool   // betaTesters linkage without meta.paging.total
	nextHost   string // betaGroups page 1 links.next points at this host
	groups     []fakeGroup
	authHeader string
	status     int // non-zero: every request answers with this status
}

type fakeGroup struct {
	id, name  string
	internal  bool
	allBuilds bool
	builds    []string
	testers   int
}

func (f *fakeASC) handler(srvURL func() string) http.Handler {
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/v1/apps", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("filter[bundleId]"); got != "com.universaltill.pos" {
			write(w, map[string]any{"data": []any{}})
			return
		}
		write(w, map[string]any{"data": []any{
			map[string]any{"id": "APP1", "attributes": map[string]any{"bundleId": "com.universaltill.pos"}},
		}})
	})
	mux.HandleFunc("/v1/builds", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("filter[app]") != "APP1" || q.Get("filter[version]") != "57.1" || q.Get("filter[preReleaseVersion.version]") != "0.30.14" {
			f.t.Errorf("unexpected builds query %q", r.URL.RawQuery)
		}
		if q.Get("include") != "buildBetaDetail" {
			f.t.Errorf("builds query must include buildBetaDetail: %q", r.URL.RawQuery)
		}
		f.polls++
		if f.failPoll > 0 && f.polls == f.failPoll && f.failed < 4 {
			// Every retry of this poll fails; the poll counter stays put.
			f.failed++
			f.polls--
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		i := f.polls - 1
		if i >= len(f.states) {
			i = len(f.states) - 1
		}
		if f.states[i] == "" {
			write(w, map[string]any{"data": []any{}})
			return
		}
		state, internal, _ := strings.Cut(f.states[i], "/")
		build := map[string]any{"type": "builds", "id": "BUILD1", "attributes": map[string]any{"version": "57.1", "processingState": state}}
		out := map[string]any{"data": []any{build}}
		if internal != "" {
			build["relationships"] = map[string]any{"buildBetaDetail": map[string]any{"data": map[string]any{"type": "buildBetaDetails", "id": "BBD1"}}}
			out["included"] = []any{map[string]any{"type": "buildBetaDetails", "id": "BBD1", "attributes": map[string]any{"internalBuildState": internal}}}
		}
		write(w, out)
	})
	mux.HandleFunc("/v1/apps/APP1/betaGroups", func(w http.ResponseWriter, r *http.Request) {
		// Two pages, to prove links.next is followed.
		var data []any
		for _, g := range f.groups {
			data = append(data, map[string]any{"id": g.id, "attributes": map[string]any{
				"name": g.name, "isInternalGroup": g.internal, "hasAccessToAllBuilds": g.allBuilds,
			}})
		}
		if r.URL.Query().Get("cursor") == "" && len(data) > 1 {
			host := srvURL()
			if f.nextHost != "" {
				host = f.nextHost
			}
			write(w, map[string]any{"data": data[:1], "links": map[string]any{"next": host + "/v1/apps/APP1/betaGroups?cursor=2"}})
			return
		}
		if r.URL.Query().Get("cursor") == "2" {
			data = data[1:]
		}
		write(w, map[string]any{"data": data, "links": map[string]any{}})
	})
	mux.HandleFunc("/v1/betaGroups/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/betaGroups/"), "/")
		var g *fakeGroup
		for i := range f.groups {
			if f.groups[i].id == parts[0] {
				g = &f.groups[i]
			}
		}
		if g == nil || len(parts) != 3 || parts[1] != "relationships" {
			http.NotFound(w, r)
			return
		}
		switch parts[2] {
		case "builds":
			var data []any
			for _, b := range g.builds {
				data = append(data, map[string]any{"type": "builds", "id": b})
			}
			write(w, map[string]any{"data": data})
		case "betaTesters":
			var data []any
			if g.testers > 0 {
				data = append(data, map[string]any{"type": "betaTesters", "id": "T1"})
			}
			if f.omitMeta {
				write(w, map[string]any{"data": data})
				return
			}
			write(w, map[string]any{"data": data, "meta": map[string]any{"paging": map[string]any{"total": g.testers, "limit": 1}}})
		default:
			http.NotFound(w, r)
		}
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.authHeader = r.Header.Get("Authorization")
		f.requests++
		if f.requests <= f.failFirst {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if f.status != 0 {
			w.WriteHeader(f.status)
			_, _ = io.WriteString(w, `{"errors":[{"status":"401","code":"NOT_AUTHORIZED","title":"Authentication credentials are missing or invalid."}]}`)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func runFake(t *testing.T, f *fakeASC) (string, error) {
	t.Helper()
	f.t = t
	var srv *httptest.Server
	srv = httptest.NewServer(f.handler(func() string { return srv.URL }))
	t.Cleanup(srv.Close)
	var log strings.Builder
	clock := time.Unix(1_800_000_000, 0)
	c := &checker{
		base:     srv.URL,
		client:   srv.Client(),
		token:    func() (string, error) { return "test-token", nil },
		interval: 30 * time.Second,
		timeout:  40 * time.Minute,
		sleep:    func(d time.Duration) { clock = clock.Add(d) },
		now:      func() time.Time { return clock },
		log:      &log,
	}
	err := c.run(context.Background(), "com.universaltill.pos", "0.30.14", "57.1")
	return log.String(), err
}

func TestPassesWithTodaysSetup(t *testing.T) {
	// One internal group with automatic distribution and 6 testers.
	f := &fakeASC{
		states: []string{"", "PROCESSING", "VALID/PROCESSING", "VALID/IN_BETA_TESTING"},
		groups: []fakeGroup{
			{id: "G0", name: "Public", internal: false, allBuilds: false, testers: 50},
			{id: "G1", name: "Owner & devs", internal: true, allBuilds: true, testers: 6},
		},
	}
	out, err := runFake(t, f)
	if err != nil {
		t.Fatalf("want pass, got %v\nlog:\n%s", err, out)
	}
	if f.polls != 4 {
		t.Errorf("want 4 build polls (not listed, processing, valid+beta processing, ready), got %d", f.polls)
	}
	if f.authHeader != "Bearer test-token" {
		t.Errorf("Authorization = %q", f.authHeader)
	}
	if !strings.Contains(out, "Owner & devs") || !strings.Contains(out, "6 tester") {
		t.Errorf("log should name the group and its testers:\n%s", out)
	}
}

func TestPassesWhenBuildAttachedToGroup(t *testing.T) {
	f := &fakeASC{
		states: []string{"VALID"},
		groups: []fakeGroup{{id: "G1", name: "Devs", internal: true, builds: []string{"OTHER", "BUILD1"}, testers: 2}},
	}
	if out, err := runFake(t, f); err != nil {
		t.Fatalf("want pass, got %v\n%s", err, out)
	}
}

func TestTesterCountWithoutPagingTotal(t *testing.T) {
	// meta.paging.total is optional in Apple's schema: one linkage in data
	// is a tester, not "no testers".
	f := &fakeASC{states: []string{"VALID/READY_FOR_BETA_TESTING"}, omitMeta: true,
		groups: []fakeGroup{{id: "G1", name: "Owner & devs", internal: true, allBuilds: true, testers: 6}}}
	if out, err := runFake(t, f); err != nil {
		t.Fatalf("want pass, got %v\n%s", err, out)
	}
}

func TestRetriesTransientErrors(t *testing.T) {
	f := &fakeASC{states: []string{"VALID"}, failFirst: 2,
		groups: []fakeGroup{{id: "G1", name: "Owner & devs", internal: true, allBuilds: true, testers: 6}}}
	if out, err := runFake(t, f); err != nil {
		t.Fatalf("two 503s then 200 must pass, got %v\n%s", err, out)
	}
	if f.requests < 3 {
		t.Errorf("want the 503s retried, got %d requests", f.requests)
	}
}

func TestPollsThroughAnOutageWhileWaiting(t *testing.T) {
	// A poll whose every retry fails must not end a 40-minute wait.
	f := &fakeASC{states: []string{"PROCESSING", "PROCESSING", "VALID"}, failPoll: 2,
		groups: []fakeGroup{{id: "G1", name: "Owner & devs", internal: true, allBuilds: true, testers: 6}}}
	out, err := runFake(t, f)
	if err != nil {
		t.Fatalf("want pass, got %v\n%s", err, out)
	}
	if f.failed != 4 || !strings.Contains(out, "API error") {
		t.Fatalf("want one poll's 4 attempts all failing and logged, got %d failures\n%s", f.failed, out)
	}
}

func TestNeverSendsTheTokenToAnotherHost(t *testing.T) {
	var hits int
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer other.Close()
	f := &fakeASC{states: []string{"VALID"}, nextHost: other.URL, groups: []fakeGroup{
		{id: "G0", name: "Public", testers: 1},
		{id: "G1", name: "Owner & devs", internal: true, allBuilds: true, testers: 6},
	}}
	_, err := runFake(t, f)
	if err == nil || !strings.Contains(err.Error(), "not the App Store Connect API host") {
		t.Fatalf("want refusal of the off-host next link, got %v", err)
	}
	if hits != 0 {
		t.Fatalf("the other host received %d request(s)", hits)
	}
}

func TestFailsLoudly(t *testing.T) {
	cases := []struct {
		name   string
		f      fakeASC
		expect string
	}{
		{"no group at all", fakeASC{states: []string{"VALID"}}, "no internal TestFlight group"},
		{"only an external group", fakeASC{states: []string{"VALID"}, groups: []fakeGroup{
			{id: "G0", name: "Public", allBuilds: true, testers: 9},
		}}, "no internal TestFlight group"},
		{"internal group without testers", fakeASC{states: []string{"VALID"}, groups: []fakeGroup{
			{id: "G1", name: "Owner & devs", internal: true, allBuilds: true, testers: 0},
		}}, "has no testers"},
		{"internal group cannot see the build", fakeASC{states: []string{"VALID"}, groups: []fakeGroup{
			{id: "G1", name: "Owner & devs", internal: true, builds: []string{"OTHER"}, testers: 6},
		}}, "can see build"},
		{"processing failed", fakeASC{states: []string{"PROCESSING", "FAILED"}, groups: []fakeGroup{
			{id: "G1", name: "Owner & devs", internal: true, allBuilds: true, testers: 6},
		}}, "processing ended FAILED"},
		{"build invalid", fakeASC{states: []string{"INVALID"}}, "processing ended INVALID"},
		{"export compliance missing", fakeASC{states: []string{"VALID/MISSING_EXPORT_COMPLIANCE"}, groups: []fakeGroup{
			{id: "G1", name: "Owner & devs", internal: true, allBuilds: true, testers: 6},
		}}, "MISSING_EXPORT_COMPLIANCE"},
		{"processing exception", fakeASC{states: []string{"VALID/PROCESSING_EXCEPTION"}}, "PROCESSING_EXCEPTION"},
		{"never valid", fakeASC{states: []string{"PROCESSING"}}, "not ready for testers within"},
		{"never listed", fakeASC{states: []string{""}}, "not ready for testers within"},
		{"beta state never ready", fakeASC{states: []string{"VALID/PROCESSING"}}, "not ready for testers within"},
		{"bad credentials", fakeASC{states: []string{"VALID"}, status: http.StatusUnauthorized}, "401"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.f
			out, err := runFake(t, &f)
			if err == nil {
				t.Fatalf("want failure containing %q, got pass\n%s", tc.expect, out)
			}
			if !strings.Contains(err.Error(), tc.expect) {
				t.Fatalf("error %q does not contain %q", err, tc.expect)
			}
			// A terminal state must fail at once, not after the timeout.
			if strings.Contains(tc.expect, "ended") || strings.HasSuffix(tc.expect, "COMPLIANCE") || strings.HasSuffix(tc.expect, "EXCEPTION") {
				if strings.Contains(err.Error(), "within") {
					t.Fatalf("terminal state waited for the timeout: %v", err)
				}
				if f.polls != len(f.states) {
					t.Fatalf("want %d polls, got %d", len(f.states), f.polls)
				}
			}
		})
	}
}

func TestRefusesNextLinkOffTheAPIHost(t *testing.T) {
	c := &checker{base: "https://api.appstoreconnect.apple.com"}
	if err := c.checkSameHost("https://evil.example/v1/apps?cursor=2"); err == nil {
		t.Fatal("a pagination link to another host must be refused (it would carry the bearer token)")
	}
	if err := c.checkSameHost("https://api.appstoreconnect.apple.com/v1/apps?cursor=2"); err != nil {
		t.Fatalf("same host refused: %v", err)
	}
}

func TestSignJWT(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "AuthKey_ABC123.p8")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := loadKey(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	tok, err := signJWT(key, "ABC123", "issuer-uuid", now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts", len(parts))
	}
	var hdr, claims map[string]any
	for i, dst := range []*map[string]any{&hdr, &claims} {
		raw, err := base64.RawURLEncoding.DecodeString(parts[i])
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, dst); err != nil {
			t.Fatal(err)
		}
	}
	if hdr["alg"] != "ES256" || hdr["kid"] != "ABC123" || hdr["typ"] != "JWT" {
		t.Errorf("header = %v", hdr)
	}
	if claims["iss"] != "issuer-uuid" || claims["aud"] != "appstoreconnect-v1" {
		t.Errorf("claims = %v", claims)
	}
	// App Store Connect refuses tokens living longer than 20 minutes.
	if exp, iat := claims["exp"].(float64), claims["iat"].(float64); exp-iat > 20*60 || exp <= iat || int64(iat) != now.Unix() {
		t.Errorf("iat=%v exp=%v", iat, exp)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("ES256 signature must be 64 raw bytes (r||s), got %d (%v)", len(sig), err)
	}
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&priv.PublicKey, h[:], r, s) {
		t.Fatal("signature does not verify against the key's public half")
	}
}

func TestLoadKeyRefusesNonP256(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.p8")
	if err := os.WriteFile(path, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadKey(path); err == nil {
		t.Fatal("garbage key accepted")
	}
}
