//go:build mage && darwin

package main

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func configureLocalProcessCommand(command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return nil
}

func inspectLocalProcess(pid int) (localProcessSnapshot, error) {
	processGroupID, err := syscall.Getpgid(pid)
	if errors.Is(err, syscall.ESRCH) {
		return localProcessSnapshot{}, os.ErrProcessDone
	}
	if err != nil {
		return localProcessSnapshot{}, err
	}
	stateOutput, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "stat=").Output()
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok && exitError.ExitCode() == 1 {
			return localProcessSnapshot{}, os.ErrProcessDone
		}
		return localProcessSnapshot{}, err
	}
	if strings.HasPrefix(strings.TrimSpace(string(stateOutput)), "Z") {
		return localProcessSnapshot{}, os.ErrProcessDone
	}
	output, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=").Output()
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok && exitError.ExitCode() == 1 {
			return localProcessSnapshot{}, os.ErrProcessDone
		}
		return localProcessSnapshot{}, err
	}
	identity := strings.TrimSpace(string(output))
	if identity == "" {
		return localProcessSnapshot{}, os.ErrProcessDone
	}
	return localProcessSnapshot{processGroupID: processGroupID, identity: identity}, nil
}

func signalLocalProcessGroup(processGroupID int, force bool) error {
	signal := syscall.SIGTERM
	if force {
		signal = syscall.SIGKILL
	}
	err := syscall.Kill(-processGroupID, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func localProcessGroupRunning(processGroupID int) (bool, error) {
	err := syscall.Kill(-processGroupID, 0)
	if err == nil || errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return false, err
}
