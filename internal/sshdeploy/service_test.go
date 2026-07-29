package sshdeploy

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"testing"
	"time"

	"zrt/internal/config"
	"zrt/internal/database"
	"zrt/internal/model"
	"zrt/internal/secret"
)

func TestDeploymentScriptInputKeepsExactScriptAndQuotesFixedMetadata(t *testing.T) {
	script := "printf '%s' \"$ZRT_PIPELINE_RUN_ID\"  \n# 保留末尾空白\t"
	body, err := deploymentScriptInput(Input{
		WorkingDirectory: "/srv/team's app",
		Script:           script,
		Environment: map[string]string{
			"ZRT_PIPELINE_RUN_ID": "run'$(touch /tmp/not-allowed)",
			"ZRT_APPLICATION_ID":  "application-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(body, script) {
		t.Fatalf("部署脚本原始字节未保持在输入末尾: %q", body)
	}
	if !strings.Contains(body, "export ZRT_PIPELINE_RUN_ID='run'\"'\"'$(touch /tmp/not-allowed)'\n") ||
		!strings.Contains(body, "cd '/srv/team'\"'\"'s app'\n") {
		t.Fatalf("SSH 元数据或目录没有安全引用: %q", body)
	}
	if strings.Index(body, "ZRT_APPLICATION_ID") > strings.Index(body, "ZRT_PIPELINE_RUN_ID") {
		t.Fatalf("固定环境变量应按键排序，保证输入稳定: %q", body)
	}
}

func TestDeploymentScriptInputRejectsUnexpectedEnvironmentAndWorkingDirectory(t *testing.T) {
	if _, err := deploymentScriptInput(Input{
		Script: "echo ok", Environment: map[string]string{"PATH": "/tmp"},
	}); !errors.Is(err, ErrInvalidScript) {
		t.Fatalf("只允许注入固定 ZRT 元数据: %v", err)
	}
	for _, value := range []string{"relative", "/srv/../etc", "/srv//app", "/srv/app\nnext"} {
		if normalized := normalizeWorkingDirectory(value); normalized != "" {
			t.Fatalf("无效工作目录未被拒绝: value=%q normalized=%q", value, normalized)
		}
	}
	if normalized := normalizeWorkingDirectory("/srv/zrt"); normalized != "/srv/zrt" {
		t.Fatalf("有效工作目录被错误修改: %q", normalized)
	}
}

func TestCommandErrorExposesStableCategoryAndExitCode(t *testing.T) {
	err := &CommandError{ExitCode: 23, cause: errors.New("remote detail")}
	if !errors.Is(err, ErrCommandFailed) || err.ExitCode != 23 || !strings.Contains(err.Error(), "退出码 23") {
		t.Fatalf("命令失败错误没有保留稳定分类和退出码: %v", err)
	}
}

func TestRunHostDeploymentScriptExecutesLocalSnapshot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("原生 Windows 不支持本地 sh 脚本执行")
	}
	db, err := database.Open(context.Background(), config.Database{
		Driver: "sqlite", DSN: "file:" + t.Name() + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("打开本地命令测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = database.Close(db) })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatalf("迁移本地命令测试数据库失败: %v", err)
	}
	now := time.Now().UTC()
	environment := model.Environment{
		ID: "environment-local-exec", Name: "本地命令测试", Level: model.EnvironmentDevelopment,
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&environment).Error; err != nil {
		t.Fatalf("创建本地命令测试环境失败: %v", err)
	}
	if err := db.Model(&model.Host{}).Where("id = ?", model.BuiltinLocalHostID).
		Update("is_active", true).Error; err != nil {
		t.Fatalf("绑定本地主机测试环境失败: %v", err)
	}
	if err := db.Create(&model.EnvironmentHost{
		EnvironmentID: environment.ID, HostID: model.BuiltinLocalHostID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("绑定本地主机测试环境失败: %v", err)
	}
	capability := model.HostCapability{
		HostID: model.BuiltinLocalHostID, Kind: model.HostCapabilityLocalExec,
		Status: model.HostCapabilityReady, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Where("host_id = ? AND kind = ?", capability.HostID, capability.Kind).
		Assign(map[string]any{"status": model.HostCapabilityReady, "updated_at": now}).
		FirstOrCreate(&capability).Error; err != nil {
		t.Fatalf("创建本地执行能力失败: %v", err)
	}
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	secretManager, err := secret.New(key)
	if err != nil {
		t.Fatalf("初始化测试密钥失败: %v", err)
	}
	service := NewService(db, secretManager, config.Runtime{})
	if err := db.Model(&model.Environment{}).Where("id = ?", environment.ID).
		Update("level", model.EnvironmentProduction).Error; err != nil {
		t.Fatalf("修改旧环境安全级别兼容列失败: %v", err)
	}
	t.Setenv("ZRT_SECRETS_KEY", "must-not-be-inherited")
	var stdout, stderr bytes.Buffer
	result, err := service.RunHostDeploymentScript(context.Background(), Input{
		HostID: model.BuiltinLocalHostID, EnvironmentID: environment.ID,
		WorkingDirectory: t.TempDir(),
		Script: "if [ -n \"${ZRT_SECRETS_KEY+x}\" ]; then exit 99; fi\n" +
			"printf '%s' \"$ZRT_PIPELINE_RUN_ID\"; printf 'stderr-output' >&2; exit 7\n",
		Timeout: 30 * time.Second, Environment: map[string]string{"ZRT_PIPELINE_RUN_ID": "run-local"},
		Stdout: &stdout, Stderr: &stderr,
	})
	if !errors.Is(err, ErrCommandFailed) || !result.Started || result.ExitCode != 7 {
		t.Fatalf("本地脚本退出状态未被正确保留: result=%+v err=%v", result, err)
	}
	if stdout.String() != "run-local" || stderr.String() != "stderr-output" {
		t.Fatalf("本地脚本输出未完整归档: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestLocalCommandEnvironmentUsesBuilderWithoutLeakingZRTConfiguration(t *testing.T) {
	t.Setenv("ZRT_SECRETS_KEY", "secret")
	t.Setenv("DOCKER_HOST", "tcp://ignored:2375")
	environment := localCommandEnvironment("tcp://docker-builder:2375")
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "ZRT_SECRETS_KEY") || strings.Contains(joined, "secret") {
		t.Fatalf("本地脚本不得继承 ZRT 进程密钥: %q", joined)
	}
	if !strings.Contains(joined, "DOCKER_HOST=tcp://docker-builder:2375") || strings.Contains(joined, "ignored") {
		t.Fatalf("容器模式应显式使用隔离的 Docker 构建运行时: %q", joined)
	}
}

func TestLocalShellPathRejectsNativeWindowsAndMissingShell(t *testing.T) {
	called := false
	if _, err := localShellPath("windows", func(string) (string, error) {
		called = true
		return "sh", nil
	}); !errors.Is(err, ErrLocalExecUnsupported) || called {
		t.Fatalf("原生 Windows 应在查找 sh 前被拒绝: called=%v err=%v", called, err)
	}
	if _, err := localShellPath("linux", func(string) (string, error) {
		return "", errors.New("not found")
	}); !errors.Is(err, ErrLocalExecUnsupported) {
		t.Fatalf("缺少 sh 时不应声称支持本地执行: %v", err)
	}
}
