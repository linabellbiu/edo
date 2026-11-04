package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 独立目录中的真实容器回归，不接触运行中的 EDO、数据库或其他容器。
func TestComposeBootstrapFilesystem(t *testing.T) {
	if os.Getenv("EDO_TEST_COMPOSE_BOOTSTRAP") != "1" {
		t.Skip("设置 EDO_TEST_COMPOSE_BOOTSTRAP=1 后使用 Docker 验证首次初始化")
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// Docker 卷保留真实 Linux UID/GID，避免 Docker Desktop 的宿主机身份映射干扰权限断言。
	data := "edo-compose-bootstrap-test-" + time.Now().Format("20060102150405.000000000")
	if output, err := exec.Command("docker", "volume", "create", data).CombinedOutput(); err != nil {
		t.Fatalf("创建独立测试卷失败：%v，日志：%s", err, output)
	}
	t.Cleanup(func() {
		if output, err := exec.Command("docker", "volume", "rm", data).CombinedOutput(); err != nil {
			t.Errorf("清理独立测试卷失败：%v，日志：%s", err, output)
		}
	})
	run := func(user string, environment []string, args ...string) (string, error) {
		command := []string{"run", "--rm", "--network", "none", "--read-only", "--user", user,
			"-v", data + ":/app/data",
			"-v", filepath.Join(root, "deploy", "compose-init.sh") + ":/init.sh:ro",
			"-v", filepath.Join(root, "deploy", "docker-entrypoint.sh") + ":/entrypoint.sh:ro"}
		for _, value := range environment {
			command = append(command, "-e", value)
		}
		command = append(command, "alpine:3.22")
		command = append(command, args...)
		output, err := exec.Command("docker", command...).CombinedOutput()
		return string(output), err
	}
	mustRun := func(user string, environment []string, args ...string) string {
		t.Helper()
		output, err := run(user, environment, args...)
		if err != nil {
			t.Fatalf("独立初始化容器失败：%v，日志：%s", err, output)
		}
		return output
	}
	initialize := func() (string, error) { return run("0:0", nil, "sh", "/init.sh") }
	if output, err := initialize(); err != nil {
		t.Fatalf("全新目录应自动初始化：%v，日志：%s", err, output)
	}
	digests := mustRun("0:0", nil, "sh", "-ec", "sha256sum /app/data/bootstrap/secrets-key /app/data/bootstrap/postgres-password /app/data/bootstrap/postgres-dsn")
	if output, err := initialize(); err != nil {
		t.Fatalf("重复启动应复用原配置：%v，日志：%s", err, output)
	}
	if after := mustRun("0:0", nil, "sh", "-ec", "sha256sum /app/data/bootstrap/secrets-key /app/data/bootstrap/postgres-password /app/data/bootstrap/postgres-dsn"); after != digests {
		t.Fatal("重复启动改写了密钥或密码")
	}
	// 实际以 API 和 PostgreSQL 身份验证读取边界，不凭静态 chmod 命令判断成功。
	mustRun("10001:10001", nil, "sh", "-ec", "cat /app/data/bootstrap/secrets-key /app/data/bootstrap/postgres-dsn >/dev/null && touch /app/data/api-write-probe")
	// PG 通过子目录 bind 挂载，不经过仅允许 API 读取的 data 顶层。
	if output, err := exec.Command("docker", "run", "--rm", "--network", "none", "--read-only", "--user", "999:999", "--mount", "type=volume,src="+data+",dst=/run/bootstrap,volume-subpath=bootstrap/postgres,readonly", "alpine:3.22", "sh", "-ec", "test -r /run/bootstrap/password && test ! -e /run/bootstrap/secrets-key && test ! -e /run/bootstrap/postgres-dsn").CombinedOutput(); err != nil {
		t.Fatalf("PostgreSQL 文件权限边界错误：%v，日志：%s", err, output)
	}
	canary := "SYNTHETIC_INVALID_SECRET_MUST_NOT_APPEAR"
	output, err := run("0:0", []string{"EDO_SECRETS_KEY=" + canary}, "sh", "/init.sh")
	if err == nil || strings.Contains(output, canary) {
		t.Fatal("密钥不一致必须拒绝，日志不得泄露输入值")
	}
	// 保留初始化标记，模拟丢失密钥；不能自动生成另一个密钥继续启动。
	mustRun("0:0", nil, "sh", "-ec", "mv /app/data/bootstrap/secrets-key /app/data/bootstrap/key.backup")
	if output, err := initialize(); err == nil || !strings.Contains(output, "已有数据缺少加密密钥") {
		t.Fatal("已有数据丢失密钥必须阻断")
	}
	mustRun("0:0", nil, "sh", "-ec", "test ! -e /app/data/bootstrap/secrets-key && mv /app/data/bootstrap/key.backup /app/data/bootstrap/secrets-key; mv /app/data/bootstrap/postgres-password /app/data/bootstrap/password.backup")
	if output, err := initialize(); err == nil || !strings.Contains(output, "已有数据缺少 PostgreSQL 密码") {
		t.Fatal("已有数据丢失数据库密码必须阻断")
	}
	mustRun("0:0", nil, "sh", "-ec", "test ! -e /app/data/bootstrap/postgres-password && mv /app/data/bootstrap/password.backup /app/data/bootstrap/postgres-password; printf fixture > /app/data/edo.db")
	if output, err := initialize(); err == nil || !strings.Contains(output, "已有 SQLite 数据") {
		t.Fatal("已有 SQLite 不得静默切换 PostgreSQL")
	}
	// 入口实际将文件配置传给进程；替身只报告是否收到，不输出任何密钥。
	stub := filepath.Join(t.TempDir(), "edo")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nset -eu\ntest \"${#EDO_SECRETS_KEY}\" -eq 44\ncase \"$EDO_DATABASE_DSN\" in postgres://edo:*@postgres:5432/edo*) ;; *) exit 1 ;; esac\ntest \"$1\" = migrate\necho runtime-loaded\n"), 0o555); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("docker", "run", "--rm", "--network", "none", "--read-only", "--user", "10001:10001",
		"-v", data+":/app/data:ro", "-v", stub+":/app/edo:ro", "-v", filepath.Join(root, "deploy", "docker-entrypoint.sh")+":/entrypoint.sh:ro",
		"-e", "EDO_SECRETS_KEY_FILE=/app/data/bootstrap/secrets-key", "-e", "EDO_DATABASE_DRIVER=postgres", "-e", "EDO_DATABASE_DSN_FILE=/app/data/bootstrap/postgres-dsn",
		"alpine:3.22", "sh", "/entrypoint.sh", "migrate")
	if output, err := command.CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != "runtime-loaded" {
		t.Fatalf("入口未正确传递持久化配置：%v", err)
	}
}
