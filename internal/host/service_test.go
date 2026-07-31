package host

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	mobyclient "github.com/moby/moby/client"
	"gorm.io/gorm"

	"zrt/internal/config"
	"zrt/internal/database"
	"zrt/internal/dockerengine"
	"zrt/internal/hostcredential"
	"zrt/internal/model"
	"zrt/internal/secret"
)

type dockerProbeStub struct {
	fingerprint string
}

func (s dockerProbeStub) TestSSH(context.Context, dockerengine.Input) (dockerengine.SSHTestResult, error) {
	return dockerengine.SSHTestResult{
		Fingerprint: s.fingerprint, DockerVersion: "27.2.0",
	}, nil
}

func (s dockerProbeStub) TestSSHConnection(context.Context, dockerengine.Input) (dockerengine.SSHConnectionTestResult, error) {
	return dockerengine.SSHConnectionTestResult{Fingerprint: s.fingerprint}, nil
}

func (s dockerProbeStub) PingForMonitor(context.Context, string) (mobyclient.PingResult, error) {
	return mobyclient.PingResult{APIVersion: "27.2.0"}, nil
}

func (s dockerProbeStub) PingBuilder(context.Context) error { return nil }

type kubernetesProbeStub struct{}

func (kubernetesProbeStub) Ping(context.Context, string) (string, error) {
	return "v1.31.2", nil
}

type recordingDockerProbe struct {
	fingerprint string
	input       dockerengine.Input
	dockerErr   error
}

func (s *recordingDockerProbe) TestSSH(_ context.Context, input dockerengine.Input) (dockerengine.SSHTestResult, error) {
	s.input = input
	if s.dockerErr != nil {
		return dockerengine.SSHTestResult{}, s.dockerErr
	}
	return dockerengine.SSHTestResult{Fingerprint: s.fingerprint, DockerVersion: "27.2.0"}, nil
}

func (s *recordingDockerProbe) TestSSHConnection(_ context.Context, input dockerengine.Input) (dockerengine.SSHConnectionTestResult, error) {
	s.input = input
	return dockerengine.SSHConnectionTestResult{Fingerprint: s.fingerprint}, nil
}

func (s *recordingDockerProbe) PingForMonitor(context.Context, string) (mobyclient.PingResult, error) {
	if s.dockerErr != nil {
		return mobyclient.PingResult{}, s.dockerErr
	}
	return mobyclient.PingResult{APIVersion: "27.2.0"}, nil
}

func (s *recordingDockerProbe) PingBuilder(context.Context) error { return s.dockerErr }

type blockingDockerProbe struct {
	fingerprint string
	started     chan struct{}
	release     chan struct{}
	mu          sync.Mutex
	calls       int
	active      int
	maxActive   int
}

func (s *blockingDockerProbe) TestSSH(context.Context, dockerengine.Input) (dockerengine.SSHTestResult, error) {
	return dockerengine.SSHTestResult{Fingerprint: s.fingerprint, DockerVersion: "27.2.0"}, nil
}

func (s *blockingDockerProbe) PingForMonitor(ctx context.Context, _ string) (mobyclient.PingResult, error) {
	s.mu.Lock()
	s.calls++
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	s.mu.Unlock()
	select {
	case s.started <- struct{}{}:
	default:
	}
	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}()
	select {
	case <-s.release:
		return mobyclient.PingResult{APIVersion: "27.2.0"}, nil
	case <-ctx.Done():
		return mobyclient.PingResult{}, ctx.Err()
	}
}

func (s *blockingDockerProbe) TestSSHConnection(context.Context, dockerengine.Input) (dockerengine.SSHConnectionTestResult, error) {
	return dockerengine.SSHConnectionTestResult{Fingerprint: s.fingerprint}, nil
}

func (s *blockingDockerProbe) PingBuilder(context.Context) error { return nil }

