package selfupdate

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/releasesig"
)

func sigSeed(n byte) []byte { return bytes.Repeat([]byte{n}, ed25519.SeedSize) }

func sigPubHex(n byte) string {
	return hex.EncodeToString(ed25519.NewKeyFromSeed(sigSeed(n)).Public().(ed25519.PublicKey))
}

func armKeys(t *testing.T, stamp string) {
	t.Helper()
	old := buildinfo.ReleasePublicKeys
	buildinfo.ReleasePublicKeys = stamp
	t.Cleanup(func() { buildinfo.ReleasePublicKeys = old })
}

// hitServer is a fake release server that counts asset downloads.
type hitServer struct {
	srv  *httptest.Server
	mu   sync.Mutex
	hits map[string]int
}

func newHitServer(t *testing.T, tag string, assets map[string][]byte) *hitServer {
	t.Helper()
	hs := &hitServer{hits: map[string]int{}}
	mux := http.NewServeMux()
	rel := func(w http.ResponseWriter, r *http.Request) {
		var sb strings.Builder
		fmt.Fprintf(&sb, `{"tag_name":%q,"assets":[`, tag)
		first := true
		for name := range assets {
			if !first {
				sb.WriteString(",")
			}
			first = false
			fmt.Fprintf(&sb, `{"name":%q,"browser_download_url":%q}`, name, hs.srv.URL+"/asset/"+name)
		}
		sb.WriteString("]}")
		w.Write([]byte(sb.String()))
	}
	mux.HandleFunc("/latest", rel)
	mux.HandleFunc("/tags/", rel)
	mux.HandleFunc("/asset/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/asset/")
		hs.mu.Lock()
		hs.hits[name]++
		hs.mu.Unlock()
		w.Write(assets[name])
	})
	hs.srv = httptest.NewServer(mux)
	t.Cleanup(hs.srv.Close)
	old := releasesLatest
	releasesLatest = hs.srv.URL + "/latest"
	t.Cleanup(func() { releasesLatest = old })
	return hs
}

func (h *hitServer) count(name string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hits[name]
}

// signingPath is one install path under test.
type signingPath struct {
	name     string
	artifact string
	// setup builds the fixture and returns the artifact bytes plus the
	// post-run checks.
	setup func(t *testing.T) (art []byte, installed func(t *testing.T) bool, untouched func(t *testing.T))
}

func signingPaths() []signingPath {
	return []signingPath{
		{
			name:     "tar.gz",
			artifact: archiveNameFor("0.2.0"),
			setup: func(t *testing.T) ([]byte, func(*testing.T) bool, func(*testing.T)) {
				fix := newInstallFixture(t)
				art := makeTarGz(t, []tarEntry{{name: "unitill-pos", body: "NEW-BINARY-v2"}})
				return art,
					func(t *testing.T) bool { b, _ := os.ReadFile(fix.exe); return string(b) == "NEW-BINARY-v2" },
					func(t *testing.T) {
						if b, _ := os.ReadFile(fix.exe); string(b) != fix.oldBin {
							t.Errorf("install touched: %q", b)
						}
						if _, err := os.Stat(fix.exe + ".bak"); !os.IsNotExist(err) {
							t.Errorf(".bak created: %v", err)
						}
					}
			},
		},
		{
			name:     "windows installer",
			artifact: winSetup,
			setup: func(t *testing.T) ([]byte, func(*testing.T) bool, func(*testing.T)) {
				f := newWinFixture(t)
				return []byte("SETUP"),
					func(t *testing.T) bool {
						select {
						case <-f.helper:
							return true
						case <-time.After(5 * time.Second):
							return false
						}
					},
					func(t *testing.T) {
						f.noHelper(t)
						if f.sigCalls.Load() != 0 {
							t.Error("Authenticode ran for a refused release")
						}
						if left := attemptDirs(t, f.work); len(left) != 0 {
							t.Errorf("left %v", left)
						}
					}
			},
		},
	}
}

func goodSums(name string, art []byte) []byte {
	return []byte(sha256hex(art) + "  " + name + "\n")
}

