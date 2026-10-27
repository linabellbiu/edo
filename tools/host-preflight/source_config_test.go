package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.yaml.in/yaml/v3"

	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/secret"
)

func sourceFixtureOptions() options {
	o := fixtureOptions()
	o.configureNativeImages = true
	o.nativeInstallationID = uuid.NewString()
	o.nativeClusterID = uuid.NewString()
	return o
}

func sourceFixtureBinding() nativeSourceBinding {
	return nativeSourceBinding{InstallationID: "11111111-1111-4111-8111-111111111111", HostID: "22222222-2222-4222-8222-222222222222", Version: "v1.37.1+k3s1", DataDir: "/storage/edo-k3s/fixture", NodeName: "fixture"}
}

func sourceFixtureYAML(b nativeSourceBinding) []byte {
	return []byte("data-dir: " + b.DataDir + "\nnode-name: " + b.NodeName + "\nnode-label:\n  - edo.dev/installation=" + b.InstallationID + "\n  - edo.dev/host=" + b.HostID + "\ntoken: 'PRIVATE: password 123'\nunknown-extension:\n  numeric-looking-string: '001'\n  boolean-looking-string: 'on'\n  nested: &private\n    key: secret\n  copy: *private\ndisable:\n  - traefik\n  - servicelb\n")
}

func TestNativeSourceFlagsAreExplicitExclusiveAndRequireSavedAuthorization(t *testing.T) {
	o := sourceFixtureOptions()
	args := append(fixtureArgs(), "--configure-native-k3s-china-images", "--expect-native-installation-id", o.nativeInstallationID, "--expect-native-cluster-id", o.nativeClusterID)
	if _, err := parseOptions(args); err != nil {
		t.Fatal(err)
	}
	for _, extra := range [][]string{{"--test-sudo"}, {"--test-sudo=false"}, {"--stop-only-k3d"}, {"--backup-stop-k3d"}, {"--expect-container-id", strings.Repeat("a", 64)}, {"--expect-native-cluster-id", "invalid"}, {"--expect-native-installation-id", ""}, {"--backup-directory", ""}, {"--k3d-cluster", "edo-host-link"}} {
		if _, err := parseOptions(append(args, extra...)); err == nil {
			t.Fatalf("源维护接受冲突参数 %s", extra[0])
		}
	}
	for _, flag := range []string{"--expect-native-installation-id", "--expect-native-cluster-id"} {
		if _, err := parseOptions(append(fixtureArgs(), flag, "")); err == nil {
			t.Fatal("只读模式接受源维护参数")
		}
	}
	for _, mutate := range []func(*options){func(o *options) { o.testSudo = true }, func(o *options) { o.stopOnlyK3d = true }, func(o *options) { o.nativeInstallationID = "" }} {
		invalid := o
		mutate(&invalid)
		stage := ""
		if err := run(context.Background(), invalid, io.Discard, &stage); err == nil || stage != "校验固定运维范围" {
			t.Fatal("run未在读取配置前拒绝冲突")
		}
	}
}

func TestNativeSourceYAMLPreservesAllUnrelatedFieldsAndSecrets(t *testing.T) {
	b := sourceFixtureBinding()
	original := sourceFixtureYAML(b)
	updated, changed, err := updateNativeSourceYAML(original, b)
	if err != nil || !changed {
		t.Fatal(err)
	}
	var before, after map[string]any
	if yaml.Unmarshal(original, &before) != nil || yaml.Unmarshal(updated, &after) != nil {
		t.Fatal("配置解析失败")
	}
	if after["system-default-registry"] != nativeChinaRegistry {
		t.Fatal("未限定国内系统镜像源")
	}
	delete(after, "system-default-registry")
	if !reflect.DeepEqual(before, after) || !bytes.Contains(updated, []byte("PRIVATE: password 123")) {
		t.Fatal("维护改变了其他配置或凭据")
	}
	again, changed, err := updateNativeSourceYAML(updated, b)
	if err != nil || changed || !bytes.Equal(again, updated) {
		t.Fatal("已配置时不应再次改写")
	}
	for _, value := range []string{string(original) + "system-default-registry: unknown.example\n", string(original) + "data-dir: /other\n", strings.Replace(string(original), b.DataDir, "/other", 1), strings.Replace(string(original), b.InstallationID, uuid.NewString(), 1), strings.Replace(string(original), b.HostID, uuid.NewString(), 1), string(original) + "---\nkey: value\n", strings.Repeat("x", 65537)} {
		if _, _, err := updateNativeSourceYAML([]byte(value), b); err == nil || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatal("接受未知配置或泄密")
		}
	}
}

