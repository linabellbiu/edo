package k3s

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
	"gorm.io/datatypes"

	"edo/internal/model"
)

const (
	sourcesCNInstaller = "https://rancher-mirror.rancher.cn/k3s/k3s-install.sh"
	sourcesCNArtifacts = "https://rancher-mirror.rancher.cn/k3s"
	sourcesRegistry    = "registry.example.com:5000/team"
)

func requireSources(t *testing.T, actual *Sources, source, installer, artifact, mirror string, registry *string) {
	t.Helper()
	if actual == nil || actual.DownloadSource != source || actual.InstallerURL != installer || actual.ArtifactURL != artifact || actual.Mirror != mirror || !reflect.DeepEqual(actual.SystemImageRegistry, registry) {
		t.Fatalf("来源快照不正确: %+v，期望 source=%q installer=%q artifact=%q mirror=%q registry=%v", actual, source, installer, artifact, mirror, registry)
	}
}

func sourcesString(value string) *string { return &value }

func sourcesRecord(t *testing.T, f fixture, id string) model.K3sInstallation {
	t.Helper()
	var record model.K3sInstallation
	if err := f.db.First(&record, "uuid = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	return record
}

func requireSourcesNoEffects(t *testing.T, f fixture) {
	t.Helper()
	if f.transport.reads != 0 || len(f.transport.writes) != 0 {
		t.Fatalf("无效来源配置不应访问远程主机: reads=%d writes=%d", f.transport.reads, len(f.transport.writes))
	}
	var count int64
	if err := f.db.Model(&model.K3sInstallation{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("无效来源配置不应保存计划: count=%d err=%v", count, err)
	}
}

func TestSourcesDefaultProfilePreservesSelectionsAndEncryptedSnapshot(t *testing.T) {
	f := newFixture(t)
	input := f.input()
	input.DownloadSource = "official_cn"
	input.SystemImageRegistry = sourcesString(sourcesRegistry)
	// 默认方案忽略保留的高级草稿，但不能丢弃独立的来源选择。
	input.ServerOptions = map[string]any{"node-name": "retained-custom-node"}
	input.Installer = map[string]any{"version": "v1.36.0+k3s1"}
	input.Overrides = "server: [invalid retained YAML"
	plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	requireSources(t, plan.Sources, "official_cn", sourcesCNInstaller, sourcesCNArtifacts, "cn", sourcesString(sourcesRegistry))
	var config map[string]any
	if err := yaml.Unmarshal([]byte(plan.ServerConfig), &config); err != nil || config["system-default-registry"] != sourcesRegistry {
		t.Fatalf("系统镜像仓库未进入实际 Server 配置: %+v err=%v", config, err)
	}
	if _, retained := config["node-name"]; retained {
		t.Fatal("默认方案不应恢复高级节点名称")
	}
	record := sourcesRecord(t, f, plan.ID)
	if !strings.HasPrefix(record.InputCiphertext, "v1:") || strings.Contains(record.InputCiphertext, sourcesRegistry) {
		t.Fatal("来源选择必须保存在加密输入快照中")
	}
	// 修改调用者草稿及返回对象，不得反向改变已保存的审核计划。
	input.DownloadSource, input.SystemImageRegistry = "official", sourcesString("different.example.com/team")
	plan.Sources.InstallerURL = "https://different.example.com/install.sh"
	snapshot, err := f.service.decode(record)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Input.DownloadSource != "official_cn" || !reflect.DeepEqual(snapshot.Input.SystemImageRegistry, sourcesString(sourcesRegistry)) || snapshot.Input.ServerOptions["system-default-registry"] != sourcesRegistry {
		t.Fatalf("解密输入未冻结来源选择: %+v", snapshot.Input)
	}
	requireSources(t, snapshot.Sources, "official_cn", sourcesCNInstaller, sourcesCNArtifacts, "cn", sourcesString(sourcesRegistry))
	if len(snapshot.Input.Installer) != 1 || snapshot.Input.Installer["INSTALL_K3S_VERSION"] != CatalogK3sVersion {
		t.Fatalf("默认方案必须保持目录选项与来源选择分离: %+v", snapshot.Input.Installer)
	}
	for _, assignment := range []string{"INSTALL_K3S_MIRROR=cn", "INSTALL_K3S_MIRROR_URL=https://rancher-mirror.rancher.cn"} {
		if !strings.Contains(strings.Join(plan.Commands, "\n"), assignment) {
			t.Fatalf("预览丢失由冻结来源生成的安装器环境变量: %s", assignment)
		}
	}
	if err := f.db.Model(&record).Update("status", "failed").Error; err != nil {
		t.Fatal(err)
	}
	operation, err := f.service.Get(f.ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	requireSources(t, operation.Sources, "official_cn", sourcesCNInstaller, sourcesCNArtifacts, "cn", sourcesString(sourcesRegistry))
	history, err := f.service.List(f.ctx, "", 20, "create")
	if err != nil || len(history) != 1 {
		t.Fatalf("来源历史读取失败: count=%d err=%v", len(history), err)
	}
	requireSources(t, history[0].Sources, "official_cn", sourcesCNInstaller, sourcesCNArtifacts, "cn", sourcesString(sourcesRegistry))
	if f.transport.reads != 1 || len(f.transport.writes) != 0 || plan.Preflight == nil || !plan.Preflight.Passed {
		t.Fatal("来源预览只能执行只读预检，不能安装")
	}
}

func TestSourcesOfficialDefaultDoesNotInferSystemRegistry(t *testing.T) {
	f := newFixture(t)
	plan, err := f.service.Preview(f.ctx, f.actor.ID, f.input())
	if err != nil {
		t.Fatal(err)
	}
	requireSources(t, plan.Sources, "official", "https://get.k3s.io", "https://github.com/k3s-io/k3s/releases/download", "", sourcesString(""))
	snapshot, err := f.service.decode(sourcesRecord(t, f, plan.ID))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Input.DownloadSource != "official" || snapshot.Input.SystemImageRegistry != nil {
		t.Fatalf("官方来源默认值不正确: %+v", snapshot.Input)
	}
	if _, exists := snapshot.Input.ServerOptions["system-default-registry"]; exists {
		t.Fatal("未选择系统仓库时不能自动设置仓库")
	}
	if _, exists := snapshot.Input.Installer["INSTALL_K3S_MIRROR"]; exists {
		t.Fatal("官方来源不能静默使用国内镜像")
	}
}

func TestSourcesHistoryWithoutRecordedSourcesDoesNotInventThem(t *testing.T) {
	f := newFixture(t)
	input := f.input()
	input.DownloadSource, input.SystemImageRegistry = "official_cn", sourcesString(sourcesRegistry)
	plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	record := sourcesRecord(t, f, plan.ID)
	var preview map[string]any
	if err := json.Unmarshal(record.Preview, &preview); err != nil {
		t.Fatal(err)
	}
	delete(preview, "sources")
	encoded, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟旧版已保存的公开预览；即使密文可读，也不能伪造当时未展示的来源信息。
	if err := f.db.Model(&record).Updates(map[string]any{"preview": datatypes.JSON(encoded), "status": "failed"}).Error; err != nil {
		t.Fatal(err)
	}
	operation, err := f.service.Get(f.ctx, plan.ID)
	if err != nil || operation.Sources != nil {
		t.Fatalf("旧记录不应补造来源信息: %+v err=%v", operation, err)
	}
	history, err := f.service.List(f.ctx, "", 20, "create")
	if err != nil || len(history) != 1 || history[0].Sources != nil {
		t.Fatalf("旧历史不应补造来源信息: %+v err=%v", history, err)
	}
}

func TestSourcesCreateRegistryMatchesAdvancedAndYAML(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		advanced  string
		yamlValue string
		invalid   bool
	}{
		{name: "仅顶层"},
		{name: "高级配置同值", advanced: sourcesRegistry},
		{name: "YAML 同值", yamlValue: sourcesRegistry},
		{name: "高级配置和 YAML 均同值", advanced: sourcesRegistry, yamlValue: sourcesRegistry},
		{name: "高级配置异值", advanced: "different.example.com/team", invalid: true},
		{name: "YAML 异值", yamlValue: "different.example.com/team", invalid: true},
		{name: "YAML 覆盖为异值", advanced: sourcesRegistry, yamlValue: "different.example.com/team", invalid: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newFixture(t)
			input := f.input()
			input.Profile, input.SystemImageRegistry = "custom", sourcesString(sourcesRegistry)
			if scenario.advanced != "" {
				input.ServerOptions = map[string]any{"system-default-registry": scenario.advanced}
			}
			if scenario.yamlValue != "" {
				input.Overrides = "server:\n  system-default-registry: " + scenario.yamlValue + "\n"
			}
			plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
			if scenario.invalid {
				if !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("来源冲突必须拒绝: plan=%+v err=%v", plan, err)
				}
				requireSourcesNoEffects(t, f)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if plan.Sources == nil || plan.Sources.SystemImageRegistry == nil || *plan.Sources.SystemImageRegistry != sourcesRegistry {
				t.Fatalf("同值来源未保留: %+v", plan.Sources)
			}
			snapshot, err := f.service.decode(sourcesRecord(t, f, plan.ID))
			if err != nil || snapshot.Input.ServerOptions["system-default-registry"] != sourcesRegistry {
				t.Fatalf("实际执行配置未保留系统仓库: %+v err=%v", snapshot.Input.ServerOptions, err)
			}
		})
	}
}

func TestSourcesJoinRejectsTopLevelRegistryBeforeClusterRead(t *testing.T) {
	f := newFixture(t)
	input := f.input()
	input.Action, input.ServerHostID, input.ClusterID = "join", "", "missing-target-cluster"
	input.JoinHostIDs = []string{f.host.ID}
	input.SystemImageRegistry = sourcesString(sourcesRegistry)
	// fixture 未配置 Kubernetes 客户端；必须在读取目标集群前拒绝此创建专属字段。
	if _, err := f.service.Preview(f.ctx, f.actor.ID, input); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("加入节点不应设置目标集群系统仓库: %v", err)
	}
	requireSourcesNoEffects(t, f)
}

