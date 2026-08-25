// TSE reseller-provisioning, till side (ADR-0053, ut-docs#802): the setup
// wizard's Germany-only business-identity step hands the shop's identity to
// Universal Till Cloud, which drives fiskaly with the root reseller key the
// till must never hold. The till then waits for the cloud's payload-less
// fiscal_tse_ready directive, fetches the merchant-scoped operational
// credential once over a dedicated single-use endpoint, and stores it in
// internal/fiscal's at-rest credential store. fiscal.tse_configured flips
// true ONLY after that credential is confirmed on local disk — never at
// wizard-submit time.
//
// Shape mirrors setup_base_plugins.go exactly (persist pending BEFORE any
// network attempt, one time-boxed synchronous attempt in the wizard's POST
// handler, a background retry ticker, a merchant-visible/dismissible pending
// state in Settings) — offline-first is non-negotiable (ADR-0003): the
// wizard must finish with no network.
package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// tseProvisionCountry gates the whole flow to the Germany pilot (ADR-0053;
// same market fiscal.RequiresHardGate names). The next reseller-provisioned
// market is a deliberate decision, not a one-line edit here.
const tseProvisionCountry = "DE"

// tseBusinessIdentity is the four-field shape ut-cloud's kickoff endpoint
// requires (all four required, max 200 chars each). There is deliberately NO
// bank-details field and there never will be — binding product-owner
// decision (ut-docs#663/#802).
type tseBusinessIdentity struct {
	LegalName string `json:"legal_name"`
	OwnerName string `json:"owner_name"`
	TaxNumber string `json:"tax_number"`
	Address   string `json:"address"`
}

func (id tseBusinessIdentity) isZero() bool {
	return id.LegalName == "" && id.OwnerName == "" && id.TaxNumber == "" && id.Address == ""
}

func (id tseBusinessIdentity) isComplete() bool {
	return id.LegalName != "" && id.OwnerName != "" && id.TaxNumber != "" && id.Address != ""
}

// The provisioning lifecycle states persisted under
// common.KeyTSEProvisioningState. ut-docs#802 item 5: failure is loud and
// specific — (a) kickoff rejected, (b) credential fetch failed and (c) still
// pending are distinct, and Settings renders each distinctly.
const (
	// tseStatusPendingKickoff: identity collected, the cloud hasn't accepted
	// the kickoff yet (offline, cloud unreachable, transient 5xx). The
	// background retry keeps attempting.
	tseStatusPendingKickoff = "pending_kickoff"
	// tseStatusAwaitingReady: kickoff accepted; waiting for the cloud's
	// fiscal_tse_ready directive on the normal cloudsync pull. Not a failure.
	tseStatusAwaitingReady = "awaiting_ready"
	// tseStatusKickoffRejected: the cloud definitively declined
	// (subscription_inactive, not_configured, invalid data). Terminal for
	// the retry loop — retrying the same request cannot succeed; the
	// merchant sees why in Settings and can dismiss.
	tseStatusKickoffRejected = "kickoff_rejected"
	// tseStatusCredentialFailed: the ready directive arrived but the
	// credential fetch/store failed (network, 5xx, unusable payload). The
	// cloud re-serves the un-acked directive on the next sync tick, so this
	// self-retries; the state makes the in-between visible, never silent.
	tseStatusCredentialFailed = "credential_failed"
)

// tseProvisioningState is the JSON persisted under
// common.KeyTSEProvisioningState — it survives a process restart between the
// wizard's own attempt and the background retry, same contract as
// KeyPendingBasePlugins.
type tseProvisioningState struct {
	Status    string              `json:"status"`
	Country   string              `json:"country"`
	Identity  tseBusinessIdentity `json:"identity"`
	ErrorCode string              `json:"error_code,omitempty"`
}

// tseKickoffAttemptTimeout bounds the ONE synchronous kickoff attempt POST
// /api/setup makes before it responds — same budget as
// setupBasePluginAttemptTimeout, same reason.
const tseKickoffAttemptTimeout = 5 * time.Second

// tseRetryInitialDelay/tseRetryInterval shape the background kickoff retry,
// mirroring basePluginRetry*'s "short delay, then a loose ticker" reasoning:
// a kickoff that is merely offline has no terminal failure to give up on
// (definitive rejections leave the retry loop via tseStatusKickoffRejected
// instead), so it retries indefinitely at an interval far looser than the
// cloudsync tick.
const (
	tseRetryInitialDelay = 30 * time.Second
	tseRetryInterval     = 5 * time.Minute
)

