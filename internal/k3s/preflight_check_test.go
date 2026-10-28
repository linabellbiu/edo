package k3s

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"edo/internal/kube"
	"edo/internal/model"
	"edo/internal/sshclient"
)

func assertCheckReadOnly(t *testing.T, f fixture) {
	t.Helper()
	for _, record := range []any{&model.K3sInstallation{}, &model.K3sInstallationHostLock{}, &model.Job{}, &model.OutboxEvent{}} {
		var count int64
		if err := f.db.Model(record).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("只读检测产生持久化任务或计划 %T: count=%d err=%v", record, count, err)
		}
	}
	if len(f.transport.writes) != 0 {
		t.Fatal("只读检测执行了安装命令")
	}
}

func TestCheckReturnsPassedAndBlockedReportsWithoutExecutablePlan(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "通过", true: "已有安装阻断"}[existing], func(t *testing.T) {
			f := newFixture(t)
			f.transport.existing = existing
			report, err := f.service.Check(f.ctx, f.actor.ID, f.input())
			if err != nil || report == nil || report.Passed == existing || report.CheckedAt.IsZero() || len(report.Hosts) != 1 {
				t.Fatalf("独立检测结果错误: report=%+v err=%v", report, err)
			}
			encoded, _ := json.Marshal(report)
			for _, private := range []string{"ssh-fixture-password", "input_ciphertext", "expires_at", "commands", "server_config"} {
				if strings.Contains(string(encoded), private) {
					t.Fatalf("只读检测暴露计划或敏感字段 %q", private)
				}
			}
			assertCheckReadOnly(t, f)
		})
	}
}

func TestCheckRechecksAuthorizationAndHostAfterProbe(t *testing.T) {
	for _, scenario := range []struct {
		name string
		edit func(f fixture) error
		want error
	}{
		{"权限撤销", func(f fixture) error { f.auth.denied = true; return nil }, ErrPermission},
		{"身份修订", func(f fixture) error {
			return f.db.Model(&model.User{}).Where("uuid = ?", f.actor.ID).Update("auth_version", f.actor.AuthVersion+1).Error
		}, ErrPermission},
		{"主机更换", func(f fixture) error {
			return f.db.Model(&model.Host{}).Where("uuid = ?", f.host.ID).Update("address", "192.0.2.11").Error
		}, ErrHostChanged},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newFixture(t)
			f.service.WithTransport(preflightReadTransport{Transport: f.transport, read: func(ctx context.Context, host model.Host, bundle sshclient.Bundle, script string) (string, error) {
				output, err := f.transport.Read(ctx, host, bundle, script)
				if editErr := scenario.edit(f); editErr != nil {
					t.Error(editErr)
				}
				return output, err
			}})
			if report, err := f.service.Check(f.ctx, f.actor.ID, f.input()); report != nil || !errors.Is(err, scenario.want) {
				t.Fatalf("过期范围检测仍成功: report=%+v err=%v", report, err)
			}
			assertCheckReadOnly(t, f)
		})
	}
}

func TestCheckRejectsUnauthorizedBeforeRemoteRead(t *testing.T) {
	f := newFixture(t)
	f.auth.denied = true
	if report, err := f.service.Check(f.ctx, f.actor.ID, f.input()); report != nil || !errors.Is(err, ErrPermission) || f.transport.reads != 0 {
		t.Fatalf("未授权检测触达主机: report=%+v err=%v reads=%d", report, err, f.transport.reads)
	}
	assertCheckReadOnly(t, f)
}

func TestCheckK3dDoesNotCreateClusterOrBootstrapCredentials(t *testing.T) {
	f, _, clusters, input := k3dFixture(t)
	report, err := f.service.Check(f.ctx, f.actor.ID, input)
	if err != nil || report == nil || !report.Passed || clusters.joins != 0 || clusters.created != 0 {
		t.Fatalf("k3d 独立检测结果错误或产生写操作: report=%+v err=%v", report, err)
	}
	assertCheckReadOnly(t, f)
}

func TestCheckMasterBlockedConditionIsReportAndStillRechecksScope(t *testing.T) {
	f, clusters, input, _ := serverJoinFixture(t)
	clusters.inspect = func() (*kube.ServerJoinInfo, error) { return nil, ErrPreflightFailed }
	report, err := f.service.Check(f.ctx, f.actor.ID, input)
	if err != nil || report == nil || report.Passed || clusters.joins != 0 {
		t.Fatalf("Master 条件失败应为只读检测结果: report=%+v err=%v", report, err)
	}
	assertCheckReadOnly(t, f)
}