func TestSourcesRegistryInputPresenceDistinguishesExplicitDefaultFromLegacy(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		registry  *string
		advanced  string
		yamlValue string
		expected  string
		invalid   bool
	}{
		{name: "显式官方默认", registry: sourcesString("")},
		{name: "显式官方与高级非空值冲突", registry: sourcesString(""), advanced: sourcesRegistry, invalid: true},
		{name: "显式官方与 YAML 非空值冲突", registry: sourcesString(""), yamlValue: sourcesRegistry, invalid: true},
		{name: "旧客户端兼容高级仓库", advanced: sourcesRegistry, expected: sourcesRegistry},
		{name: "旧客户端兼容 YAML 仓库", yamlValue: sourcesRegistry, expected: sourcesRegistry},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newFixture(t)
			input := f.input()
			input.Profile, input.SystemImageRegistry = "custom", scenario.registry
			if scenario.advanced != "" {
				input.ServerOptions = map[string]any{"system-default-registry": scenario.advanced}
			}
			if scenario.yamlValue != "" {
				input.Overrides = "server:\n  system-default-registry: " + scenario.yamlValue + "\n"
			}
			plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
			if scenario.invalid {
				if !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("显式官方默认不能被高级或 YAML 仓库静默覆盖: %v", err)
				}
				requireSourcesNoEffects(t, f)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			requireSources(t, plan.Sources, "official", "https://get.k3s.io", "https://github.com/k3s-io/k3s/releases/download", "", sourcesString(scenario.expected))
			snapshot, err := f.service.decode(sourcesRecord(t, f, plan.ID))
			if err != nil || !reflect.DeepEqual(snapshot.Input.SystemImageRegistry, scenario.registry) {
				t.Fatalf("加密输入快照丢失 nil 与显式空串区别: %+v err=%v", snapshot.Input, err)
			}
			encoded, err := json.Marshal(snapshot.Input)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			if _, present := fields["system_image_registry"]; present != (scenario.registry != nil) {
				t.Fatal("JSON 必须保留显式空串，并省略旧客户端未发送的仓库字段")
			}
		})
	}
}

