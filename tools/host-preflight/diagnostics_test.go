package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type fixtureRemoteExit int

func (fixtureRemoteExit) Error() string         { return "DO_NOT_PRINT_REMOTE_ERROR" }
func (value fixtureRemoteExit) ExitStatus() int { return int(value) }

func TestPreflightFailureDiagnosticsOnlyAcceptFixedStagesAndNumericCodes(t *testing.T) {
	for _, scenario := range []struct {
		name, value, stage string
		code               int
	}{
		{"query", "EDO_HOST_PREFLIGHT_STAGE=k3d_identity\nEDO_HOST_PREFLIGHT_STAGE=K3D_NAMESPACES\nEDO_HOST_PREFLIGHT_QUERY_EXIT=127\n", "K3D_NAMESPACES", 127},
		{"identity", "EDO_HOST_PREFLIGHT_STAGE=k3d_identity\nDO_NOT_PRINT_PRIVATE_STDERR\n", "k3d_identity", -1},
		{"unknown stage", "EDO_HOST_PREFLIGHT_STAGE=DO_NOT_PRINT_PRIVATE_STDERR\n", "unknown", -1},
		{"invalid number", "EDO_HOST_PREFLIGHT_STAGE=K3D_PODS\nEDO_HOST_PREFLIGHT_QUERY_EXIT=DO_NOT_PRINT_PRIVATE_STDERR\n", "K3D_PODS", -1},
		{"overflow", "EDO_HOST_PREFLIGHT_STAGE=K3D_PODS\nEDO_HOST_PREFLIGHT_QUERY_EXIT=256\n", "K3D_PODS", -1},
		{"negative", "EDO_HOST_PREFLIGHT_STAGE=K3D_PODS\nEDO_HOST_PREFLIGHT_QUERY_EXIT=-1\n", "K3D_PODS", -1},
		{"noncanonical", "EDO_HOST_PREFLIGHT_STAGE=K3D_PODS\nEDO_HOST_PREFLIGHT_QUERY_EXIT=01\n", "K3D_PODS", -1},
		{"later stage", "EDO_HOST_PREFLIGHT_STAGE=K3D_PODS\nEDO_HOST_PREFLIGHT_QUERY_EXIT=1\nEDO_HOST_PREFLIGHT_STAGE=K3D_JOBS\n", "K3D_JOBS", -1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var buffer limitedBuffer
			_, _ = buffer.Write([]byte(scenario.value))
			stage, code := readPreflightDiagnostics(&buffer)
			if stage != scenario.stage || code != scenario.code || strings.Contains(stage, "DO_NOT_PRINT") {
				t.Fatalf("未限制安全诊断: %q %d", stage, code)
			}
		})
	}
	var exceeded limitedBuffer
	_, _ = exceeded.Write([]byte("EDO_HOST_PREFLIGHT_STAGE=K3D_PODS\n" + strings.Repeat("x", 256*1024)))
	if stage, code := readPreflightDiagnostics(&exceeded); stage != "unknown" || code != -1 {
		t.Fatal("截断的诊断不能冒充完整阶段")
	}
}

func TestPreflightFailureKeepsExitStatusButNeverEmitsRawData(t *testing.T) {
	for _, status := range []int{1, 76, 77, 78, 79, 80, 255} {
		var buffer limitedBuffer
		_, _ = buffer.Write([]byte("DO_NOT_PRINT_ENV_OR_TEMPLATE_OBJECT\nHOST_PREFLIGHT_END\n"))
		var output bytes.Buffer
		err := writePreflightReport(&output, &buffer, fixtureRemoteExit(status), nil)
		var diagnostic *preflightQueryError
		if !errors.As(err, &diagnostic) || diagnostic.exitCode != status || output.Len() != 0 || strings.Contains(err.Error(), "DO_NOT_PRINT") {
			t.Fatal("退出码遗失或失败数据进入输出")
		}
	}
	if preflightExitCode(errors.New("DO_NOT_PRINT_PRIVATE_ERR")) != -1 || preflightExitCode(fixtureRemoteExit(256)) != -1 {
		t.Fatal("非退出状态被当成可信退出码")
	}
	for _, scenario := range []struct{ content, reason string }{
		{"DO_NOT_PRINT_UNFINISHED_OBJECT", "missing_end_marker"},
		{strings.Repeat("x", 256*1024+1), "output_limit"},
	} {
		var buffer limitedBuffer
		_, _ = buffer.Write([]byte(scenario.content))
		var output bytes.Buffer
		var diagnostic *preflightQueryError
		err := writePreflightReport(&output, &buffer, nil, nil)
		if !errors.As(err, &diagnostic) || diagnostic.reason != scenario.reason || diagnostic.exitCode != 0 || output.Len() != 0 {
			t.Fatal("输出校验故障未安全区分")
		}
	}
}

