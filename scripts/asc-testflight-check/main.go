// Command asc-testflight-check fails the ios-testflight job when an
// uploaded build would reach nobody (ut-docs#3217): a green upload once sat
// for 6 h in an app with no beta group and no testers.
//
// After the upload it polls App Store Connect until the build is VALID,
// then requires at least one INTERNAL beta group that can see the build
// (hasAccessToAllBuilds, or the build attached) and has at least one tester.
//
// Usage (ios-testflight.yml, after the export/upload step):
//
//	go run ./scripts/asc-testflight-check \
//	  -key-path "$RUNNER_TEMP/private_keys/AuthKey_$ASC_KEY_ID.p8" \
//	  -key-id "$ASC_KEY_ID" -issuer-id "$ASC_ISSUER_ID" \
//	  -bundle-id com.universaltill.pos \
//	  -version "$MARKETING_VERSION" -build "$BUILD_NUMBER"
//
// Standard library only, so the runner needs nothing beyond setup-go. The
// key is read from the file the job already wrote; nothing secret is logged.
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
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

const apiBase = "https://api.appstoreconnect.apple.com"

func main() {
	keyPath := flag.String("key-path", "", "App Store Connect API key (.p8)")
	keyID := flag.String("key-id", "", "API key ID")
	issuer := flag.String("issuer-id", "", "API key issuer ID")
	bundleID := flag.String("bundle-id", "", "app bundle ID")
	version := flag.String("version", "", "marketing version (CFBundleShortVersionString)")
	build := flag.String("build", "", "build number (CFBundleVersion)")
	timeout := flag.Duration("timeout", 40*time.Minute, "how long to wait for the build to become VALID")
	interval := flag.Duration("interval", 30*time.Second, "poll interval")
	flag.Parse()
	required := map[string]string{"key-path": *keyPath, "key-id": *keyID, "issuer-id": *issuer,
		"bundle-id": *bundleID, "version": *version, "build": *build}
	var missing []string
	for name, v := range required {
		if v == "" {
			missing = append(missing, "-"+name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		fmt.Fprintf(os.Stderr, "::error::asc-testflight-check: %s required\n", strings.Join(missing, ", "))
		os.Exit(2)
	}
	key, err := loadKey(*keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "::error::%v\n", err)
		os.Exit(1)
	}
	c := &checker{
		base:     apiBase,
		client:   &http.Client{Timeout: 60 * time.Second},
		token:    func() (string, error) { return signJWT(key, *keyID, *issuer, time.Now()) },
		interval: *interval,
		timeout:  *timeout,
		sleep:    time.Sleep,
		now:      time.Now,
		log:      os.Stdout,
	}
	if err := c.run(context.Background(), *bundleID, *version, *build); err != nil {
		fmt.Fprintf(os.Stderr, "::error::%v\n", err)
		os.Exit(1)
	}
}

// loadKey reads an App Store Connect .p8 (PKCS#8 PEM, P-256).
func loadKey(path string) (*ecdsa.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read API key: %w", err)
	}
	blk, _ := pem.Decode(raw)
	if blk == nil {
		return nil, errors.New("API key is not PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse API key: %w", err)
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, errors.New("API key is not a P-256 EC key")
	}
	return ec, nil
}

// signJWT makes an ES256 App Store Connect token. Apple refuses tokens that
// live longer than 20 minutes, so a fresh one is minted per request.
func signJWT(key *ecdsa.PrivateKey, keyID, issuer string, now time.Time) (string, error) {
	enc := func(v any) (string, error) {
		b, err := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b), err
	}
	h, err := enc(map[string]string{"alg": "ES256", "kid": keyID, "typ": "JWT"})
	if err != nil {
		return "", err
	}
	p, err := enc(map[string]any{"iss": issuer, "iat": now.Unix(), "exp": now.Add(15 * time.Minute).Unix(), "aud": "appstoreconnect-v1"})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(h + "." + p))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return h + "." + p + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

type checker struct {
	base     string
	client   *http.Client
	token    func() (string, error)
	interval time.Duration
	timeout  time.Duration
	sleep    func(time.Duration)
	now      func() time.Time
	log      io.Writer
}

type resource struct {
	Type          string          `json:"type"`
	ID            string          `json:"id"`
	Attributes    json.RawMessage `json:"attributes"`
	Relationships struct {
		BuildBetaDetail struct {
			Data *struct {
				ID string `json:"id"`
			} `json:"data"`
		} `json:"buildBetaDetail"`
	} `json:"relationships"`
}

type page struct {
	Data     []resource `json:"data"`
	Included []resource `json:"included"`
	Links    struct {
		Next string `json:"next"`
	} `json:"links"`
	Meta struct {
		Paging struct {
			Total int `json:"total"`
		} `json:"paging"`
	} `json:"meta"`
}

