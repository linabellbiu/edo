package host

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseResourceInventory(t *testing.T) {
	for _, test := range []struct {
		name, output, os, state string
		cpu                     int
		memory, disk            uint64
	}{
		{"Linux 容量换算", "os\tLinux\ncpu\t8\nmemory_kib\t16777216\ndisk_kib\t104857600\nend\t1\n", "linux", "ok", 8, 16 << 30, 100 << 30},
		{"macOS 原始字节", "os\tDarwin\ncpu\t10\nmemory_bytes\t34359738368\ndisk_kib\t209715200\nend\t1\n", "darwin", "ok", 10, 32 << 30, 200 << 30},
		{"缺测不是零值", "os\tLinux\ncpu\t\nmemory_kib\t\ndisk_kib\t\nend\t1\n", "linux", "unavailable", 0, 0, 0},
		{"零容量不可用", "os\tLinux\ncpu\t0\nmemory_kib\t0\ndisk_kib\t0\nend\t1\n", "linux", "unavailable", 0, 0, 0},
		{"非法容量独立缺测", "os\tLinux\ncpu\t-2\nmemory_kib\tNaN\ndisk_kib\t100\nend\t1\n", "linux", "ok", 0, 0, 102400},
		{"溢出不能回绕", "os\tLinux\ncpu\t18446744073709551616\nmemory_kib\t18446744073709551615\ndisk_kib\t18446744073709551616\nend\t1\n", "linux", "unavailable", 0, 0, 0},
		{"仅 CPU 可读仍可展示", "os\tLinux\ncpu\t2\nmemory_kib\tunreadable\ndisk_kib\t-1\nend\t1\n", "linux", "ok", 2, 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseResourceInventory(test.output)
			if err != nil {
				t.Fatal(err)
			}
			if got.OS != test.os || got.State != test.state || got.Scope != "current_environment" || got.DiskMount != "/" {
				t.Fatalf("资源范围或状态错误: %#v", got)
			}
			assertResourceInventoryInt(t, "cpu", got.CPUCores, test.cpu)
			assertResourceInventoryBytes(t, "memory", got.MemoryTotalBytes, test.memory)
			assertResourceInventoryBytes(t, "disk", got.DiskTotalBytes, test.disk)
		})
	}
}

func TestHostResourcesLocalCollectorUsesCurrentSystem(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := collectLocalResources(ctx)
	if err != nil || result.State != "ok" || result.OS == "" {
		t.Fatalf("读取当前系统真实资源失败: state=%s err=%v", result.State, err)
	}
	if result.CPUCores == nil || *result.CPUCores <= 0 || result.MemoryTotalBytes == nil || *result.MemoryTotalBytes == 0 || result.DiskTotalBytes == nil || *result.DiskTotalBytes == 0 {
		t.Fatal("当前系统 CPU、内存或根文件系统容量缺失")
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := collectLocalResources(canceled); err != context.Canceled {
		t.Fatalf("采集未保留取消语义: %v", err)
	}
}

func TestParseResourceInventoryRejectsIncompleteOrAmbiguousProtocol(t *testing.T) {
	valid := "os\tLinux\ncpu\t4\nmemory_kib\t1024\ndisk_kib\t2048\nend\t1\n"
	for _, test := range []struct{ name, output string }{
		{"无结束标记", strings.TrimSuffix(valid, "end\t1\n")},
		{"错误结束值", strings.Replace(valid, "end\t1", "end\t0", 1)},
		{"重复键", strings.Replace(valid, "cpu\t4", "cpu\t4\ncpu\t6", 1)},
		{"未知键", strings.Replace(valid, "end\t1", "token\tprivate-value\nend\t1", 1)},
		{"双内存单位", strings.Replace(valid, "disk_kib", "memory_bytes\t1048576\ndisk_kib", 1)},
		{"结束后仍有记录", valid + "cpu\t9\n"},
		{"重复结束标记", valid + "end\t1\n"},
		{"缺少 TAB", strings.Replace(valid, "cpu\t4", "cpu=4", 1)},
		{"未知操作系统", strings.Replace(valid, "Linux", "OtherOS", 1)},
		{"响应超限", valid + strings.Repeat("x", 4097)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseResourceInventory(test.output); err == nil {
				t.Fatal("无效或不完整的资源协议被接受")
			}
		})
	}
}

