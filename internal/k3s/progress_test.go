package k3s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"edo/internal/model"
)

func TestInstallationProgressOnlyMapsOfficialInfoAndExactMarkers(t *testing.T) {
	var stages []string
	d := &installationDiagnostics{onStage: func(stage string, _, _ time.Time, _ bool) error { stages = append(stages, stage); return nil }}
	for _, part := range []string{
		"installer\nEDO_K3S_STAGE\tunknown-private-token\nEDO_K3S_STAGE\tinst",
		"aller\n[INFO]  Downloading hash https://fixture.invalid/private-token/" + strings.Repeat("x", 200) + "\n",
		"[INFO]  Downloading binary https://fixture.invalid/private-token\n[INFO]  Verifying binary download\n",
		"[INFO]  Installing k3s to /private-fixture\n[INFO]  env: Creating environment file /private-fixture\n",
		"[INFO]  systemd: Creating service file /private-fixture\n[INFO]  systemd: Starting private-fixture-service\n",
		"[ERROR] private-fixture-token\n[INFO]  an unknown private-fixture-token\nEDO_K3S_STAGE\tversion" + strings.Repeat("x", 200) + "\n",
		"EDO_K3S_STAGE\tversion\nEDO_K3S_STAGE\tversion\nEDO_K3S_STAGE\tinstaller\n",
	} {
		if _, err := io.WriteString(d, part); err != nil {
			t.Fatal(err)
		}
	}
	want := "installer,installer_download,installer_verify,installer_binary,installer_environment,installer_service,installer_start,version"
	if strings.Join(stages, ",") != want || d.stage != "version" || len(d.line) > 80 {
		t.Fatalf("只允许固定安全状态且不可倒退或泄密: %v", stages)
	}
	for _, line := range []string{"[INFO]  openrc: Starting k3s-agent", "[INFO] openrc: Creating service file /private"} {
		if officialInstallerStage(line) == "" {
			t.Fatal("未识别官方 OpenRC 形态")
		}
	}
	for _, line := range []string{"[WARN] systemd: Starting k3s", "prefix [INFO] systemd: Starting k3s", "[INFO] Verifying binary download private", "EDO_K3S_STAGE installer"} {
		if officialInstallerStage(line) != "" {
			t.Fatal("猜测了未知输出")
		}
	}
}

func TestInstallationProgressHeartbeatIsThrottledAndServiceStartIsBounded(t *testing.T) {
	var updates, beats int
	d := &installationDiagnostics{onStage: func(_ string, started, updated time.Time, heartbeat bool) error {
		updates++
		if heartbeat {
			beats++
			if updated.Before(started) {
				t.Fatal("负耗时")
			}
		}
		return nil
	}}
	if err := d.start("installer_start"); err != nil {
		t.Fatal(err)
	}
	start := d.started
	for seconds := 1; seconds <= 299; seconds++ {
		if err := d.heartbeat(start.Add(time.Duration(seconds) * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if updates != 10 || beats != 9 || !d.started.Equal(start) {
		t.Fatalf("心跳写入未限频或改变阶段起点: updates=%d beats=%d", updates, beats)
	}
	err := d.heartbeat(start.Add(5 * time.Minute))
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(safeFailure(err), "启动系统服务超时") {
		t.Fatal("服务启动没有有界失败", err)
	}
	if d.failure(nil) != err {
		t.Fatal("迟到成功覆盖了已触发的阶段超时")
	}
}

func TestInstallationProgressConcurrentWritesAndHeartbeatsAreBounded(t *testing.T) {
	var updates int
	d := &installationDiagnostics{onStage: func(string, time.Time, time.Time, bool) error { updates++; return nil }}
	if err := d.start("installer"); err != nil {
		t.Fatal(err)
	}
	start := d.started
	var workers sync.WaitGroup
	for index := 0; index < 12; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for iteration := 0; iteration < 50; iteration++ {
				_, _ = io.WriteString(d, "EDO_K3S_STAGE\tversion\n")
				_ = d.heartbeat(start.Add(31 * time.Second))
			}
		}()
	}
	workers.Wait()
	if updates > 4 || d.err != nil {
		t.Fatalf("并发重复输出导致无界写入: %d %v", updates, d.err)
	}
}

func TestInstallationProgressStageClockDoesNotRegressOrAcceptRemoteTimes(t *testing.T) {
	d := &installationDiagnostics{}
	if err := d.start("installer_start"); err != nil {
		t.Fatal(err)
	}
	started := d.started
	if _, err := io.WriteString(d, "EDO_K3S_STAGE\tversion\t2099-01-01T00:00:00Z\n"); err != nil || d.stage != "installer_start" {
		t.Fatal("不应接收远端时间或未知阶段扩展")
	}
	_ = d.heartbeat(started.Add(-time.Minute))
	d.mu.Lock()
	d.setStageLocked("version", started.Add(-time.Second))
	d.mu.Unlock()
	if d.started.Before(started) || d.stage != "version" {
		t.Fatal("阶段时钟发生倒退")
	}
	if err := d.heartbeat(started.Add(5 * time.Minute)); err != nil {
		t.Fatal("离开启动阶段后旧期限仍生效", err)
	}
	late := &installationDiagnostics{}
	_ = late.start("installer_start")
	late.mu.Lock()
	late.setStageLocked("version", late.started.Add(5*time.Minute))
	late.mu.Unlock()
	if !errors.Is(late.failure(nil), context.DeadlineExceeded) || late.stage != "installer_start" {
		t.Fatal("超时后迟到阶段覆盖失败")
	}
}

func TestInstallationLogRetainsRecentCompleteLinesWithSingleMarker(t *testing.T) {
	logs := ""
	for index := 0; index < 3000; index++ {
		logs = appendInstallationLog(logs, fmt.Sprintf("%04d %s\n", index, strings.Repeat("阶段", 24)))
	}
	if len(logs) > installationLogLimit || !utf8.ValidString(logs) || !strings.HasPrefix(logs, installationLogTruncated) || strings.Count(logs, installationLogTruncated) != 1 || !strings.Contains(logs, "2999 ") {
		t.Fatal("日志未按完整行有界保留最新记录")
	}
	if strings.Contains(appendInstallationLog(logs, strings.Repeat("x", installationLogLimit+1)), strings.Repeat("x", 100)) {
		t.Fatal("超长单行没有限制")
	}
}

func runningProgressFixture(t *testing.T) (fixture, *model.K3sInstallation, []Host) {
	t.Helper()
	f := newFixture(t)
	plan, err := f.service.Preview(f.ctx, f.actor.ID, f.input())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); err != nil {
		t.Fatal(err)
	}
	record, err := f.service.claim(f.ctx)
	if err != nil || record == nil {
		t.Fatal(err)
	}
	var hosts []Host
	if err := json.Unmarshal(record.Hosts, &hosts); err != nil {
		t.Fatal(err)
	}
	return f, record, hosts
}

