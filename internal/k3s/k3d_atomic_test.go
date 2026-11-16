package k3s

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/hostcredential"
	"edo/internal/kube"
	"edo/internal/model"
	"edo/internal/sshclient"
)

func claimedK3dFixture(t *testing.T) (fixture, *model.K3sInstallation, *fakeClusters, kube.Input, []Host) {
	t.Helper()
	f, _, clusters, input := k3dFixture(t)
	plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Execute(f.ctx, f.actor.ID, plan.ID); err != nil {
		t.Fatal(err)
	}
	record, err := f.service.claim(f.ctx)
	if err != nil || record == nil {
		t.Fatal(err)
	}
	var hosts []Host
	if err = json.Unmarshal(record.Hosts, &hosts); err != nil {
		t.Fatal(err)
	}
	return f, record, clusters, kube.Input{Name: input.Name, Mode: model.KubernetesKubeconfig, DefaultNamespace: "default", Kubeconfig: &f.transport.kubeconfig}, hosts
}

func TestK3dRegistrationAndSuccessCommitTogether(t *testing.T) {
	for _, failAt := range []string{"cluster", "installation", "host-lock", "none"} {
		t.Run(failAt, func(t *testing.T) {
			f, record, _, input, hosts := claimedK3dFixture(t)
			injected := errors.New("fixture transaction failure")
			callback := func(db *gorm.DB) {
				if db.Statement.Table == "kubernetes_clusters" && failAt == "cluster" || db.Statement.Table == "k3s_installations" && failAt == "installation" || db.Statement.Table == "k3s_installation_host_locks" && failAt == "host-lock" {
					db.AddError(injected)
				}
			}
			if err := f.db.Callback().Create().Before("gorm:create").Register("fixture:atomic-create", callback); err != nil {
				t.Fatal(err)
			}
			if err := f.db.Callback().Update().Before("gorm:update").Register("fixture:atomic-update", callback); err != nil {
				t.Fatal(err)
			}
			if err := f.db.Callback().Delete().Before("gorm:delete").Register("fixture:atomic-delete", callback); err != nil {
				t.Fatal(err)
			}
			err := f.service.completeK3d(f.ctx, record, input, hosts)
			var stored model.K3sInstallation
			if readErr := f.db.First(&stored, "uuid = ?", record.ID).Error; readErr != nil {
				t.Fatal(readErr)
			}
			var clusters, locks int64
			f.db.Model(&model.KubernetesCluster{}).Count(&clusters)
			f.db.Model(&model.K3sInstallationHostLock{}).Where("installation_id = ?", record.ID).Count(&locks)
			if failAt == "none" {
				if err != nil || stored.Status != "succeeded" || stored.ClusterID == "" || clusters != 1 || locks != 0 {
					t.Fatalf("集群和任务未同步成功: err=%v status=%s cluster=%s clusters=%d locks=%d", err, stored.Status, stored.ClusterID, clusters, locks)
				}
			} else if err == nil || stored.Status != "running" || stored.ClusterID != "" || clusters != 0 || locks != 1 {
				t.Fatalf("事务失败留下半成品: err=%v status=%s cluster=%s clusters=%d locks=%d", err, stored.Status, stored.ClusterID, clusters, locks)
			}
		})
	}
}

func TestK3dSuccessRejectsLateFailureAndLostLeaseCannotRegister(t *testing.T) {
	t.Run("late failure", func(t *testing.T) {
		f, record, _, input, hosts := claimedK3dFixture(t)
		stale := *record
		if err := f.service.completeK3d(f.ctx, record, input, hosts); err != nil {
			t.Fatal(err)
		}
		f.service.finish(f.ctx, stale, context.Canceled)
		var stored model.K3sInstallation
		if err := f.db.First(&stored, "uuid = ?", record.ID).Error; err != nil || stored.Status != "succeeded" || stored.ClusterID != record.ClusterID {
			t.Fatalf("迟到失败覆盖成功安装: %+v err=%v", stored, err)
		}
		if len(f.transport.writes) != 0 {
			t.Fatal("成功接入的集群被执行清理")
		}
	})
	t.Run("lost lease", func(t *testing.T) {
		f, record, _, input, hosts := claimedK3dFixture(t)
		if err := f.db.Model(record).Update("lease_owner", uuid.NewString()).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.service.completeK3d(f.ctx, record, input, hosts); err == nil {
			t.Fatal("失去租约的进程仍能接入集群")
		}
		var count int64
		f.db.Model(&model.KubernetesCluster{}).Count(&count)
		if count != 0 {
			t.Fatal("失去租约后产生了孤立集群记录")
		}
	})
}

