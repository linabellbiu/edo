package host

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"edo/internal/model"
)

type validatingClusterProbe struct {
	kubernetesProbeStub
	validationErr error
	guardErr      error
	seen          []model.Host
}

func (p *validatingClusterProbe) ValidateHost(_ context.Context, _ string, current model.Host) error {
	p.seen = append(p.seen, current)
	return p.validationErr
}

func (p *validatingClusterProbe) ValidateHostForSave(ctx context.Context, clusterID string, current model.Host) (func(*gorm.DB) error, error) {
	if err := p.ValidateHost(ctx, clusterID, current); err != nil {
		return nil, err
	}
	return func(*gorm.DB) error { return p.guardErr }, nil
}

func TestKubernetesHostMismatchDoesNotIssueHostTestToken(t *testing.T) {
	service, _, _ := newHostTestService(t)
	mismatch := errors.New("node identity mismatch")
	probe := &validatingClusterProbe{validationErr: mismatch}
	service.kubernetes = probe
	if tested, err := service.Test(context.Background(), hostTestInput()); !errors.Is(err, mismatch) || tested.Token != "" {
		t.Fatalf("节点身份不匹配不能获得保存令牌: %+v, %v", tested, err)
	}
	if len(service.tests) != 0 {
		t.Fatal("身份核对失败不能缓存成功测试")
	}
	if len(probe.seen) != 1 || probe.seen[0].SSHCredentialCiphertext == "" || probe.seen[0].SSHHostKeyFingerprint == "" || probe.seen[0].Address != "host.example.com" {
		t.Fatal("节点校验必须使用本次 SSH 测试指纹及加密凭据")
	}
}

func TestKubernetesHostCreateRevalidatesIdentityAndRollsBackGuardFailure(t *testing.T) {
	for _, changed := range []string{"identity", "connection_snapshot"} {
		t.Run(changed, func(t *testing.T) {
			service, db, _ := newHostTestService(t)
			probe := &validatingClusterProbe{}
			service.kubernetes = probe
			input := hostTestInput()
			tested, err := service.Test(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			input.TestToken = tested.Token
			rejected := errors.New("association changed after test")
			if changed == "identity" {
				probe.validationErr = rejected
			} else {
				probe.guardErr = rejected
			}
			if _, err := service.Create(context.Background(), "admin", input); !errors.Is(err, rejected) {
				t.Fatalf("保存前必须再次核对关联: %v", err)
			}
			var count int64
			if err := db.Model(&model.Host{}).Where("is_builtin = ?", false).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("校验失败不能创建主机: %d, %v", count, err)
			}
			if len(probe.seen) != 2 {
				t.Fatalf("测试和保存应分别验证身份: %d", len(probe.seen))
			}
		})
	}
}

func TestKubernetesHostUpdateRejectsMismatchAndKeepsSavedConnection(t *testing.T) {
	service, _, _ := newHostTestService(t)
	probe := &validatingClusterProbe{}
	service.kubernetes = probe
	input := hostTestInput()
	tested, err := service.Test(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.TestToken = tested.Token
	created, err := service.Create(context.Background(), "admin", input)
	if err != nil {
		t.Fatal(err)
	}
	input.Address = "other-host.example.com"
	tested, err = service.TestExisting(context.Background(), created.Host.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	input.TestToken = tested.Token
	probe.validationErr = errors.New("node identity changed")
	if _, err := service.Update(context.Background(), created.Host.ID, input); !errors.Is(err, probe.validationErr) {
		t.Fatalf("编辑主机也必须核对身份: %v", err)
	}
	current, err := service.Get(context.Background(), created.Host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Host.Address != created.Host.Address {
		t.Fatal("校验失败不能覆盖原主机地址")
	}
	var linked bool
	for _, capability := range current.Capabilities {
		if capability.Kind == model.HostCapabilityKubernetes && capability.RuntimeID == input.KubernetesClusterID {
			linked = true
		}
	}
	if !linked {
		t.Fatal("校验失败不能清除原集群关联")
	}
}

func TestKubernetesHostRequiresIdentityValidator(t *testing.T) {
	service, _, _ := newHostTestService(t)
	service.kubernetes = &associatedClusterProbe{}
	if _, err := service.Test(context.Background(), hostTestInput()); !errors.Is(err, ErrCapabilityUnavailable) {
		t.Fatalf("仅能 Ping 的探测器不能跳过身份核对: %v", err)
	}
}

type associatedClusterProbe struct {
	err error
}

func (p *associatedClusterProbe) Ping(context.Context, string) (string, error) {
	return "v1.35.5+k3s1", p.err
}

func TestExplicitKubernetesAssociationRefreshUsesClusterAPIWithoutSSHOrHostBinaries(t *testing.T) {
	_, db, secrets := newHostTestService(t)
	probe := &associatedClusterProbe{}
	// Docker/SSH 探测器为空且主机没有凭据，模拟只有容器内 K3s 的主机。
	service := NewService(db, secrets, nil, probe)
	host := model.Host{ID: "k3d-nas-host", Name: "K3s 容器主机", Mode: model.HostModeSSH, Address: "192.0.2.241", IsActive: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	capability := model.HostCapability{HostID: host.ID, Kind: model.HostCapabilityKubernetes, RuntimeID: "container-k3s-cluster", Status: model.HostCapabilityUnchecked, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.Create(&capability).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.RefreshRuntimeStatuses(context.Background()); err != nil {
		t.Fatal(err)
	}
	readCapability := func() model.HostCapability {
		t.Helper()
		var current model.HostCapability
		if err := db.Where("host_id = ? AND kind = ?", host.ID, model.HostCapabilityKubernetes).First(&current).Error; err != nil {
			t.Fatal(err)
		}
		return current
	}
	current := readCapability()
	if current.RuntimeID != capability.RuntimeID || current.Status != model.HostCapabilityReady || current.Version != "v1.35.5+k3s1" {
		t.Fatalf("关联集群可达时无需在主机中发现 kubelet/kubectl: %+v", current)
	}
	probe.err = errors.New("temporary cluster API failure")
	if _, err := service.RefreshRuntimeStatuses(context.Background()); err != nil {
		t.Fatal(err)
	}
	current = readCapability()
	if current.RuntimeID != capability.RuntimeID || current.Status != model.HostCapabilityUnreachable {
		t.Fatalf("临时 API 故障只能改变状态，不能清除显式关联: %+v", current)
	}
	probe.err = nil
	if _, err := service.RefreshRuntimeStatuses(context.Background()); err != nil {
		t.Fatal(err)
	}
	if current := readCapability(); current.RuntimeID != capability.RuntimeID || current.Status != model.HostCapabilityReady {
		t.Fatalf("API 恢复后应继续使用原集群关联: %+v", current)
	}
}
