package sshdeploy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"edo/internal/config"
	"edo/internal/hostcredential"
	"edo/internal/model"
	"edo/internal/secret"
	"edo/internal/sshclient"
	"edo/internal/testutil"
)

func TestDiagnosticRedactionsOnlyFollowAuthorizedHostCredentialLoad(t *testing.T) {
	db := testutil.NewDatabase(t)
	manager, err := secret.New(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatal(err)
	}
	host := model.Host{ID: "diagnostic-host", Name: "诊断主机", Mode: model.HostModeSSH, Address: "127.0.0.1", SSHPort: 22, SSHUsername: "deploy", IsActive: true, ProjectID: "project-a", DepartmentID: "department-a", SSHHostKeyFingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(make([]byte, 32))}
	bundle := sshclient.Bundle{Password: "ssh-password-value", UseSudo: true, SudoPassword: "sudo-password-value"}
	plaintext, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	host.SSHCredentialCiphertext, err = manager.Encrypt(string(plaintext), hostcredential.AAD(host.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&model.HostCapability{HostID: host.ID, Kind: model.HostCapabilitySSH, Status: model.HostCapabilityReady}).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, manager, config.Runtime{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var values []string
	result, err := service.RunHostDeploymentScript(ctx, Input{HostID: host.ID, Script: ":", Timeout: 30 * time.Second, OnDiagnosticRedactions: func(secrets ...string) { values = append(values, secrets...); cancel() }})
	if !reflect.DeepEqual(values, []string{bundle.Password, "", "", bundle.SudoPassword}) || result.Started || result.ExitCode != -1 || err == nil {
		t.Fatalf("未在执行连接前取得准确脱敏值: result=%+v callback=%t err=%v", result, len(values) > 0, err)
	}
	for _, tc := range []struct{ name, hostID, environmentID string }{
		{"missing-host", "missing-host", ""},
		{"cross-project-group", host.ID, "other-project-environment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.environmentID != "" {
				if err := db.Create(&model.Environment{ID: tc.environmentID, Name: tc.environmentID, IsActive: true, ProjectID: "project-b", DepartmentID: host.DepartmentID}).Error; err != nil {
					t.Fatal(err)
				}
			}
			called := false
			_, err := service.RunHostDeploymentScript(context.Background(), Input{HostID: tc.hostID, EnvironmentID: tc.environmentID, Script: ":", Timeout: 30 * time.Second, OnDiagnosticRedactions: func(...string) { called = true }})
			if !errors.Is(err, ErrHostUnavailable) || called {
				t.Fatalf("主机拒绝后仍暴露脱敏秘密: callback=%t err=%v", called, err)
			}
		})
	}
}
