//go:build linux && (amd64 || arm64)

package terminalsession

import "syscall"

type unixProcAttr = syscall.SysProcAttr