func TestSourcesJoinRejectsExplicitEmptyRegistryBeforeClusterRead(t *testing.T) {
	f := newFixture(t)
	input := f.input()
	input.Action, input.ServerHostID, input.ClusterID = "join", "", "missing-target-cluster"
	input.JoinHostIDs = []string{f.host.ID}
	input.SystemImageRegistry = sourcesString("")
	if _, err := f.service.Preview(f.ctx, f.actor.ID, input); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("加入节点即使显式选择官方默认也必须拒绝: %v", err)
	}
	requireSourcesNoEffects(t, f)
}

func TestSourcesJoinRecordsDownloadsWithoutInventingRegistry(t *testing.T) {
	f, _, input := joinVersionFixture(t, "v1.35.5+k3s1")
	input.DownloadSource = "official_cn"
	plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	requireSources(t, plan.Sources, "official_cn", sourcesCNInstaller, sourcesCNArtifacts, "cn", nil)
	if plan.TargetVersion != "v1.35.5+k3s1" || plan.Version != plan.TargetVersion {
		t.Fatal("加入节点的下载版本必须跟随真实目标集群")
	}
	snapshot, err := f.service.decode(sourcesRecord(t, f, plan.ID))
	if err != nil {
		t.Fatal(err)
	}
	requireSources(t, snapshot.Sources, "official_cn", sourcesCNInstaller, sourcesCNArtifacts, "cn", nil)
	operation, err := f.service.Get(f.ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	requireSources(t, operation.Sources, "official_cn", sourcesCNInstaller, sourcesCNArtifacts, "cn", nil)
	if len(f.transport.writes) != 0 {
		t.Fatal("加入节点的来源预览不能执行安装")
	}
}

func TestSourcesK3dRejectsNativeSourceFields(t *testing.T) {
	for _, scenario := range []struct {
		name string
		edit func(*Input)
	}{
		{"显式官方来源", func(input *Input) { input.DownloadSource = "official" }},
		{"国内来源", func(input *Input) { input.DownloadSource = "official_cn" }},
		{"系统镜像仓库", func(input *Input) { input.SystemImageRegistry = sourcesString(sourcesRegistry) }},
		{"显式官方默认仓库", func(input *Input) { input.SystemImageRegistry = sourcesString("") }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f, transport, _, input := k3dFixture(t)
			scenario.edit(&input)
			if _, err := f.service.Preview(f.ctx, f.actor.ID, input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("k3d 不应接受原生来源字段: %v", err)
			}
			if len(transport.writes) != 0 {
				t.Fatal("无效 k3d 来源配置产生了远程写入")
			}
		})
	}
	f, _, _, input := k3dFixture(t)
	plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
	if err != nil || plan.Sources != nil {
		t.Fatalf("k3d 不应伪造原生来源: %+v err=%v", plan, err)
	}
	operation, err := f.service.Get(f.ctx, plan.ID)
	if err != nil || operation.Sources != nil {
		t.Fatalf("k3d 历史不应伪造原生来源: %+v err=%v", operation, err)
	}
}

