// Package policy reproduces the Android 9 (AOSP android-9.0.0_r61) app seccomp
// filter for 32-bit ARM apps: the checked-in arm/arm64 BPF tables parsed from
// the vendored .cpp files and assembled the way bionic's seccomp_policy.cpp
// _set_seccomp_filter(APP) does on __arm__ (dual arch, arm64 primary, arm
// secondary, SECCOMP_RET_TRAP). Pure Go, all platforms (ut-docs#6300).
package policy

import (
	"crypto/sha256"
	"embed"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

//go:embed aosp/*.cpp
var aosp embed.FS

var (
	armSrc   = mustRead("aosp/arm_app_policy.cpp")
	arm64Src = mustRead("aosp/arm64_app_policy.cpp")
)

func mustRead(name string) string {
	b, err := aosp.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// Ins is one classic-BPF instruction (struct sock_filter).
type Ins struct {
	Code   uint16
	Jt, Jf uint8
	K      uint32
}

const (
	ldWAbs   = 0x20 // BPF_LD|BPF_W|BPF_ABS
	jeqK     = 0x15 // BPF_JMP|BPF_JEQ|BPF_K
	jgeK     = 0x35 // BPF_JMP|BPF_JGE|BPF_K
	retK     = 0x06 // BPF_RET|BPF_K
	RetAllow = 0x7fff0000
	RetTrap  = 0x00030000 // SECCOMP_RET_TRAP: SIGSYS, si_code SYS_SECCOMP
	ArchARM  = 0x40000028 // AUDIT_ARCH_ARM
	ArchA64  = 0xC00000B7 // AUDIT_ARCH_AARCH64
)

var jumpRe = regexp.MustCompile(`^BPF_JUMP\(BPF_JMP\|BPF_JGE\|BPF_K, (\d+), (\d+), (\d+)\),`)

const retAllowLine = "BPF_STMT(BPF_RET|BPF_K, SECCOMP_RET_ALLOW),"

// parse is strict: a line starting BPF_ that is not a JGE jump or the
// RET_ALLOW statement is an error, as is an operand that does not fit.
func parse(src string) ([]Ins, error) {
	var out []Ins
	for n, l := range strings.Split(src, "\n") {
		l = strings.TrimSpace(l)
		if m := jumpRe.FindStringSubmatch(l); m != nil {
			k, err := strconv.ParseUint(m[1], 10, 32)
			if err != nil {
				return nil, fmt.Errorf("line %d: %v", n+1, err)
			}
			jt, err := strconv.ParseUint(m[2], 10, 8)
			if err != nil {
				return nil, fmt.Errorf("line %d: jt: %v", n+1, err)
			}
			jf, err := strconv.ParseUint(m[3], 10, 8)
			if err != nil {
				return nil, fmt.Errorf("line %d: jf: %v", n+1, err)
			}
			out = append(out, Ins{jgeK, uint8(jt), uint8(jf), uint32(k)})
		} else if strings.HasPrefix(l, retAllowLine) {
			out = append(out, Ins{retK, 0, 0, RetAllow})
		} else if strings.HasPrefix(l, "BPF_") {
			return nil, fmt.Errorf("line %d: unsupported BPF line %q", n+1, l)
		}
	}
	return out, nil
}

// Assemble mirrors seccomp_policy.cpp _set_seccomp_filter(APP) on __arm__.
func Assemble() ([]Ins, error) {
	a64, err := parse(arm64Src)
	if err != nil {
		return nil, fmt.Errorf("arm64: %w", err)
	}
	a32, err := parse(armSrc)
	if err != nil {
		return nil, fmt.Errorf("arm: %w", err)
	}
	trap := Ins{retK, 0, 0, RetTrap}
	f := []Ins{{ldWAbs, 0, 0, 4}, {jeqK, 2, 0, ArchA64}, {jeqK, 1, 0, ArchARM}, trap}
	secondary := len(f) - 2 // the JEQ ARM, patched below
	f = append(f, Ins{ldWAbs, 0, 0, 0})
	f = append(f, a64...)
	f = append(f, trap)
	jl := len(f) - secondary - 1
	if jl > 255 { // AOSP LOG(FATAL)s here too
		return nil, fmt.Errorf("secondary jump %d does not fit in 8 bits", jl)
	}
	f[secondary] = Ins{jeqK, uint8(jl), 0, ArchARM}
	f = append(f, Ins{ldWAbs, 0, 0, 0})
	f = append(f, a32...)
	f = append(f, trap)
	return f, nil
}

// Eval runs the program for a syscall (arch, nr) like the kernel would and
// returns the SECCOMP_RET_* value. An arch other than ARM/AARCH64 reaches the
// RET_TRAP after the two arch compares, as in AOSP.
func Eval(f []Ins, arch, nr uint32) uint32 {
	var a uint32
	for pc := 0; pc < len(f); pc++ {
		i := f[pc]
		switch i.Code {
		case ldWAbs:
			switch i.K {
			case 0:
				a = nr
			case 4:
				a = arch
			default:
				panic(fmt.Sprintf("policy.Eval: load offset %d", i.K))
			}
		case jeqK:
			if a == i.K {
				pc += int(i.Jt)
			} else {
				pc += int(i.Jf)
			}
		case jgeK:
			if a >= i.K {
				pc += int(i.Jt)
			} else {
				pc += int(i.Jf)
			}
		case retK:
			return i.K
		default:
			panic(fmt.Sprintf("policy.Eval: opcode %#x", i.Code))
		}
	}
	panic("policy.Eval: fell off the end of the program")
}

// Bytes serialises the program as the kernel's struct sock_filter array
// (little-endian: the guest is ARM LE, and so are the hosts that run this).
func Bytes(f []Ins) []byte {
	b := make([]byte, 0, 8*len(f))
	for _, i := range f {
		b = binary.LittleEndian.AppendUint16(b, i.Code)
		b = append(b, i.Jt, i.Jf)
		b = binary.LittleEndian.AppendUint32(b, i.K)
	}
	return b
}

// SHA256 is the hex digest of Bytes(f); host and guest compare it.
func SHA256(f []Ins) string {
	s := sha256.Sum256(Bytes(f))
	return hex.EncodeToString(s[:])
}
