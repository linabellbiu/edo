//go:build mage

package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 这里只读取独立临时目录，不调用 Start，也不启动依赖、构建、迁移或监听。
func TestMageMissingEnvironmentRefusesTemplateInitialization(t *testing.T) {
	t.Setenv("EDO_SECRETS_KEY", "")
	root := t.TempDir()
	path := filepath.Join(root, ".env")
	template := []byte("EDO_MAGE_PUBLIC_FIXTURE=only-temporary\nEDO_SECRETS_KEY=\n")
	if err := os.WriteFile(path+".example", template, 0o600); err != nil {
		t.Fatal("写入临时公开模板失败")
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	err := loadEnvironmentFile(path)
	if err == nil {
		t.Error("含后端启动缺少 .env 时必须拒绝，不得按模板自动初始化")
	} else if err.Error() != "未找到根目录 .env，请先复制 .env.example 并配置 EDO_SECRETS_KEY" {
		t.Error("缺少配置应返回稳定中文提示")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("读取失败不得创建 .env 或生成并持久化新密钥")
	}
	after, readErr := os.ReadFile(path + ".example")
	if readErr != nil || !bytes.Equal(template, after) {
		t.Error("拒绝启动不得修改配置模板")
	}
	for _, field := range []string{"operation=mage_environment_load", "file=", "error_kind=missing_file"} {
		if !strings.Contains(logs.String(), field) {
			t.Errorf("缺少配置的安全诊断缺少上下文：%s", field)
		}
	}
}