func TestSourcesInvalidSelectionsFailBeforeRemoteAccess(t *testing.T) {
	for _, source := range []string{"cn", "mirror", "OFFICIAL_CN", "https://rancher-mirror.rancher.cn", "official_cn\nINSTALL_K3S_EXEC=bad"} {
		t.Run("下载源/"+source, func(t *testing.T) {
			f := newFixture(t)
			input := f.input()
			input.DownloadSource = source
			if _, err := f.service.Preview(f.ctx, f.actor.ID, input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("非法下载来源必须拒绝: %v", err)
			}
			requireSourcesNoEffects(t, f)
		})
	}
	for _, registry := range []string{"https://registry.example.com", "user:pass@registry.example.com", "registry.example.com/team?token=private", "registry.example.com/team#fragment", "registry.example.com//team", "registry.example.com/team/..", "registry.example.com/team with-space", "registry.example.com/team\nnext", "/team"} {
		t.Run("系统仓库/"+registry, func(t *testing.T) {
			f := newFixture(t)
			input := f.input()
			input.SystemImageRegistry = sourcesString(registry)
			if _, err := f.service.Preview(f.ctx, f.actor.ID, input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("非法系统镜像仓库必须拒绝: %v", err)
			}
			requireSourcesNoEffects(t, f)
		})
	}
}

