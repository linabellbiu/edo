package host

import (
	"context"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"testing"

	"edo/internal/dockerengine"
	"edo/internal/hostcredential"
	"edo/internal/model"
)

func TestHostResourcesStrictPinRejectsChangedKeyBeforeAuthentication(t *testing.T) {
	for _, correct := range []bool{false, true} {
		t.Run(strconv.FormatBool(correct), func(t *testing.T) {
			s, db, keys := newHostTestService(t)
			server := newPinnedSSHFixture(t, "success")
			address, port, err := net.SplitHostPort(server.address)
			if err != nil {
				t.Fatal(err)
			}
			host := resourceFixture(t, s)
			plain, _ := json.Marshal(dockerengine.SSHBundle{Password: "test-password", UseSudo: true})
			ciphertext, err := keys.Encrypt(string(plain), hostcredential.AAD(host.ID))
			if err != nil {
				t.Fatal(err)
			}
			pin := "SHA256:" + strings.Repeat("A", 43)
			if correct {
				pin = server.fingerprint
			}
			sshPort, _ := strconv.Atoi(port)
			if err := db.Model(&model.Host{}).Where(&model.Host{ID: host.ID}).Updates(map[string]any{"address": address, "ssh_port": sshPort, "ssh_host_key_fingerprint": pin, "ssh_credential_ciphertext": ciphertext}).Error; err != nil {
				t.Fatal(err)
			}
			result, err := s.Resources(context.Background(), host.ID)
			if err != nil || result.State != "unavailable" {
				t.Fatalf("失败采集不应伪造容量: %+v %v", result, err)
			}
			server.mu.Lock()
			commands := append([]string(nil), server.commands...)
			server.mu.Unlock()
			if !correct && (server.auth.Load() != 0 || len(commands) != 0) {
				t.Fatal("指纹改变后仍认证或执行命令")
			}
			if correct && (server.auth.Load() != 1 || len(commands) != 1 || commands[0] != "env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin LC_ALL=C sh -se") {
				t.Fatalf("未使用固定非提权读取入口: %v", commands)
			}
			for _, command := range commands {
				if strings.Contains(command, "sudo") || strings.Contains(command, "test-password") {
					t.Fatal("资源读取发生提权或命令泄密")
				}
			}
		})
	}
}