func TestHostResourceProbeCommand(t *testing.T) {
	for _, test := range []struct {
		name, os, failure, state string
		cpu                      int
		memory, disk             uint64
	}{
		{"Linux 固定只读采集", "Linux", "", "ok", 6, 8 << 30, 50 << 30},
		{"macOS 固定只读采集", "Darwin", "", "ok", 6, 8 << 30, 50 << 30},
		{"Linux 容量缺测", "Linux", "1", "unavailable", 0, 0, 0},
		{"macOS 容量缺测", "Darwin", "1", "unavailable", 0, 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			bin := t.TempDir()
			writeResourceInventoryStub(t, bin, "uname", `case "$1" in -s) printf '%s\n' "$EDO_RESOURCE_TEST_OS";; -m) printf 'x86_64\n';; *) exit 1;; esac`)
			writeResourceInventoryStub(t, bin, "getconf", `[ -z "$EDO_RESOURCE_TEST_FAILURE" ] || exit 1
[ "$1" = _NPROCESSORS_ONLN ] || exit 1
printf '6\n'`)
			writeResourceInventoryStub(t, bin, "sysctl", `[ -z "$EDO_RESOURCE_TEST_FAILURE" ] || exit 1
case "$*" in '-n hw.ncpu'|'-n hw.logicalcpu') printf '6\n';; '-n hw.memsize') printf '8589934592\n';; *) exit 1;; esac`)
			writeResourceInventoryStub(t, bin, "awk", `case "$*" in *'/proc/meminfo'*) [ -z "$EDO_RESOURCE_TEST_FAILURE" ] || exit 1; printf '8388608\n';; *) exec /usr/bin/awk "$@";; esac`)
			writeResourceInventoryStub(t, bin, "df", `[ -z "$EDO_RESOURCE_TEST_FAILURE" ] || exit 1
[ "$*" = '-Pk /' ] || exit 1
printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\nfixture 52428800 10485760 41943040 20%% /\n'`)
			for _, forbidden := range []string{"sudo", "docker", "ssh", "curl", "wget"} {
				writeResourceInventoryStub(t, bin, forbidden, "printf 'FORBIDDEN_COMMAND\\n' >&2; exit 97")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "/bin/sh", "-s")
			command.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "LC_ALL=C", "EDO_RESOURCE_TEST_OS=" + test.os, "EDO_RESOURCE_TEST_FAILURE=" + test.failure}
			command.Stdin = strings.NewReader(hostResourceProbeCommand)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("固定采集脚本执行失败: %v\n%s", err, output)
			}
			if strings.Contains(string(output), "FORBIDDEN_COMMAND") {
				t.Fatal("资源采集执行了非只读探测命令")
			}
			got, err := parseResourceInventory(string(output))
			if err != nil {
				t.Fatalf("固定脚本输出不能解析: %v\n%s", err, output)
			}
			if got.State != test.state || got.OS != strings.ToLower(test.os) {
				t.Fatalf("固定脚本状态错误: %#v", got)
			}
			assertResourceInventoryInt(t, "cpu", got.CPUCores, test.cpu)
			assertResourceInventoryBytes(t, "memory", got.MemoryTotalBytes, test.memory)
			assertResourceInventoryBytes(t, "disk", got.DiskTotalBytes, test.disk)
		})
	}
}

func writeResourceInventoryStub(t *testing.T, directory, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), []byte("#!/bin/sh\n"+script+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
}

func assertResourceInventoryInt(t *testing.T, name string, got *int, want int) {
	t.Helper()
	if want == 0 {
		if got != nil {
			t.Fatalf("%s 缺测应为 NULL，实际 %d", name, *got)
		}
		return
	}
	if got == nil || *got != want {
		t.Fatalf("%s 错误，期望 %s，实际 %v", name, strconv.Itoa(want), got)
	}
}

func assertResourceInventoryBytes(t *testing.T, name string, got *uint64, want uint64) {
	t.Helper()
	if want == 0 {
		if got != nil {
			t.Fatalf("%s 缺测应为 NULL，实际 %d", name, *got)
		}
		return
	}
	if got == nil || *got != want {
		t.Fatalf("%s 错误，期望 %d 字节，实际 %v", name, want, got)
	}
}
