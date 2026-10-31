package policy

import (
	"errors"

	"golang.org/x/net/bpf"
)

const (
	Allow         = uint32(0x7fff0000)
	KillProcess   = uint32(0x80000000)
	ErrnoEPERM    = uint32(0x00050001)
	ErrnoENOSYS   = uint32(0x00050026)
	CloneUntraced = uint32(0x00800000)
)

// 这里只支持产品已有的两种 Linux 原生 64 位 ABI，不接受兼容 ABI 绕过规则。
func Instructions(arch string) ([]bpf.Instruction, error) {
	var audit, clone, ptrace uint32
	switch arch {
	case "amd64":
		audit, clone, ptrace = 0xc000003e, 56, 101
	case "arm64":
		audit, clone, ptrace = 0xc00000b7, 220, 117
	default:
		return nil, errors.New("不支持的会话运行架构")
	}
	return []bpf.Instruction{
		bpf.LoadAbsolute{Off: 4, Size: 4},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: audit, SkipTrue: 1},
		bpf.RetConstant{Val: KillProcess},
		bpf.LoadAbsolute{Off: 0, Size: 4},
		bpf.JumpIf{Cond: bpf.JumpBitsSet, Val: 0x40000000, SkipFalse: 1},
		bpf.RetConstant{Val: ErrnoENOSYS},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: 435, SkipFalse: 1},
		bpf.RetConstant{Val: ErrnoENOSYS},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: ptrace, SkipFalse: 1},
		bpf.RetConstant{Val: ErrnoEPERM},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: clone, SkipFalse: 3},
		bpf.LoadAbsolute{Off: 16, Size: 4},
		bpf.JumpIf{Cond: bpf.JumpBitsSet, Val: CloneUntraced, SkipFalse: 1},
		bpf.RetConstant{Val: ErrnoEPERM},
		bpf.RetConstant{Val: Allow},
	}, nil
}
