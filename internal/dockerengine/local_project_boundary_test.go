package dockerengine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"edo/internal/database"
	"edo/internal/model"
)

func TestLocalDockerEndpointRequiresVisibleActiveProjectHost(t *testing.T) {
	service, db := newMonitorTestService(t)
	// 无效协议保证红灯阶段也不会接触开发机 Docker。
	service.config.DockerBuilderHost = "boundary-invalid://must-not-connect"
	owner := database.WithDepartmentScope(context.Background(), database.DepartmentScope{
		UserID: "local-owner", DepartmentID: database.DefaultDepartmentID,
		ProjectID: database.DefaultProjectID(database.DefaultDepartmentID),
	})
	foreign := database.WithDepartmentScope(context.Background(), database.DepartmentScope{
		UserID: "foreign-owner", DepartmentID: database.DefaultDepartmentID, ProjectID: "foreign-project",
	})
	local, err := service.Find(owner, LocalEndpointID)
	if err != nil || !local.IsActive {
		t.Fatalf("本地主机所属项目应保留正常目标: %v", err)
	}
	for _, test := range []struct {
		name string
		run  func() error
	}{
		{"查找", func() error { _, err := service.Find(foreign, LocalEndpointID); return err }},
		{"客户端", func() error { _, err := service.Client(foreign, LocalEndpointID); return err }},
		{"长任务客户端", func() error { _, err := service.executionClient(foreign, LocalEndpointID); return err }},
		{"容器列表", func() error { _, err := service.Containers(foreign, LocalEndpointID, true); return err }},
		{"容器日志", func() error {
			_, err := service.ContainerLogs(foreign, LocalEndpointID, strings.Repeat("a", 64), ContainerLogOptions{Tail: 10})
			return err
		}},
		{"监控", func() error { _, err := service.PingForMonitor(foreign, LocalEndpointID); return err }},
		{"终端版本", func() error { _, err := service.TerminalRevision(foreign, LocalEndpointID); return err }},
		{"直接客户端", func() error { _, err := service.clientForEndpoint(foreign, local); return err }},
		{"直接监控版本", func() error { _, err := service.monitorRevision(foreign, local); return err }},
		{"改名", func() error { _, err := service.Rename(foreign, LocalEndpointID, "禁止跨项目改名"); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); !errors.Is(err, ErrEndpointNotFound) {
				t.Fatalf("没有本地主机授权的项目必须在传输或写入之前拒绝: %v", err)
			}
		})
	}
	endpoints, err := service.List(foreign)
	if err != nil || len(endpoints) != 0 {
		t.Fatalf("未授权项目不得看到合成本地目标: count=%d err=%v", len(endpoints), err)
	}
	var localHost model.Host
	if err := db.WithContext(owner).First(&localHost, "uuid = ?", model.BuiltinLocalHostID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(owner).Model(&localHost).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.Find(owner, LocalEndpointID); !errors.Is(err, ErrEndpointNotFound) {
		t.Fatalf("停用本地主机后不得再合成可用连接: %v", err)
	}
}

func TestLocalDockerBoundaryKeepsOwnerAndRemoteEndpointsScoped(t *testing.T) {
	service, db := newMonitorTestService(t)
	ownerProject := database.DefaultProjectID(database.DefaultDepartmentID)
	owner := database.WithDepartmentScope(context.Background(), database.DepartmentScope{DepartmentID: database.DefaultDepartmentID, ProjectID: ownerProject})
	other := database.WithDepartmentScope(context.Background(), database.DepartmentScope{DepartmentID: database.DefaultDepartmentID, ProjectID: "remote-project"})
	endpoint := model.DockerEndpoint{ID: "project-remote-endpoint", Name: "项目远端", Host: "unix:///must-not-connect.sock", IsActive: true}
	if err := db.WithContext(other).Create(&endpoint).Error; err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"原项目仍有本地目标", owner, LocalEndpointID},
		{"新项目只看自己的远程目标", other, endpoint.ID},
	} {
		t.Run(test.name, func(t *testing.T) {
			items, err := service.List(test.ctx)
			if err != nil || len(items) != 1 || items[0].ID != test.want {
				t.Fatalf("目标列表未保持真实项目范围: count=%d err=%v", len(items), err)
			}
		})
	}
	local, err := service.Find(owner, LocalEndpointID)
	if err != nil || !local.IsActive || local.ProjectID != ownerProject || local.DepartmentID != database.DefaultDepartmentID {
		t.Fatalf("本地目标必须携带真实主机归属: %v", err)
	}
	if _, err := service.Find(owner, endpoint.ID); !errors.Is(err, ErrEndpointNotFound) {
		t.Fatalf("本地分支调整不能放开远端跨项目查询: %v", err)
	}
	if _, err := service.Find(other, endpoint.ID); err != nil {
		t.Fatalf("本项目远端目标不能受到误伤: %v", err)
	}
}
