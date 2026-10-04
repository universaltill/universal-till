package discovery

// Cloud-assisted main-till lookup (ut-docs#2774): the second candidate
// source for PrimaryWatch.rediscover, asked only when mDNS found nothing
// usable. The main till reports its LAN host:port on every cloud check-in;
// a stranded replica asks, as its own enrolled device (ut-docs#2730),
//
//	GET <endpoint>/v1/stores/main-till-address?store_id=…
//
// and gets {"data":{"lan_address":"192.168.1.20:37673"}} — or an empty
// lan_address when the cloud has no fresh one. The answer is only ever a
// candidate: rediscover challenges it exactly like an mDNS candidate, so a
// wrong, stale or hostile answer costs one refused proof, never a switch.
//
// Status handling mirrors cloudsync's getCheckin: a 404/405 (a cloud that
// predates the endpoint) is "no candidate" with no error, and the cloud is
// not asked again for cloudLookupRetryOld; any other non-200 is an error for
// the caller's info log, asked again at the next re-discovery.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/netaccess"
)

// CloudLookupPath is the endpoint, relative to the marketplace endpoint URL
// (which already carries the /api prefix, like /v1/stores/checkin).
const CloudLookupPath = "/v1/stores/main-till-address"

// cloudLookupRetryOld: after an old-cloud answer, don't ask again for this
// long (cloudsync's checkinRetryOld).
const cloudLookupRetryOld = time.Hour

// cloudLookupMaxBody bounds the answer: one small JSON object.
const cloudLookupMaxBody = 4 << 10

// CloudLookupFunc asks the cloud for this store's main-till LAN address.
// "" with a nil error means "the cloud has none" (or is too old to know).
type CloudLookupFunc func(ctx context.Context) (string, error)

// CloudCredentials returns this till's own cloud identity: the marketplace
// endpoint, its store id and its device bearer. Any empty value means the
// till isn't enrolled and no request is made.
type CloudCredentials func() (endpoint, storeID, token string)

// NewCloudLookup builds the production lookup over creds.
func NewCloudLookup(creds CloudCredentials) CloudLookupFunc {
	return newCloudLookup(creds, time.Now)
}

func newCloudLookup(creds CloudCredentials, now func() time.Time) CloudLookupFunc {
	client := netaccess.NewClient(5 * time.Second)
	// The bearer goes to the configured cloud and nowhere else.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	var (
		mu       sync.Mutex
		oldKey   string
		oldUntil time.Time
	)
	return func(ctx context.Context) (string, error) {
		endpoint, storeID, token := creds()
		endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
		if endpoint == "" || strings.TrimSpace(storeID) == "" || strings.TrimSpace(token) == "" {
			return "", nil
		}
		key := endpoint + "|" + storeID
		mu.Lock()
		skip := oldKey == key && now().Before(oldUntil)
		mu.Unlock()
		if skip {
			return "", nil
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+CloudLookupPath+"?store_id="+url.QueryEscape(storeID), nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK:
		case http.StatusNotFound, http.StatusMethodNotAllowed:
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, cloudLookupMaxBody))
			mu.Lock()
			oldKey, oldUntil = key, now().Add(cloudLookupRetryOld)
			mu.Unlock()
			return "", nil
		default:
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, cloudLookupMaxBody))
			return "", errors.New("cloud answered " + resp.Status)
		}
		var body struct {
			Data struct {
				LANAddress string `json:"lan_address"`
			} `json:"data"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, cloudLookupMaxBody)).Decode(&body); err != nil {
			return "", errors.New("unreadable cloud answer: " + err.Error())
		}
		return strings.TrimSpace(body.Data.LANAddress), nil
	}
}

// cloudCandidateURL turns a cloud answer into a candidate base URL, or ""
// when it isn't a plain dialable IP:port. The cloud validates what it
// stores too; this is the till not trusting that (validate all external
// input). Hostnames are refused: a name would add a DNS lookup the proof's
// host binding doesn't cover. Loopback is allowed on purpose — harmless (it
// can only fail the proof) and what tests listen on.
func cloudCandidateURL(addr string) string {
	host, portStr, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return ""
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil || ip.IsUnspecified() || ip.IsMulticast() {
		return ""
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return ""
	}
	return "http://" + net.JoinHostPort(ip.String(), strconv.Itoa(port))
}