func TestK3dRecoveryWaitsForUncertainCreatorWithoutUnlocking(t *testing.T) {
	f, record, _, _, _ := claimedK3dFixture(t)
	snapshot, err := f.service.decode(*record)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.K3dCleanup = &k3dCleanupProof{InstallationID: record.ID, HostID: f.host.ID, PreparedAt: time.Now().UTC()}
	ciphertext, err := f.service.encodeK3dSnapshot(record.ID, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	expired := time.Now().UTC().Add(-time.Minute)
	if err = f.db.Model(record).Updates(map[string]any{"input_ciphertext": ciphertext, "lease_expires_at": expired}).Error; err != nil {
		t.Fatal(err)
	}
	if err = f.service.recoverExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	var stored model.K3sInstallation
	if err = f.db.First(&stored, "uuid = ?", record.ID).Error; err != nil {
		t.Fatal(err)
	}
	var locks int64
	f.db.Model(&model.K3sInstallationHostLock{}).Where("installation_id = ?", record.ID).Count(&locks)
	if stored.Status != "running" || stored.FinishedAt != nil || !strings.Contains(stored.ErrorMessage, "等待原创建进程退出") || stored.LeaseExpiresAt == nil || !stored.LeaseExpiresAt.After(time.Now()) || locks != 1 || len(f.transport.writes) != 0 {
		t.Fatalf("未隔离失联创建进程: status=%s message=%s locks=%d", stored.Status, stored.ErrorMessage, locks)
	}
}

type blockedInstallationTransport struct {
	*fakeTransport
	blockedHost string
	started     chan string
	release     chan struct{}
}

func independentInstallationHost(t *testing.T, f fixture) model.Host {
	t.Helper()
	other := f.host
	other.RowID, other.ID, other.Name, other.Address = 0, uuid.NewString(), "独立主机", "192.0.2.11"
	bundle, _ := json.Marshal(sshclient.Bundle{Password: "ssh-fixture-password"})
	var err error
	other.SSHCredentialCiphertext, err = f.service.secrets.Encrypt(string(bundle), hostcredential.AAD(other.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err = f.db.Create(&model.HostCapability{HostID: other.ID, Kind: model.HostCapabilitySSH, Status: model.HostCapabilityReady}).Error; err != nil {
		t.Fatal(err)
	}
	return other
}

func (b *blockedInstallationTransport) Run(ctx context.Context, host model.Host, bundle sshclient.Bundle, script string, out io.Writer) error {
	b.started <- host.ID
	if host.ID == b.blockedHost {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-b.release:
		}
	}
	return errors.New("fixture install failure")
}

func TestInstallationWorkerRunsIndependentHostsWithinConfiguredConcurrency(t *testing.T) {
	f := newFixture(t)
	f.service.kubernetes = fakeClusterClient(t, f)
	f.service.WithConcurrency(2)
	other := independentInstallationHost(t, f)
	var err error
	transport := &blockedInstallationTransport{fakeTransport: f.transport, blockedHost: f.host.ID, started: make(chan string, 2), release: make(chan struct{})}
	f.service.WithTransport(transport)
	var ids []string
	for index, host := range []model.Host{f.host, other} {
		input := f.input()
		input.ServerHostID, input.Name = host.ID, host.Name+"集群"
		plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.service.Execute(f.ctx, f.actor.ID, plan.ID); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, plan.ID)
		// 明确最慢任务先进入队列。
		if err = f.db.Model(&model.K3sInstallation{}).Where("uuid = ?", plan.ID).Update("created_at", time.Now().Add(time.Duration(index)*time.Second)).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.service.Run(ctx) }()
	defer func() { cancel(); <-done }()
	seen := map[string]bool{}
	for len(seen) != 2 {
		select {
		case id := <-transport.started:
			seen[id] = true
		case <-time.After(5 * time.Second):
			t.Fatal("慢主机阻塞了独立主机安装")
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		var stored model.K3sInstallation
		if err = f.db.First(&stored, "uuid = ?", ids[1]).Error; err != nil {
			t.Fatal(err)
		}
		if stored.Status == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("独立任务未在慢任务结束前归档")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var blocked model.K3sInstallation
	f.db.First(&blocked, "uuid = ?", ids[0])
	if blocked.Status != "running" {
		t.Fatal("测试未保留阻塞中的首个任务")
	}
	close(transport.release)
}

type blockedK3dRecoveryTransport struct {
	*fakeTransport
	cleanupStarted, installStarted, release chan struct{}
}

func (b *blockedK3dRecoveryTransport) Read(ctx context.Context, host model.Host, bundle sshclient.Bundle, script string) (string, error) {
	if strings.Contains(script, "EDO_K3D_FLOCK_READY") {
		return "EDO_K3D_FLOCK_READY\n", nil
	}
	if strings.Contains(script, "{{json .ID}}") {
		select {
		case b.cleanupStarted <- struct{}{}:
		default:
		}
		select {
		case <-b.release:
			return "", ErrRemote
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return b.fakeTransport.Read(ctx, host, bundle, script)
}

func (b *blockedK3dRecoveryTransport) Run(ctx context.Context, host model.Host, bundle sshclient.Bundle, script string, out io.Writer) error {
	if strings.Contains(script, "EDO_TERMINAL_OBSERVER") {
		return b.fakeTransport.Run(ctx, host, bundle, script, out)
	}
	select {
	case b.installStarted <- struct{}{}:
	default:
	}
	return ErrRemote
}

func TestK3dRecoveryDoesNotBlockIndependentPendingInstallation(t *testing.T) {
	f, record, _, _, _ := claimedK3dFixture(t)
	snapshot, err := f.service.decode(*record)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.K3dCleanup = &k3dCleanupProof{Version: 1, InstallationID: record.ID, HostID: f.host.ID, HostDigest: snapshot.HostDigests[f.host.ID], DaemonID: "fixture-daemon", PreparedAt: time.Now().Add(-executionTimeout)}
	ciphertext, err := f.service.encodeK3dSnapshot(record.ID, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	expired := time.Now().UTC().Add(-executionTimeout - time.Minute)
	if err = f.db.Model(record).Updates(map[string]any{"input_ciphertext": ciphertext, "lease_expires_at": expired, "started_at": expired}).Error; err != nil {
		t.Fatal(err)
	}
	other := independentInstallationHost(t, f)
	input := f.input()
	input.ServerHostID, input.Name = other.ID, "独立安装"
	plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Execute(f.ctx, f.actor.ID, plan.ID); err != nil {
		t.Fatal(err)
	}
	transport := &blockedK3dRecoveryTransport{fakeTransport: f.transport, cleanupStarted: make(chan struct{}, 1), installStarted: make(chan struct{}, 1), release: make(chan struct{})}
	f.service.WithTransport(transport).WithConcurrency(1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.service.Run(ctx) }()
	defer func() { close(transport.release); cancel(); <-done }()
	for _, signal := range []chan struct{}{transport.cleanupStarted, transport.installStarted} {
		select {
		case <-signal:
		case <-time.After(5 * time.Second):
			t.Fatal("清理任务阻塞了独立安装，或未实际执行恢复")
		}
	}
	var recovering model.K3sInstallation
	if err = f.db.First(&recovering, "uuid = ?", record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if recovering.Status != "running" || !strings.Contains(recovering.ErrorMessage, "正在清理") {
		t.Fatalf("首轮清理过早发布终态: status=%s message=%s", recovering.Status, recovering.ErrorMessage)
	}
}

func TestK3dRecoveryNeverRewritesNativeFailedCause(t *testing.T) {
	f := newFixture(t)
	plan, err := f.service.Preview(f.ctx, f.actor.ID, f.input())
	if err != nil {
		t.Fatal(err)
	}
	expired := time.Now().UTC().Add(-2 * executionTimeout)
	if err = f.db.Model(&model.K3sInstallation{}).Where("uuid = ?", plan.ID).Updates(map[string]any{"status": "failed", "error_message": "原生安装原始失败", "lease_expires_at": expired, "updated_at": expired}).Error; err != nil {
		t.Fatal(err)
	}
	if err = f.service.recoverExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	var stored model.K3sInstallation
	if err = f.db.First(&stored, "uuid = ?", plan.ID).Error; err != nil || stored.Status != "failed" || stored.ErrorMessage != "原生安装原始失败" {
		t.Fatalf("k3d 恢复覆盖原生失败: %+v err=%v", stored, err)
	}
}

func TestK3dSlowRecoveryDoesNotBlockOtherExpiredTask(t *testing.T) {
	f, record, _, _, _ := claimedK3dFixture(t)
	snapshot, err := f.service.decode(*record)
	if err != nil {
		t.Fatal(err)
	}
	expired := time.Now().UTC().Add(-executionTimeout - time.Minute)
	snapshot.K3dCleanup = &k3dCleanupProof{Version: 1, InstallationID: record.ID, HostID: f.host.ID, HostDigest: snapshot.HostDigests[f.host.ID], DaemonID: "fixture-daemon", PreparedAt: expired}
	ciphertext, err := f.service.encodeK3dSnapshot(record.ID, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.Model(record).Updates(map[string]any{"input_ciphertext": ciphertext, "lease_expires_at": expired, "started_at": expired}).Error; err != nil {
		t.Fatal(err)
	}
	other := independentInstallationHost(t, f)
	input := f.input()
	input.ServerHostID, input.Name = other.ID, "另一过期任务"
	plan, err := f.service.Preview(f.ctx, f.actor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.Model(&model.K3sInstallation{}).Where("uuid = ?", plan.ID).Updates(map[string]any{"status": "running", "lease_owner": uuid.NewString(), "lease_expires_at": expired, "started_at": expired}).Error; err != nil {
		t.Fatal(err)
	}
	transport := &blockedK3dRecoveryTransport{fakeTransport: f.transport, cleanupStarted: make(chan struct{}, 1), installStarted: make(chan struct{}, 1), release: make(chan struct{})}
	f.service.WithTransport(transport).WithConcurrency(2)
	done := make(chan error, 1)
	go func() { done <- f.service.recoverExpired(context.Background()) }()
	defer func() { close(transport.release); <-done }()
	select {
	case <-transport.cleanupStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("未进入真实阻塞的恢复读取")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		var second model.K3sInstallation
		if err = f.db.First(&second, "uuid = ?", plan.ID).Error; err != nil {
			t.Fatal(err)
		}
		if second.Status == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("慢清理阻塞了其他过期任务恢复")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestK3dUnreadableSnapshotNeverFallsBackToNativeUnlock(t *testing.T) {
	for _, operation := range []string{"finish", "recover"} {
		for _, corrupt := range []string{"ciphertext", "json"} {
			t.Run(operation+"/"+corrupt, func(t *testing.T) {
				f, record, _, _, _ := claimedK3dFixture(t)
				value := "broken-ciphertext"
				if corrupt == "json" {
					var err error
					value, err = f.service.secrets.Encrypt("{broken-json", []byte("k3s-installation:"+record.ID))
					if err != nil {
						t.Fatal(err)
					}
				}
				updates := map[string]any{"input_ciphertext": value}
				if operation == "recover" {
					updates["lease_expires_at"] = time.Now().UTC().Add(-time.Minute)
				}
				if err := f.db.Model(record).Updates(updates).Error; err != nil {
					t.Fatal(err)
				}
				record.InputCiphertext = value
				if operation == "finish" {
					f.service.finish(f.ctx, *record, ErrRemote)
				} else if err := f.service.recoverExpired(context.Background()); err != nil {
					t.Fatal(err)
				}
				var stored model.K3sInstallation
				if err := f.db.First(&stored, "uuid = ?", record.ID).Error; err != nil {
					t.Fatal(err)
				}
				var locks int64
				f.db.Model(&model.K3sInstallationHostLock{}).Where("installation_id = ?", record.ID).Count(&locks)
				if stored.Status != "failed" || locks != 1 || !strings.Contains(stored.ErrorMessage, "快照无法读取") || stored.LeaseExpiresAt == nil || !stored.LeaseExpiresAt.After(time.Now()) || len(f.transport.writes) != 0 {
					t.Fatalf("损坏快照未保留现场占用: status=%s locks=%d message=%s", stored.Status, locks, stored.ErrorMessage)
				}
			})
		}
	}
}
