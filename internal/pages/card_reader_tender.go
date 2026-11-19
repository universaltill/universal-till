package pages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/universaltill/universal-till/internal/cardreaders"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#6566 (ADR-0201 amendment 2026-11-18): the tender side of the
// per-till card reader registry. Core never names a provider here: a method
// is `managed` when its plugin declares the hook payment.<key>.readers
// (promising to honour `reader_id` and never fall back to another reader)
// AND the method has at least one active registry row. Everything else is
// `legacy`: no new field, request ids byte-identical to before.

// Reader source of a tender: the cashier's till, or the unattended self-order
// kiosk (no picker, never a Tap to Pay row).
const (
	readerSourcePOS   = "pos"
	readerSourceKiosk = "kiosk"
)

// Error codes of a refused reader choice (i18n keys where one exists).
const (
	readerCodeRequired = "reader_required"
	readerCodeInvalid  = "card_reader.invalid"
	readerCodeBusy     = "card_reader.busy"
	readerCodeKioskNo  = "card_reader.kiosk_none"
)

// cardReaderError is a refused reader choice, raised BEFORE any plugin is
// asked, so no money moved.
type cardReaderError struct {
	Status int
	Code   string
	Method string
}

func (e *cardReaderError) Error() string {
	return fmt.Sprintf("card reader: %s (method %s)", e.Code, e.Method)
}

// readersHook is the event a plugin declares to take part in multi-reader.
func readersHook(method string) string { return "payment." + method + ".readers" }

// managedReaders returns the method's active registry rows when the method is
// in managed mode, else (nil, false).
func managedReaders(ctx context.Context, d *common.Deps, repo *data.CardReaderRepo, method string) ([]data.CardReader, bool, error) {
	rows, err := repo.ListActiveByMethod(ctx, method)
	if err != nil || len(rows) == 0 {
		return nil, false, err
	}
	ok, err := data.NewPluginRepo(d.Db).HasActiveHook(ctx, rows[0].PluginID, readersHook(method))
	if err != nil || !ok {
		return nil, false, err
	}
	return rows, true, nil
}

// readerEligible: a tap_to_pay row needs a registered device bridge (flags
// default OFF give none) and is never offered to the unattended kiosk.
func readerEligible(c data.CardReader, source string) bool {
	if c.Kind == data.CardReaderKindTapToPay {
		return source != readerSourceKiosk && cardreaders.TapToPayAvailable()
	}
	return true
}

// resolveReaderLegs resolves, for every leg of a managed method, which
// registry row it is paid on (rules of the Architect note 3.2), writing
// ReaderRowID/ReaderRef/ReaderKind onto the payment. It runs before any
// authorize. A leg of a legacy method is left alone (a stray `reader` value
// is ignored). The returned release func frees the in-flight marks; it is
// non-nil whenever any were taken, even alongside an error.
func resolveReaderLegs(ctx context.Context, d *common.Deps, payments []pos.PaymentInput, source string) (release func(), err error) {
	repo := data.NewCardReaderRepo(d.Db)
	inflight := cardreaders.SharedInFlight()
	var held []string
	release = func() {
		for _, id := range held {
			inflight.Release(id)
		}
		held = nil
	}
	fail := func(status int, code, method string) (func(), error) {
		release()
		return nil, &cardReaderError{Status: status, Code: code, Method: method}
	}
	for i := range payments {
		p := &payments[i]
		if p.MethodID == "" {
			continue
		}
		rows, managed, err := managedReaders(ctx, d, repo, p.MethodID)
		if err != nil {
			release()
			return nil, fmt.Errorf("card readers: %w", err)
		}
		if !managed {
			p.ReaderRowID = ""
			continue
		}
		var eligible []data.CardReader
		for _, c := range rows {
			if readerEligible(c, source) {
				eligible = append(eligible, c)
			}
		}
		var chosen *data.CardReader
		switch {
		case p.ReaderRowID != "":
			for j := range eligible {
				if eligible[j].ID == p.ReaderRowID {
					chosen = &eligible[j]
				}
			}
			if chosen == nil {
				return fail(http.StatusBadRequest, readerCodeInvalid, p.MethodID)
			}
		case len(eligible) == 1:
			chosen = &eligible[0]
		case source == readerSourceKiosk:
			for j := range eligible {
				if eligible[j].UnattendedDefault {
					chosen = &eligible[j]
				}
			}
			if chosen == nil {
				return fail(http.StatusPaymentRequired, readerCodeKioskNo, p.MethodID)
			}
		default:
			return fail(http.StatusConflict, readerCodeRequired, p.MethodID)
		}
		already := false
		for _, id := range held {
			if id == chosen.ID {
				already = true
			}
		}
		if !already {
			if !inflight.Acquire(chosen.ID) {
				return fail(http.StatusConflict, readerCodeBusy, p.MethodID)
			}
			held = append(held, chosen.ID)
		}
		p.ReaderRowID, p.ReaderRef, p.ReaderKind = chosen.ID, chosen.ReaderRef, chosen.Kind
	}
	return release, nil
}

// readerErrorMessageKey is the i18n key of the operator-facing text.
func readerErrorMessageKey(code string) string {
	switch code {
	case readerCodeBusy, readerCodeInvalid, readerCodeKioskNo:
		return code
	}
	return "tap.reader.pick"
}

// writeCardReaderError answers a refused reader choice in the JSON envelope,
// with an HX-Trigger so the till page opens the picker; nothing was charged.
func writeCardReaderError(w http.ResponseWriter, r *http.Request, e *cardReaderError) {
	msg := httpx.T(httpx.ResolveLocale(w, r), readerErrorMessageKey(e.Code))
	trig, _ := json.Marshal(map[string]any{"card-reader-needed": map[string]string{"method": e.Method, "code": e.Code}})
	w.Header().Set("HX-Trigger", string(trig))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data":  nil,
		"error": map[string]string{"code": e.Code, "method": e.Method, "message": msg},
	})
}

// touchReaderLastUsed records the reader as the newest used on this till.
// Best effort: a failure is logged and never fails the sale.
func touchReaderLastUsed(ctx context.Context, d *common.Deps, rowID string) {
	if rowID == "" {
		return
	}
	if err := data.NewCardReaderRepo(d.Db).TouchLastUsed(ctx, rowID); err != nil {
		logging.L().Warnf("card readers: could not record the last used reader: %v", err)
	}
}

// readerStateQuerier asks a plugin's payment.<key>.readers hook for the state
// of refs (read only, blocking, bounded by the caller's deadline).
func readerStateQuerier(d *common.Deps) cardreaders.Querier {
	return func(ctx context.Context, method string, refs []string) (json.RawMessage, error) {
		payload := map[string]any{"op": "state", "readers": refs}
		resp, dispatched, err := blockingPaymentEventDispatch(ctx, d, method, "readers", "", payload)
		if err != nil {
			return nil, err
		}
		if !dispatched {
			return nil, errors.New("readers hook not subscribed")
		}
		return resp, nil
	}
}