// tseHTTPClient is the client for both cloud fiscal endpoints. Its own
// timeout is a backstop; the wizard-time attempt is additionally bounded by
// tseKickoffAttemptTimeout via context.
var tseHTTPClient = &http.Client{Timeout: 15 * time.Second}

// validGermanTaxNumber loosely validates a German Steuernummer or USt-IdNr —
// a format hint, deliberately not over-validation (ut-docs#802 item 1): the
// authoritative check happens when the cloud/provider processes it.
// Accepted: "DE" + 9 digits (USt-IdNr., spaces ignored), or a Steuernummer
// of 10-13 digits with optional "/", "." or "-" separators.
func validGermanTaxNumber(s string) bool {
	s = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "DE") {
		rest := s[2:]
		if len(rest) != 9 {
			return false
		}
		for _, r := range rest {
			if r < '0' || r > '9' {
				return false
			}
		}
		return true
	}
	digits := 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '/' || r == '.' || r == '-':
			// separator, fine
		default:
			return false
		}
	}
	return digits >= 10 && digits <= 13
}

// parseTSEIdentityForm reads the wizard's business-identity fields. Returns
// a zero identity and no error key for a non-DE country or an entirely
// skipped step; a non-empty error key (rendered by renderWizard on the
// business-identity step) for a partial/invalid submission. maxIdentityField
// mirrors ut-cloud's own 200-char bound so a value the cloud would reject
// never leaves the till in the first place.
const maxTSEIdentityFieldLen = 200

func parseTSEIdentityForm(country string, get func(string) string) (tseBusinessIdentity, string) {
	if !strings.EqualFold(strings.TrimSpace(country), tseProvisionCountry) {
		return tseBusinessIdentity{}, ""
	}
	id := tseBusinessIdentity{
		LegalName: strings.TrimSpace(get("tse_legal_name")),
		OwnerName: strings.TrimSpace(get("tse_owner_name")),
		TaxNumber: strings.TrimSpace(get("tse_tax_number")),
		Address:   strings.TrimSpace(get("tse_address")),
	}
	if id.isZero() {
		return tseBusinessIdentity{}, "" // step skipped — free tier brings its own fiscalisation (ADR-0045)
	}
	if !id.isComplete() {
		return id, "setup.error.tse_identity_incomplete"
	}
	for _, v := range []string{id.LegalName, id.OwnerName, id.TaxNumber, id.Address} {
		if len(v) > maxTSEIdentityFieldLen {
			return id, "setup.error.tse_identity_too_long"
		}
	}
	if !validGermanTaxNumber(id.TaxNumber) {
		return id, "setup.error.tse_tax_number"
	}
	return id, ""
}

// loadTSEProvisioningState / saveTSEProvisioningState persist the lifecycle
// state as JSON under common.KeyTSEProvisioningState. nil st (or a load of
// an empty value) means "nothing in flight". Save(nil) clears.
func loadTSEProvisioningState(ctx context.Context, d *common.Deps) (*tseProvisioningState, error) {
	raw, ok, err := d.Settings.Get(ctx, common.KeyTSEProvisioningState)
	if err != nil || !ok || strings.TrimSpace(raw) == "" {
		return nil, err
	}
	var st tseProvisioningState
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return nil, err
	}
	if st.Status == "" {
		return nil, nil
	}
	return &st, nil
}

