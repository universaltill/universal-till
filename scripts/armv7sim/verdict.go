package main

import (
	"fmt"
	"regexp"
	"strings"
)

const wantKernel = "4.9.0-13-armmp-lpae"

var (
	readyRe = regexp.MustCompile(`^UTSIM READY kernel=(\S+) filter_len=(\d+) filter_sha256=([0-9a-f]{64})\b`)
	endRe   = regexp.MustCompile(`^UTSIM END (\S+) exited=(true|false) code=(-?\d+) signaled=(true|false) signal=(-?\d+) timeout=(true|false)`)
	r7Re    = regexp.MustCompile(`^r7\s+0x([0-9a-fA-F]+)\b`)
)

type scenario struct {
	exited, signaled, timeout bool
	code                      string
	block                     []string
}

// Verdict decides the simulator result from the guest console log. It returns
// the failed rules; an empty slice is green. wantSHA is the host's own
// SHA256(Assemble()), so a guest that installed any other program is red.
//
// Green needs all of: READY on the pinned kernel with the AOSP filter hash;
// the control (pristine, no filter) boots to "OK 200"; the pristine build under
// the filter is KILLED (signaled, SIGSYS with sigcode=1, first register dump
// r7=0x18d=statx); the fork build under the same filter boots to "OK 200"
// with no SIGSYS; DONE present, no FATAL, each scenario exactly once.
func Verdict(log, wantSHA string) []string {
	var fails []string
	fail := func(f string, a ...any) { fails = append(fails, fmt.Sprintf(f, a...)) }

	lines := strings.Split(strings.ReplaceAll(log, "\r", ""), "\n")

	ready := 0
	for _, l := range lines {
		if m := readyRe.FindStringSubmatch(l); m != nil {
			ready++
			if m[1] != wantKernel {
				fail("kernel is %q, want %q", m[1], wantKernel)
			}
			if m[3] != wantSHA {
				fail("filter_sha256 is %s, the host's AOSP program is %s: the guest did not install the AOSP filter", m[3], wantSHA)
			}
		}
		if strings.HasPrefix(l, "UTSIM FATAL") {
			fail("guest reported %q", l)
		}
	}
	if ready != 1 {
		fail("READY line (kernel, filter hash) seen %d times, want 1", ready)
	}
	if !containsLine(lines, "UTSIM DONE") {
		fail("UTSIM DONE missing: the guest did not finish all scenarios")
	}

	sc := map[string]*scenario{}
	for _, name := range []string{"control-pristine-nofilter", "pristine-filtered", "fork-filtered"} {
		s, err := parseScenario(lines, name)
		if err != "" {
			fail("%s", err)
			continue
		}
		sc[name] = s
		if s.timeout {
			fail("%s: timeout=true", name)
		}
	}

	if s := sc["control-pristine-nofilter"]; s != nil {
		if !(s.exited && s.code == "0" && !s.timeout) || !hasLine(s.block, "OK 200") {
			fail("control-pristine-nofilter: the pristine build must boot without the filter (exit 0 and OK 200); it did not, so the kernel/environment is broken")
		}
	}
	if s := sc["pristine-filtered"]; s != nil {
		if s.exited && s.code == "0" {
			fail("pristine-filtered: the pristine build SURVIVED the filter; the simulator cannot kill, so it proves nothing")
		}
		if !s.signaled {
			fail("pristine-filtered: process was not ended by a signal")
		}
		if !hasLine(s.block, "SIGSYS: bad system call") {
			fail("pristine-filtered: no 'SIGSYS: bad system call' in the output")
		}
		checkFirstDump(s.block, fail)
	}
	if s := sc["fork-filtered"]; s != nil {
		if !(s.exited && s.code == "0" && !s.timeout) {
			fail("fork-filtered: the fork build must exit 0 under the filter")
		}
		if !hasLine(s.block, "OK 200") {
			fail("fork-filtered: no 'OK 200' in the output")
		}
		for _, l := range s.block {
			if strings.Contains(l, "SIGSYS") {
				fail("fork-filtered: SIGSYS in the output: %q", l)
				break
			}
		}
	}
	return fails
}

func checkFirstDump(block []string, fail func(string, ...any)) {
	pc := -1
	for i, l := range block {
		// Go prints "SIGSYS: bad system call" then "PC=... sigcode=1"; the
		// pair must be adjacent (sigcode 1 alone is also SEGV_MAPERR etc.).
		if i > 0 && strings.TrimSpace(block[i-1]) == "SIGSYS: bad system call" &&
			strings.HasPrefix(l, "PC=") && strings.Contains(l, " sigcode=1") {
			pc = i
			break
		}
	}
	if pc < 0 {
		fail("pristine-filtered: no 'SIGSYS: bad system call' followed by 'PC=... sigcode=1' (SYS_SECCOMP): not a seccomp kill")
		return
	}
	for _, l := range block[pc+1:] {
		if m := r7Re.FindStringSubmatch(l); m != nil {
			if !strings.EqualFold(m[1], "18d") {
				fail("pristine-filtered: first register dump has r7=0x%s, want 0x18d (397, statx)", m[1])
			}
			return
		}
	}
	fail("pristine-filtered: no register dump with r7 after the sigcode=1 line (want r7 0x18d)")
}

func parseScenario(lines []string, name string) (*scenario, string) {
	begin, end := -1, -1
	nb, ne := 0, 0
	var s scenario
	for i, l := range lines {
		if l == "UTSIM BEGIN "+name {
			nb++
			begin = i
		}
		if m := endRe.FindStringSubmatch(l); m != nil && m[1] == name {
			ne++
			end = i
			s.exited, s.code, s.signaled, s.timeout = m[2] == "true", m[3], m[4] == "true", m[6] == "true"
		}
	}
	if nb != 1 || ne != 1 {
		return nil, fmt.Sprintf("scenario %s: BEGIN seen %d and END seen %d times, want exactly once each", name, nb, ne)
	}
	if end < begin {
		return nil, fmt.Sprintf("scenario %s: END before BEGIN", name)
	}
	s.block = lines[begin+1 : end]
	return &s, ""
}

func containsLine(lines []string, want string) bool { return hasLine(lines, want) }

func hasLine(lines []string, want string) bool {
	for _, l := range lines {
		if strings.TrimSpace(l) == want {
			return true
		}
	}
	return false
}
