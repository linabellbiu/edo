//go:build mage && linux

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

func configureLocalProcessCommand(command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return nil
}

func inspectLocalProcess(pid int) (localProcessSnapshot, error) {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if errors.Is(err, os.ErrNotExist) {
		return localProcessSnapshot{}, os.ErrProcessDone
	}
	if err != nil {
		return localProcessSnapshot{}, err
	}
	closingParenthesis := strings.LastIndexByte(string(stat), ')')
	if closingParenthesis < 0 {
		return localProcessSnapshot{}, errors.New("无法解析 /proc 进程状态")
	}
	fields := strings.Fields(string(stat[closingParenthesis+1:]))
	if len(fields) < 20 {
		return localProcessSnapshot{}, errors.New("/proc 进程状态字段不足")
	}
	if fields[0] == "Z" {
		return localProcessSnapshot{}, os.ErrProcessDone
	}
	processGroupID, err := syscall.Getpgid(pid)
	if errors.Is(err, syscall.ESRCH) {
		return localProcessSnapshot{}, os.ErrProcessDone
	}
	if err != nil {
		return localProcessSnapshot{}, err
	}
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return localProcessSnapshot{}, err
	}
	return localProcessSnapshot{
		processGroupID: processGroupID,
		identity:       strings.TrimSpace(string(bootID)) + ":" + fields[19],
	}, nil
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
