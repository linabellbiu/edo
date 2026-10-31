//go:build linux && (amd64 || arm64)

package terminalsession

import "edo/internal/terminalwire"

func shellPath(shell string) (string, bool) { return terminalwire.ShellPath(shell) }
