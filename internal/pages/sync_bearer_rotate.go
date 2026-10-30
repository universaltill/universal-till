package pages

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/lantls"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// LAN sync bearer rotation (ut-docs#5198, migration 093; the file-held
// replica side is ut-docs#5172). The main till marks an answer with
// lantls.BearerRotateHeader when the bearer it just authenticated is due to
// be swapped (syncTill); the replica's peer transport then runs
// SyncBearerRotator in the background. An older replica ignores the header
// and an older main till never sends it, so mixed versions keep working.

// registerSyncBearerRotate: POST /api/sync/bearer/rotate, answered by a MAIN
// till. Bearer-authed by syncTill in the handler (the auth middleware lets
// the path through, like the rest of /api/sync/*). The caller gets a fresh
// bearer; the one it presented keeps working until SyncBearerGrace after the
// new one is first used, so a replica that never sees this answer is not
// locked out and retries with the bearer it still holds.
func registerSyncBearerRotate(mux *http.ServeMux, d *common.Deps) {
	tills := data.NewTillsRepo(d.Db)
	// The pattern is a literal (the demo-mode route classification reads
	// it); keep it equal to lantls.BearerRotatePath.
	mux.HandleFunc("POST /api/sync/bearer/rotate", func(w http.ResponseWriter, r *http.Request) {
		// A replica has no tills to rotate; its own bearer is the main
		// till's to change.
		if d.SyncPrimaryURL(r.Context()) != "" {
			writeProofError(w, http.StatusConflict, "this till follows a main till and has no tills to rotate")
			return
		}
		till, ok := syncTill(w, r, tills)
		if !ok {
			writeProofError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		// The previous bearer, once the new one is in use, is on its way
		// out: it must not mint a replacement for the one that is live.
		if till.BearerPrev && !till.BearerPending {
			writeProofError(w, http.StatusConflict, "this bearer was already replaced")
			return
		}
		raw := make([]byte, 32)
		_, _ = rand.Read(raw)
		bearer := hex.EncodeToString(raw)
		presented := hashBearer(bearerOf(r))
		swapped, err := tills.RotateBearer(r.Context(), till.ID, presented, hashBearer(bearer), syncBearerNow())
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "sync_bearer_rotate", err)
			return
		}
		if !swapped {
			// Lost a race with another rotation or a revoke.
			writeProofError(w, http.StatusConflict, "this bearer was already replaced")
			return
		}
		logging.L().Infof("sync: till %s rotated its sync credential", till.ID)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"bearer": bearer}, "error": nil})
	})
}

// bearerOf is the bearer a request presented (syncTill has accepted it).
func bearerOf(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

// SyncBearerRotator returns the replica's rotation hook for
// lantls.SetBearerRotate: it asks the main till for a fresh bearer and stores
// it. Any failure is logged and leaves the old bearer in place; the main
// till keeps honouring it until the new one is used, and the next answer
// carrying the header tries again.
func SyncBearerRotator(d *common.Deps) lantls.BearerRotate {
	return func(ctx context.Context) {
		base, old, ok := d.SyncTarget(ctx)
		if !ok {
			return
		}
		fresh, err := requestRotatedBearer(ctx, base, old)
		if err != nil {
			logging.L().Warnf("sync: could not rotate this till's sync credential (it keeps the old one and tries again): %v", err)
			return
		}
		// A re-join or another change while the call was out wins: the swap
		// happens only if the stored bearer is still the one this call
		// started from. The old one is kept as the previous bearer
		// (ut-docs#5203).
		swapped, err := d.Settings.RotateSyncBearer(ctx, old, fresh)
		if err != nil {
			logging.L().Warnf("sync: could not store this till's rotated sync credential (it keeps the old one and tries again): %v", err)
			return
		}
		if swapped {
			logging.L().Infof("sync: this till's sync credential was rotated")
		}
	}
}

// SyncBearerFallback returns the replica's hook for lantls.SetBearerFallback
// (ut-docs#5203). When the main till answers 401 to the current bearer -- it
// was restored from a backup taken before the last rotation, so it knows only
// the bearer this till rotated away from -- the transport retries once with
// the previous bearer the data layer kept, and adopts it if accepted. The hook
// offers it only for the bearer that is current right now, so a request that
// raced a re-join or a rotation is never answered with a stale credential.
func SyncBearerFallback(d *common.Deps) lantls.BearerFallback {
	return func(ctx context.Context, presented string) (string, func() bool, bool) {
		if d.Settings == nil {
			return "", nil, false
		}
		if _, current, ok := d.SyncTarget(ctx); !ok || current != presented {
			return "", nil, false
		}
		prev, err := d.Settings.SyncBearerPrev(ctx)
		if err != nil || prev == "" || prev == presented {
			return "", nil, false
		}
		return prev, func() bool {
			_, adopted, err := d.Settings.AdoptPrevSyncBearer(ctx, presented)
			if adopted {
				logging.L().Infof("sync: this till's sync credential was restored to the previous one the main till still knows (main till restored from a backup?)")
			}
			if err != nil {
				logging.L().Warnf("sync: could not settle this till's previous sync credential: %v", err)
			}
			return adopted
		}, true
	}
}

// requestRotatedBearer calls the main till's rotation endpoint with the
// current bearer and returns the one it issues.
func requestRotatedBearer(ctx context.Context, base, bearer string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+lantls.BearerRotatePath, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	client := lantls.NewPeerClient(15 * time.Second)
	// The bearer goes to the main till's address only (as the link probe):
	// a redirect must not carry it elsewhere, nor turn the POST into a GET.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("main till answered %s", resp.Status)
	}
	var out struct {
		Data struct {
			Bearer string `json:"bearer"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&out); err != nil {
		return "", err
	}
	if !validIssuedBearer(out.Data.Bearer) {
		return "", fmt.Errorf("main till sent a malformed bearer")
	}
	return out.Data.Bearer, nil
}

// validIssuedBearer: what the enrolment handler mints -- 32 random bytes, hex.
func validIssuedBearer(b string) bool {
	raw, err := hex.DecodeString(b)
	return err == nil && len(raw) == 32
}