func TestHostOperationPersistsStageBeforeReturningAndGetSurvivesServiceRecreation(t *testing.T) {
	f, record, hosts := runningProgressFixture(t)
	err := f.service.runHostOperation(f.ctx, record, hosts, 0, "ssh", func(_ context.Context, output io.Writer) error {
		if _, err := io.WriteString(output, "EDO_K3S_STAGE\tinstaller\n[INFO]  systemd: Starting private-fixture-service\nsecret=private-fixture-token\n"); err != nil {
			return err
		}
		var current model.K3sInstallation
		if err := f.db.First(&current, "uuid = ?", record.ID).Error; err != nil {
			return err
		}
		var saved []Host
		if err := json.Unmarshal(current.Hosts, &saved); err != nil {
			return err
		}
		if saved[0].Stage != "installer_start" || saved[0].StageStartedAt == nil || saved[0].ProgressUpdatedAt == nil || current.Logs == "" || strings.Contains(current.Logs, "private-fixture") {
			t.Fatal("运行期间没有安全落库真实进度")
		}
		if saved[0].Status != "installing" {
			t.Fatal("真实步骤执行中未标记当前主机")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	reopened := *f.service
	result, err := reopened.Get(f.ctx, record.ID)
	if err != nil || result.Hosts[0].Stage != "installer_start" || result.Hosts[0].StageStartedAt.IsZero() || result.Logs == "" {
		t.Fatal("刷新或重建服务后进度丢失", err)
	}
}

func TestHostProgressFreezesCompletedStageTimeWhileOtherHostsRun(t *testing.T) {
	f, record, hosts := runningProgressFixture(t)
	if err := f.service.runHostOperation(f.ctx, record, hosts, 0, "node", func(context.Context, io.Writer) error { return nil }); err != nil {
		t.Fatal(err)
	}
	started := *hosts[0].StageStartedAt
	if err := f.service.hostProgress(record, hosts, 0, "ready", "控制平面已安装，等待集群验收"); err != nil {
		t.Fatal(err)
	}
	if hosts[0].Status != "ready" || hosts[0].ProgressUpdatedAt.Before(started) || !hosts[0].StageStartedAt.Equal(started) {
		t.Fatal("等待其他步骤时未冻结节点阶段或重置了阶段起点")
	}
	if err := f.service.runHostOperation(f.ctx, record, hosts, 0, "components", func(context.Context, io.Writer) error {
		if hosts[0].Status != "installing" || hosts[0].Stage != "components" {
			t.Fatal("组件验收没有恢复真实执行状态")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHostOperationStopsOnProgressLeaseLossAndNeverStartsWithoutDurableProgress(t *testing.T) {
	for _, before := range []bool{true, false} {
		t.Run(fmt.Sprint(before), func(t *testing.T) {
			f, record, hosts := runningProgressFixture(t)
			lose := func() {
				if err := f.db.Model(&model.K3sInstallation{}).Where("uuid = ?", record.ID).Update("lease_owner", "different-owner").Error; err != nil {
					t.Fatal(err)
				}
			}
			if before {
				lose()
			}
			called := false
			err := f.service.runHostOperation(f.ctx, record, hosts, 0, "ssh", func(ctx context.Context, output io.Writer) error {
				called = true
				lose()
				_, err := io.WriteString(output, "EDO_K3S_STAGE\tinstaller\n")
				if err == nil || ctx.Err() == nil {
					t.Fatal("持久化失败未取消远端执行上下文")
				}
				return err
			})
			if err == nil || called == before || !strings.Contains(safeFailure(err), "保存安装进度") {
				t.Fatal("没有在持久化失败时停止", err)
			}
		})
	}
}
