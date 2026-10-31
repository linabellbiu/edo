package policy

import (
	"encoding/binary"
	"testing"

	"golang.org/x/net/bpf"
)

func TestNativePolicyIsClosedToUntracedAndForeignABI(t *testing.T) {
	for _, a := range []struct {
		name                 string
		audit, clone, ptrace uint32
	}{{"amd64", 0xc000003e, 56, 101}, {"arm64", 0xc00000b7, 220, 117}} {
		t.Run(a.name, func(t *testing.T) {
			instructions, err := Instructions(a.name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := bpf.Assemble(instructions); err != nil {
				t.Fatal(err)
			}
			vm, err := bpf.NewVM(instructions)
			if err != nil {
				t.Fatal(err)
			}
			cases := []struct {
				name                   string
				audit, nr, flags, want uint32
			}{
				{"ordinary", a.audit, 1, 0, Allow},
				{"fork_like_clone", a.audit, a.clone, 17, Allow},
				{"thread_clone", a.audit, a.clone, 0x10000 | 0x100 | 0x800, Allow},
				{"untraced", a.audit, a.clone, CloneUntraced, ErrnoEPERM},
				{"untraced_mixed", a.audit, a.clone, CloneUntraced | 17, ErrnoEPERM},
				{"clone3_fallback", a.audit, 435, 0, ErrnoENOSYS},
				{"nested_debugger", a.audit, a.ptrace, 0, ErrnoEPERM},
				{"x32", a.audit, 0x40000000 | a.clone, 0, ErrnoENOSYS},
				{"foreign_abi", 0x40000003, a.clone, 0, KillProcess},
			}
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					data := make([]byte, 64)
					// x/net 的网络包 VM 按大端装载；这里只验证与 seccomp 原生字装载等价的寄存器值，不冒充真实内核执行。
					binary.BigEndian.PutUint32(data[0:4], c.nr)
					binary.BigEndian.PutUint32(data[4:8], c.audit)
					binary.BigEndian.PutUint32(data[16:20], c.flags)
					got, err := vm.Run(data)
					if err != nil || uint32(got) != c.want {
						t.Fatalf("got=%x want=%x err=%v", got, c.want, err)
					}
				})
			}
		})
	}
}

func TestUnknownArchitectureIsRejected(t *testing.T) {
	if _, err := Instructions("386"); err == nil {
		t.Fatal("接受了兼容架构")
	}
}
