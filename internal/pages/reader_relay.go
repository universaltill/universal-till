package pages

import (
	"context"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/cardreaders"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#6538 phase 2 (ADR-0198 amendment 2026-11-25): an additional till
// sends a sale to a phone reader of its main. The phones live on the main; here
// they are synthetic, never-stored picker rows built from the relay directory.

// relayedSource marks a synthetic registry row for a relayed phone. It is never
// written to card_readers.
const relayedSource = "relayed"

// relayRowPrefix prefixes a relayed row's id so it can never collide with (or
// be mistaken for) a card_readers row id.
const relayRowPrefix = "relay:"

// settingRelayLastRef is this till's own "last used relayed phone" (value:
// "<ref>|<RFC3339>"). Till-local, never synced (data.PerTillSettingPrefixes).
const settingRelayLastRef = "card_readers.relay_last_ref"

const relayEnsureTimeout = 800 * time.Millisecond

func isRelayRowID(id string) bool { return strings.HasPrefix(id, relayRowPrefix) }

// onAdditionalTill reports whether this till is an additional till whose role
// is explicitly recorded: the only till that relays.
func onAdditionalTill(ctx context.Context, d *common.Deps) bool {
	return d != nil && d.Settings != nil && effectiveReaderSource(ctx, d, readerSourcePOS) == readerSourceAdditional
}

// tapMethodsFor lists this till's tap payment entries whose broker provider is
// provider, as (method key, plugin id).
func tapMethodsFor(ctx context.Context, d *common.Deps, provider string) [][2]string {
	entries, err := data.NewPluginRepo(d.Db).ListPaymentEntries(ctx)
	if err != nil {
		return nil
	}
	var out [][2]string
	for _, e := range entries {
		if p, isTap := tapEntry(ctx, d, e.EntryKey); isTap && p == provider && p != "" {
			out = append(out, [2]string{e.EntryKey, e.PluginID})
		}
	}
	return out
}

// relayedReaders is the main's phones, as synthetic registry rows of method,
// on an additional till only. Empty when the link cannot relay (older main,
// plain HTTP, phone pairing off, directory stale). A phone named like this
// till is hidden: an iPhone that is both an additional till and a reader would
// otherwise offer itself to itself (name the reader like the till).
func relayedReaders(ctx context.Context, d *common.Deps, method string) []data.CardReader {
	if d.RelayReaders == nil || !onAdditionalTill(ctx, d) {
		return nil
	}
	provider, isTap := tapEntry(ctx, d, method)
	if !isTap || provider == "" {
		return nil
	}
	pluginID := ""
	for _, m := range tapMethodsFor(ctx, d, provider) {
		if m[0] == method {
			pluginID = m[1]
		}
	}
	ectx, cancel := context.WithTimeout(ctx, relayEnsureTimeout)
	d.RelayReaders.Ensure(ectx, 5*time.Second)
	cancel()
	own := strings.ToLower(strings.TrimSpace(enroll.DeviceName(ctx, d.Settings)))
	lastRef, lastAt := relayLastUsed(ctx, d)
	var out []data.CardReader
	for _, r := range d.RelayReaders.Snapshot() {
		if r.Provider != provider {
			continue
		}
		if own != "" && strings.ToLower(strings.TrimSpace(r.Name)) == own {
			continue
		}
		row := data.CardReader{ID: relayRowPrefix + r.Ref, Name: r.Name, MethodKey: method, PluginID: pluginID,
			ReaderRef: r.Ref, Kind: data.CardReaderKindPhone, Transport: data.CardReaderTransportInternet, Source: relayedSource}
		if r.Ref == lastRef {
			row.LastUsedAt = lastAt
		}
		out = append(out, row)
	}
	return out
}

// relayedPhoneReady reports whether a relayed phone of provider is ready now
// (the tap entry is then offered on this additional till).
func relayedPhoneReady(ctx context.Context, d *common.Deps, method, provider string) bool {
	if d.RelayReaders == nil || d.ReaderPay == nil || provider == "" {
		return false
	}
	for _, c := range relayedReaders(ctx, d, method) {
		if st, ok := d.ReaderPay.PhoneState(ctx, c.ReaderRef); ok && st == fleetlink.ReaderReady {
			return true
		}
	}
	return false
}

// relayLastUsed reads this till's last used relayed phone.
func relayLastUsed(ctx context.Context, d *common.Deps) (ref, at string) {
	v, _, _ := d.Settings.Get(ctx, settingRelayLastRef)
	ref, at, _ = strings.Cut(strings.TrimSpace(v), "|")
	return ref, at
}

// touchRelayLastUsed records the relayed phone as this till's newest used.
// Best effort: a failure is logged and never fails the sale.
func touchRelayLastUsed(ctx context.Context, d *common.Deps, rowID string) {
	ref := strings.TrimPrefix(rowID, relayRowPrefix)
	if ref == "" || d.Settings == nil {
		return
	}
	if err := d.Settings.Set(ctx, settingRelayLastRef, ref+"|"+time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		logging.L().Warnf("card readers: could not record the last used relayed reader: %v", err)
	}
}

// phoneState is a phone reader's presence as this till knows it (its own hub,
// or the relay through its main): core knows it, the plugin cannot. ok is false
// for a row that is not a phone reader or when there is no collector.
func phoneState(ctx context.Context, d *common.Deps, c data.CardReader) (string, bool) {
	if c.Kind != data.CardReaderKindPhone || d.ReaderPay == nil {
		return "", false
	}
	if st, ok := d.ReaderPay.PhoneState(ctx, c.ReaderRef); ok {
		return st, true
	}
	return fleetlink.ReaderUnavailable, true
}

// phoneAnswer maps a phone's presence to the picker's answer.
func phoneAnswer(state string) cardreaders.Answer {
	switch state {
	case fleetlink.ReaderReady:
		return cardreaders.Answer{State: cardreaders.Ready}
	case fleetlink.ReaderBusy:
		return cardreaders.Answer{State: cardreaders.Busy, Reason: "reader_busy"}
	case fleetlink.ReaderNeedsSetup:
		return cardreaders.Answer{State: cardreaders.NeedsSetup, Reason: "reader_needs_setup"}
	}
	return cardreaders.Answer{State: cardreaders.Offline, Reason: "reader_unreachable"}
}

// relayMethods is method, or every tap payment entry when method is "".
func relayMethods(ctx context.Context, d *common.Deps, method string) []string {
	if method != "" {
		return []string{method}
	}
	entries, err := data.NewPluginRepo(d.Db).ListPaymentEntries(ctx)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if _, isTap := tapEntry(ctx, d, e.EntryKey); isTap {
			out = append(out, e.EntryKey)
		}
	}
	return out
}