func TestHostCreateRequiresInputBoundTestAndEncryptsCredential(t *testing.T) {
	service, db, secrets := newHostTestService(t)
	ctx := context.Background()
	input := hostTestInput()

	tested, err := service.Test(ctx, input)
	if err != nil {
		t.Fatalf("测试主机失败: %v", err)
	}
	changed := input
	changed.Name = "被修改的主机"
	changed.TestToken = tested.Token
	if _, err := service.Create(ctx, "admin", changed); !errors.Is(err, ErrHostTestRequired) {
		t.Fatalf("测试令牌未绑定完整输入: %v", err)
	}

	tested, err = service.Test(ctx, input)
	if err != nil {
		t.Fatalf("重新测试主机失败: %v", err)
	}
	input.TestToken = tested.Token
	created, err := service.Create(ctx, "admin", input)
	if err != nil {
		t.Fatalf("创建主机失败: %v", err)
	}
	if created.Host.SSHCredentialCiphertext == "" ||
		created.Host.SSHCredentialCiphertext == input.SSH.Password ||
		created.Host.SSHHostKeyFingerprint != tested.Fingerprint {
		t.Fatalf("主机 SSH 凭据或指纹保存错误: %+v", created.Host)
	}
	if len(created.Capabilities) != 2 {
		t.Fatalf("主机能力没有完整保存: %+v", created.Capabilities)
	}

	plaintext, err := secrets.Decrypt(
		created.Host.SSHCredentialCiphertext,
		hostcredential.AAD(created.Host.ID),
	)
	if err != nil {
		t.Fatalf("无法使用主机 AAD 解密凭据: %v", err)
	}
	var stored dockerengine.SSHBundle
	if err := json.Unmarshal([]byte(plaintext), &stored); err != nil {
		t.Fatalf("解析主机 SSH 凭据失败: %v", err)
	}
	if stored.Password != input.SSH.Password || !stored.UseSudo {
		t.Fatalf("加密保存的 SSH 配置不完整: %+v", stored)
	}

	var endpoint model.DockerEndpoint
	if err := db.First(&endpoint, "host_id = ?", created.Host.ID).Error; err != nil {
		t.Fatalf("未创建主机对应的 Docker 运行时: %v", err)
	}
	if endpoint.SSHCredentialCiphertext != "" || endpoint.HostID != created.Host.ID {
		t.Fatalf("新 Docker 运行时不应重复保存 SSH 密文: %+v", endpoint)
	}
	if _, err := service.Create(ctx, "admin", input); !errors.Is(err, ErrHostTestRequired) {
		t.Fatalf("一次性测试令牌被重复使用: %v", err)
	}
}

