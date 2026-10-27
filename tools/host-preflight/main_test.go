package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/hostcredential"
	"edo/internal/model"
	"edo/internal/secret"
	"edo/internal/sshclient"
)

func fixtureOptions() options {
	return options{envFile: "/fixture/runtime.env", workingDirectory: "/fixture", hostID: "d67b480f-35d0-4af4-bb11-9117b517208f", address: "192.0.2.10", username: "operator", fingerprint: "SHA256:" + strings.Repeat("A", 43), port: 22, timeout: 2 * time.Minute}
}

func fixtureArgs() []string {
	o := fixtureOptions()
	return []string{"--env-file", o.envFile, "--working-directory", o.workingDirectory, "--host-id", o.hostID, "--expect-address", o.address, "--expect-port", "22", "--expect-user", o.username, "--expect-fingerprint", o.fingerprint}
}

func TestParametersRequireExplicitMatchingIdentityAndNoCommand(t *testing.T) {
	if _, err := parseOptions(fixtureArgs()); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < len(fixtureArgs()); index += 2 {
		args := append([]string(nil), fixtureArgs()...)
		args = append(args[:index], args[index+2:]...)
		if _, err := parseOptions(args); err == nil {
			t.Fatalf("缺少参数仍被接受: %d", index)
		}
	}
	for _, bad := range []struct{ flag, value string }{
		{"--env-file", "relative.env"}, {"--working-directory", "relative"}, {"--host-id", "not-a-uuid"},
		{"--host-id", "00000000-0000-0000-0000-000000000000"}, {"--expect-address", "0.0.0.0"}, {"--expect-address", "127.0.0.1"},
		{"--expect-address", "host.invalid"}, {"--expect-address", "192.0.2.10;id"}, {"--expect-port", "0"}, {"--expect-port", "65536"},
		{"--expect-user", "operator;id"}, {"--expect-fingerprint", "SHA256:truncated"}, {"--timeout", "11m"}, {"--command", "id"},
	} {
		t.Run(bad.flag+bad.value, func(t *testing.T) {
			args := append(fixtureArgs(), bad.flag, bad.value)
			if _, err := parseOptions(args); err == nil {
				t.Fatal("无效或任意命令参数被接受")
			}
		})
	}
	if _, err := parseOptions(append(fixtureArgs(), "id")); err == nil {
		t.Fatal("接受额外命令")
	}
}

func TestSQLiteReadOnlyAddressNeverCreatesMissingDatabase(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "fixture.db")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, dsn := range []string{"fixture.db", "file:" + path, "file://" + path + "?mode=ro"} {
		value, err := readOnlySQLiteDSN(dsn, directory)
		if err != nil || !strings.Contains(value, "mode=ro") || !strings.Contains(value, "query_only%281%29") {
			t.Fatalf("未强制只读: %q %v", value, err)
		}
	}
	for _, dsn := range []string{"missing.db", ":memory:", "file::memory:?cache=shared", "file:" + path + "?mode=rw", "file:" + path + "?mode=memory"} {
		if _, err := readOnlySQLiteDSN(dsn, directory); err == nil {
			t.Fatalf("接受非持久只读文件: %q", dsn)
		}
	}
	if _, err := os.Stat(filepath.Join(directory, "missing.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("创建了缺失数据库")
	}
}

