package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeServiceProbeOnlyReportsFixedShapeAndPID(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		command                        []byte
		active, pid, exe, stage, shape string
	}{
		{"original", []byte("/usr/local/bin/k3s\x00server\x00--config\x00/etc/rancher/k3s/config.yaml\x00"), "1", "123", "/usr/local/bin/k3s", "matched", "OriginalArgv=1 TitleAbsolute=0 TitleBasename=0"},
		{"absolute-title", []byte("/usr/local/bin/k3s server\x00\x00\x00"), "1", "123", "/usr/local/bin/k3s", "unexpected_arguments", "OriginalArgv=0 TitleAbsolute=1 TitleBasename=0"},
		{"basename-title", []byte("k3s server\x00\x00\x00"), "1", "123", "/usr/local/bin/k3s", "unexpected_arguments", "OriginalArgv=0 TitleAbsolute=0 TitleBasename=1"},
		{"private-unknown", []byte("k3s server --token PRIVATE_SECRET\x00"), "1", "123", "/usr/local/bin/k3s", "unexpected_arguments", "OriginalArgv=0 TitleAbsolute=0 TitleBasename=0"},
		{"wrong-exe", []byte("k3s server\x00"), "1", "123", "/private/PRIVATE_SECRET", "unexpected_executable", "OriginalArgv=0 TitleAbsolute=0 TitleBasename=1"},
		{"invalid-pid", nil, "1", "PRIVATE_SECRET", "/usr/local/bin/k3s", "invalid_pid", "OriginalArgv=0 TitleAbsolute=0 TitleBasename=0"},
		{"inactive", nil, "0", "0", "/usr/local/bin/k3s", "inactive", "OriginalArgv=0 TitleAbsolute=0 TitleBasename=0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			proc := filepath.Join(root, "proc")
			if os.Mkdir(bin, 0700) != nil || os.Mkdir(proc, 0700) != nil {
				t.Fatal("创建夹具失败")
			}
			write := func(name, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			write("systemctl", "case \"$1\" in is-active) [ '"+tc.active+"' = 1 ];; show) printf '%s\\n' '"+tc.pid+"';; *)exit 1;;esac\n")
			write("readlink", "printf '%s\\n' '"+tc.exe+"'\n")
			if tc.pid == "123" {
				directory := filepath.Join(proc, tc.pid)
				if os.Mkdir(directory, 0700) != nil {
					t.Fatal("创建进程夹具失败")
				}
				if err := os.WriteFile(filepath.Join(directory, "cmdline"), tc.command, 0600); err != nil {
					t.Fatal(err)
				}
			}
			script := "set -eu\nPATH='" + bin + ":" + os.Getenv("PATH") + "'\nexport PATH\n" + strings.ReplaceAll(nativeServiceProbe, "/proc/", proc+"/")
			cmd := exec.Command("sh", "-se")
			cmd.Stdin = strings.NewReader(script)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("只读探针失败 %v stderr=%q", err, stderr.String())
			}
			value := stdout.String()
			if !strings.Contains(value, "K3S_IDENTITY_STAGE="+tc.stage+" ") || !strings.Contains(value, tc.shape) || strings.Contains(value, "PRIVATE") || strings.Contains(stderr.String(), "PRIVATE") {
				t.Fatalf("形态判断或保密失败: %q", value)
			}
			if tc.pid == "PRIVATE_SECRET" && !strings.Contains(value, "MainPID=0") {
				t.Fatal("无效PID原文被输出")
			}
		})
	}
	if !strings.Contains(preflightScript, nativeServiceProbe) || strings.Contains(nativeServiceProbe, "systemctl restart") || strings.Contains(nativeServiceProbe, "systemctl stop") {
		t.Fatal("默认探针接线错误或发生写入")
	}
	if !strings.Contains(nativeSourceCommon, "[ \"$edo_command\" = \"$(printf '/usr/local/bin/k3s\\nserver\\n--config\\n/etc/rancher/k3s/config.yaml')\" ] || edo_fail service") {
		t.Fatal("诊断修改放宽了原维护守卫")
	}
}