func saveTSEProvisioningState(ctx context.Context, d *common.Deps, st *tseProvisioningState) error {
	if st == nil {
		return d.Settings.Set(ctx, common.KeyTSEProvisioningState, "")
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return d.Settings.Set(ctx, common.KeyTSEProvisioningState, string(raw))
}

// startTSEProvisioningForSetup is POST /api/setup's hook: persists the
// collected identity as pending BEFORE any network attempt (so it survives
// the process dying mid-request and the wizard finishing offline), then
// makes one best-effort, time-boxed synchronous kickoff attempt. Mirrors
// installBasePluginsForSetup's posture exactly — a failure here must never
// delay or fail the wizard's own response.
func startTSEProvisioningForSetup(ctx context.Context, d *common.Deps, country string, id tseBusinessIdentity) {
	if !strings.EqualFold(strings.TrimSpace(country), tseProvisionCountry) || !id.isComplete() {
		return
	}
	st := &tseProvisioningState{Status: tseStatusPendingKickoff, Country: tseProvisionCountry, Identity: id}
	if err := saveTSEProvisioningState(ctx, d, st); err != nil {
		logging.L().Errorf("setup wizard: persist pending TSE provisioning: %v", err)
	}
	attemptCtx, cancel := context.WithTimeout(ctx, tseKickoffAttemptTimeout)
	defer cancel()
	tseKickoffAttempt(attemptCtx, d, st)
}

// tseKickoffAttempt performs one kickoff POST against the cloud and updates
// st (persisting it) according to the outcome:
//   - accepted            -> awaiting_ready
//   - definitive rejection -> kickoff_rejected (+ the cloud's error code)
//   - anything transient   -> stays pending_kickoff for the next retry
func tseKickoffAttempt(ctx context.Context, d *common.Deps, st *tseProvisioningState) {
	effCfg := enroll.EnsureRegistered(ctx, d.Cfg, d.Settings)
	m := effCfg.Marketplace
	if m.EndpointURL == "" || m.StoreID == "" || m.MerchantToken == "" {
		logging.L().Infof("tse provisioning: till not registered with the cloud yet, will retry")
		return // stays pending_kickoff
	}
	payload, err := json.Marshal(map[string]any{
		"store_id":          m.StoreID,
		"country":           st.Country,
		"business_identity": st.Identity,
	})
	if err != nil {
		logging.L().Errorf("tse provisioning: encode kickoff: %v", err)
		return
	}
	status, code, err := tseCloudPost(ctx, m, "/v1/stores/fiscal/tse/provision", payload)
	switch {
	case err == nil && status == http.StatusOK:
		st.Status = tseStatusAwaitingReady
		st.ErrorCode = ""
		if err := saveTSEProvisioningState(ctx, d, st); err != nil {
			logging.L().Errorf("tse provisioning: persist awaiting_ready: %v", err)
		}
		logging.L().Infof("tse provisioning: kickoff accepted, awaiting fiscal_tse_ready directive")
	case err == nil && tseKickoffRejected(status):
		// Loud and specific, never a silent half-provisioned state:
		// retrying the identical request cannot succeed for these codes
		// (subscription_inactive, not_configured, invalid data), so this is
		// terminal for the retry loop and Settings says exactly why.
		st.Status = tseStatusKickoffRejected
		st.ErrorCode = code
		if st.ErrorCode == "" {
			st.ErrorCode = fmt.Sprintf("http_%d", status)
		}
		if err := saveTSEProvisioningState(ctx, d, st); err != nil {
			logging.L().Errorf("tse provisioning: persist kickoff_rejected: %v", err)
		}
		logging.L().Warnf("tse provisioning: kickoff rejected by the cloud (%s), not retrying", st.ErrorCode)
	default:
		// Transient (network error, 404 store-propagation lag, 5xx incl.
		// 502 provisioning_failed — the cloud calls those safe to retry):
		// stay pending for the background ticker.
		logging.L().Warnf("tse provisioning: kickoff not accepted yet (status=%d err=%v), will retry in background", status, err)
	}
}

// tseKickoffRejected classifies the cloud's definitive-decline statuses per
// the ut-cloud contract: 400 invalid_request, 403 subscription_inactive and
// 503 not_configured are not retryable with the same request; everything
// else non-200 is treated as transient.
func tseKickoffRejected(status int) bool {
	return status == http.StatusBadRequest ||
		status == http.StatusForbidden ||
		status == http.StatusServiceUnavailable
}

// tseProvisionRetryTick is one pass of the background retry: only a
// pending_kickoff state is ever retried (awaiting_ready progresses via the
// fiscal_tse_ready directive, credential_failed via the cloud re-serving
// that directive, kickoff_rejected is terminal).
func tseProvisionRetryTick(ctx context.Context, d *common.Deps) {
	st, err := loadTSEProvisioningState(ctx, d)
	if err != nil {
		logging.L().Warnf("tse provisioning retry: load state: %v", err)
		return
	}
	if st == nil || st.Status != tseStatusPendingKickoff {
		return
	}
	tseKickoffAttempt(ctx, d, st)
}

// StartTSEProvisionRetry launches the background half of the TSE kickoff
// (ut-docs#802): the wizard's own attempt only gets tseKickoffAttemptTimeout,
// so a kickoff still offline at that point is retried here until the cloud
// accepts or definitively rejects it. Shape mirrors StartBasePluginRetry
// exactly — a goroutine, a short initial delay, then a ticker, everything
// silent-and-retry, wg.Done() on ctx.Done(). Wired in internal/pages/init.go
// alongside StartBasePluginRetry/StartCloudSync.
func StartTSEProvisionRetry(ctx context.Context, d *common.Deps, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-time.After(tseRetryInitialDelay):
		case <-ctx.Done():
			return
		}
		tseProvisionRetryTick(ctx, d)
		t := time.NewTicker(tseRetryInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				tseProvisionRetryTick(ctx, d)
			case <-ctx.Done():
				return
			}
		}
	}()
}