func TestVerifiedChecksumsRefusals(t *testing.T) {
	for _, p := range signingPaths() {
		sigs := func(sums []byte) map[string][]byte {
			return map[string][]byte{
				"missing sig":   nil,
				"unknown key":   []byte(releasesig.Sign(sigSeed(9), sums) + "\n"),
				"bad signature": []byte(releasesig.Sign(sigSeed(1), append([]byte("x"), sums...)) + "\n"),
				"malformed":     []byte("garbage\n"),
			}
		}
		for _, name := range []string{"missing sig", "unknown key", "bad signature", "malformed"} {
			t.Run(p.name+"/"+name, func(t *testing.T) {
				armKeys(t, sigPubHex(1))
				art, _, untouched := p.setup(t)
				sums := goodSums(p.artifact, art)
				assets := map[string][]byte{p.artifact: art, "checksums.txt": sums}
				if sig := sigs(sums)[name]; sig != nil {
					assets[releasesig.SigAsset] = sig
				}
				hs := newHitServer(t, "v0.2.0", assets)
				err := Apply(context.Background())
				if !errors.Is(err, ErrSignature) {
					t.Fatalf("err = %v, want ErrSignature", err)
				}
				if n := hs.count(p.artifact); n != 0 {
					t.Errorf("artifact downloaded %d times before refusal", n)
				}
				untouched(t)
			})
		}
		t.Run(p.name+"/unparseable stamp", func(t *testing.T) {
			armKeys(t, "not-hex")
			art, _, untouched := p.setup(t)
			sums := goodSums(p.artifact, art)
			hs := newHitServer(t, "v0.2.0", map[string][]byte{p.artifact: art, "checksums.txt": sums,
				releasesig.SigAsset: []byte(releasesig.Sign(sigSeed(1), sums) + "\n")})
			err := Apply(context.Background())
			if !errors.Is(err, ErrSignature) {
				t.Fatalf("err = %v, want ErrSignature", err)
			}
			if hs.count(p.artifact) != 0 {
				t.Error("artifact downloaded")
			}
			untouched(t)
		})
		t.Run(p.name+"/env var cannot supply a key (AC 5)", func(t *testing.T) {
			armKeys(t, sigPubHex(1))
			t.Setenv("UT_RELEASE_PUBLIC_KEYS", sigPubHex(7))
			art, _, untouched := p.setup(t)
			sums := goodSums(p.artifact, art)
			hs := newHitServer(t, "v0.2.0", map[string][]byte{p.artifact: art, "checksums.txt": sums,
				releasesig.SigAsset: []byte(releasesig.Sign(sigSeed(7), sums) + "\n")})
			if err := Apply(context.Background()); !errors.Is(err, ErrSignature) {
				t.Fatalf("err = %v, want ErrSignature", err)
			}
			if hs.count(p.artifact) != 0 {
				t.Error("artifact downloaded")
			}
			untouched(t)
		})
	}
}

func TestVerifiedChecksumsAcceptances(t *testing.T) {
	for _, p := range signingPaths() {
		for _, tc := range []struct {
			name  string
			stamp string
			sig   func(sums []byte) []byte // nil result = no .sig asset
		}{
			{"dormant without sig", "", func([]byte) []byte { return nil }},
			{"dormant ignores a present sig", "", func(s []byte) []byte { return []byte("garbage\n") }},
			{"armed valid", sigPubHex(1), func(s []byte) []byte { return []byte(releasesig.Sign(sigSeed(1), s) + "\n") }},
			{"armed two-line (post-dmg shape)", sigPubHex(1) + "," + sigPubHex(2), func(s []byte) []byte {
				return releasesig.Append([]byte(releasesig.Sign(sigSeed(3), s)+"\n"), releasesig.Sign(sigSeed(2), s))
			}},
		} {
			t.Run(p.name+"/"+tc.name, func(t *testing.T) {
				armKeys(t, tc.stamp)
				art, installed, _ := p.setup(t)
				sums := goodSums(p.artifact, art)
				assets := map[string][]byte{p.artifact: art, "checksums.txt": sums}
				if sig := tc.sig(sums); sig != nil {
					assets[releasesig.SigAsset] = sig
				}
				newHitServer(t, "v0.2.0", assets)
				if err := Apply(context.Background()); err != nil {
					t.Fatalf("Apply: %v", err)
				}
				if !installed(t) {
					t.Error("update was not installed")
				}
			})
		}
	}
}