func TestNativeSourceBindingRequiresFailedOwnedNativeInstallationAndIsReadOnly(t *testing.T) {
	for _, mode := range []string{"success", "running", "wrong-cluster", "wrong-host", "k3d", "wrong-endpoint", "wrong-project", "wrong-pin", "active-lock"} {
		t.Run(mode, func(t *testing.T) {
			o := sourceFixtureOptions()
			b := sourceFixtureBinding()
			o.hostID = b.HostID
			key := base64.StdEncoding.EncodeToString(make([]byte, 32))
			manager, _ := secret.New(key)
			dbPath := filepath.Join(t.TempDir(), "fixture.db")
			cfg := config.Database{Driver: "sqlite", DSN: dbPath, MaxOpenConns: 1}
			db, err := database.Open(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			if err := db.AutoMigrate(&model.Host{}, &model.HostCapability{}, &model.KubernetesCluster{}, &model.K3sInstallation{}, &model.K3sInstallationHostLock{}); err != nil {
				t.Fatal(err)
			}
			project, department := uuid.NewString(), uuid.NewString()
			host := model.Host{ID: o.hostID, Name: "fixture", Mode: model.HostModeSSH, Address: o.address, SSHPort: o.port, SSHUsername: o.username, SSHCredentialCiphertext: "private-cipher", SSHHostKeyFingerprint: o.fingerprint, ProjectID: project, DepartmentID: department, IsActive: true}
			input := map[string]any{"action": "create", "mode": "native", "server_host_id": host.ID, "endpoint": "https://" + o.address + ":6443", "server_options": map[string]any{"data-dir": b.DataDir, "node-name": b.NodeName}, "installer": map[string]any{"INSTALL_K3S_VERSION": b.Version}}
			if mode == "wrong-host" {
				input["server_host_id"] = uuid.NewString()
			}
			if mode == "k3d" {
				input["mode"] = "k3d"
			}
			if mode == "wrong-endpoint" {
				input["endpoint"] = "https://192.0.2.99:6443"
			}
			plain, _ := json.Marshal(map[string]any{"input": input})
			cipher, _ := manager.Encrypt(string(plain), []byte("k3s-installation:"+o.nativeInstallationID))
			record := model.K3sInstallation{ID: o.nativeInstallationID, Action: "create", Status: "failed", ClusterID: o.nativeClusterID, ProjectID: project, DepartmentID: department, InputCiphertext: cipher, Preview: []byte("{}"), Hosts: []byte("[]")}
			if mode == "running" {
				record.Status = "running"
			}
			if mode == "wrong-cluster" {
				record.ClusterID = uuid.NewString()
			}
			if mode == "wrong-project" {
				record.ProjectID = uuid.NewString()
			}
			cluster := model.KubernetesCluster{ID: o.nativeClusterID, Name: "fixture", Mode: model.KubernetesKubeconfig, APIServer: "https://" + o.address + ":6443", ProjectID: project, DepartmentID: department, IsActive: true}
			for _, row := range []any{&host, &cluster, &record, &model.HostCapability{HostID: host.ID, Kind: model.HostCapabilityKubernetes, RuntimeID: cluster.ID}} {
				if err := db.Create(row).Error; err != nil {
					t.Fatal(err)
				}
			}
			if mode == "wrong-pin" {
				if err := db.Model(&model.Host{}).Where(&model.Host{ID: host.ID}).Update("ssh_host_key_fingerprint", "other").Error; err != nil {
					t.Fatal(err)
				}
			}
			if mode == "active-lock" {
				if err := db.Create(&model.K3sInstallationHostLock{HostID: host.ID, InstallationID: record.ID}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := database.Close(db); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(dbPath)
			cfg.DSN, err = readOnlySQLiteDSN(dbPath, filepath.Dir(dbPath))
			if err != nil {
				t.Fatal(err)
			}
			binding, err := loadNativeSourceBinding(context.Background(), o, cfg, key, host)
			if (err == nil) != (mode == "success") {
				t.Fatalf("安装范围检查错误 %s: %v", mode, err)
			}
			if err == nil && (binding.InstallationID != record.ID || binding.Version != b.Version) {
				t.Fatal("绑定未冻结")
			}
			after, _ := os.ReadFile(dbPath)
			if !bytes.Equal(before, after) {
				t.Fatal("维护查询修改业务数据库")
			}
		})
	}
}

func TestNativeSourceWriteShellIsAtomicAndOnlyRestartsOwnedService(t *testing.T) {
	for _, mode := range []string{"success", "changed-config", "bad-registry", "changed-guard", "restart-failed"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			directory := filepath.Join(root, "k3s")
			if os.Mkdir(bin, 0700) != nil || os.Mkdir(directory, 0700) != nil {
				t.Fatal("创建夹具失败")
			}
			file := filepath.Join(directory, "config.yaml")
			old := []byte("token: PRIVATE\n")
			updated := []byte("token: PRIVATE\nsystem-default-registry: " + nativeChinaRegistry + "\n")
			if err := os.WriteFile(file, old, 0600); err != nil {
				t.Fatal(err)
			}
			calls := filepath.Join(root, "calls")
			write := func(name, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			write("curl", "printf '%s' "+map[bool]string{true: "503", false: "401"}[mode == "bad-registry"]+"\n")
			write("systemctl", "printf '%s\\n' \"$*\" >> '"+calls+"'\n"+map[bool]string{true: "exit 1\n", false: "exit 0\n"}[mode == "restart-failed"])
			identity := "fixture-identity"
			guard := strings.Repeat("a", 64)
			sum := sha256.Sum256(old)
			oldHash := hex.EncodeToString(sum[:])
			sum = sha256.Sum256(updated)
			newHash := hex.EncodeToString(sum[:])
			prefix := "set -eu\nedo_fail(){ printf 'EDO_NATIVE_SOURCE_FAILED=%s\\n' \"$1\" >&2;exit 82; }\nedo_check(){ :; }\nedo_guard_hash(){ printf '%s' '" + guard + "'; }\nedo_hash(){ shasum -a 256 \"$1\" | awk '{print $1}'; }\nstat(){ printf '%s' '" + identity + "'; }\n"
			if mode == "changed-config" {
				oldHash = strings.Repeat("b", 64)
			}
			if mode == "changed-guard" {
				guard = strings.Repeat("b", 64)
			}
			prefix += "edo_expected_identity='" + identity + "'\nedo_expected_hash='" + oldHash + "'\nedo_expected_guard='" + guard + "'\nedo_updated_hash='" + newHash + "'\nedo_updated_config='" + base64.StdEncoding.EncodeToString(updated) + "'\nPATH='" + bin + ":" + os.Getenv("PATH") + "'\nexport PATH\n"
			script := prefix + strings.ReplaceAll(nativeSourceWrite, "/etc/rancher/k3s", directory)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "sh", "-se")
			cmd.Stdin = strings.NewReader(script)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			if (err == nil) != (mode == "success") {
				t.Fatalf("维护脚本状态错误 %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			}
			log, _ := os.ReadFile(calls)
			wantRestart := mode == "success" || mode == "restart-failed"
			if (string(log) == "restart k3s.service\n") != wantRestart {
				t.Fatalf("越界或错误重启 %q", log)
			}
			actual, _ := os.ReadFile(file)
			if wantRestart && !bytes.Equal(actual, updated) {
				t.Fatal("没有保留原子更新结果")
			}
			if !wantRestart && !bytes.Equal(actual, old) {
				t.Fatal("拒绝路径覆盖配置")
			}
			if bytes.Contains(stdout.Bytes(), []byte("PRIVATE")) || bytes.Contains(stderr.Bytes(), []byte("PRIVATE")) {
				t.Fatal("配置泄密")
			}
		})
	}
}
