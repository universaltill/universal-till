// Command asc-submit drives the App Store review submission of an already
// processed TestFlight build (ut-docs#6413, ADR-0194; opened by ut-docs#6414).
//
// It reuses the open appStoreVersion (PATCH versionString; a new one is
// created, release type MANUAL, only if none is editable), attaches the
// build, makes sure a phased release exists (created INACTIVE: it starts when
// the owner releases the version), and submits the version for App Review.
//
// -dry-run (the default in the workflow) only reads: it prints what it would
// do and makes no write request. A real run is refused until
// store-release/apple.opened exists and names its owner-approved card
// (ut-docs#N), the same switch the Microsoft Store path uses (ut-docs#6408);
// scripts/ci/guard-store-private.sh checks that this file tests it.
//
// Standard library only. The .p8 key is read from the file the workflow wrote;
// nothing secret is logged, and API error bodies are capped to one line.
package main

import (
	"bytes"
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
	"regexp"
	"strings"
	"time"
)

// openedPath is fixed, not a flag: a workflow edit cannot point the switch
// at some other file (guard rule appstore-submit-switch checks it by name).
const openedPath = "store-release/apple.opened"

const apiBase = "https://api.appstoreconnect.apple.com"

// The App Review submission API names. Each carries the store guard marker
// (scripts/ci/guard-store-private.sh) because it is the one path that can send
// a build to App Review; the real submit stays shut until the owner's opening
// file exists (checkOpened, guard rule appstore-submit-switch).
const (
	pathReviewSubmissions     = "/v1/reviewSubmissions"     // store-release:allow ut-docs#6414
	pathReviewSubmissionItems = "/v1/reviewSubmissionItems" // store-release:allow ut-docs#6414
	typeReviewSubmissions     = "reviewSubmissions"         // store-release:allow ut-docs#6414
	typeReviewSubmissionItems = "reviewSubmissionItems"     // store-release:allow ut-docs#6414
)

// editableStates are the appStoreVersion states in which versionString and
// the build may still be changed.
var editableStates = []string{"PREPARE_FOR_SUBMISSION", "DEVELOPER_REJECTED", "REJECTED", "METADATA_REJECTED", "INVALID_BINARY"}

type config struct {
	BundleID   string
	Version    string
	Build      string
	DryRun     bool
	OpenedPath string
}

func main() {
	keyPath := flag.String("key-path", "", "App Store Connect API key (.p8)")
	keyID := flag.String("key-id", "", "API key ID")
	issuer := flag.String("issuer-id", "", "API key issuer ID")
	bundleID := flag.String("bundle-id", "", "app bundle ID")
	version := flag.String("version", "", "marketing version to submit")
	build := flag.String("build", "", "build number (CFBundleVersion) to attach")
	dry := flag.Bool("dry-run", true, "read only: print the plan, make no write request")
	flag.Parse()
	if *keyPath == "" || *keyID == "" || *issuer == "" || *bundleID == "" || *version == "" || *build == "" {
		fmt.Fprintln(os.Stderr, "::error::asc-submit: -key-path -key-id -issuer-id -bundle-id -version -build are required")
		os.Exit(2)
	}
	if !regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,2}$`).MatchString(*version) || !regexp.MustCompile(`^[0-9]+(\.[0-9]+)*$`).MatchString(*build) {
		fmt.Fprintln(os.Stderr, "::error::asc-submit: version must be N.N.N and build numeric (N.N)")
		os.Exit(2)
	}
	key, err := loadKey(*keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "::error::%v\n", err)
		os.Exit(1)
	}
	c := &client{base: apiBase, http: &http.Client{Timeout: 60 * time.Second}, sleep: time.Sleep,
		token: func() (string, error) { return signJWT(key, *keyID, *issuer, time.Now()) }}
	cfg := config{BundleID: *bundleID, Version: *version, Build: *build, DryRun: *dry, OpenedPath: openedPath}
	if err := run(context.Background(), cfg, c, os.Stdout); err != nil {
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

type client struct {
	base  string
	http  *http.Client
	token func() (string, error)
	sleep func(time.Duration)
}

type resource struct {
	Type       string          `json:"type"`
	ID         string          `json:"id"`
	Attributes json.RawMessage `json:"attributes"`
	// Relationships is kept raw: only the version id inside it is searched.
	Relationships json.RawMessage `json:"relationships"`
}

type doc struct {
	Data json.RawMessage `json:"data"`
}

// list decodes a response whose data is an array; one decodes an object (or
// null / empty array as "absent").
func (d doc) list() []resource {
	var l []resource
	if json.Unmarshal(d.Data, &l) == nil {
		return l
	}
	if r := d.one(); r != nil {
		return []resource{*r}
	}
	return nil
}

func (d doc) one() *resource {
	var r resource
	if json.Unmarshal(d.Data, &r) == nil && r.ID != "" {
		return &r
	}
	return nil
}

func attr(r resource, name string) string {
	var m map[string]any
	if json.Unmarshal(r.Attributes, &m) != nil {
		return ""
	}
	s, _ := m[name].(string)
	return s
}

// do sends one request. GETs retry 429/5xx/network errors; writes never
// retry (a repeated POST could submit twice).
func (c *client) do(ctx context.Context, method, path string, q url.Values, body any) (doc, error) {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return doc{}, err
		}
	}
	attempts := 1
	if method == http.MethodGet {
		attempts = 4
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			c.sleep(time.Duration(i) * 5 * time.Second)
		}
		tok, err := c.token()
		if err != nil {
			return doc{}, fmt.Errorf("sign API token: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(payload))
		if err != nil {
			return doc{}, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%s %s: %w", method, path, err)
			continue
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("%s %s: %w", method, path, err)
			continue
		}
		if method == http.MethodGet && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) {
			lastErr = fmt.Errorf("%s %s: HTTP %d", method, path, resp.StatusCode)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			msg := strings.Join(strings.Fields(string(b)), " ")
			if len(msg) > 1024 {
				msg = msg[:1024] + "…"
			}
			return doc{}, fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, msg)
		}
		var d doc
		if len(bytes.TrimSpace(b)) > 0 {
			if err := json.Unmarshal(b, &d); err != nil {
				return doc{}, fmt.Errorf("%s %s: decode: %w", method, path, err)
			}
		}
		return d, nil
	}
	return doc{}, lastErr
}

var cardRE = regexp.MustCompile(`ut-docs#[0-9]+`)

