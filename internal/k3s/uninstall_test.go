package k3s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/sshclient"
)

func uninstallProofFixture() string {
	var output strings.Builder
	for index, key := range []string{"script", "killall", "config", "unit", "environment"} {
		fmt.Fprintf(&output, "EDO_K3S_UNINSTALL_PROOF\t%s\t1:%d:0:0:700:1:100:1:1:%s\n", key, index+1, strings.Repeat("a", 64))
	}
	return output.String() + "EDO_K3S_UNINSTALL_PROOF_END\n"
}

type uninstallTransport struct {
	output    string
	verify    string
	readErr   error
	runErr    error
	runs      []string
	reads     int
	afterRead func()
	afterRun  func()
}

func (t *uninstallTransport) Read(ctx context.Context, _ model.Host, _ sshclient.Bundle, script string) (string, error) {
	t.reads++
	if t.afterRead != nil {
		t.afterRead()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.Contains(script, "EDO_K3S_UNINSTALL_VERIFIED") {
		return t.verify, t.readErr
	}
	return t.output, t.readErr
}
func (t *uninstallTransport) Run(ctx context.Context, _ model.Host, _ sshclient.Bundle, script string, output io.Writer) error {
	t.runs = append(t.runs, script)
	if t.afterRun != nil {
		t.afterRun()
	}
	_, _ = io.WriteString(output, "private-uninstaller-output-token\nEDO_K3S_STAGE\timport\n")
	if err := ctx.Err(); err != nil {
		return err
	}
	return t.runErr
}

func uninstallFixture(t *testing.T) (fixture, *Plan, *uninstallTransport) {
	t.Helper()
	f := newFixture(t)
	source, err := f.service.Preview(f.ctx, f.actor.ID, f.input())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.K3sInstallation{}).Where("uuid = ?", source.ID).Updates(map[string]any{"status": "failed", "finished_at": time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	transport := &uninstallTransport{output: uninstallProofFixture(), verify: "EDO_K3S_UNINSTALL_VERIFIED\n"}
	f.service.WithTransport(transport)
	return f, source, transport
}

func uninstallPlan(t *testing.T, f fixture, source *Plan) *Plan {
	t.Helper()
	plan, err := f.service.PreviewUninstall(f.ctx, f.actor.ID, UninstallInput{InstallationID: source.ID, HostID: f.host.ID})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestUninstallPlanFreezesOneHostOfficialScriptWithoutRemoteWrite(t *testing.T) {
	f, source, transport := uninstallFixture(t)
	targets, err := f.service.ListUninstallTargets(f.ctx, f.actor.ID, f.host.ID)
	if err != nil || len(targets) != 1 || targets[0].Status != "failed" {
		t.Fatalf("targets=%+v err=%v", targets, err)
	}
	plan := uninstallPlan(t, f, source)
	if plan.Action != "uninstall" || plan.Uninstall == nil || plan.Uninstall.SourceInstallationID != source.ID || plan.Uninstall.ScriptPath != "/usr/local/bin/k3s-uninstall.sh" || len(plan.Hosts) != 1 || len(plan.Warnings) < 2 {
		t.Fatalf("invalid plan: %+v", plan)
	}
	if len(transport.runs) != 0 || transport.reads != 1 {
		t.Fatal("preview must only inspect remote state")
	}
	var record model.K3sInstallation
	if err := f.db.First(&record, "uuid = ?", plan.ID).Error; err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.service.decode(record)
	if err != nil || snapshot.Uninstall == nil || snapshot.Uninstall.Proof.Script == "" || len(snapshot.HostDigests) != 1 {
		t.Fatal("incomplete frozen snapshot")
	}
	public, _ := json.Marshal(plan)
	if strings.Contains(string(public), snapshot.Uninstall.Proof.Script) || strings.Contains(string(public), "ssh-fixture-password") {
		t.Fatal("private snapshot leaked")
	}
	var locks int64
	f.db.Model(&model.K3sInstallationHostLock{}).Count(&locks)
	if locks != 0 {
		t.Fatal("preview must not acquire task/host locks")
	}
}

func TestUninstallConfirmationAndInstallationEndpointCannotBypass(t *testing.T) {
	f, source, transport := uninstallFixture(t)
	plan := uninstallPlan(t, f, source)
	for _, confirmation := range []string{"", "确认 ", " 确认", "CONFIRM", "确认\n"} {
		if _, err := f.service.ExecuteUninstall(f.ctx, f.actor.ID, plan.ID, confirmation); !errors.Is(err, ErrUninstallConfirmation) {
			t.Fatalf("confirmation=%q err=%v", confirmation, err)
		}
	}
	if _, err := f.service.Execute(f.ctx, f.actor.ID, plan.ID); !errors.Is(err, ErrUninstallConfirmation) {
		t.Fatalf("installation bypass: %v", err)
	}
	if len(transport.runs) != 0 {
		t.Fatal("invalid confirmation executed remote script")
	}
	task, err := f.service.ExecuteUninstall(f.ctx, f.actor.ID, plan.ID, "确认")
	if err != nil || task.Status != "pending" || task.Uninstall == nil {
		t.Fatalf("submit=%+v err=%v", task, err)
	}
	if _, err := f.service.ExecuteUninstall(f.ctx, f.actor.ID, plan.ID, "确认"); !errors.Is(err, ErrPlanConsumed) {
		t.Fatalf("duplicate=%v", err)
	}
}

func TestUninstallWorkerOfficialOnlyAndVerifiedSuccess(t *testing.T) {
	f, source, transport := uninstallFixture(t)
	plan := uninstallPlan(t, f, source)
	if _, err := f.service.ExecuteUninstall(f.ctx, f.actor.ID, plan.ID, "确认"); err != nil {
		t.Fatal(err)
	}
	record, err := f.service.claim(f.ctx)
	if err != nil || record == nil {
		t.Fatal(err)
	}
	f.service.runInstallation(f.ctx, *record)
	task, err := f.service.Get(f.ctx, plan.ID)
	if err != nil || task.Status != "succeeded" {
		t.Fatalf("task=%+v err=%v", task, err)
	}
	if len(transport.runs) != 1 || !strings.Contains(transport.runs[0], `env K3S_DATA_DIR="$edo_data" sh -- "$edo_script"`) {
		t.Fatal("official script not used")
	}
	for _, bad := range []string{"rm -rf", "systemctl stop", "kill -9", "rmdir", "mkdir"} {
		if strings.Contains(transport.runs[0], bad) {
			t.Fatalf("custom uninstall mutation: %s", bad)
		}
	}
	if strings.Contains(task.Logs, "private-uninstaller-output-token") || strings.Contains(task.Logs, "接入集群") {
		t.Fatal("untrusted official output persisted")
	}
	var sourceRecord model.K3sInstallation
	f.db.First(&sourceRecord, "uuid = ?", source.ID)
	if sourceRecord.Status != "failed" {
		t.Fatal("source history rewritten")
	}
}

func TestUninstallWorkerFailureNeverRetriesOrClaimsSuccess(t *testing.T) {
	for _, scenario := range []string{"remote-error", "unverified", "unknown-output", "permission", "permission-during-run", "source-changed", "script-changed"} {
		t.Run(scenario, func(t *testing.T) {
			f, source, transport := uninstallFixture(t)
			plan := uninstallPlan(t, f, source)
			if _, err := f.service.ExecuteUninstall(f.ctx, f.actor.ID, plan.ID, "确认"); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "remote-error":
				transport.runErr = errors.New("private-transport-message")
			case "unverified":
				transport.verify = ""
			case "unknown-output":
				transport.verify += "private-output\n"
			case "permission":
				f.auth.denied = true
			case "permission-during-run":
				transport.afterRun = func() { f.auth.denied = true }
			case "source-changed":
				f.db.Model(&model.K3sInstallation{}).Where("uuid = ?", source.ID).Update("status", "running")
			case "script-changed":
				transport.output = strings.Replace(transport.output, ":100:", ":101:", 1)
			}
			record, err := f.service.claim(f.ctx)
			if err != nil || record == nil {
				t.Fatal(err)
			}
			f.service.runInstallation(f.ctx, *record)
			var saved model.K3sInstallation
			f.db.First(&saved, "uuid = ?", plan.ID)
			if saved.Status != "failed" || saved.ErrorMessage == "" || strings.Contains(saved.ErrorMessage+saved.Logs, "private-") {
				t.Fatalf("unsafe result: %+v", saved)
			}
			next, err := f.service.claim(f.ctx)
			if err != nil || next != nil {
				t.Fatal("failed uninstall requeued")
			}
		})
	}
}

func TestUninstallRejectsChangedExpiredBusyAndLateRevocation(t *testing.T) {
	for _, scenario := range []string{"script", "host", "expired", "busy", "permission-after-probe", "source"} {
		t.Run(scenario, func(t *testing.T) {
			f, source, transport := uninstallFixture(t)
			plan := uninstallPlan(t, f, source)
			switch scenario {
			case "script":
				transport.output = strings.Replace(transport.output, ":100:", ":101:", 1)
			case "host":
				f.db.Model(&model.Host{}).Where("uuid = ?", f.host.ID).Update("address", "192.0.2.11")
			case "expired":
				f.db.Model(&model.K3sInstallation{}).Where("uuid = ?", plan.ID).Update("expires_at", time.Now().Add(-time.Minute))
			case "busy":
				f.db.Create(&model.K3sInstallationHostLock{HostID: f.host.ID, InstallationID: uuid.NewString(), ProjectID: f.host.ProjectID, DepartmentID: f.host.DepartmentID, ExpiresAt: time.Now().Add(time.Hour)})
			case "permission-after-probe":
				transport.afterRead = func() { f.auth.denied = true }
			case "source":
				f.db.Model(&model.K3sInstallation{}).Where("uuid = ?", source.ID).Update("status", "running")
			}
			if _, err := f.service.ExecuteUninstall(f.ctx, f.actor.ID, plan.ID, "确认"); err == nil {
				t.Fatal("changed/invalid target submitted")
			}
			if len(transport.runs) != 0 {
				t.Fatal("submission must not execute scripts")
			}
		})
	}
}

func TestUninstallPermissionIsIndependentFreshAndSourceMayBeColleague(t *testing.T) {
	f, source, _ := uninstallFixture(t)
	actor := model.User{ID: uuid.NewString(), Username: "uninstall-" + uuid.NewString(), Nickname: "卸载同事", DepartmentID: f.actor.DepartmentID, IsActive: true, AuthVersion: 1, PasswordHash: "fixture-only", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	role := model.Role{ID: uuid.NewString(), Name: "uninstall-" + uuid.NewString(), DisplayName: "仅卸载", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	for _, value := range []any{&actor, &role, &model.RolePermission{RoleID: role.ID, Permission: access.PermissionK3sUninstall}, &model.RolePermission{RoleID: role.ID, Permission: "cluster.read"}, &model.ProjectMember{ProjectID: f.host.ProjectID, UserID: actor.ID, RoleID: role.ID}} {
		if err := f.db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: f.host.ProjectID, DepartmentID: actor.DepartmentID, UserID: actor.ID})
	targets, err := f.service.ListUninstallTargets(ctx, actor.ID, f.host.ID)
	if err != nil || len(targets) != 1 || targets[0].SourceInstallationID != source.ID {
		t.Fatalf("colleague source inaccessible: %v", err)
	}
	plan, err := f.service.PreviewUninstall(ctx, actor.ID, UninstallInput{InstallationID: source.ID, HostID: f.host.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ExecuteUninstall(ctx, actor.ID, plan.ID, "确认"); err != nil {
		t.Fatal("uninstall must not require install/node creation permissions:", err)
	}
	if err := f.db.Create(&model.UserPermission{UserID: actor.ID, Permission: access.PermissionK3sUninstall, Effect: model.PermissionDeny, CreatedAt: time.Now(), UpdatedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ListUninstallTargets(ctx, actor.ID, f.host.ID); !errors.Is(err, ErrUninstallPermission) {
		t.Fatalf("fresh deny ignored by permissive authorizer: %v", err)
	}
	if _, err := f.service.Get(f.ctx, plan.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("source creator must not read colleague uninstall task")
	}
}

func TestUninstallHistoryFiltersHostBeforeLimit(t *testing.T) {
	f, source, _ := uninstallFixture(t)
	plan := uninstallPlan(t, f, source)
	if _, err := f.service.ExecuteUninstall(f.ctx, f.actor.ID, plan.ID, "确认"); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 12; index++ {
		preview, _ := json.Marshal(Plan{Uninstall: &UninstallSummary{HostID: uuid.NewString()}})
		record := model.K3sInstallation{ID: uuid.NewString(), ProjectID: f.host.ProjectID, DepartmentID: f.host.DepartmentID, CreatedBy: f.actor.ID, Action: "uninstall", Name: "other", Status: "failed", Preview: datatypes.JSON(preview), Hosts: datatypes.JSON("[]"), InputCiphertext: "fixture", ExpiresAt: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now()}
		if err := f.db.Create(&record).Error; err != nil {
			t.Fatal(err)
		}
	}
	tasks, err := f.service.ListUninstallations(f.ctx, f.actor.ID, f.host.ID, 1)
	if err != nil || len(tasks) != 1 || tasks[0].ID != plan.ID {
		t.Fatalf("filtered after pagination: %+v %v", tasks, err)
	}
}

func TestUninstallTargetsPaginationNeverSilentlyLosesOldHost(t *testing.T) {
	f, source, _ := uninstallFixture(t)
	seed := func(count int) {
		t.Helper()
		records := make([]model.K3sInstallation, 0, count)
		for index := 0; index < count; index++ {
			hosts, _ := json.Marshal([]Host{{ID: uuid.NewString(), Role: "server"}})
			now := time.Now().UTC()
			records = append(records, model.K3sInstallation{ID: uuid.NewString(), ProjectID: f.host.ProjectID, DepartmentID: f.host.DepartmentID, CreatedBy: f.actor.ID, Action: "create", Name: "其他主机", Status: "failed", Preview: datatypes.JSON("{}"), Hosts: datatypes.JSON(hosts), InputCiphertext: "unrelated-fixture", ExpiresAt: now, CreatedAt: now, UpdatedAt: now})
		}
		if err := f.db.CreateInBatches(&records, 40).Error; err != nil {
			t.Fatal(err)
		}
	}
	seed(201)
	targets, err := f.service.ListUninstallTargets(f.ctx, f.actor.ID, f.host.ID)
	if err != nil || len(targets) != 1 || targets[0].SourceInstallationID != source.ID {
		t.Fatalf("old host target silently lost: %+v %v", targets, err)
	}
	seed(800)
	if targets, err := f.service.ListUninstallTargets(f.ctx, f.actor.ID, f.host.ID); err == nil || targets != nil {
		t.Fatal("scan limit returned misleading empty/incomplete success")
	}
}

func TestUninstallNativeJoinRoleChoosesExactOfficialScript(t *testing.T) {
	for _, role := range []string{"server", "agent"} {
		t.Run(role, func(t *testing.T) {
			f, source, _ := uninstallFixture(t)
			var record model.K3sInstallation
			if err := f.db.First(&record, "uuid = ?", source.ID).Error; err != nil {
				t.Fatal(err)
			}
			snapshot, err := f.service.decode(record)
			if err != nil {
				t.Fatal(err)
			}
			snapshot.Input.Action = "join"
			snapshot.Input.JoinRole = role
			snapshot.Input.JoinHostIDs = []string{f.host.ID}
			snapshot.Input.ServerHostID = ""
			encoded, _ := json.Marshal(snapshot)
			record.InputCiphertext, err = f.service.secrets.Encrypt(string(encoded), []byte("k3s-installation:"+record.ID))
			if err != nil {
				t.Fatal(err)
			}
			record.Action = "join"
			hosts, _ := json.Marshal([]Host{{ID: f.host.ID, Name: f.host.Name, Address: f.host.Address, Role: role}})
			record.Hosts = datatypes.JSON(hosts)
			if err := f.db.Save(&record).Error; err != nil {
				t.Fatal(err)
			}
			plan := uninstallPlan(t, f, source)
			service := "k3s"
			if role == "agent" {
				service = "k3s-agent"
			}
			if plan.Uninstall.ServiceName != service || plan.Uninstall.ScriptPath != "/usr/local/bin/"+service+"-uninstall.sh" || len(plan.Hosts) != 1 || plan.Hosts[0].Role != role {
				t.Fatal("join role changed uninstall target")
			}
		})
	}
}

func TestUninstallProofRejectsUnknownTruncatedDuplicateAndSecretOutput(t *testing.T) {
	valid := uninstallProofFixture()
	if _, err := parseUninstallProof(valid); err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{"", valid + "secret-token", strings.Replace(valid, "\tkillall\t", "\tscript\t", 1), strings.Replace(valid, "\tunit\t", "\tunknown\t", 1), strings.Replace(valid, ":0:0:700:", ":1000:0:700:", 1), strings.Replace(valid, "EDO_K3S_UNINSTALL_PROOF_END", "", 1), strings.Repeat("x", 5000)} {
		if _, err := parseUninstallProof(output); !errors.Is(err, ErrUninstallUnsupported) {
			t.Fatalf("invalid protocol accepted: %v", err)
		}
	}
}

func TestUninstallSourceRejectsK3dCustomServiceAmbiguousHostAndForeignScope(t *testing.T) {
	for _, scenario := range []string{"k3d", "custom-bin", "custom-service", "custom-systemd", "duplicate-host", "foreign-project", "foreign-department", "nonterminal"} {
		t.Run(scenario, func(t *testing.T) {
			f, source, transport := uninstallFixture(t)
			var record model.K3sInstallation
			if err := f.db.First(&record, "uuid = ?", source.ID).Error; err != nil {
				t.Fatal(err)
			}
			snapshot, err := f.service.decode(record)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "k3d":
				snapshot.Input.Mode = "k3d"
			case "custom-bin":
				snapshot.Input.Installer["INSTALL_K3S_BIN_DIR"] = "/opt/bin"
			case "custom-service":
				snapshot.Input.Installer["INSTALL_K3S_NAME"] = "custom"
			case "custom-systemd":
				snapshot.Input.Installer["INSTALL_K3S_SYSTEMD_DIR"] = "/opt/systemd"
			case "duplicate-host":
				record.Hosts = datatypes.JSON(fmt.Sprintf(`[{"id":%q,"role":"server"},{"id":%q,"role":"server"}]`, f.host.ID, f.host.ID))
			case "foreign-project":
				record.ProjectID = uuid.NewString()
			case "foreign-department":
				record.DepartmentID = uuid.NewString()
			case "nonterminal":
				record.Status = "running"
			}
			encoded, _ := json.Marshal(snapshot)
			record.InputCiphertext, err = f.service.secrets.Encrypt(string(encoded), []byte("k3s-installation:"+record.ID))
			if err != nil {
				t.Fatal(err)
			}
			if err := f.db.Save(&record).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := f.service.PreviewUninstall(f.ctx, f.actor.ID, UninstallInput{InstallationID: source.ID, HostID: f.host.ID}); err == nil {
				t.Fatal("unsafe source accepted")
			}
			if transport.reads != 0 || len(transport.runs) != 0 {
				t.Fatal("invalid source contacted remote")
			}
		})
	}
}

func TestUninstallPreviewCancelledAndLateRevocationCannotPersistPlan(t *testing.T) {
	for _, scenario := range []string{"cancel", "permission"} {
		t.Run(scenario, func(t *testing.T) {
			f, source, transport := uninstallFixture(t)
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			transport.afterRead = func() {
				if scenario == "cancel" {
					cancel()
				} else {
					f.auth.denied = true
				}
			}
			if _, err := f.service.PreviewUninstall(ctx, f.actor.ID, UninstallInput{InstallationID: source.ID, HostID: f.host.ID}); err == nil {
				t.Fatal("cancelled or revoked preview persisted")
			}
			var count int64
			f.db.Model(&model.K3sInstallation{}).Where("action = ?", "uninstall").Count(&count)
			if count != 0 || len(transport.runs) != 0 {
				t.Fatal("invalid preview wrote a plan or executed remote script")
			}
		})
	}
}

func TestUninstallDataDirectoryRejectsDestructiveOrAmbiguousPaths(t *testing.T) {
	for _, directory := range []string{"/", "/var", "/var/lib", "/var/lib/rancher", "/tmp/a/b", "/etc/rancher/k3s", "/var/lib/docker/a", "/var/lib/../root", "relative/data/dir", "/volume2/a\nb/c", "/var/lib/rancher/k3s suffix", "/volume2/group/k3s*", "/volume2/group/k3s?", "/volume2/group/k3s[0]", "/volume2/group/$k3s"} {
		if _, err := uninstallDataDir(map[string]any{"data-dir": directory}); err == nil {
			t.Fatalf("unsafe directory accepted: %q", directory)
		}
	}
	// 官方 grep 将 data-dir 当 BRE；带点目录不能误匹配另一套 containerd-shim。
	if _, err := uninstallDataDir(map[string]any{"data-dir": "/volume2/edo.k3s/nas24"}); err == nil {
		t.Fatal("BRE 通配路径被接受")
	}
	for _, directory := range []string{"/var/lib/rancher/k3s", "/volume2/edo-k3s/nas24"} {
		if _, err := uninstallDataDir(map[string]any{"data-dir": directory}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUninstallUnknownDestructiveResultKeepsHostLockedAndBlocksNewOperations(t *testing.T) {
	f, source, transport := uninstallFixture(t)
	plan := uninstallPlan(t, f, source)
	if _, err := f.service.ExecuteUninstall(f.ctx, f.actor.ID, plan.ID, "确认"); err != nil {
		t.Fatal(err)
	}
	transport.runErr = context.DeadlineExceeded
	record, err := f.service.claim(f.ctx)
	if err != nil || record == nil {
		t.Fatal(err)
	}
	f.service.runInstallation(f.ctx, *record)
	var locks int64
	f.db.Model(&model.K3sInstallationHostLock{}).Where("host_id = ?", f.host.ID).Count(&locks)
	if locks != 1 {
		t.Fatal("未知破坏阶段释放了主机互斥")
	}
	f.db.Model(&model.K3sInstallationHostLock{}).Where("host_id = ?", f.host.ID).Update("expires_at", time.Now().Add(-time.Hour))
	if err := f.service.recoverExpired(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.db.Model(&model.K3sInstallationHostLock{}).Where("host_id = ?", f.host.ID).Count(&locks)
	if locks != 1 {
		t.Fatal("未知锁被过期回收静默删除")
	}
	if _, err := f.service.PreviewUninstall(f.ctx, f.actor.ID, UninstallInput{InstallationID: source.ID, HostID: f.host.ID}); !errors.Is(err, ErrUninstallIncomplete) {
		t.Fatalf("未知状态允许重新卸载: %v", err)
	}
	snapshot := savedInput{Input: f.input(), HostDigests: map[string]string{f.host.ID: hostDigest(f.host)}, AuthVersion: f.actor.AuthVersion}
	if err := f.service.recheckInstallationAuthority(f.ctx, f.actor.ID, "create", snapshot); !errors.Is(err, ErrUninstallIncomplete) {
		t.Fatalf("未知状态允许新安装: %v", err)
	}
}
