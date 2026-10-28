package host

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/terminal"
)

func hostInteractionFixture(t *testing.T, mode model.HostMode) (*Service, *gorm.DB, context.Context, string) {
	t.Helper()
	service, db, _ := newHostTestService(t)
	id, kind := uuid.NewString(), model.HostCapabilitySSH
	if mode == model.HostModeLocal {
		id, kind = model.BuiltinLocalHostID, model.HostCapabilityLocalExec
	}
	project := database.DefaultProjectID(database.DefaultDepartmentID)
	current := model.Host{ID: id, Name: "终端隔离测试", Mode: mode, IsActive: true, ProjectID: project, DepartmentID: database.DefaultDepartmentID, SSHHostKeyFingerprint: "SHA256:" + strings.Repeat("A", 43)}
	if err := db.Where("uuid = ?", id).Delete(&model.Host{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&current).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("host_id = ? AND kind = ?", id, kind).Delete(&model.HostCapability{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.HostCapability{HostID: id, Kind: kind, Status: model.HostCapabilityReady}).Error; err != nil {
		t.Fatal(err)
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: "terminal-test-user", DepartmentID: database.DefaultDepartmentID, ProjectID: project})
	return service, db, ctx, id
}

func TestHostTerminalRealLocalPTYAndEnvironmentIsolation(t *testing.T) {
	t.Setenv("EDO_TERMINAL_TEST_SECRET", "must-not-be-in-shell")
	service, _, ctx, id := hostInteractionFixture(t, model.HostModeLocal)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	session, err := service.OpenTerminal(ctx, id, "sh", terminal.Size{Columns: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.Resize(ctx, terminal.Size{Columns: 101, Rows: 31}); err != nil {
		t.Fatal(err)
	}
	local := session.(*localHostTerminal)
	rows, columns, err := pty.Getsize(local.file)
	if err != nil || rows != 31 || columns != 101 {
		t.Fatalf("真实 PTY 尺寸未更新: %d %d %v", rows, columns, err)
	}
	_, err = session.Write([]byte("printf '\\nPTY_TERM=%s SECRET_VALUE=%s\\n' \"$TERM\" \"${EDO_TERMINAL_TEST_SECRET-unset}\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	output := make(chan string, 1)
	go func() {
		var text strings.Builder
		buffer := make([]byte, 4096)
		for {
			n, err := session.Read(buffer)
			text.Write(buffer[:n])
			if strings.Contains(text.String(), "PTY_TERM=xterm-256color SECRET_VALUE=unset") || err != nil {
				output <- text.String()
				return
			}
		}
	}()
	select {
	case text := <-output:
		if !strings.Contains(text, "PTY_TERM=xterm-256color SECRET_VALUE=unset") || strings.Contains(text, "must-not-be-in-shell") {
			t.Fatalf("PTY 未连接或泄露 EDO 环境: %q", text)
		}
	case <-ctx.Done():
		t.Fatal("真实 PTY 输出超时")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Write([]byte("not-executed\n")); err == nil {
		t.Fatal("已关闭的 PTY 仍可写入")
	}
}

func TestHostInteractionRequiresExactProjectAndEnabledCapability(t *testing.T) {
	service, db, ctx, id := hostInteractionFixture(t, model.HostModeLocal)
	if _, err := service.OpenTerminal(context.Background(), id, "sh", terminal.Size{Columns: 80, Rows: 24}); !errors.Is(err, ErrHostInteractiveUnavailable) {
		t.Fatalf("无项目仍可打开主机终端: %v", err)
	}
	other := database.WithDepartmentScope(context.Background(), database.DepartmentScope{UserID: "terminal-test-user", DepartmentID: database.DefaultDepartmentID, ProjectID: uuid.NewString(), AllDepartments: true})
	if _, err := service.OpenFiles(other, id); !errors.Is(err, ErrHostNotFound) {
		t.Fatalf("管理员跨项目仍可打开主机: %v", err)
	}
	if err := db.Where("host_id = ? AND kind = ?", id, model.HostCapabilityLocalExec).Delete(&model.HostCapability{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.OpenFiles(ctx, id); !errors.Is(err, ErrHostInteractiveUnavailable) {
		t.Fatalf("已移除能力仍可浏览文件: %v", err)
	}
}

func TestHostTerminalRevokesWhenHostIdentityChanges(t *testing.T) {
	service, db, ctx, id := hostInteractionFixture(t, model.HostModeLocal)
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	session, err := service.OpenTerminal(ctx, id, "sh", terminal.Size{Columns: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := db.Model(&model.Host{}).Where("uuid = ?", id).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.(*localHostTerminal).lease.ctx.Done():
	case <-ctx.Done():
		t.Fatal("主机停用未关闭长期终端")
	}
	if _, err := session.Read(make([]byte, 32)); err == nil {
		// PTY 在关闭前可能已有提示符残留，再次读必须结束。
		if _, err = session.Read(make([]byte, 4096)); err == nil {
			t.Fatal("撤销后终端仍未结束")
		}
	}
}

func TestHostTerminalRejectsUnsafeShellAndSize(t *testing.T) {
	service := &Service{}
	for _, shell := range []string{"sh -c id", "/bin/sh", "bash;id"} {
		if _, err := service.OpenTerminal(context.Background(), "unused", shell, terminal.Size{Columns: 80, Rows: 24}); !errors.Is(err, terminal.ErrInvalidRequest) {
			t.Fatalf("未拒绝终端命令: %v", err)
		}
	}
	if _, err := service.OpenTerminal(context.Background(), "unused", "sh", terminal.Size{Columns: 0, Rows: 1}); !errors.Is(err, terminal.ErrInvalidRequest) {
		t.Fatalf("未拒绝错误尺寸: %v", err)
	}
}

func TestHostInteractionConnectionLimitAndCapabilityRevocation(t *testing.T) {
	service, db, ctx, id := hostInteractionFixture(t, model.HostModeLocal)
	slots := make(chan struct{}, 1)
	lease, err := service.newInteractionLease(ctx, id, slots)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if _, err := service.newInteractionLease(ctx, id, slots); !errors.Is(err, ErrHostInteractiveBusy) {
		t.Fatalf("连接数未受限: %v", err)
	}
	if err := db.Where("host_id = ? AND kind = ?", id, model.HostCapabilityLocalExec).Delete(&model.HostCapability{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := lease.check(ctx); !errors.Is(err, ErrHostInteractiveChanged) {
		t.Fatalf("能力移除后旧连接仍有效: %v", err)
	}
	if err := lease.Close(); err != nil || len(slots) != 0 {
		t.Fatalf("关闭未释放并发槽位: %v %d", err, len(slots))
	}
}
