package discovery

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// A second way to ask a device what it is, before writing to it.
//
// ut-docs#1606 established the rule: never send the ESC/POS status query to a
// device that has told us it speaks something else, because to a printer that
// does not implement it those three bytes ARE a print job — the product
// owner's HP OfficeJet printed one character per page for each probe.
//
// That rule was enforced from ONE source, the mDNS "pdl=" TXT record, and
// mDNS is the one source a shop LAN routinely breaks. Guest/VLAN'd Wi-Fi, an
// access point filtering multicast, a wired/wireless segment split: any of
// them makes the browse fail, and DiscoverPrinters deliberately carries on
// with sweep results alone (a LAN with no multicast is exactly when the sweep
// earns its keep). With no browse there are no advertisements, so the
// exclusion set is empty and the inkjet gets written to after all
// (ut-docs#1607).
//
// IPP is the second source, and it needs no multicast: an office printer IS
// an IPP printer, and answers Get-Printer-Attributes over plain unicast TCP
// on :631 with the list of document formats it accepts. Measured on the
// product owner's LAN, 2026-09-05:
//
//	                    :80   :515  :631  :9100
//	thermal 1.111       open  -     -     open
//	HP OfficeJet 1.245  open  open  open  open
//
// # Get-Printer-Attributes prints nothing
//
// It is IPP's read operation — the same one a driver install uses to discover
// capabilities. The write-shaped operation is Print-Job (0x0002), which this
// file never sends.
//
// # The trap this rule nearly fell into
//
// nonESCPOSPDL treats "application/octet-stream" in an mDNS pdl= list as a
// positive claim of raw printing, and that is right: the HP's pdl= names five
// languages and does NOT include it. But its IPP document-format-supported
// list DOES:
//
//	application/vnd.hp-PCL, image/jpeg, image/urf, image/pwg-raster,
//	application/PCLm, application/octet-stream
//
// Nearly every IPP printer lists octet-stream, meaning "send anything and I
// will sniff it" — for the HP, sniff it and rasterise it. So the same test
// that reads an mDNS advertisement correctly inverts the truth when applied
// to an IPP list.
//
// This file therefore asks a POSITIVE question instead: does the list name a
// page description language it recognises? The HP names four (PCL, PCLm, URF,
// PWG-raster). Asking the negative question — "anything that is not raw
// counts" — reads text/plain, application/vnd.cups-raw and every vendor raw
// type as a page description language, and those appear in real receipt
// printers' lists; that direction hides the shop's only receipt printer,
// which printers.go is explicit is the worse bug of the two.
const (
	ippPort = 631

	// Get-Printer-Attributes. Named as a constant so a future edit that
	// changes it has to say so out loud — 0x0002 here would print.
	ippOpGetPrinterAttributes = 0x000B

	// IPP delimiter and value tags used below (RFC 8010 §3.5).
	ippTagEndOfAttributes = 0x03
	ippTagCharset         = 0x47
	ippTagNaturalLanguage = 0x48
	ippTagURI             = 0x45
	ippTagKeyword         = 0x44
	ippTagMimeMediaType   = 0x49

	// ippDialTimeout matches the raw-print port's connect budget: same LAN,
	// same single-digit-millisecond handshake.
	ippDialTimeout = escposDialTimeout

	// ippExchangeTimeout covers one request/response. Longer than the dial,
	// because an IPP printer building an attribute set is doing real work,
	// but well short of the ESC/POS read budget — an IPP printer that
	// cannot answer in this long is excluded anyway.
	ippExchangeTimeout = 2 * time.Second

	// ippGuardBudget caps the WHOLE guard for one device, across every path
	// attempted. Without it the cost is per-attempt and multiplies: three
	// paths x (dial + exchange) is 8.1s for a single unresponsive device,
	// and this runs twice per address (the sweep's probe pass and the
	// browsed-candidate pass), inside a scan the operator is watching a
	// spinner for. A device that has not answered in this long lands on the
	// fail-closed branch, which is the safe side.
	ippGuardBudget = 2500 * time.Millisecond

	// ippMaxResponse caps how much of a reply body is READ INTO MEMORY. It
	// is not what stops a device streaming forever — the connection deadline
	// below does that; this only bounds the allocation a LAN-open,
	// unauthenticated responder can provoke. A real Get-Printer-Attributes
	// answer is a few kilobytes.
	ippMaxResponse = 64 << 10

	// ippStatusMaxSuccess is the top of IPP's "successful-*" status-code
	// range (RFC 8011 §13.1.2). Anything above it is an error response —
	// client-error-not-found, most importantly, which is what a printer
	// answers when the resource path is wrong.
	ippStatusMaxSuccess = 0x00FF
)