func TestHostDetailReturnsAllEnvironmentsAndRemovalCleansMemberships(t *testing.T) {
	service, db, _ := newHostTestService(t)
	ctx := context.Background()
	input := hostTestInput()
	input.Name = "共享主机"
	input.CapabilityKinds = []model.HostCapabilityKind{model.HostCapabilitySSH}
	tested, err := service.Test(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	input.TestToken = tested.Token
	created, err := service.Create(ctx, "admin", input)
	if err != nil {
		t.Fatal(err)
	}
	if created.EnvironmentIDs == nil || len(created.EnvironmentIDs) != 0 {
		t.Fatalf("新主机环境列表必须返回空数组: %+v", created.EnvironmentIDs)
	}
	now := time.Now().UTC()
	environments := []model.Environment{
		{ID: "environment-a", Name: "环境 A", IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
		{ID: "environment-b", Name: "环境 B", IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
	}
	if err := db.Create(&environments).Error; err != nil {
		t.Fatal(err)
	}
	memberships := []model.EnvironmentHost{
		{EnvironmentID: environments[1].ID, HostID: created.Host.ID, CreatedAt: now},
		{EnvironmentID: environments[0].ID, HostID: created.Host.ID, CreatedAt: now},
	}
	if err := db.Create(&memberships).Error; err != nil {
		t.Fatal(err)
	}
	detail, err := service.Get(ctx, created.Host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(detail.EnvironmentIDs, []string{"environment-a", "environment-b"}) {
		t.Fatalf("主机详情未返回全部环境: %+v", detail.EnvironmentIDs)
	}
	if err := service.Remove(ctx, created.Host.ID); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&model.EnvironmentHost{}).Where("host_id = ?", created.Host.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("删除主机后仍残留环境关联: count=%d err=%v", count, err)
	}
}

func TestHostCapabilityAndRemovalProtectReferencedRuntime(t *testing.T) {
	service, db, _ := newHostTestService(t)
	ctx := context.Background()
	input := hostTestInput()
	input.CapabilityKinds = []model.HostCapabilityKind{model.HostCapabilityDocker}
	input.KubernetesClusterID = ""
	tested, err := service.Test(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	input.TestToken = tested.Token
	created, err := service.Create(ctx, "admin", input)
	if err != nil {
		t.Fatal(err)
	}
	runtimeID := created.Capabilities[0].RuntimeID
	now := time.Now().UTC()
	target := model.DeploymentTarget{
		ID: "target-1", Name: "引用主机的发布目标", Platform: model.DeploymentDocker,
		RuntimeID:    runtimeID,
		WorkloadName: "app", RolloutTimeout: 300, IsActive: true,
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}

	update := hostTestInput()
	update.Name = "更新后名称"
	update.CapabilityKinds = []model.HostCapabilityKind{model.HostCapabilityKubernetes}
	tested, err = service.TestExisting(ctx, created.Host.ID, update)
	if err != nil {
		t.Fatal(err)
	}
	update.TestToken = tested.Token
	if _, err := service.Update(ctx, created.Host.ID, update); !errors.Is(err, ErrHostReferenced) {
		t.Fatalf("被发布目标引用的 Docker 能力被移除: %v", err)
	}
	unchanged, err := service.Get(ctx, created.Host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Host.Name != input.Name || len(unchanged.Capabilities) != 1 ||
		unchanged.Capabilities[0].Kind != model.HostCapabilityDocker {
		t.Fatalf("失败更新没有完整回滚: %+v", unchanged)
	}
	if err := service.Remove(ctx, created.Host.ID); !errors.Is(err, ErrHostReferenced) {
		t.Fatalf("被引用主机可以直接删除: %v", err)
	}
	if err := db.Delete(&target).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.Remove(ctx, created.Host.ID); err != nil {
		t.Fatalf("解除引用后删除主机失败: %v", err)
	}
	if _, err := service.Get(ctx, created.Host.ID); !errors.Is(err, ErrHostNotFound) {
		t.Fatalf("删除后的主机仍可读取: %v", err)
	}
}

func TestSSHCapabilityUsesNoRuntimeAndProtectsInactiveTargetReference(t *testing.T) {
	service, db, _ := newHostTestService(t)
	ctx := context.Background()
	input := hostTestInput()
	input.CapabilityKinds = []model.HostCapabilityKind{model.HostCapabilitySSH}
	input.KubernetesClusterID = ""
	tested, err := service.Test(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	input.TestToken = tested.Token
	created, err := service.Create(ctx, "admin", input)
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Capabilities) != 1 || created.Capabilities[0].Kind != model.HostCapabilitySSH ||
		created.Capabilities[0].RuntimeID != "" || created.Capabilities[0].UseSudo {
		t.Fatalf("SSH 命令能力不应伪造运行时或 sudo 配置: %+v", created.Capabilities)
	}
	now := time.Now().UTC()
	target := model.DeploymentTarget{
		ID: "ssh-target", Name: "已停用 SSH 发布目标", Platform: model.DeploymentSSH,
		EnvironmentID: "environment-1", HostID: created.Host.ID,
		RolloutTimeout: 60, IsActive: false, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}

	update := hostTestInput()
	update.CapabilityKinds = []model.HostCapabilityKind{model.HostCapabilityDocker}
	update.KubernetesClusterID = ""
	tested, err = service.TestExisting(ctx, created.Host.ID, update)
	if err != nil {
		t.Fatal(err)
	}
	update.TestToken = tested.Token
	if _, err := service.Update(ctx, created.Host.ID, update); !errors.Is(err, ErrHostReferenced) {
		t.Fatalf("停用发布目标引用的 SSH 能力不应被移除: %v", err)
	}
	if err := service.Remove(ctx, created.Host.ID); !errors.Is(err, ErrHostReferenced) {
		t.Fatalf("停用发布目标引用的主机不应被删除: %v", err)
	}
}

func TestHostPingUsesStoredCredentialAndPinnedFingerprint(t *testing.T) {
	service, db, secrets := newHostTestService(t)
	recorder := &recordingDockerProbe{fingerprint: "SHA256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	service = NewService(db, secrets, recorder, kubernetesProbeStub{})
	ctx := context.Background()
	input := hostTestInput()
	input.CapabilityKinds = []model.HostCapabilityKind{model.HostCapabilitySSH}
	input.KubernetesClusterID = ""
	tested, err := service.Test(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	input.TestToken = tested.Token
	created, err := service.Create(ctx, "admin", input)
	if err != nil {
		t.Fatal(err)
	}
	recorder.input = dockerengine.Input{}
	result, err := service.Ping(ctx, created.Host.ID)
	if err != nil {
		t.Fatalf("使用已保存凭据检测主机失败: %v", err)
	}
	if result.Fingerprint != created.Host.SSHHostKeyFingerprint ||
		recorder.input.SSHHostKeyFingerprint != created.Host.SSHHostKeyFingerprint ||
		recorder.input.SSH == nil || recorder.input.SSH.Password != input.SSH.Password {
		t.Fatalf("主机检测没有使用加密保存的凭据和固定指纹: result=%+v input=%+v", result, recorder.input)
	}
}

func TestHostPingCanTestSSHCapabilityIndependently(t *testing.T) {
	service, db, secrets := newHostTestService(t)
	recorder := &recordingDockerProbe{fingerprint: "SHA256:ccccccccccccccccccccccccccccccccccccccccccc"}
	service = NewService(db, secrets, recorder, kubernetesProbeStub{})
	ctx := context.Background()
	input := hostTestInput()
	input.CapabilityKinds = []model.HostCapabilityKind{model.HostCapabilitySSH}
	input.KubernetesClusterID = ""
	tested, err := service.Test(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	input.TestToken = tested.Token
	created, err := service.Create(ctx, "admin", input)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.HostCapability{
		HostID: created.Host.ID, Kind: model.HostCapabilityDocker, RuntimeID: "broken-docker",
		Status: model.HostCapabilityReady, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	recorder.dockerErr = errors.New("docker unavailable")
	if _, err := service.Ping(ctx, created.Host.ID, model.HostCapabilitySSH); err != nil {
		t.Fatalf("独立 SSH 能力检测不应被 Docker 故障阻断: %v", err)
	}
	if _, err := service.Ping(ctx, created.Host.ID); err == nil {
		t.Fatal("默认全能力检测仍应报告 Docker 故障")
	}
}

func TestRefreshRuntimeStatusesTracksRemoteDockerReachability(t *testing.T) {
	_, db, secrets := newHostTestService(t)
	recorder := &recordingDockerProbe{fingerprint: "SHA256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}
	service := NewService(db, secrets, recorder, kubernetesProbeStub{})
	ctx := context.Background()
	input := hostTestInput()
	input.CapabilityKinds = []model.HostCapabilityKind{model.HostCapabilityDocker}
	input.KubernetesClusterID = ""
	tested, err := service.Test(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	input.TestToken = tested.Token
	created, err := service.Create(ctx, "admin", input)
	if err != nil {
		t.Fatal(err)
	}

	recorder.dockerErr = errors.New("docker unavailable")
	changes, err := service.RefreshRuntimeStatuses(ctx)
	if err != nil {
		t.Fatalf("刷新不可达状态失败: %v", err)
	}
	if len(changes) != 1 || changes[0].HostID != created.Host.ID ||
		changes[0].Previous != model.HostCapabilityReady || changes[0].Status != model.HostCapabilityUnreachable {
		t.Fatalf("运行时失败没有生成正确状态变化: %+v", changes)
	}
	unreachable, err := service.Get(ctx, created.Host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(unreachable.Capabilities) != 1 || unreachable.Capabilities[0].Status != model.HostCapabilityUnreachable {
		t.Fatalf("运行时失败状态没有持久化: %+v", unreachable.Capabilities)
	}

	recorder.dockerErr = nil
	changes, err = service.RefreshRuntimeStatuses(ctx)
	if err != nil {
		t.Fatalf("刷新恢复状态失败: %v", err)
	}
	if len(changes) != 1 || changes[0].Previous != model.HostCapabilityUnreachable ||
		changes[0].Status != model.HostCapabilityReady {
		t.Fatalf("运行时恢复没有生成正确状态变化: %+v", changes)
	}
	recovered, err := service.Get(ctx, created.Host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Capabilities[0].Status != model.HostCapabilityReady || recovered.Capabilities[0].Version != "27.2.0" {
		t.Fatalf("运行时恢复状态没有持久化: %+v", recovered.Capabilities)
	}

	changes, err = service.RefreshRuntimeStatuses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("状态未变化时不应重复生成事件: %+v", changes)
	}
}

func TestRefreshRuntimeStatusesKeepsOneProbePerRuntime(t *testing.T) {
	_, db, secrets := newHostTestService(t)
	fingerprint := "SHA256:fffffffffffffffffffffffffffffffffffffffffff"
	service := NewService(db, secrets, &recordingDockerProbe{fingerprint: fingerprint}, kubernetesProbeStub{})
	ctx := context.Background()
	input := hostTestInput()
	input.CapabilityKinds = []model.HostCapabilityKind{model.HostCapabilityDocker}
	input.KubernetesClusterID = ""
	tested, err := service.Test(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	input.TestToken = tested.Token
	if _, err := service.Create(ctx, "admin", input); err != nil {
		t.Fatal(err)
	}

	probe := &blockingDockerProbe{
		fingerprint: fingerprint,
		started:     make(chan struct{}, 1),
		release:     make(chan struct{}),
	}
	service.docker = probe
	firstDone := make(chan error, 1)
	go func() {
		_, err := service.RefreshRuntimeStatuses(ctx)
		firstDone <- err
	}()
	select {
	case <-probe.started:
	case <-time.After(time.Second):
		t.Fatal("首轮运行时探测没有启动")
	}

	secondDone := make(chan error, 1)
	go func() {
		_, err := service.RefreshRuntimeStatuses(ctx)
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("重叠刷新失败: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("重叠刷新被同一运行时的在途探测阻塞")
	}
	probe.mu.Lock()
	calls, maxActive := probe.calls, probe.maxActive
	probe.mu.Unlock()
	if calls != 1 || maxActive != 1 {
		t.Fatalf("同一运行时发生重复并发探测: calls=%d max_active=%d", calls, maxActive)
	}

	close(probe.release)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("首轮刷新失败: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("释放探测后首轮刷新没有结束")
	}
}

func TestRemoteHostUpdateCanReuseEncryptedCredential(t *testing.T) {
	_, db, secrets := newHostTestService(t)
	recorder := &recordingDockerProbe{fingerprint: "SHA256:ddddddddddddddddddddddddddddddddddddddddddd"}
	service := NewService(db, secrets, recorder, kubernetesProbeStub{})
	ctx := context.Background()
	input := hostTestInput()
	tested, err := service.Test(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	input.TestToken = tested.Token
	created, err := service.Create(ctx, "admin", input)
	if err != nil {
		t.Fatal(err)
	}

	useSudo := false
	update := input
	update.Name = "无需重输凭据"
	update.SSH = nil
	update.TestToken = ""
	update.ReuseCredential = true
	update.UseSudo = &useSudo
	tested, err = service.TestExisting(ctx, created.Host.ID, update)
	if err != nil {
		t.Fatalf("使用已保存凭据重新测试失败: %v", err)
	}
	if recorder.input.SSH == nil || recorder.input.SSH.Password != "test-password" {
		t.Fatalf("测试没有使用服务端解密的凭据: %+v", recorder.input.SSH)
	}
	update.TestToken = tested.Token
	updated, err := service.Update(ctx, created.Host.ID, update)
	if err != nil {
		t.Fatalf("使用已保存凭据更新主机失败: %v", err)
	}
	plaintext, err := secrets.Decrypt(updated.Host.SSHCredentialCiphertext, hostcredential.AAD(updated.Host.ID))
	if err != nil {
		t.Fatal(err)
	}
	var stored dockerengine.SSHBundle
	if err := json.Unmarshal([]byte(plaintext), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Password != "test-password" || stored.UseSudo {
		t.Fatalf("更新 sudo 配置时不应丢失原凭据: %+v", stored)
	}
}

func TestBuiltinLocalHostCapabilitiesCanBeEditedButHostCannotBeDeleted(t *testing.T) {
	_, db, secrets := newHostTestService(t)
	probe := &recordingDockerProbe{fingerprint: "SHA256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}
	service := NewService(db, secrets, probe, kubernetesProbeStub{})
	now := time.Now().UTC()
	local := model.Host{
		ID: model.BuiltinLocalHostID, Name: "本地", Mode: model.HostModeLocal,
		SSHPort: 22, IsBuiltin: true, IsActive: true, CreatedBy: "system",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&local).Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	updated, err := service.Update(ctx, local.ID, Input{
		Name: "开发机", CapabilityKinds: []model.HostCapabilityKind{model.HostCapabilityDocker},
	})
	if err != nil {
		t.Fatalf("编辑本地主机失败: %v", err)
	}
	if updated.Host.Name != "开发机" || len(updated.Capabilities) != 1 ||
		updated.Capabilities[0].Kind != model.HostCapabilityDocker ||
		updated.Capabilities[0].RuntimeID != dockerengine.LocalEndpointID {
		t.Fatalf("本地主机配置保存错误: %+v", updated)
	}
	if len(updated.CapabilityOptions) != 2 {
		t.Fatalf("未返回本地能力可用性: %+v", updated.CapabilityOptions)
	}

	target := model.DeploymentTarget{
		ID: "local-target", Name: "本地 Docker 发布", Platform: model.DeploymentDocker,
		RuntimeID:    dockerengine.LocalEndpointID,
		WorkloadName: "app", RolloutTimeout: 60, IsActive: true,
		CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(ctx, local.ID, Input{Name: "开发机"}); !errors.Is(err, ErrHostReferenced) {
		t.Fatalf("被发布目标引用的本地 Docker 能力不应被关闭: %v", err)
	}
	if err := db.Delete(&target).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(ctx, local.ID, Input{Name: "开发机"}); err != nil {
		t.Fatalf("解除引用后无法关闭本地能力: %v", err)
	}
	probe.dockerErr = errors.New("docker unavailable")
	if _, err := service.Update(ctx, local.ID, Input{
		Name: "开发机", CapabilityKinds: []model.HostCapabilityKind{model.HostCapabilityDocker},
	}); !errors.Is(err, ErrCapabilityUnavailable) {
		t.Fatalf("Docker 不可用时仍能启用本地 Docker: %v", err)
	}
	if err := service.Remove(ctx, local.ID); !errors.Is(err, ErrBuiltinHost) {
		t.Fatalf("内置本地主机不应允许删除: %v", err)
	}
}

func TestRemovingHostDoesNotTreatIndependentKubernetesTargetAsHostReference(t *testing.T) {
	service, db, _ := newHostTestService(t)
	ctx := context.Background()
	input := hostTestInput()
	input.CapabilityKinds = []model.HostCapabilityKind{model.HostCapabilityKubernetes}
	tested, err := service.Test(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	input.TestToken = tested.Token
	created, err := service.Create(ctx, "admin", input)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	target := model.DeploymentTarget{
		ID: "shared-cluster-target", Name: "共享集群发布", Platform: model.DeploymentKubernetes,
		RuntimeID: input.KubernetesClusterID,
		Namespace: "default", WorkloadName: "app", ContainerName: "app", RolloutTimeout: 60,
		IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.Remove(ctx, created.Host.ID); err != nil {
		t.Fatalf("独立 Kubernetes 集群目标不应阻止删除主机关系: %v", err)
	}
	if err := db.First(&target, "id = ?", target.ID).Error; err != nil {
		t.Fatalf("删除主机不应删除独立集群发布目标: %v", err)
	}
}

func TestDetectLocalExecCapability(t *testing.T) {
	windows := detectLocalExecCapability("windows", func(string) (string, error) {
		return "", errors.New("不应调用")
	})
	if windows.Available || windows.Reason == "" {
		t.Fatalf("Windows 原生运行不应启用直接终端执行: %+v", windows)
	}
	missing := detectLocalExecCapability("linux", func(string) (string, error) {
		return "", errors.New("not found")
	})
	if missing.Available || missing.Reason == "" {
		t.Fatalf("缺少 sh 时不应启用直接终端执行: %+v", missing)
	}
	available := detectLocalExecCapability("darwin", func(string) (string, error) {
		return "/bin/sh", nil
	})
	if !available.Available || available.Kind != model.HostCapabilityLocalExec {
		t.Fatalf("类 Unix 环境应支持直接终端执行: %+v", available)
	}
}

func TestListStatusesReturnsOnlyCurrentHostState(t *testing.T) {
	service, db, _ := newHostTestService(t)
	now := time.Now().UTC()
	hosts := []model.Host{
		{ID: "status-host-a", Name: "状态主机 A", Mode: model.HostModeSSH, SSHPort: 22, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
		{ID: "status-host-b", Name: "状态主机 B", Mode: model.HostModeSSH, SSHPort: 22, IsActive: true, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
	}
	if err := db.Create(&hosts).Error; err != nil {
		t.Fatalf("创建状态测试主机失败: %v", err)
	}
	if err := db.Model(&model.Host{}).Where("id = ?", hosts[1].ID).Update("is_active", false).Error; err != nil {
		t.Fatalf("停用状态测试主机失败: %v", err)
	}
	capability := model.HostCapability{
		HostID: hosts[0].ID, Kind: model.HostCapabilityDocker, RuntimeID: "docker-status-a",
		Status: model.HostCapabilityReady, Version: "1.48", UseSudo: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&capability).Error; err != nil {
		t.Fatalf("创建状态测试能力失败: %v", err)
	}

	statuses, err := service.ListStatuses(context.Background())
	if err != nil {
		t.Fatalf("读取轻量主机状态失败: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("轻量主机状态数量错误: %+v", statuses)
	}
	statusByID := make(map[string]Status, len(statuses))
	for i := range statuses {
		statusByID[statuses[i].HostID] = statuses[i]
	}
	if statusByID[hosts[1].ID].IsActive {
		t.Fatalf("轻量状态没有返回主机停用状态: %+v", statusByID[hosts[1].ID])
	}
	first := statusByID[hosts[0].ID]
	if !first.IsActive || len(first.Capabilities) != 1 || first.Capabilities[0].RuntimeID != capability.RuntimeID ||
		first.Capabilities[0].Status != model.HostCapabilityReady || !first.Capabilities[0].UseSudo {
		t.Fatalf("轻量状态丢失运行时能力: %+v", first)
	}
}

func newHostTestService(t *testing.T) (*Service, *gorm.DB, *secret.Manager) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := database.Open(context.Background(), config.Database{
		Driver:          "sqlite",
		DSN:             "file:" + t.Name() + "?mode=memory&cache=shared",
		MaxOpenConns:    1,
		MaxIdleConns:    1,
		ConnMaxLifetime: time.Minute,
	}, logger)
	if err != nil {
		t.Fatalf("打开主机测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = database.Close(db) })
	if err := db.AutoMigrate(
		&model.Environment{}, &model.Host{}, &model.EnvironmentHost{}, &model.HostCapability{},
		&model.DockerEndpoint{}, &model.DeploymentTarget{},
	); err != nil {
		t.Fatalf("初始化主机测试表失败: %v", err)
	}
	secrets, err := secret.New(base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatalf("初始化主机测试密钥失败: %v", err)
	}
	fingerprint := "SHA256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	service := NewService(db, secrets, dockerProbeStub{fingerprint: fingerprint}, kubernetesProbeStub{})
	return service, db, secrets
}

func hostTestInput() Input {
	return Input{
		Name: "生产主机", Address: "host.example.com", SSHPort: 22,
		SSHUsername: "deploy", SSHAuthType: model.SSHAuthPassword,
		SSH: &dockerengine.SSHBundle{Password: "test-password", UseSudo: true},
		CapabilityKinds: []model.HostCapabilityKind{
			model.HostCapabilityDocker,
			model.HostCapabilityKubernetes,
		},
		KubernetesClusterID: "cluster-1",
	}
}