func TestVerifiedChecksumsMissingChecksumsTxtKeepsText(t *testing.T) {
	armKeys(t, sigPubHex(1))
	newInstallFixture(t)
	newHitServer(t, "v0.2.0", map[string][]byte{archiveNameFor("0.2.0"): []byte("x")})
	err := Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "has no checksums.txt — refusing to install an unverified update") {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchReleaseRefusesDowngradeOnLatest(t *testing.T) {
	for _, tc := range []struct {
		cur, tag, want string // want: "" proceeds
	}{
		{"0.30.0", "v0.29.0", "not newer"},
		{"0.30.0", "v0.30.0", "already on the latest version"},
		{"0.30.0", "v0.31.0", ""},
		{"dev", "v0.1.0", ""},
	} {
		t.Run(tc.cur+"/"+tc.tag, func(t *testing.T) {
			old := buildinfo.Version
			buildinfo.Version = tc.cur
			t.Cleanup(func() { buildinfo.Version = old })
			newHitServer(t, tc.tag, map[string][]byte{})
			rel, err := fetchRelease(context.Background(), "")
			if tc.want == "" {
				if err != nil || rel == nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// statusServer serves checksums.txt / checksums.txt.sig with chosen statuses.
func statusServer(t *testing.T, sumsStatus, sigStatus int, sums []byte) *ghRelease {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st := sumsStatus
		if strings.HasSuffix(r.URL.Path, ".sig") {
			st = sigStatus
		}
		w.WriteHeader(st)
		w.Write(sums)
	}))
	t.Cleanup(srv.Close)
	return &ghRelease{TagName: "v0.2.0", Assets: []ghAsset{
		{Name: "checksums.txt", URL: srv.URL + "/checksums.txt"},
		{Name: releasesig.SigAsset, URL: srv.URL + "/checksums.txt.sig"},
	}}
}

// A non-200 checksums.txt keeps today's text (-> failed:checksum), dormant or armed.
func TestVerifiedChecksumsNon200KeepsChecksumCode(t *testing.T) {
	for _, stamp := range []string{"", sigPubHex(1)} {
		armKeys(t, stamp)
		rel := statusServer(t, 404, 200, []byte("Not Found"))
		_, err := verifiedChecksums(context.Background(), rel)
		if err == nil || !strings.Contains(err.Error(), "checksum not found") || errors.Is(err, ErrSignature) {
			t.Fatalf("stamp %q: err = %v", stamp, err)
		}
	}
}

// Only evidence of tampering is ErrSignature; an outage of the .sig fetch is not.
func TestVerifiedChecksumsSigFetchClassification(t *testing.T) {
	armKeys(t, sigPubHex(1))
	sums := []byte("abc  x\n")
	for _, tc := range []struct {
		status  int
		wantSig bool
	}{{404, true}, {500, false}, {503, false}} {
		rel := statusServer(t, 200, tc.status, sums)
		_, err := verifiedChecksums(context.Background(), rel)
		if err == nil || errors.Is(err, ErrSignature) != tc.wantSig {
			t.Errorf("sig HTTP %d: err = %v, ErrSignature want %v", tc.status, err, tc.wantSig)
		}
	}
	// Transport error: the server goes away between the two fetches.
	rel := statusServer(t, 200, 200, sums)
	rel.Assets[1].URL = "http://127.0.0.1:1/checksums.txt.sig"
	_, err := verifiedChecksums(context.Background(), rel)
	var ue *url.Error
	if err == nil || errors.Is(err, ErrSignature) || !errors.As(err, &ue) {
		t.Errorf("transport error: err = %v, want a *url.Error that is not ErrSignature", err)
	}
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func TestVerifiedChecksumsDormantLogsADR0178Line(t *testing.T) {
	armKeys(t, "")
	buf := &lockedBuf{}
	defer logging.CaptureForTest(buf)()
	rel := statusServer(t, 200, 404, []byte("abc  x\n"))
	if _, err := verifiedChecksums(context.Background(), rel); err != nil {
		t.Fatal(err)
	}
	const want = "self-update: release metadata not signature-checked (no release key compiled in)"
	if !strings.Contains(buf.String(), "[INFO] "+want) {
		t.Fatalf("log = %q, want it to contain %q", buf.String(), want)
	}
}

// AC7: the fleet path (ApplyVersionWhenIdle, what update_follow calls) refuses
// an unsigned release on an armed build, before any download and before the
// idle gate matters; pages maps ErrSignature to failed:signature.
func TestApplyVersionWhenIdleArmedRefusesUnsignedRelease(t *testing.T) {
	armKeys(t, sigPubHex(1))
	fix := newInstallFixture(t)
	art := makeTarGz(t, []tarEntry{{name: "unitill-pos", body: "NEW-BINARY-v2"}})
	name := archiveNameFor("0.2.0")
	hs := newHitServer(t, "v0.2.0", map[string][]byte{name: art, "checksums.txt": goodSums(name, art)})
	idleCalls := 0
	err := ApplyVersionWhenIdle(context.Background(), "0.2.0", func() bool { idleCalls++; return true })
	if !errors.Is(err, ErrSignature) {
		t.Fatalf("err = %v, want ErrSignature", err)
	}
	if hs.count(name) != 0 {
		t.Error("artifact downloaded before refusal")
	}
	if b, _ := os.ReadFile(fix.exe); string(b) != fix.oldBin {
		t.Errorf("install touched: %q", b)
	}
}