// checkOpened refuses a real submit until the owner's opening file exists and
// names its card. It runs before any API call.
func checkOpened(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return errors.New("the App Store is not opened: store-release/apple.opened is missing (the owner's opening card ut-docs#6414 adds it); run with dry_run=true")
	}
	if !cardRE.Match(b) {
		return errors.New("store-release/apple.opened must name its owner-approved card (ut-docs#N); apple.opened is not valid")
	}
	return nil
}

func rel(typ, id string) map[string]any {
	return map[string]any{"data": map[string]any{"type": typ, "id": id}}
}

func run(ctx context.Context, cfg config, c *client, out io.Writer) error {
	say := func(format string, a ...any) { _, _ = fmt.Fprintf(out, format+"\n", a...) }
	if !cfg.DryRun {
		if err := checkOpened(cfg.OpenedPath); err != nil {
			return err
		}
	}

	apps, err := c.do(ctx, http.MethodGet, "/v1/apps", url.Values{"filter[bundleId]": {cfg.BundleID}, "limit": {"200"}}, nil)
	if err != nil {
		return err
	}
	appID := ""
	for _, r := range apps.list() {
		if attr(r, "bundleId") == cfg.BundleID {
			appID = r.ID
		}
	}
	if appID == "" {
		return fmt.Errorf("App Store Connect has no app with bundle ID %s", cfg.BundleID)
	}

	builds, err := c.do(ctx, http.MethodGet, "/v1/builds", url.Values{
		"filter[app]": {appID}, "filter[version]": {cfg.Build}, "filter[preReleaseVersion.version]": {cfg.Version}, "limit": {"2"}}, nil)
	if err != nil {
		return err
	}
	bl := builds.list()
	if len(bl) == 0 {
		return fmt.Errorf("build %s (%s) is not listed in App Store Connect yet: upload it with ios-testflight first", cfg.Version, cfg.Build)
	}
	if st := attr(bl[0], "processingState"); st != "VALID" {
		return fmt.Errorf("build %s (%s) is %s, not VALID: wait for processing (ios-testflight checks it)", cfg.Version, cfg.Build, st)
	}
	buildID := bl[0].ID

	vers, err := c.do(ctx, http.MethodGet, "/v1/apps/"+url.PathEscape(appID)+"/appStoreVersions", url.Values{
		"filter[platform]": {"IOS"}, "filter[appStoreState]": {strings.Join(editableStates, ",")}, "limit": {"10"}}, nil)
	if err != nil {
		return err
	}
	var verID string
	vl := vers.list()
	// Prefer an open version already named like the target.
	for _, v := range vl {
		if attr(v, "versionString") == cfg.Version {
			verID = v.ID
		}
	}
	if verID == "" && len(vl) > 0 {
		verID = vl[0].ID
		old := attr(vl[0], "versionString")
		say("reusing the open appStoreVersion %s", verID)
		if cfg.DryRun {
			say("would set versionString %s -> %s", old, cfg.Version)
		} else {
			_, err := c.do(ctx, http.MethodPatch, "/v1/appStoreVersions/"+url.PathEscape(verID), nil, map[string]any{"data": map[string]any{
				"type": "appStoreVersions", "id": verID, "attributes": map[string]any{"versionString": cfg.Version}}})
			if err != nil {
				return err
			}
			say("set versionString %s -> %s", old, cfg.Version)
		}
	} else if verID == "" {
		if cfg.DryRun {
			say("would create appStoreVersion %s (IOS, manual release)", cfg.Version)
			verID = "NEW"
		} else {
			d, err := c.do(ctx, http.MethodPost, "/v1/appStoreVersions", nil, map[string]any{"data": map[string]any{
				"type":          "appStoreVersions",
				"attributes":    map[string]any{"platform": "IOS", "versionString": cfg.Version, "releaseType": "MANUAL"},
				"relationships": map[string]any{"app": rel("apps", appID)}}})
			if err != nil {
				return err
			}
			r := d.one()
			if r == nil {
				return errors.New("creating the appStoreVersion returned no id")
			}
			verID = r.ID
			say("created appStoreVersion %s (%s)", verID, cfg.Version)
		}
	} else {
		say("appStoreVersion %s already named %s", verID, cfg.Version)
	}

	if cfg.DryRun {
		say("would attach build %s (%s)", cfg.Build, buildID)
	} else {
		if _, err := c.do(ctx, http.MethodPatch, "/v1/appStoreVersions/"+url.PathEscape(verID)+"/relationships/build", nil, rel("builds", buildID)); err != nil {
			return err
		}
		say("attached build %s", cfg.Build)
	}

	havePhased := false
	if verID != "NEW" {
		p, err := c.do(ctx, http.MethodGet, "/v1/appStoreVersions/"+url.PathEscape(verID)+"/appStoreVersionPhasedRelease", nil, nil)
		if err != nil && !strings.Contains(err.Error(), "HTTP 404") {
			return err
		}
		havePhased = p.one() != nil
	}
	switch {
	case havePhased:
		say("phased release already set")
	case cfg.DryRun:
		say("would create the phased release (INACTIVE until the owner releases)")
	default:
		_, err := c.do(ctx, http.MethodPost, "/v1/appStoreVersionPhasedReleases", nil, map[string]any{"data": map[string]any{
			"type": "appStoreVersionPhasedReleases", "attributes": map[string]any{"phasedReleaseState": "INACTIVE"},
			"relationships": map[string]any{"appStoreVersion": rel("appStoreVersions", verID)}}})
		if err != nil {
			// Apple offers phased release for updates only: a first release
			// is refused. Release stays manual; do not block the submission.
			say("::warning::phased release not created (a first release has none): %v", err)
		} else {
			say("created the phased release")
		}
	}

	if cfg.DryRun {
		say("would submit %s (%s) for App Review; dry run, nothing was changed", cfg.Version, cfg.Build)
		return nil
	}
	return submit(ctx, c, appID, verID, say)
}