func TestSourcesChinaRejectsCustomArtifactURLs(t *testing.T) {
	for _, location := range []string{"高级配置", "YAML"} {
		for _, artifact := range []string{sourcesCNArtifacts, "https://different.example.com/k3s"} {
			t.Run(location+"/"+artifact, func(t *testing.T) {
				f := newFixture(t)
				input := f.input()
				input.Profile, input.DownloadSource = "custom", "official_cn"
				if location == "YAML" {
					input.Overrides = "installer:\n  INSTALL_K3S_ARTIFACT_URL: " + artifact + "\n"
				} else {
					input.Installer = map[string]any{"INSTALL_K3S_ARTIFACT_URL": artifact}
				}
				if _, err := f.service.Preview(f.ctx, f.actor.ID, input); !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("国内来源不能接受独立制品源覆盖: %v", err)
				}
				requireSourcesNoEffects(t, f)
			})
		}
	}
}

func TestSourcesChinaScriptsUseFrozenHTTPSMirrorAndReleasePath(t *testing.T) {
	f := newFixture(t)
	input := f.input()
	input.DownloadSource = "official_cn"
	plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	// 下载源与系统仓库是独立选择，国内安装器不会自动代表已选择系统镜像仓库。
	requireSources(t, plan.Sources, "official_cn", sourcesCNInstaller, sourcesCNArtifacts, "cn", sourcesString(""))
	snapshot, err := f.service.decode(sourcesRecord(t, f, plan.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := snapshot.Input.ServerOptions["system-default-registry"]; exists {
		t.Fatal("国内下载源不能自动写入系统镜像仓库")
	}
	if len(plan.Commands) == 0 {
		t.Fatal("预览缺少实际安装命令")
	}
	assertCN := func(t *testing.T, script string) {
		t.Helper()
		for _, forbidden := range []string{"get.k3s.io", "github.com", "http://rancher-mirror.rancher.cn"} {
			if strings.Contains(script, forbidden) {
				t.Fatalf("国内来源链路仍出现 %q", forbidden)
			}
		}
		if !strings.Contains(script, sourcesCNInstaller) {
			t.Fatal("安装器未使用已审核的 HTTPS 国内来源")
		}
	}
	for _, command := range plan.Commands {
		assertCN(t, command)
		for _, assignment := range []string{"INSTALL_K3S_MIRROR=cn", "INSTALL_K3S_MIRROR_URL=https://rancher-mirror.rancher.cn"} {
			if !strings.Contains(command, assignment) {
				t.Fatalf("预览未显式冻结国内安装器环境变量 %q", assignment)
			}
		}
	}
	versionPath := strings.ReplaceAll(CatalogK3sVersion, "+", "-")
	for _, role := range []string{"server", "agent"} {
		t.Run(role, func(t *testing.T) {
			host := plan.Hosts[0]
			host.Role = role
			script, err := installScript(host, snapshot.Input, plan.ID, "fixture-only-token")
			if err != nil {
				t.Fatal(err)
			}
			assertCN(t, script)
			for _, assignment := range []string{"INSTALL_K3S_MIRROR=cn", "INSTALL_K3S_MIRROR_URL=https://rancher-mirror.rancher.cn"} {
				if !strings.Contains(script, assignment) {
					t.Fatalf("实际脚本丢失已审核的国内安装器环境变量 %q", assignment)
				}
			}
			probe := preflightProbeScript(host, snapshot.Input, role == "agent")
			assertCN(t, probe)
			if !strings.Contains(probe, sourcesCNArtifacts+"/"+versionPath+"/") || strings.Contains(probe, sourcesCNArtifacts+"/"+CatalogK3sVersion+"/") {
				t.Fatal("国内制品预检必须使用加号转短横线的版本目录")
			}
		})
	}
	staged, err := serverInstallerDownloadScript(plan.ID, snapshot.Input)
	if err != nil {
		t.Fatal(err)
	}
	assertCN(t, staged)
	if !strings.Contains(staged, sourcesCNArtifacts+"/"+versionPath) || strings.Contains(staged, sourcesCNArtifacts+"/"+strings.ReplaceAll(CatalogK3sVersion, "+", "%2B")) {
		t.Fatal("Master 暂存下载必须使用同一国内制品版本目录")
	}
	if len(f.transport.writes) != 0 {
		t.Fatal("脚本生成测试不得执行任何安装写入")
	}
}
