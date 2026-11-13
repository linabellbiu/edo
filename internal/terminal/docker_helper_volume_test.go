package terminal

import (
	"errors"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/volume"

	"edo/internal/terminalassets"
	"edo/internal/terminalwire"
)

func (f *helperInstallFixture) withToolsVolume() {
	f.readonly = true
	f.target = container.InspectResponse{
		HostConfig: &container.HostConfig{ReadonlyRootfs: true},
		Config:     &container.Config{Labels: map[string]string{"com.docker.compose.project": "fixture"}},
		Mounts:     []container.MountPoint{{Type: mount.TypeVolume, Name: "fixture_edo_terminal_tools", Driver: "local", Destination: terminalassets.Directory}},
	}
	f.volume = volume.Volume{Name: "fixture_edo_terminal_tools", Driver: "local", Labels: map[string]string{terminalassets.VolumeLabel: terminalassets.Version, "com.docker.compose.project": "fixture", "com.docker.compose.volume": terminalassets.VolumeKey}}
}

func TestDockerHelperReadOnlyRootWithoutToolsVolumeRequiresNewDeployment(t *testing.T) {
	f := newHelperInstallFixture(t, "arm64")
	f.target.HostConfig = &container.HostConfig{ReadonlyRootfs: true}
	if _, err := f.prepare(); !errors.Is(err, ErrDockerHelperReadOnly) || f.copies != 0 || f.creates != 0 {
		t.Fatal("只读容器未明确要求下一次部署，或仍然写入", err)
	}
	// 只读镜像内已有当前摘要工具时继续复用，不要求重建。
	_, digest, err := readDockerHelper(f.directory, "arm64")
	if err != nil {
		t.Fatal(err)
	}
	f.files[terminalwire.HelperPath+"-"+digest[:24]] = helperFixtureBinary("arm64")
	if _, err := f.prepare(); err != nil || f.copies != 0 {
		t.Fatal("只读容器已有工具没有复用", err)
	}
}

func TestDockerHelperInstallsOnlyThroughStoppedToolsVolumeContainer(t *testing.T) {
	f := newHelperInstallFixture(t, "arm64")
	f.withToolsVolume()
	file, err := f.prepare()
	if err != nil || !strings.HasPrefix(file, terminalassets.Directory+"/") || f.creates != 1 || f.removes != 1 || f.copies != 1 || !f.events[0].Succeeded || f.events[0].Volume != f.volume.Name {
		t.Fatal("只读工具卷安装或清理失败", f.creates, f.removes, err)
	}
	if _, err := f.prepare(); err != nil || f.creates != 1 || f.copies != 1 {
		t.Fatal("已安装工具卷未复用", err)
	}
}

func TestDockerHelperToolsVolumeRejectsForeignOrUnsafeMounts(t *testing.T) {
	for _, scenario := range []string{"可写挂载", "绑定目录", "其他项目", "业务卷", "驱动选项", "其他驱动", "卷身份变化", "无授权"} {
		t.Run(scenario, func(t *testing.T) {
			f := newHelperInstallFixture(t, "arm64")
			f.withToolsVolume()
			switch scenario {
			case "可写挂载":
				f.target.Mounts[0].RW = true
			case "绑定目录":
				f.target.Mounts[0].Type = mount.TypeBind
			case "其他项目":
				f.volume.Labels["com.docker.compose.project"] = "other"
			case "业务卷":
				delete(f.volume.Labels, terminalassets.VolumeLabel)
			case "驱动选项":
				f.volume.Options = map[string]string{"device": "/etc", "type": "none", "o": "bind"}
			case "其他驱动":
				f.volume.Driver = "other"
			case "卷身份变化":
				f.volume.Name = "other"
			case "无授权":
				f.allowed = false
			}
			if _, err := f.prepare(); err == nil || f.creates != 0 || f.copies != 0 {
				t.Fatal("不安全或未授权的工具卷仍被写入", err)
			}
		})
	}
}

func TestDockerHelperToolsVolumeRevocationAndFailedCleanupCannotSucceed(t *testing.T) {
	for _, scenario := range []string{"创建后撤权", "创建响应丢失", "复制后撤权", "复制校验失败", "清理被拒绝", "准备容器身份变化"} {
		t.Run(scenario, func(t *testing.T) {
			f := newHelperInstallFixture(t, "arm64")
			f.withToolsVolume()
			switch scenario {
			case "创建后撤权":
				f.revokeOnCreate = true
			case "创建响应丢失":
				f.createDisconnect = true
			case "复制后撤权":
				f.revoke = true
			case "复制校验失败":
				f.corrupt = true
			case "清理被拒绝":
				f.cleanupDenied = true
			case "准备容器身份变化":
				f.changePreparer = true
			}
			if _, err := f.prepare(); err == nil || len(f.events) != 1 || f.events[0].Succeeded {
				t.Fatal("撤权或清理失败仍报告成功", err)
			}
			if scenario == "准备容器身份变化" {
				if f.removes != 0 {
					t.Fatal("删除了身份变化的容器")
				}
			} else if f.removes != 1 {
				t.Fatal("失败后未回收本次准备容器")
			}
			if (scenario == "创建后撤权" || scenario == "创建响应丢失") && f.copies != 0 {
				t.Fatal("撤权后仍然复制工具")
			}
		})
	}
}