func TestConfigMetadataProbeOnlyAcceptsSingleFourBitProtocol(t *testing.T) {
	for mask := range 16 {
		bits := []byte("0000")
		for bit := range 4 {
			if mask&(1<<bit) != 0 {
				bits[bit] = '1'
			}
		}
		var buffer limitedBuffer
		_, _ = buffer.Write([]byte("EDO_HOST_PREFLIGHT_STAGE=k3d_config_probe\nEDO_HOST_PREFLIGHT_CONFIG_META=" + string(bits) + "\nEDO_HOST_PREFLIGHT_STAGE=K3D_NAMESPACES\n"))
		meta := readPreflightConfigMetadata(&buffer)
		if meta == nil || meta.etcReadable != (bits[0] == '1') || meta.outputReadable != (bits[1] == '1') || meta.etcSymlink != (bits[2] == '1') || meta.nullCharacterDevice != (bits[3] == '1') {
			t.Fatal("四位元数据含义未严格保留")
		}
	}
	for _, value := range []string{"", "1", "111", "11111", "10x1", "true", "1011\nEDO_HOST_PREFLIGHT_CONFIG_META=1011", "1011\nEDO_HOST_PREFLIGHT_CONFIG_META=DO_NOT_PRINT_META", "1011 DO_NOT_PRINT_META"} {
		var buffer limitedBuffer
		_, _ = buffer.Write([]byte("EDO_HOST_PREFLIGHT_STAGE=k3d_config_probe\nEDO_HOST_PREFLIGHT_CONFIG_META=" + value + "\n"))
		if readPreflightConfigMetadata(&buffer) != nil {
			t.Fatal("非单一四位协议被当成元数据")
		}
	}
	var wrongStage limitedBuffer
	_, _ = wrongStage.Write([]byte("EDO_HOST_PREFLIGHT_STAGE=K3D_NAMESPACES\nEDO_HOST_PREFLIGHT_CONFIG_META=1111\n"))
	if readPreflightConfigMetadata(&wrongStage) != nil {
		t.Fatal("查询阶段的非协议正文被当成配置探针")
	}
	for _, unsafe := range []string{"cat ", "readlink", "export ", "--kubeconfig=/output"} {
		if strings.Contains(k3dInventoryPrelude, unsafe) {
			t.Fatal("配置探针读取正文或自动切换了目标")
		}
	}
}

func TestConfigMetadataProbeRunsOnlyFileTestsWithoutReadingContents(t *testing.T) {
	_, remainder, ok := strings.Cut(k3dInventoryPrelude, " /bin/sh -c '")
	if !ok {
		t.Fatal("缺少固定元数据探针")
	}
	probe, _, ok := strings.Cut(remainder, "' 2>/dev/null")
	if !ok {
		t.Fatal("固定元数据探针边界变化")
	}
	probe = strings.ReplaceAll(probe, "/etc/rancher/k3s/k3s.yaml", `"$EDO_FIXTURE_ETC"`)
	probe = strings.ReplaceAll(probe, "/output/kubeconfig.yaml", `"$EDO_FIXTURE_OUTPUT"`)
	probe = strings.ReplaceAll(probe, "/dev/null", `"$EDO_FIXTURE_NULL"`)
	for _, scenario := range []struct{ name, expected string }{
		{"regular", "1101"}, {"symlink", "1111"}, {"missing", "0101"}, {"directory", "0101"}, {"broken-symlink", "0011"}, {"regular-null", "1100"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			directory := t.TempDir()
			etc, output, null := filepath.Join(directory, "etc-config"), filepath.Join(directory, "output-config"), "/dev/null"
			if scenario.name != "broken-symlink" {
				if err := os.WriteFile(output, []byte("DO_NOT_PRINT_OUTPUT_CONFIG"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			switch scenario.name {
			case "symlink", "broken-symlink":
				err = os.Symlink(output, etc)
			case "directory":
				err = os.Mkdir(etc, 0700)
			case "missing":
			default:
				err = os.WriteFile(etc, []byte("DO_NOT_PRINT_ETC_CONFIG"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario.name == "regular-null" {
				null = filepath.Join(directory, "regular-null")
				if err := os.WriteFile(null, []byte("DO_NOT_PRINT_NULL_CONTENT"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command("/bin/sh", "-c", probe)
			command.Env = append(os.Environ(), "EDO_FIXTURE_ETC="+etc, "EDO_FIXTURE_OUTPUT="+output, "EDO_FIXTURE_NULL="+null)
			value, err := command.CombinedOutput()
			if err != nil || string(value) != scenario.expected+"\n" {
				t.Fatalf("元数据探针结果不准确或泄露正文: %v %q", err, value)
			}
		})
	}
}