// ippPaths are the resource paths tried, in order, until one returns a
// successful IPP response.
//
// There is no universal path. /ipp/print is IPP Everywhere's, and what the
// product owner's HP answers on; Brother uses /ipp/port1; CUPS-backed devices
// answer on /. Trying only the first would make a Brother look exactly like a
// device that answers 200 with client-error-not-found and no attributes —
// which, under the fail-closed rule below, would hide it. Each attempt is one
// short-lived connection, and this runs only for hosts that already answered
// on :631 at all.
var ippPaths = []string{"/ipp/print", "/ipp/port1", "/"}

// officePrinterOverIPP reports whether the device at host looks like an
// office printer, judged WITHOUT sending it anything printable.
//
// It answers false — leave it alone, let the ESC/POS probe decide — for the
// hardware this sweep exists to find: a cheap receipt printer has no IPP
// service at all, so the check ends at a refused connection on :631.
//
// It answers true when the device is on :631 and either names a page
// description language, or cannot be got to answer IPP properly at all. That
// second branch is fail-closed on purpose and it is the one that took a
// second pass to get right: checking only the HTTP status let a printer
// answering "200 OK, client-error-not-found, no attributes" — the normal
// reply to asking the wrong resource path — read as "declares no page
// description language", i.e. as permission to write. A device sitting on the
// IPP port that will not complete an IPP exchange is not a POS58/80 box; the
// cost of being wrong that way is a printer we do not offer, against paper in
// a shop.
func officePrinterOverIPP(ctx context.Context, host string) bool {
	formats, reachable, answered := ippDocumentFormats(ctx, host)
	switch {
	case !reachable:
		return false
	case !answered:
		return true // fail closed: on :631, but would not say what it is
	default:
		return namesPageDescriptionLanguage(formats)
	}
}

