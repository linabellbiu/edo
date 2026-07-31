//go:build mage && !linux && !darwin

package main

import (
	"errors"
	"os/exec"
)

var errLocalProcessControlUnsupported = errors.New("Mage 本地进程控制仅支持 Linux 和 macOS")

func configureLocalProcessCommand(_ *exec.Cmd) error {
	return errLocalProcessControlUnsupported
}

func inspectLocalProcess(_ int) (localProcessSnapshot, error) {
	return localProcessSnapshot{}, errLocalProcessControlUnsupported
}

func signalLocalProcessGroup(_ int, _ bool) error {
	return errLocalProcessControlUnsupported
}

func localProcessGroupRunning(_ int) (bool, error) {
	return false, errLocalProcessControlUnsupported
}
