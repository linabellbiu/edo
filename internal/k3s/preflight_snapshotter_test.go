package k3s

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestSnapshotterPreflightRejectsIncompatibleDefaultFilesystem(t *testing.T) {
	for _, fs := range []string{"overlay", "overlayfs", "aufs", "nfs", "nfs4", "cifs", "smb2", "ecryptfs", "zfs"} {
		t.Run(fs, func(t *testing.T) {
			assertSnapshotterProbe(t, nil, fs, "0", "failed")
		})
	}
	assertSnapshotterProbe(t, map[string]any{"snapshotter": "overlayfs"}, "overlay", "0", "failed")
}

func TestSnapshotterPreflightDoesNotInventCompatibility(t *testing.T) {
	for _, fs := range []string{"ext2/ext3", "ext4", "btrfs"} {
		assertSnapshotterProbe(t, nil, fs, "0", "passed")
	}
	for _, fs := range []string{"xfs", "tmpfs", "unknown", "", "overlay\nEDO_K3S_CHECK\tsnapshotter\tpassed"} {
		assertSnapshotterProbe(t, nil, fs, "0", "warning")
	}
	assertSnapshotterProbe(t, nil, "overlay", "1", "warning")
	for _, options := range []map[string]any{
		{"snapshotter": "native"}, {"snapshotter": "fuse-overlayfs"},
		{"docker": true}, {"disable-agent": true}, {"container-runtime-endpoint": "unix:///run/custom.sock"},
	} {
		assertSnapshotterProbe(t, options, "overlay", "0", "warning")
	}
}

func assertSnapshotterProbe(t *testing.T, options map[string]any, filesystem, status, want string) {
	t.Helper()
	directory := t.TempDir()
	writeLinuxToolFixtures(t, directory)
	writePreflightCommand(t, directory, "stat", "printf '%s' \"$EDO_TEST_FS\"\nexit \"$EDO_TEST_FS_STATUS\"\n")
	script := "set +e\nset -u\nedo_data=" + shellQuote(directory+"/does-not-exist") + "\nedo_check() { printf '%s:%s\\n' \"$1\" \"$2\"; }\n" + snapshotterPreflightScript(options)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "sh", "-se")
	command.Stdin = strings.NewReader(script)
	command.Env = append(os.Environ(), "PATH="+directory+":"+os.Getenv("PATH"), "EDO_TEST_FS="+filesystem, "EDO_TEST_FS_STATUS="+status)
	output, err := command.CombinedOutput()
	if err != nil || string(output) != "snapshotter:"+want+"\n" {
		t.Fatalf("快照预检结果错误: %q, %v", output, err)
	}
	if strings.Contains(script, "mkdir") || strings.Contains(script, "mount ") || strings.Contains(script, "native\n") {
		t.Fatal("只读快照检查不得创建目录、挂载或修改安装配置")
	}
	if _, err := os.Stat(directory + "/does-not-exist"); !os.IsNotExist(err) {
		t.Fatal("只读检查写入了数据目录")
	}
}

func TestPreflightSnapshotterUsesFinalRoleDataDirectory(t *testing.T) {
	input := Input{ServerOptions: map[string]any{"data-dir": "/server-data"}, AgentOptions: map[string]any{"data-dir": "/agent-data", "snapshotter": "native"}}
	server := preflightEnvironmentScript(Host{Role: "server"}, input, false)
	agent := preflightEnvironmentScript(Host{Role: "agent"}, input, false)
	if !strings.Contains(server, "edo_data='/server-data'") || !strings.Contains(server, "stat -f -c %T") || strings.Contains(server, "/agent-data") {
		t.Fatal("控制平面未检查最终数据目录")
	}
	if !strings.Contains(agent, "edo_data='/agent-data'") || !strings.Contains(agent, "edo_check snapshotter warning") || strings.Contains(agent, "stat -f -c %T") {
		t.Fatal("工作节点未采用最终快照配置")
	}
	blocked := parsePreflightProbe(Host{}, completePreflightFixture(map[string]string{"snapshotter": "failed"}), nil)
	if blocked.Passed || preflightStatus(blocked, "snapshotter") != "failed" {
		t.Fatal("不兼容快照文件系统未阻止安装")
	}
}