func (c *checker) run(ctx context.Context, bundleID, version, build string) error {
	appID, err := c.appID(ctx, bundleID)
	if err != nil {
		return err
	}
	buildID, err := c.waitValid(ctx, appID, version, build)
	if err != nil {
		return err
	}
	return c.checkAudience(ctx, appID, buildID, version, build)
}

func (c *checker) appID(ctx context.Context, bundleID string) (string, error) {
	q := url.Values{"filter[bundleId]": {bundleID}, "fields[apps]": {"bundleId"}, "limit": {"200"}}
	var p page
	if err := c.get(ctx, c.base+"/v1/apps?"+q.Encode(), &p); err != nil {
		return "", err
	}
	for _, r := range p.Data {
		var a struct {
			BundleID string `json:"bundleId"`
		}
		if json.Unmarshal(r.Attributes, &a) == nil && a.BundleID == bundleID {
			return r.ID, nil
		}
	}
	return "", fmt.Errorf("App Store Connect has no app with bundle ID %s — create the app record first (ios/README.md)", bundleID)
}

// waitValid polls until the uploaded build is VALID and ready for internal
// testers. A build the API does not list yet is still being ingested, so it
// is polled like PROCESSING. A transient API error (429/5xx/network) is
// logged and polled through until the deadline; any other error fails.
//
// VALID alone is not enough (review of ut-docs#3217): a VALID build whose
// buildBetaDetail.internalBuildState is MISSING_EXPORT_COMPLIANCE can't be
// installed by any tester, so that state is checked too.
func (c *checker) waitValid(ctx context.Context, appID, version, build string) (string, error) {
	q := url.Values{
		"filter[app]":                       {appID},
		"filter[version]":                   {build},
		"filter[preReleaseVersion.version]": {version},
		"fields[builds]":                    {"version,processingState,buildBetaDetail"},
		"include":                           {"buildBetaDetail"},
		"fields[buildBetaDetails]":          {"internalBuildState"},
		"limit":                             {"2"},
	}
	deadline := c.now().Add(c.timeout)
	last := "not listed yet"
	for {
		var p page
		err := c.get(ctx, c.base+"/v1/builds?"+q.Encode(), &p)
		var te transientError
		switch {
		case errors.As(err, &te):
			last = "API error: " + te.Error()
		case err != nil:
			return "", err
		case len(p.Data) > 0:
			var a struct {
				ProcessingState string `json:"processingState"`
			}
			if err := json.Unmarshal(p.Data[0].Attributes, &a); err != nil {
				return "", fmt.Errorf("decode build: %w", err)
			}
			last = a.ProcessingState
			switch a.ProcessingState {
			case "FAILED", "INVALID":
				return "", fmt.Errorf("build %s (%s) processing ended %s in App Store Connect — see the TestFlight tab / Apple's email for the reason", version, build, a.ProcessingState)
			case "VALID":
				internal, known := internalBuildState(p.Data[0], p.Included)
				switch {
				case !known:
					_, _ = fmt.Fprintf(c.log, "build %s (%s) is VALID (internal beta state not reported)\n", version, build)
					return p.Data[0].ID, nil
				case internal == "READY_FOR_BETA_TESTING" || internal == "IN_BETA_TESTING":
					_, _ = fmt.Fprintf(c.log, "build %s (%s) is VALID and %s\n", version, build, internal)
					return p.Data[0].ID, nil
				case internal == "MISSING_EXPORT_COMPLIANCE":
					return "", fmt.Errorf("build %s (%s) is MISSING_EXPORT_COMPLIANCE, so no tester can install it — answer export compliance in App Store Connect → TestFlight, and check ITSAppUsesNonExemptEncryption in ios/project.yml", version, build)
				case internal == "PROCESSING_EXCEPTION" || internal == "EXPIRED":
					return "", fmt.Errorf("build %s (%s) internal beta state is %s — no tester can install it", version, build, internal)
				default:
					last = "VALID, internal beta state " + internal
				}
			}
		}
		if !c.now().Before(deadline) {
			return "", fmt.Errorf("build %s (%s) was not ready for testers within %s (last state: %s) — Apple may just be slow: the upload stands and still reaches testers once processed; a re-run uploads a new build", version, build, c.timeout, last)
		}
		_, _ = fmt.Fprintf(c.log, "build %s (%s): %s; waiting\n", version, build, last)
		c.sleep(c.interval)
	}
}

// internalBuildState finds the build's buildBetaDetail in `included`.
func internalBuildState(b resource, included []resource) (string, bool) {
	rel := b.Relationships.BuildBetaDetail.Data
	for _, r := range included {
		if r.Type != "buildBetaDetails" || (rel != nil && r.ID != rel.ID) {
			continue
		}
		var a struct {
			InternalBuildState string `json:"internalBuildState"`
		}
		if json.Unmarshal(r.Attributes, &a) == nil && a.InternalBuildState != "" {
			return a.InternalBuildState, true
		}
	}
	return "", false
}