func TestCheckCancellationHasNoInstallationSideEffects(t *testing.T) {
	f := newFixture(t)
	f.service.WithTransport(preflightReadTransport{Transport: f.transport, read: func(ctx context.Context, _ model.Host, _ sshclient.Bundle, _ string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}})
	ctx, cancel := context.WithTimeout(f.ctx, 50*time.Millisecond)
	defer cancel()
	before := time.Now()
	if report, err := f.service.Check(ctx, f.actor.ID, f.input()); report != nil || !errors.Is(err, context.DeadlineExceeded) || time.Since(before) > time.Second {
		t.Fatalf("取消未停止独立检测: report=%+v err=%v", report, err)
	}
	assertCheckReadOnly(t, f)
}

func TestCheckRemoteReadFailureReturnsSafeReportWithoutWrites(t *testing.T) {
	f, clusters, input := joinVersionFixture(t, CatalogK3sVersion)
	f.service.WithTransport(preflightReadTransport{Transport: f.transport, read: func(context.Context, model.Host, sshclient.Bundle, string) (string, error) {
		return "private-ssh-output", errors.New("private-ssh-password")
	}})
	report, err := f.service.Check(f.ctx, f.actor.ID, input)
	if err != nil || report == nil || report.Passed || clusters.joins != 0 || clusters.created != 0 {
		t.Fatalf("远端读取失败检测错误: report=%+v err=%v", report, err)
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), "private-") {
		t.Fatal("远端读取内容或内部错误泄露至检测结果")
	}
	assertCheckReadOnly(t, f)
}

type checkNetworkHookClusters struct {
	*fakeClusters
	pings, inspections int
	onPing             func(int)
	onInspect          func(int)
}

func (c *checkNetworkHookClusters) Ping(ctx context.Context, id string) (string, error) {
	result, err := c.fakeClusters.Ping(ctx, id)
	c.pings++
	if c.onPing != nil {
		c.onPing(c.pings)
	}
	return result, err
}

func (c *checkNetworkHookClusters) InspectServerJoin(ctx context.Context, id, endpoint, token string) (*kube.ServerJoinInfo, error) {
	result, err := c.fakeClusters.InspectServerJoin(ctx, id, endpoint, token)
	c.inspections++
	if c.onInspect != nil {
		c.onInspect(c.inspections)
	}
	return result, err
}

func TestCheckRechecksLocalScopeAfterLastNetworkRead(t *testing.T) {
	for _, branch := range []string{"worker_ping", "blocked_master_ping", "master_inspect"} {
		for _, mutateHost := range []bool{false, true} {
			t.Run(branch+map[bool]string{false: "_权限撤销", true: "_主机变化"}[mutateHost], func(t *testing.T) {
				var f fixture
				var clusters *fakeClusters
				var input Input
				if branch == "worker_ping" {
					f, clusters, input = joinVersionFixture(t, CatalogK3sVersion)
				} else {
					f, clusters, input, _ = serverJoinFixture(t)
					if branch == "blocked_master_ping" {
						clusters.inspect = func() (*kube.ServerJoinInfo, error) { return nil, kube.ErrServerJoinUnhealthy }
					}
				}
				hooked := &checkNetworkHookClusters{fakeClusters: clusters}
				changed := false
				edit := func() {
					changed = true
					if mutateHost {
						if err := f.db.Model(&model.Host{}).Where("uuid = ?", f.host.ID).Update("address", "192.0.2.11").Error; err != nil {
							t.Error(err)
						}
					} else {
						f.auth.denied = true
					}
				}
				if branch == "master_inspect" {
					hooked.onInspect = func(count int) {
						if count == 3 {
							edit()
						}
					}
				} else {
					hooked.onPing = func(count int) {
						if count == 2 {
							edit()
						}
					}
				}
				f.service.kubernetes = hooked
				report, err := f.service.Check(f.ctx, f.actor.ID, input)
				want := ErrPermission
				if mutateHost {
					want = ErrHostChanged
				}
				if !changed || report != nil || !errors.Is(err, want) {
					t.Fatalf("最后网络读取窗口未重新核验范围: changed=%v report=%+v err=%v", changed, report, err)
				}
				if clusters.joins != 0 || clusters.created != 0 {
					t.Fatal("检测产生集群写入或加入凭据")
				}
				assertCheckReadOnly(t, f)
			})
		}
	}
}

func TestCheckHonorsCancellationAfterLastNetworkRead(t *testing.T) {
	f, clusters, input := joinVersionFixture(t, CatalogK3sVersion)
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	hooked := &checkNetworkHookClusters{fakeClusters: clusters, onPing: func(count int) {
		if count == 2 {
			cancel()
		}
	}}
	f.service.kubernetes = hooked
	if report, err := f.service.Check(ctx, f.actor.ID, input); report != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("最后网络读取后的取消请求未生效: report=%+v err=%v", report, err)
	}
	assertCheckReadOnly(t, f)
}
