package k3s

import (
	"bytes"
	"context"
	"encoding/base32"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"edo/internal/model"
	"edo/internal/sshclient"
)

func TestK3dResourceNamePreservesUUIDWithinOfficialContract(t *testing.T) {
	// k3d v5.9.0 的 CheckName 在任何集群资源创建前要求名称不超过 32 字符。
	const officialMaximum = 32
	validDNS := regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	seed := uuid.MustParse("1ec72535-b388-4165-82d8-10cafb3f8793")
	seen := make(map[string]uuid.UUID)
	check := func(id uuid.UUID) {
		t.Helper()
		name := k3dResourceName(id.String())
		if len(name) != 30 || len(name) > officialMaximum || !validDNS.MatchString(name) || name != strings.ToLower(name) || !strings.HasPrefix(name, "edo-") {
			t.Fatalf("资源名不符合固定版本 CLI 的长度与 DNS 契约: %q，长度 %d", name, len(name))
		}
		encoded := strings.TrimPrefix(name, "edo-")
		decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(encoded))
		if err != nil || !bytes.Equal(decoded, id[:]) {
			t.Fatalf("资源名未可逆保留完整 128 位 UUID: %q，err=%v", name, err)
		}
		if prior, exists := seen[name]; exists && prior != id {
			t.Fatal("不同 UUID 发生资源名截断碰撞")
		}
		seen[name] = id
		if k3dResourceName(id.String()) != name || k3dResourceName(strings.ToUpper(id.String())) != name {
			t.Fatal("同一 UUID 的资源名不确定或依赖大小写")
		}
	}
	check(seed)
	check(uuid.Nil)
	for bit := range 128 {
		other := seed
		other[bit/8] ^= 1 << (bit % 8)
		check(other)
	}
	if len(seen) != 130 {
		t.Fatal("逐位变化的 UUID 没有全部保留为不同资源名")
	}
}

func TestK3dInvalidIdentityNeverProducesResourceCommands(t *testing.T) {
	agents := 1
	input := Input{Action: "create", Mode: "k3d", Endpoint: "https://192.0.2.10:16443", K3d: &K3dOptions{Agents: &agents, APIPort: 16443, BindAddress: "192.0.2.10"}}
	for _, id := range []string{"", "not-a-uuid", "1ec72535-b388-4165-82d8-10cafb3f879z", "1ec72535b388416582d810cafb3f8793", "urn:uuid:1ec72535-b388-4165-82d8-10cafb3f8793", "{1ec72535-b388-4165-82d8-10cafb3f8793}", " 1ec72535-b388-4165-82d8-10cafb3f8793", "1ec72535-b388-4165-82d8-10cafb3f8793\n", "$(touch /tmp/forbidden)"} {
		t.Run(id, func(t *testing.T) {
			if got := k3dResourceName(id); got != "" {
				t.Errorf("非法安装标识生成了资源名: %q", got)
			}
			if len(k3dWarnings(id)) != 0 {
				t.Error("非法安装标识生成了可执行资源警告")
			}
			if k3dCreateCommand(input, id) != "" {
				t.Error("非法安装标识进入了创建命令")
			}
			for name, build := range map[string]func(Input, string) (string, error){"prepare": k3dPrepareToolScript, "install": k3dInstallScript} {
				script, err := build(input, id)
				if !errors.Is(err, ErrInvalidInput) || script != "" {
					t.Errorf("%s 未拒绝非法安装标识: err=%v", name, err)
				}
			}
			if script := k3dReadConfigScript(id); strings.TrimSpace(script) != "exit 44" || strings.Contains(script, "edo_cli") || strings.Contains(script, "kubeconfig get") {
				t.Error("非法安装标识进入了 kubeconfig 读取脚本")
			}
		})
	}
}

func TestK3dCommandsShareCompactNameAndFullInstallationLabel(t *testing.T) {
	const id = "1ec72535-b388-4165-82d8-10cafb3f8793"
	name := k3dResourceName(id)
	if len(name) != 30 {
		t.Fatalf("资源名未满足固定 CLI 长度: %q", name)
	}
	agents := 2
	input := Input{Action: "create", Mode: "k3d", Endpoint: "https://192.0.2.10:16443", K3d: &K3dOptions{Agents: &agents, APIPort: 16443, BindAddress: "192.0.2.10"}}
	create := k3dCreateCommand(input, id)
	install, err := k3dInstallScript(input, id)
	if err != nil {
		t.Fatal(err)
	}
	read := k3dReadConfigScript(id)
	if !strings.HasPrefix(create, "edo_cli cluster create "+shellQuote(name)+" ") || !strings.Contains(install, create+"\n") || !strings.HasSuffix(read, "edo_cli kubeconfig get "+shellQuote(name)+"\n") {
		t.Fatal("创建、安装与连接读取没有使用同一个完整 UUID 资源名")
	}
	for _, role := range []string{"server-0", "agent-0", "agent-1"} {
		container := shellQuote("k3d-" + name + "-" + role)
		if strings.Count(install, container) != 3 {
			t.Errorf("容器 %s 的身份核验、资源限制与限制复核目标不一致", role)
		}
	}
	for _, flag := range []string{"--runtime-label " + shellQuote("edo.dev/installation="+id+"@all"), "--k3s-node-label " + shellQuote("edo.dev/installation="+id+"@all")} {
		if !strings.Contains(create, flag) {
			t.Errorf("运行资源或 Kubernetes 标签没有保留完整 UUID: %s", flag)
		}
	}
	if !strings.Contains(strings.Join(k3dWarnings(id), "\n"), name) || strings.Contains(install+read, "edo-"+id) {
		t.Fatal("确认计划、实际资源名或读取目标仍混用旧超长名称")
	}
}

func TestK3dInvalidIdentityStopsWorkerBeforeRemoteAccess(t *testing.T) {
	service := &Service{}
	record := &model.K3sInstallation{ID: "invalid"}
	if err := service.performK3d(context.Background(), record, savedInput{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("无效任务仍进入远程安装流程: %v", err)
	}
	if value, err := service.waitForK3dConnection(context.Background(), record, savedInput{}, model.Host{}, sshclient.Bundle{}); !errors.Is(err, ErrInvalidInput) || value != "" {
		t.Fatalf("无效任务仍进入连接读取流程: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if value, err := service.waitForK3dConnection(ctx, record, savedInput{}, model.Host{}, sshclient.Bundle{}); !errors.Is(err, context.Canceled) || value != "" {
		t.Fatalf("标识检查覆盖了已取消状态: %v", err)
	}
}
