// Package sim is a stand-in Turkish cash register (YN ÖKC) that speaks the
// Universal Till ÖKC bridge protocol v0 (plugins/tax-tr/okc.BridgeDriver)
// over TCP. It lets the till, the plugin and the e2e suite exercise the
// whole "pay on the device" tender before a real device or a maker's
// integrator pack is in hand, and keeps CI honest afterwards.
//
// It behaves like a device, not like a mock: a receipt counter that only
// goes up, a Z (daily close) counter, per-request idempotency on
// request_id, and switchable failure modes (decline everything, answer
// slowly, go silent) so the fail-closed tender path is tested too.
package sim

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// Options tune the simulated device.
type Options struct {
	Serial string
	Maker  string
	// ZNo is the starting daily-report counter.
	ZNo int64
	// DeclineAll makes every sale/refund answer {"ok":false}.
	DeclineAll bool
	// Silent accepts connections and never answers — the "device hung"
	// case the plugin's read timeout must cover.
	Silent bool
	// Delay is added before every answer.
	Delay time.Duration
}

// Server is one simulated device listening on a loopback (or LAN) port.
type Server struct {
	ln   net.Listener
	opts Options

	mu        sync.Mutex
	receiptNo int64
	zNo       int64
	today     int64
	seen      map[string]answer // request_id → answer (idempotency)
	log       []Printed
}

// Printed is one receipt the simulated device "printed" — tests read these
// to assert what reached the device.
type Printed struct {
	Op        string
	RequestID string
	Amount    int64
	Lines     int
	ReceiptNo string
}

type answer map[string]any

// Start listens on addr ("127.0.0.1:0" for an ephemeral test port) and
// serves until Close.
func Start(addr string, opts Options) (*Server, error) {
	if opts.Serial == "" {
		opts.Serial = "SIM-0001"
	}
	if opts.Maker == "" {
		opts.Maker = "sim"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s := &Server{ln: ln, opts: opts, zNo: opts.ZNo, seen: map[string]answer{}}
	go s.accept()
	return s, nil
}

// Addr is the bound address (host:port).
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Port is the bound TCP port.
func (s *Server) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

// Close stops listening.
func (s *Server) Close() error { return s.ln.Close() }

// SetDeclineAll flips the decline-everything failure mode at runtime.
func (s *Server) SetDeclineAll(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opts.DeclineAll = v
}

// Log returns every receipt printed so far, oldest first.
func (s *Server) Log() []Printed {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Printed, len(s.log))
	copy(out, s.log)
	return out
}

func (s *Server) accept() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.serve(conn)
	}
}

func (s *Server) serve(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	rd := bufio.NewReaderSize(conn, 64*1024)
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			return
		}
		if s.opts.Silent {
			select {} // hang forever: the client's read deadline must fire
		}
		resp := s.handle(line)
		if s.opts.Delay > 0 {
			time.Sleep(s.opts.Delay)
		}
		body, _ := json.Marshal(resp)
		body = append(body, '\n')
		if _, err := conn.Write(body); err != nil {
			return
		}
	}
}

// Handle answers one raw request line — exported so a test can drive the
// device logic without a socket.
func (s *Server) Handle(line []byte) map[string]any { return s.handle(line) }

func (s *Server) handle(line []byte) answer {
	var req struct {
		Op        string `json:"op"`
		RequestID string `json:"request_id"`
		Amount    int64  `json:"amount"`
		Total     int64  `json:"total"`
		Lines     []struct {
			Name      string  `json:"name"`
			Qty       float64 `json:"qty"`
			UnitPrice int64   `json:"unit_price"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(line, &req); err != nil {
		return answer{"ok": false, "error": "unparseable request"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch req.Op {
	case "status":
		return answer{"ok": true, "serial": s.opts.Serial, "maker": s.opts.Maker, "z_no": s.zNo, "receipts_today": s.today, "online": true}
	case "z_close":
		s.zNo++
		s.today = 0
		return answer{"ok": true, "serial": s.opts.Serial, "z_no": s.zNo}
	case "sale", "refund":
		if req.RequestID != "" {
			if prev, ok := s.seen[req.RequestID]; ok {
				return prev // already printed for this request: same evidence again
			}
		}
		if s.opts.DeclineAll {
			return answer{"ok": false, "error": "declined by device (simulator decline mode)"}
		}
		if req.Op == "sale" {
			if req.Amount <= 0 {
				return answer{"ok": false, "error": "amount must be positive"}
			}
			if req.Total != 0 && req.Amount != req.Total {
				return answer{"ok": false, "error": "split tender not supported"}
			}
			if len(req.Lines) == 0 {
				return answer{"ok": false, "error": "a fiscal receipt needs at least one line"}
			}
		}
		s.receiptNo++
		s.today++
		no := fmt.Sprintf("%07d", s.receiptNo)
		kind := "mali_fis"
		if req.Op == "refund" {
			kind = "iade_fisi"
		}
		s.log = append(s.log, Printed{Op: req.Op, RequestID: req.RequestID, Amount: req.Amount, Lines: len(req.Lines), ReceiptNo: no})
		resp := answer{
			"ok":           true,
			"kind":         "okc",
			"serial":       s.opts.Serial,
			"maker":        s.opts.Maker,
			"receipt_no":   no,
			"receipt_kind": kind,
			"z_no":         s.zNo,
			"issued_at":    time.Now().Format(time.RFC3339),
		}
		if req.RequestID != "" {
			s.seen[req.RequestID] = resp
		}
		return resp
	default:
		return answer{"ok": false, "error": "unknown op " + strings.TrimSpace(req.Op)}
	}
}