// checkAudience requires an internal group that can see the build and has
// at least one tester.
func (c *checker) checkAudience(ctx context.Context, appID, buildID, version, build string) error {
	q := url.Values{"fields[betaGroups]": {"name,isInternalGroup,hasAccessToAllBuilds"}, "limit": {"200"}}
	type group struct {
		id, name  string
		allBuilds bool
	}
	var internal []group
	next := c.base + "/v1/apps/" + url.PathEscape(appID) + "/betaGroups?" + q.Encode()
	for next != "" {
		var p page
		if err := c.get(ctx, next, &p); err != nil {
			return err
		}
		for _, r := range p.Data {
			var a struct {
				Name                 string `json:"name"`
				IsInternalGroup      bool   `json:"isInternalGroup"`
				HasAccessToAllBuilds bool   `json:"hasAccessToAllBuilds"`
			}
			if err := json.Unmarshal(r.Attributes, &a); err != nil {
				return fmt.Errorf("decode beta group: %w", err)
			}
			if a.IsInternalGroup {
				internal = append(internal, group{r.ID, a.Name, a.HasAccessToAllBuilds})
			}
		}
		next = p.Links.Next
	}
	if len(internal) == 0 {
		return errors.New("the upload reaches nobody: the app has no internal TestFlight group — in App Store Connect → TestFlight, create an internal group, add the owner and developers, and turn on automatic distribution (ios/README.md)")
	}
	var problems []string
	for _, g := range internal {
		sees := g.allBuilds
		if !sees {
			var err error
			if sees, err = c.groupHasBuild(ctx, g.id, buildID); err != nil {
				return err
			}
		}
		var p page
		if err := c.get(ctx, c.base+"/v1/betaGroups/"+url.PathEscape(g.id)+"/relationships/betaTesters?limit=1", &p); err != nil {
			return err
		}
		// meta.paging.total is optional in Apple's schema; with limit=1 one
		// linkage in data already proves a tester (review of ut-docs#3217).
		testers := p.Meta.Paging.Total
		if testers == 0 {
			testers = len(p.Data)
		}
		switch {
		case sees && testers > 0:
			_, _ = fmt.Fprintf(c.log, "internal group %q can see build %s (%s) and has %d tester(s)\n", g.name, version, build, testers)
			return nil
		case testers == 0:
			problems = append(problems, fmt.Sprintf("internal group %q has no testers", g.name))
		default:
			problems = append(problems, fmt.Sprintf("internal group %q (%d tester(s)) can't see this build: automatic distribution is off and the build isn't added", g.name, testers))
		}
	}
	return fmt.Errorf("the upload reaches nobody: no internal TestFlight group with testers can see build %s (%s): %s — fix it in App Store Connect → TestFlight (ios/README.md)", version, build, strings.Join(problems, "; "))
}

func (c *checker) groupHasBuild(ctx context.Context, groupID, buildID string) (bool, error) {
	next := c.base + "/v1/betaGroups/" + url.PathEscape(groupID) + "/relationships/builds?limit=200"
	for next != "" {
		var p page
		if err := c.get(ctx, next, &p); err != nil {
			return false, err
		}
		for _, r := range p.Data {
			if r.ID == buildID {
				return true, nil
			}
		}
		next = p.Links.Next
	}
	return false, nil
}

// checkSameHost refuses a URL (a pagination link from a response) that would
// send the bearer token anywhere but the API host.
func (c *checker) checkSameHost(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	b, err := url.Parse(c.base)
	if err != nil {
		return err
	}
	if u.Scheme != b.Scheme || u.Host != b.Host {
		return fmt.Errorf("refusing to follow %s://%s: not the App Store Connect API host", u.Scheme, u.Host)
	}
	return nil
}

// transientError is a 429/5xx/network failure that outlived get's retries.
type transientError struct{ err error }

func (e transientError) Error() string { return e.err.Error() }
func (e transientError) Unwrap() error { return e.err }

// get fetches one URL, retrying transient (429/5xx/network) failures.
func (c *checker) get(ctx context.Context, rawURL string, out any) error {
	if err := c.checkSameHost(rawURL); err != nil {
		return err
	}
	path := rawURL
	if u, err := url.Parse(rawURL); err == nil {
		path = u.Path
	}
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			c.sleep(time.Duration(attempt) * 5 * time.Second)
		}
		tok, err := c.token()
		if err != nil {
			return fmt.Errorf("sign API token: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := c.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("GET %s: %w", path, err)
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("GET %s: %w", path, err)
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("GET %s: HTTP %d", path, resp.StatusCode)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			// One annotation line, capped: Apple's error JSON is small, but
			// a multi-line or huge body would truncate the ::error:: line.
			msg := strings.Join(strings.Fields(string(body)), " ")
			if len(msg) > 1024 {
				msg = msg[:1024] + "…"
			}
			return fmt.Errorf("GET %s: HTTP %d: %s", path, resp.StatusCode, msg)
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("GET %s: decode: %w", path, err)
		}
		return nil
	}
	return transientError{lastErr}
}