// applyFiscalTSEReady is the fiscal_tse_ready directive hook (wired in
// cloudsync_wire.go): fetch the operational credential once over the
// dedicated single-use endpoint and store it at rest. Returns an error to
// leave the directive un-acked (the cloud re-serves it next tick) — success
// is only ever reported after the credential is CONFIRMED stored on local
// disk, and only then does fiscal.tse_configured flip true (binding,
// ut-docs#802 item 4).
func applyFiscalTSEReady(ctx context.Context, d *common.Deps) (string, error) {
	store := fiscal.NewTSECredentialStore()
	// Idempotent re-serve: a directive whose ack never reached the cloud
	// re-applies after the credential already landed — never a second fetch
	// (the endpoint is single-use and would 410). Deliberately Load(), not
	// Exists(): a Stat-only check would treat a zero-length file left behind
	// by a prior failed write (review finding, ut-docs#802 — Save is now
	// write-tmp-then-rename so this shouldn't recur, but this check must not
	// depend on that alone) as "already stored" and flip tse_configured true
	// over an unreadable credential. Load() is the same confirmed-readable
	// bar the success path below holds itself to.
	if cred, ok, err := store.Load(); err == nil && ok && len(cred) > 0 {
		return finishTSEProvisioning(ctx, d, "TSE operational credential already stored")
	}

	eff := enroll.Effective(d.Cfg)
	m := eff.Marketplace
	if m.EndpointURL == "" || m.StoreID == "" || m.MerchantToken == "" {
		return "", fmt.Errorf("till is not registered with the cloud")
	}
	payload, err := json.Marshal(map[string]string{"store_id": m.StoreID})
	if err != nil {
		return "", err
	}
	status, _, body, err := tseCloudPostBody(ctx, m, "/v1/stores/fiscal/tse/credential", payload)
	if err != nil {
		markTSECredentialFailed(ctx, d, "unreachable")
		return "", fmt.Errorf("credential fetch failed: %w", err)
	}
	switch {
	case status == http.StatusOK:
		// fall through to decode below
	case status == http.StatusGone:
		// The single-use retrieval record is spent or expired and nothing is
		// stored locally: only a fresh kickoff can mint a new one (ADR-0053
		// Decision 1), so requeue the kickoff for the retry loop.
		requeueTSEKickoff(ctx, d)
		return "", fmt.Errorf("credential unavailable (single-use record spent/expired) — re-running kickoff")
	default:
		markTSECredentialFailed(ctx, d, fmt.Sprintf("http_%d", status))
		return "", fmt.Errorf("credential fetch returned %d", status)
	}

	var resp struct {
		Data struct {
			OperationalCredential map[string]any `json:"operational_credential"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		markTSECredentialFailed(ctx, d, "bad_response")
		return "", fmt.Errorf("decode credential response: %w", err)
	}
	if len(resp.Data.OperationalCredential) == 0 {
		markTSECredentialFailed(ctx, d, "empty_credential")
		return "", fmt.Errorf("credential response carried no operational_credential")
	}
	if err := store.Save(resp.Data.OperationalCredential); err != nil {
		markTSECredentialFailed(ctx, d, "store_failed")
		return "", fmt.Errorf("store credential: %w", err)
	}
	// Confirm the store by reading it back — fiscal.tse_configured must mean
	// "the credential is on this disk, readable", not "a write call returned".
	if _, ok, err := store.Load(); err != nil || !ok {
		markTSECredentialFailed(ctx, d, "store_failed")
		return "", fmt.Errorf("credential not readable after store: ok=%v err=%v", ok, err)
	}
	return finishTSEProvisioning(ctx, d, "TSE operational credential stored")
}

// finishTSEProvisioning flips fiscal.tse_configured true (the credential is
// confirmed on disk by the caller) and clears the pending state.
func finishTSEProvisioning(ctx context.Context, d *common.Deps, msg string) (string, error) {
	if err := d.Settings.Set(ctx, fiscal.KeyTSEConfigured, "true"); err != nil {
		// Leave the directive un-acked: the re-serve is idempotent (the
		// store.Load() fast path above) and will retry this write.
		return "", fmt.Errorf("persist %s: %w", fiscal.KeyTSEConfigured, err)
	}
	if err := saveTSEProvisioningState(ctx, d, nil); err != nil {
		logging.L().Warnf("tse provisioning: clear state after success: %v", err)
	}
	return msg, nil
}

// requeueTSEKickoff moves the state back to pending_kickoff (keeping the
// stored identity) so the retry loop mints a fresh single-use retrieval
// record; with no stored identity left (e.g. dismissed) it can only record
// the failure loudly.
func requeueTSEKickoff(ctx context.Context, d *common.Deps) {
	st, err := loadTSEProvisioningState(ctx, d)
	if err != nil {
		logging.L().Warnf("tse provisioning: load state for requeue: %v", err)
	}
	if st != nil && st.Identity.isComplete() {
		st.Status = tseStatusPendingKickoff
		st.ErrorCode = "credential_unavailable"
		if err := saveTSEProvisioningState(ctx, d, st); err != nil {
			logging.L().Errorf("tse provisioning: persist requeue: %v", err)
		}
		return
	}
	markTSECredentialFailed(ctx, d, "credential_unavailable")
}

// markTSECredentialFailed records the loud (b)-case state: the ready signal
// arrived but the credential isn't locally stored yet. Preserves whatever
// identity/country the state already carries.
func markTSECredentialFailed(ctx context.Context, d *common.Deps, code string) {
	st, err := loadTSEProvisioningState(ctx, d)
	if err != nil {
		logging.L().Warnf("tse provisioning: load state to mark failure: %v", err)
	}
	if st == nil {
		st = &tseProvisioningState{Country: tseProvisionCountry}
	}
	st.Status = tseStatusCredentialFailed
	st.ErrorCode = code
	if err := saveTSEProvisioningState(ctx, d, st); err != nil {
		logging.L().Errorf("tse provisioning: persist credential_failed: %v", err)
	}
}

// tseCloudPost / tseCloudPostBody POST to the cloud with the store's bearer
// token — the same auth channel as every other store-scoped call
// (cloudsync's post(), enroll's registerDevice), never a new auth path.
// Non-2xx statuses are returned as data, not errors: the caller classifies
// them (rejected vs transient vs gone).
func tseCloudPost(ctx context.Context, m config.MarketplaceConfig, path string, payload []byte) (status int, errCode string, err error) {
	status, errCode, _, err = tseCloudPostBody(ctx, m, path, payload)
	return status, errCode, err
}

func tseCloudPostBody(ctx context.Context, m config.MarketplaceConfig, path string, payload []byte) (int, string, []byte, error) {
	url := strings.TrimRight(m.EndpointURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return 0, "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.MerchantToken)
	resp, err := tseHTTPClient.Do(req)
	if err != nil {
		return 0, "", nil, err
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	body := buf.Bytes()
	errCode := ""
	if resp.StatusCode != http.StatusOK {
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &envelope) == nil {
			errCode = envelope.Error.Code
		}
	}
	return resp.StatusCode, errCode, body, nil
}

// tseProvisioningView is the Settings-page display shape: MessageKey is the
// status line's locale key; ReasonKey (kickoff_rejected only) names the
// specific decline reason and is substituted into MessageKey's %s.
type tseProvisioningView struct {
	MessageKey string
	ReasonKey  string
}

// tseProvisioningViewFor maps a persisted state to its Settings chip. nil
// means "render nothing".
func tseProvisioningViewFor(st *tseProvisioningState) *tseProvisioningView {
	if st == nil {
		return nil
	}
	switch st.Status {
	case tseStatusPendingKickoff:
		return &tseProvisioningView{MessageKey: "settings.tse.pending_kickoff"}
	case tseStatusAwaitingReady:
		return &tseProvisioningView{MessageKey: "settings.tse.awaiting_ready"}
	case tseStatusCredentialFailed:
		return &tseProvisioningView{MessageKey: "settings.tse.credential_failed"}
	case tseStatusKickoffRejected:
		reason := "settings.tse.reason.rejected"
		switch st.ErrorCode {
		case "subscription_inactive":
			reason = "settings.tse.reason.subscription_inactive"
		case "not_configured":
			reason = "settings.tse.reason.not_configured"
		case "invalid_request":
			reason = "settings.tse.reason.invalid_request"
		}
		return &tseProvisioningView{MessageKey: "settings.tse.kickoff_rejected", ReasonKey: reason}
	}
	return nil
}