// ippDocumentFormats asks the device for its document-format-supported list.
//
// The two bools carry the safety decision, and they are not the same
// question. `reachable` is whether anything accepted a connection on :631 at
// all — false means "no IPP service here", which is silence, and silence must
// never exclude a device. `answered` is whether one of the resource paths
// produced a well-formed SUCCESSFUL IPP response; false there means something
// is on the IPP port that will not behave like an IPP printer, which the
// caller treats as an exclusion.
func ippDocumentFormats(ctx context.Context, host string) (formats []string, reachable, answered bool) {
	ctx, cancel := context.WithTimeout(ctx, ippGuardBudget)
	defer cancel()
	addr := net.JoinHostPort(host, strconv.Itoa(ippPort))

	for _, path := range ippPaths {
		conn, err := dialProbe(ctx, addr, ippDialTimeout)
		if err != nil {
			// No IPP service. Says nothing about the device either way,
			// which is precisely the point: an absent service is not a
			// declaration (same conservatism as nonESCPOSPDL's empty-pdl
			// rule).
			//
			// A refused connection and a filtered port that eats the SYN
			// are not distinguished, deliberately: both mean "this device
			// did not tell me it is an office printer", and treating a
			// slow or firewalled host as one would hide receipt printers
			// on exactly the locked-down networks where the mDNS browse
			// has already failed. Such a host still faces the ESC/POS
			// probe, as it did before this guard existed.
			return nil, reachable, false
		}
		reachable = true

		deadline := time.Now().Add(ippExchangeTimeout)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		_ = conn.SetDeadline(deadline)

		got, err := ippFormats(conn, host, path)
		_ = conn.Close()
		if err == nil {
			return got, true, true
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, reachable, false
}

// isOfficePrinter is officePrinterOverIPP addressed the way the sweep works:
// by the "host:9100" string it already holds. A malformed address is treated
// as "do not write" — this decides whether to send print-shaped bytes, and
// there is no address it cannot parse that it should send them to anyway.
func isOfficePrinter(ctx context.Context, addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return true
	}
	return officePrinterOverIPP(ctx, host)
}

// ippFormats runs one Get-Printer-Attributes exchange over an already-open
// connection and returns the document-format-supported values.
//
// An error means "this was not a successful IPP response", and every layer
// that can lie about that is checked: the HTTP status, then IPP's OWN
// status-code in the first four bytes of the body. Checking only the HTTP
// status is the bug this signature exists to prevent — a printer asked for
// the wrong resource path answers 200 OK carrying client-error-not-found and
// no attributes, and an empty format list reads as "declares no page
// description language", which is permission to write.
func ippFormats(conn net.Conn, host, path string) ([]string, error) {
	body := ippGetPrinterAttributes(host, path)
	req, err := http.NewRequest(http.MethodPost, ippEndpoint(host, path), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/ipp")
	req.ContentLength = int64(len(body))
	// The connection is used once and closed; asking the printer to do the
	// same keeps a slow embedded HTTP stack from holding it open.
	req.Close = true
	if err := req.Write(conn); err != nil {
		return nil, err
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errNotIPP
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, ippMaxResponse))
	if err != nil {
		return nil, err
	}
	if !ippSuccessful(payload) {
		return nil, errNotIPP
	}
	return parseIPPFormats(payload), nil
}

// ippSuccessful reports whether payload is an IPP response whose own
// status-code is in the successful-* range. A body too short to carry one is
// not an IPP response at all — an HTML error page, say.
func ippSuccessful(payload []byte) bool {
	const statusCodeEnd = 4
	if len(payload) < statusCodeEnd {
		return false
	}
	return binary.BigEndian.Uint16(payload[2:statusCodeEnd]) <= ippStatusMaxSuccess
}

// errNotIPP marks "answered, but not with a successful IPP response", the
// case officePrinterOverIPP resolves conservatively.
var errNotIPP = errors.New("discovery: not a successful IPP response")

// ippEndpoint and ippURI are the same address in the two forms one request
// needs it: the HTTP request line, and the printer-uri attribute inside the
// IPP body. They must agree — a printer that finds them inconsistent answers
// with an error status, which now lands on the fail-closed branch.
func ippEndpoint(host, path string) string {
	return "http://" + net.JoinHostPort(host, strconv.Itoa(ippPort)) + path
}

func ippURI(host, path string) string {
	return "ipp://" + net.JoinHostPort(host, strconv.Itoa(ippPort)) + path
}

// ippGetPrinterAttributes builds the request body: an operation group with
// the three attributes every IPP request must carry, plus a request for the
// one attribute this guard reads.
func ippGetPrinterAttributes(host, path string) []byte {
	var b bytes.Buffer
	b.Write([]byte{0x01, 0x01}) // version-number 1.1
	_ = binary.Write(&b, binary.BigEndian, uint16(ippOpGetPrinterAttributes))
	b.Write([]byte{0x00, 0x00, 0x00, 0x01}) // request-id
	b.WriteByte(0x01)                       // operation-attributes-tag
	ippAttr(&b, ippTagCharset, "attributes-charset", "utf-8")
	ippAttr(&b, ippTagNaturalLanguage, "attributes-natural-language", "en-us")
	ippAttr(&b, ippTagURI, "printer-uri", ippURI(host, path))
	ippAttr(&b, ippTagKeyword, "requested-attributes", "document-format-supported")
	b.WriteByte(ippTagEndOfAttributes)
	return b.Bytes()
}

func ippAttr(b *bytes.Buffer, tag byte, name, value string) {
	b.WriteByte(tag)
	_ = binary.Write(b, binary.BigEndian, uint16(len(name)))
	b.WriteString(name)
	_ = binary.Write(b, binary.BigEndian, uint16(len(value)))
	b.WriteString(value)
}

// parseIPPFormats walks an IPP response body and collects every
// document-format-supported value.
//
// IPP encodes a multi-valued attribute as one named entry followed by
// further entries with a zero-length name, so the current attribute name
// carries across until the next named entry — that is why the loop tracks
// `current` rather than reading each entry in isolation.
//
// Anything it cannot make sense of yields the values found so far rather
// than an error: a short or truncated read is handled by the caller's
// conservative default, and half a format list is never used to include a
// device, only to exclude one.
func parseIPPFormats(body []byte) []string {
	// version(2) + status-code(2) + request-id(4)
	const headerLen = 8
	if len(body) < headerLen {
		return nil
	}
	var (
		out     []string
		current string
		i       = headerLen
	)
	for i < len(body) {
		tag := body[i]
		i++
		if tag <= 0x0F { // delimiter tag: begins a new attribute group
			// 0x00-0x05 are the defined delimiters and 0x06-0x0F are
			// reserved for future ones (RFC 8010 §3.5.1). Treating a
			// reserved tag as a value tag would misread the bytes after it
			// and truncate the list — which, being shorter, is the
			// permissive direction.
			if tag == ippTagEndOfAttributes {
				break
			}
			current = ""
			continue
		}
		name, ok := readIPPField(body, &i)
		if !ok {
			break
		}
		value, ok := readIPPField(body, &i)
		if !ok {
			break
		}
		if len(name) > 0 {
			current = string(name)
		}
		if current == "document-format-supported" && tag == ippTagMimeMediaType {
			out = append(out, string(value))
		}
	}
	return out
}

// readIPPField reads one length-prefixed field, advancing i past it.
func readIPPField(body []byte, i *int) ([]byte, bool) {
	if *i+2 > len(body) {
		return nil, false
	}
	n := int(binary.BigEndian.Uint16(body[*i : *i+2]))
	*i += 2
	if *i+n > len(body) {
		return nil, false
	}
	v := body[*i : *i+n]
	*i += n
	return v, true
}

// pageDescriptionLanguages are the format tokens that positively identify a
// device as rendering documents rather than accepting raw receipt bytes.
// Substring matches, lower-cased: "pcl" catches both application/vnd.hp-PCL
// and application/PCLm, "image/" catches jpeg, urf, pwg-raster, tiff and png.
//
// The product owner's HP OfficeJet is caught four times over by this list.
var pageDescriptionLanguages = []string{
	"pcl",
	"postscript",
	"pdf",
	"urf",
	"pwg-raster",
	"cups-raster",
	"image/",
	"xps",
	"vnd.hp-",
}

// namesPageDescriptionLanguage reports whether an IPP
// document-format-supported list names a page description language — i.e.
// whether the device has told us, in its own answer, that it renders
// documents rather than accepting raw receipt bytes.
//
// It is a POSITIVE match, and the alternative was tried and rejected. Asking
// the negative question — "is there anything here that is not raw?" — reads
// text/plain as a page description language, and text/plain is in virtually
// every real document-format-supported list, receipt printers with an
// IPP-capable Ethernet interface included. So is application/vnd.cups-raw,
// and so are vendor raw types like application/vnd.epson.escp. Excluding on
// those would hide the shop's only receipt printer, which printers.go is
// explicit is the worse bug of the two.
//
// The cost of the positive form is that an unrecognised vendor page
// description language falls through and the device is probed. That is the
// same failure this guard already accepts for a device with no IPP service at
// all, and it is the direction that cannot make discovery useless.
//
// application/octet-stream is not in the list and could not usefully be:
// nearly every IPP printer offers it as "send anything and I will sniff it",
// the HP included, so over IPP it carries no information at all. (In an mDNS
// pdl= TXT it does — see nonESCPOSPDL, which reads the same token in the
// opposite direction for the opposite source.)
func namesPageDescriptionLanguage(formats []string) bool {
	for _, f := range formats {
		f = strings.ToLower(strings.TrimSpace(f))
		if f == "" {
			continue
		}
		for _, pdl := range pageDescriptionLanguages {
			if strings.Contains(f, pdl) {
				return true
			}
		}
	}
	return false
}

// declaresRawPrinting reports whether an mDNS pdl= TXT value is an
// unambiguous claim to raw/ESC/POS printing: it names a raw format AND no
// page description language at all.
//
// This is stricter than "nonESCPOSPDL said no", and the difference is a hole
// that was in the first draft. nonESCPOSPDL returns false the moment ANY
// entry contains octet-stream, so an office printer advertising
// "application/octet-stream,application/vnd.hp-PCL" on _pdl-datastream._tcp
// — the raw-datastream service, where listing octet-stream is entirely
// normal — passed as a positive raw claim and was exempted from the IPP guard
// altogether. It then got written to, which is the whole bug.
//
// Only a device that says raw and nothing else earns the exemption.
func declaresRawPrinting(pdl string) bool {
	formats := strings.Split(pdl, ",")
	raw := false
	for _, f := range formats {
		f = strings.ToLower(strings.TrimSpace(f))
		if strings.Contains(f, "octet-stream") ||
			strings.Contains(f, "escpos") ||
			strings.Contains(f, "esc-pos") {
			raw = true
		}
	}
	return raw && !namesPageDescriptionLanguage(formats)
}