// submit opens (or reuses) a review submission, adds the version and submits it.
func submit(ctx context.Context, c *client, appID, verID string, say func(string, ...any)) error {
	subs, err := c.do(ctx, http.MethodGet, pathReviewSubmissions, url.Values{
		"filter[app]": {appID}, "filter[platform]": {"IOS"}, "filter[state]": {"READY_FOR_REVIEW,UNRESOLVED_ISSUES"}, "limit": {"5"}}, nil)
	if err != nil {
		return err
	}
	var subID string
	if l := subs.list(); len(l) > 0 {
		subID = l[0].ID
	} else {
		d, err := c.do(ctx, http.MethodPost, pathReviewSubmissions, nil, map[string]any{"data": map[string]any{
			"type": typeReviewSubmissions, "attributes": map[string]any{"platform": "IOS"},
			"relationships": map[string]any{"app": rel("apps", appID)}}})
		if err != nil {
			return err
		}
		r := d.one()
		if r == nil {
			return errors.New("creating the review submission returned no id")
		}
		subID = r.ID
	}
	inSub, err := c.versionInSubmission(ctx, subID, verID)
	if err != nil {
		return err
	}
	if !inSub {
		if _, err := c.do(ctx, http.MethodPost, pathReviewSubmissionItems, nil, map[string]any{"data": map[string]any{
			"type": typeReviewSubmissionItems, "relationships": map[string]any{
				"reviewSubmission": rel(typeReviewSubmissions, subID), "appStoreVersion": rel("appStoreVersions", verID)}}}); err != nil {
			return err
		}
	}
	if _, err := c.do(ctx, http.MethodPatch, pathReviewSubmissions+"/"+url.PathEscape(subID), nil, map[string]any{"data": map[string]any{
		"type": typeReviewSubmissions, "id": subID, "attributes": map[string]any{"submitted": true}}}); err != nil {
		return err
	}
	say("submitted review submission %s", subID)
	return nil
}

// versionInSubmission reports whether the submission already holds the
// version, so a re-run (or a submission reopened after a rejection) never
// adds the same item twice.
func (c *client) versionInSubmission(ctx context.Context, subID, verID string) (bool, error) {
	d, err := c.do(ctx, http.MethodGet, pathReviewSubmissions+"/"+url.PathEscape(subID)+"/items", url.Values{"include": {"appStoreVersion"}, "limit": {"50"}}, nil)
	if err != nil {
		return false, err
	}
	for _, r := range d.list() {
		var rels struct {
			AppStoreVersion struct {
				Data struct {
					ID string `json:"id"`
				} `json:"data"`
			} `json:"appStoreVersion"`
		}
		if json.Unmarshal(r.Relationships, &rels) == nil && rels.AppStoreVersion.Data.ID == verID {
			return true, nil
		}
	}
	return false, nil
}
