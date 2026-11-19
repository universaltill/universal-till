package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// The vendored AOSP files are pinned: editing one must fail here.
func TestVendoredSourcesPinned(t *testing.T) {
	for name, want := range map[string]string{
		"arm_app_policy.cpp":   "9718688cea3e3096cfbaf968694a36215542142329dcd979e3b4c791a2a5a358",
		"arm64_app_policy.cpp": "dee2d8d6a2f209bdd19adc83d28a81e42319ea91490c4c9084368eca904de815",
	} {
		b, err := aosp.ReadFile("aosp/" + name)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("%s sha256 = %s, want %s", name, got, want)
		}
	}
}

func TestParserIsStrict(t *testing.T) {
	if _, err := parse("BPF_STMT(BPF_LD|BPF_W|BPF_ABS, 0),\n"); err == nil {
		t.Error("an unknown BPF_ line must be an error, not skipped")
	}
	if _, err := parse("BPF_JUMP(BPF_JMP|BPF_JGE|BPF_K, 5, 300, 0),\n"); err == nil {
		t.Error("a jump offset over 255 must be an error")
	}
}

func TestAssemble(t *testing.T) {
	arm, err := parse(armSrc)
	if err != nil {
		t.Fatal(err)
	}
	a64, err := parse(arm64Src)
	if err != nil {
		t.Fatal(err)
	}
	if len(arm) != 129 || len(a64) != 57 {
		t.Fatalf("parsed arm=%d arm64=%d, want 129/57", len(arm), len(a64))
	}
	prog, err := Assemble()
	if err != nil {
		t.Fatal(err)
	}
	if len(prog) != 194 {
		t.Fatalf("len = %d, want 194", len(prog))
	}
	// seccomp_policy.cpp ValidateArchitectureAndJumpIfNeeded + ExamineSyscall,
	// written out instruction by instruction (code, jt, jf, k).
	trap := Ins{retK, 0, 0, RetTrap}
	for i, want := range []Ins{
		{ldWAbs, 0, 0, 4},      // arch
		{jeqK, 2, 0, ArchA64},  // primary: skip to the arm64 table
		{jeqK, 60, 0, ArchARM}, // secondary: patched jump to the arm table
		trap,                   // any other arch
		{ldWAbs, 0, 0, 0},      // nr, arm64 table follows
	} {
		if prog[i] != want {
			t.Errorf("prog[%d] = %+v, want %+v", i, prog[i], want)
		}
	}
	// The secondary jump lands on "load nr" right after the arm64 table's
	// trap, and the arm table follows it verbatim.
	tgt := 2 + 1 + int(prog[2].Jt)
	if prog[tgt-1] != trap || prog[tgt] != (Ins{ldWAbs, 0, 0, 0}) {
		t.Errorf("secondary jump target %d: %+v %+v, want RET_TRAP then LD nr", tgt, prog[tgt-1], prog[tgt])
	}
	for i, in := range arm {
		if prog[tgt+1+i] != in {
			t.Fatalf("arm table instruction %d differs at prog[%d]", i, tgt+1+i)
		}
	}
	for i, in := range a64 {
		if prog[5+i] != in {
			t.Fatalf("arm64 table instruction %d differs at prog[%d]", i, 5+i)
		}
	}
	if last := prog[len(prog)-1]; last.Code != retK || last.K != RetTrap {
		t.Errorf("last instruction = %+v, want RET_TRAP", last)
	}
	const golden = "a07ae56188a587ad1def205eda378778f27be8c4e223545f3539ddac07c5bf1a"
	if got := SHA256(prog); got != golden {
		t.Errorf("SHA256(Assemble()) = %s, want %s", got, golden)
	}
}

func TestEvalARM(t *testing.T) {
	prog, err := Assemble()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		nr   uint32
		want uint32
	}{
		{"statx", 397, RetTrap}, {"clock_gettime64", 403, RetTrap}, {"futex_time64", 422, RetTrap},
		{"read", 3, RetAllow}, {"restart_syscall", 0, RetAllow}, {"execve", 11, RetAllow},
		{"futex", 240, RetAllow}, {"getrandom", 384, RetAllow}, {"clock_gettime", 263, RetAllow},
		{"sigreturn", 119, RetAllow}, {"rt_sigreturn", 173, RetAllow}, {"prctl", 172, RetAllow},
		{"cacheflush", 983042, RetAllow}, {"set_tls", 983045, RetAllow},
		{"arm-private 983043", 983043, RetTrap}, {"arm-private 983044", 983044, RetTrap},
		{"arm-private 983046", 983046, RetTrap},
	} {
		if got := Eval(prog, ArchARM, c.nr); got != c.want {
			t.Errorf("%s (%d): got %#x want %#x", c.name, c.nr, got, c.want)
		}
	}
}

func TestEvalArch(t *testing.T) {
	prog, _ := Assemble()
	if got := Eval(prog, 0xC000003E, 3); got != RetTrap { // x86_64
		t.Errorf("x86_64 arch: got %#x, want RET_TRAP", got)
	}
	// AARCH64 follows the arm64 table: read is 63 there, 397 is not a syscall.
	if got := Eval(prog, ArchA64, 63); got != RetAllow {
		t.Errorf("aarch64 read(63): got %#x, want ALLOW", got)
	}
	if got := Eval(prog, ArchA64, 397); got != RetTrap {
		t.Errorf("aarch64 nr 397: got %#x, want TRAP", got)
	}
}

// Independent cross-check: the evaluator must allow exactly as many numbers as
// the names listed in the .cpp comments.
func TestAllowedCountMatchesComments(t *testing.T) {
	prog, _ := Assemble()
	n := 0
	for nr := uint32(0); nr < 0x100010; nr++ {
		if Eval(prog, ArchARM, nr) == RetAllow {
			n++
		}
	}
	names := 0
	for _, l := range strings.Split(armSrc, "\n") {
		if i := strings.Index(l, "//"); i >= 0 && strings.HasPrefix(l, "BPF_JUMP") {
			if c := strings.TrimSpace(l[i+2:]); c != "" {
				names += len(strings.Split(c, "|"))
			}
		}
	}
	if n != names {
		t.Errorf("evaluator allows %d numbers, the .cpp comments name %d syscalls", n, names)
	}
	if n != 233 {
		t.Errorf("allowed = %d, want 233", n)
	}
}