func TestConfigurationOnlyUsesExplicitFileAndRejectsInvalidDriver(t *testing.T) {
	o := fixtureOptions()
	o.workingDirectory = t.TempDir()
	o.envFile = filepath.Join(o.workingDirectory, "runtime.env")
	t.Setenv("EDO_DATABASE_DRIVER", "sqlite")
	if err := os.WriteFile(o.envFile, []byte("EDO_DATABASE_DRIVER=postgres\nEDO_DATABASE_DSN=fixture-only-dsn\nEDO_SECRETS_KEY=fixture-only-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, key, err := readConfiguration(o)
	if err != nil || cfg.Driver != "postgres" || cfg.DSN != "fixture-only-dsn" || key != "fixture-only-key" {
		t.Fatalf("错误读取配置: %v", err)
	}
	if err := os.WriteFile(o.envFile, []byte("EDO_DATABASE_DRIVER=unknown\nEDO_DATABASE_DSN=fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readConfiguration(o); err == nil {
		t.Fatal("无效driver回退到默认数据库")
	}
}

func TestSavedCredentialReadIsBoundToTargetAndDoesNotChangeSQLite(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "fixture.db")
	cfg := config.Database{Driver: "sqlite", DSN: path, MaxOpenConns: 1, MaxIdleConns: 0, ConnMaxLifetime: time.Minute}
	db, err := database.Open(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Host{}, &model.HostCapability{}); err != nil {
		t.Fatal(err)
	}
	o := fixtureOptions()
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	manager, _ := secret.New(key)
	plain, _ := json.Marshal(sshclient.Bundle{Password: "fixture-ssh-password", UseSudo: true})
	ciphertext, err := manager.Encrypt(string(plain), hostcredential.AAD(o.hostID))
	if err != nil {
		t.Fatal(err)
	}
	host := model.Host{ID: o.hostID, Name: "Fixture host", Mode: model.HostModeSSH, Address: o.address, SSHPort: o.port, SSHUsername: o.username, SSHAuthType: model.SSHAuthPassword, SSHHostKeyFingerprint: o.fingerprint, SSHCredentialCiphertext: ciphertext, ProjectID: uuid.NewString(), DepartmentID: uuid.NewString(), IsActive: true}
	if err := db.Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.HostCapability{HostID: host.ID, Kind: model.HostCapabilitySSH, Status: model.HostCapabilityReady}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Close(db); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DSN, err = readOnlySQLiteDSN(path, directory)
	if err != nil {
		t.Fatal(err)
	}
	loaded, bundle, err := loadTarget(context.Background(), o, cfg, key)
	if err != nil || loaded.ID != host.ID || bundle.Password != "fixture-ssh-password" {
		t.Fatalf("未按AAD读取固定目标: %v", err)
	}
	for _, mutate := range []func(*options){func(o *options) { o.address = "192.0.2.11" }, func(o *options) { o.port = 2222 }, func(o *options) { o.username = "other" }, func(o *options) { o.fingerprint = "SHA256:" + strings.Repeat("B", 43) }} {
		other := o
		mutate(&other)
		if _, _, err := loadTarget(context.Background(), other, cfg, key); err == nil {
			t.Fatal("目标变化仍可取得凭据")
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("只读预检修改了数据库文件")
	}
	readOnly, err := database.Open(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(readOnly)
	if err := readOnly.Model(&model.Host{}).Where(&model.Host{ID: host.ID}).Update("name", "must-not-write").Error; err == nil {
		t.Fatal("只读连接允许写入")
	}
}

func TestSudoPasswordFramingKeepsScriptSeparateAndParentAlive(t *testing.T) {
	for _, mode := range []string{"password", "nopasswd", "denied"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			parentFile := filepath.Join(directory, "parent")
			stub := `#!/bin/sh
case " $* " in
  *" -k "*) exit 89;;
  *" -v "*)
    [ "$EDO_TEST_MODE" != denied ] || exit 88
    if [ "$EDO_TEST_MODE" = password ]; then IFS= read -r value; [ "$value" = fixture-sudo-only ] || exit 87; fi
    printf '%s' "$PPID" > "$EDO_TEST_PARENT"
    exit 0;;
esac
[ "$1" = -n ] || exit 86
[ "$(cat "$EDO_TEST_PARENT")" = "$PPID" ] || exit 85
shift
[ "$1" = -- ] && shift
exec "$@"
`
			if err := os.WriteFile(filepath.Join(directory, "sudo"), []byte(stub), 0700); err != nil {
				t.Fatal(err)
			}
			command, prefix, err := sudoCommand("operator", sshclient.Bundle{UseSudo: true, SudoPassword: "fixture-sudo-only"}, false)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			process := exec.CommandContext(ctx, "/bin/sh", "-c", command)
			process.Env = append(os.Environ(), "PATH="+directory+":"+os.Getenv("PATH"), "EDO_TEST_MODE="+mode, "EDO_TEST_PARENT="+parentFile)
			process.Stdin = strings.NewReader(prefix + "printf 'fixture-script-ok\\n'\n")
			output, err := process.CombinedOutput()
			if mode == "denied" {
				if err == nil || len(output) != 0 {
					t.Fatalf("拒绝认证仍执行脚本: %v %s", err, output)
				}
				return
			}
			if err != nil || string(output) != "fixture-script-ok\n" {
				t.Fatalf("sudo未保持安全分帧/相同父进程: %v %s", err, output)
			}
		})
	}
	if _, _, err := sudoCommand("operator", sshclient.Bundle{}, true); err == nil {
		t.Fatal("忽略保存的sudo禁用状态")
	}
	if _, prefix, err := sudoCommand("root", sshclient.Bundle{}, true); err != nil || prefix != "" {
		t.Fatal("root不应发送密码行")
	}
}

func TestDockerFunctionForcesLocalSocketAndClearsInheritedContext(t *testing.T) {
	directory := t.TempDir()
	stub := `#!/bin/sh
[ "$DOCKER_HOST" = unix:///var/run/docker.sock ] || exit 81
[ -z "${DOCKER_CONTEXT:-}${DOCKER_TLS_VERIFY:-}${KUBECONFIG:-}" ] || exit 82
[ "$1" = --host=unix:///var/run/docker.sock ] || exit 83
[ "$2" = version ] || exit 84
printf 'fixture-local-docker\n'
`
	docker := filepath.Join(directory, "docker")
	if err := os.WriteFile(docker, []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	var function string
	for _, line := range strings.Split(preflightScript, "\n") {
		if strings.HasPrefix(line, "edo_docker()") {
			function = line
		}
	}
	if function == "" {
		t.Fatal("脚本没有固定daemon包装")
	}
	process := exec.Command("/bin/sh", "-s")
	process.Stdin = strings.NewReader("set -eu\nedo_docker_binary='" + docker + "'\n" + function + "\nedo_docker version\n")
	process.Env = append(os.Environ(), "DOCKER_HOST=tcp://wrong.invalid:2375", "DOCKER_CONTEXT=wrong", "DOCKER_TLS_VERIFY=1", "KUBECONFIG=private")
	output, err := process.CombinedOutput()
	if err != nil || string(output) != "fixture-local-docker\n" {
		t.Fatalf("Docker目标或环境未冻结: %v %s", err, output)
	}
	for _, unsafe := range []string{"docker run", "cluster delete", "systemctl restart", ".Config.Env", "ps -ef", "sudo -S -k"} {
		if strings.Contains(preflightScript, unsafe) {
			t.Fatalf("固定只读脚本出现危险入口: %s", unsafe)
		}
	}
	if !strings.Contains(preflightScript, "UNAVAILABLE") || strings.Contains(preflightScript, "ip -brief") {
		t.Fatal("缺少可选工具降级标记")
	}
}

func TestLimitedConcurrentOutputAndSecretRedaction(t *testing.T) {
	var buffer limitedBuffer
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() { _, _ = buffer.Write([]byte(strings.Repeat("x", 64*1024))) })
	}
	group.Wait()
	value, exceeded := buffer.snapshot()
	if !exceeded || len(value) != 256*1024 {
		t.Fatal("输出缓存无界或未标记超限")
	}
	value = redact("fixture-password\nfixture-private-key-line\n", []string{"fixture-password", "fixture-private-key-line\nnext-line"})
	if strings.Contains(value, "fixture-password") || strings.Contains(value, "fixture-private-key-line") {
		t.Fatal("凭据未脱敏")
	}
}
