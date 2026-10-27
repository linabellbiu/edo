package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	mobyclient "github.com/moby/moby/client"
	"gorm.io/gorm"

	"edo/internal/dockerengine"
	hostmanager "edo/internal/host"
	"edo/internal/hostcredential"
	"edo/internal/model"
	"edo/internal/secret"
)

type hostSudoHTTPProbe struct {
	calls  int
	denied bool
	input  dockerengine.Input
}

func (p *hostSudoHTTPProbe) TestSSHConnection(_ context.Context, input dockerengine.Input) (dockerengine.SSHConnectionTestResult, error) {
	p.calls++
	p.input = input
	if p.denied {
		return dockerengine.SSHConnectionTestResult{}, dockerengine.ErrSSHSudoDenied
	}
	return dockerengine.SSHConnectionTestResult{Fingerprint: input.SSHHostKeyFingerprint, Architecture: model.HostArchitectureAMD64}, nil
}
func (p *hostSudoHTTPProbe) TestSSH(ctx context.Context, input dockerengine.Input) (dockerengine.SSHTestResult, error) {
	result, err := p.TestSSHConnection(ctx, input)
	return dockerengine.SSHTestResult{Fingerprint: result.Fingerprint, Architecture: result.Architecture, DockerVersion: "fixture"}, err
}
func (*hostSudoHTTPProbe) PingForMonitor(context.Context, string) (mobyclient.PingResult, error) {
	return mobyclient.PingResult{}, nil
}
func (*hostSudoHTTPProbe) PingBuilder(context.Context) error { return nil }

func TestHostSSHOnlySudoHTTPPreservesCredentialAndRequiresSuccessfulBoundTest(t *testing.T) {
	for _, scenario := range []string{"success", "missing-pin", "sudo-denied"} {
		t.Run(scenario, func(t *testing.T) {
			probe := &hostSudoHTTPProbe{denied: scenario == "sudo-denied"}
			var fixtureDB *gorm.DB
			var existing model.Host
			var keys *secret.Manager
			router, cleanup := newAuthTestRouter(t, func(deps *Dependencies, db *gorm.DB) {
				fixtureDB = db
				var err error
				keys, err = secret.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
				if err != nil {
					t.Fatal(err)
				}
				if err := db.Where(&model.Host{ID: "httpapi-deployment-host"}).First(&existing).Error; err != nil {
					t.Fatal(err)
				}
				plain, _ := json.Marshal(dockerengine.SSHBundle{Password: "fixture-preserved-password"})
				ciphertext, err := keys.Encrypt(string(plain), hostcredential.AAD(existing.ID))
				if err != nil {
					t.Fatal(err)
				}
				pin := "SHA256:" + strings.Repeat("A", 43)
				if scenario == "missing-pin" {
					pin = ""
				}
				if err := db.Model(&existing).Updates(map[string]any{"ssh_credential_ciphertext": ciphertext, "ssh_host_key_fingerprint": pin, "ssh_auth_type": model.SSHAuthPassword}).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Where(&model.Host{ID: existing.ID}).First(&existing).Error; err != nil {
					t.Fatal(err)
				}
				deps.Hosts = hostmanager.NewService(db, keys, probe, nil)
			})
			defer cleanup()
			admin := loginForDepartmentTest(t, router, "admin")
			path := "/api/v1/hosts/" + existing.ID
			body := map[string]any{"name": existing.Name, "address": existing.Address, "ssh_port": existing.SSHPort, "ssh_username": existing.SSHUsername, "ssh_auth_type": existing.SSHAuthType, "capability_kinds": []string{"ssh"}, "reuse_credential": true, "use_sudo": true}
			response := performJSONRequest(t, router, http.MethodPost, path+"/test", body, admin)
			if scenario != "success" {
				want, code := http.StatusConflict, "host_ssh_fingerprint_required"
				if scenario == "sudo-denied" {
					want, code = http.StatusBadGateway, "host_sudo_denied"
				}
				if response.Code != want || !strings.Contains(response.Body.String(), code) || strings.Contains(response.Body.String(), "test_token") || strings.Contains(response.Body.String(), "fixture-preserved-password") {
					t.Fatalf("错误测试未安全拒绝: %d %s", response.Code, response.Body.String())
				}
				if scenario == "missing-pin" && probe.calls != 0 {
					t.Fatal("缺失pin仍调用认证测试")
				}
				return
			}
			var tested hostmanager.TestResult
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &tested) != nil || tested.Token == "" || probe.input.SSHHostKeyFingerprint != existing.SSHHostKeyFingerprint || probe.input.SSH == nil || !probe.input.SSH.UseSudo {
				t.Fatalf("未固定pin测试sudo: %d %s", response.Code, response.Body.String())
			}
			var before struct {
				Host struct {
					UseSudo bool `json:"use_sudo"`
				} `json:"host"`
			}
			got := performJSONRequest(t, router, http.MethodGet, path, nil, admin)
			if got.Code != http.StatusOK || json.Unmarshal(got.Body.Bytes(), &before) != nil || before.Host.UseSudo {
				t.Fatal("仅测试不应更新保存的sudo授权")
			}
			body["test_token"] = tested.Token
			saved := performJSONRequest(t, router, http.MethodPut, path, body, admin)
			var after struct {
				Host struct {
					UseSudo      bool                     `json:"use_sudo"`
					Capabilities []hostCapabilityResponse `json:"capabilities"`
				} `json:"host"`
			}
			if saved.Code != http.StatusOK || json.Unmarshal(saved.Body.Bytes(), &after) != nil || !after.Host.UseSudo || len(after.Host.Capabilities) != 1 || after.Host.Capabilities[0].Kind != model.HostCapabilitySSH || after.Host.Capabilities[0].UseSudo || strings.Contains(saved.Body.String(), "fixture-preserved-password") {
				t.Fatalf("未保存SSH-only真实授权摘要: %d %s", saved.Code, saved.Body.String())
			}
			var stored model.Host
			if err := fixtureDB.Where(&model.Host{ID: existing.ID}).First(&stored).Error; err != nil {
				t.Fatal(err)
			}
			plain, err := keys.Decrypt(stored.SSHCredentialCiphertext, hostcredential.AAD(stored.ID))
			if err != nil {
				t.Fatal(err)
			}
			var bundle dockerengine.SSHBundle
			if json.Unmarshal([]byte(plain), &bundle) != nil || bundle.Password != "fixture-preserved-password" || !bundle.UseSudo {
				t.Fatal("保存sudo丢失原认证凭据")
			}
			repeated := performJSONRequest(t, router, http.MethodPut, path, body, admin)
			if repeated.Code != http.StatusConflict {
				t.Fatal("测试令牌可重复保存")
			}
		})
	}
}
