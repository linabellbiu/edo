package database

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"edo/internal/config"
	"edo/internal/model"
	"gorm.io/gorm"
)

func TestProjectScopeCRUDAndDerivedRecords(t *testing.T) {
	db, err := Open(context.Background(), config.Database{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "projects.db"), MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer Close(db)
	if err = Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	scope := func(project string, admin bool) context.Context {
		return WithDepartmentScope(context.Background(), DepartmentScope{UserID: "user", DepartmentID: DefaultDepartmentID, ProjectID: project, AllDepartments: admin})
	}
	a, b := scope("project-a", false), scope("project-b", false)
	if err = db.Create(&model.GitRepository{ID: "repository", Name: "代码仓库"}).Error; err != nil {
		t.Fatal(err)
	}
	appA := model.Application{ID: "app-a", Name: "同名应用", RepositoryID: "repository", IsActive: true}
	appB := model.Application{ID: "app-b", Name: "同名应用", RepositoryID: "repository", IsActive: true}
	if err = db.WithContext(a).Create(&appA).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.WithContext(b).Create(&appB).Error; err != nil {
		t.Fatal("不同项目应允许同名资源", err)
	}
	if appA.ProjectID != "project-a" || appB.ProjectID != "project-b" {
		t.Fatal("创建归属没有冻结")
	}
	for _, ctx := range []context.Context{a, scope("project-a", true)} {
		var rows []model.Application
		if err = db.WithContext(ctx).Find(&rows).Error; err != nil || len(rows) != 1 || rows[0].ID != appA.ID {
			t.Fatalf("列表跨项目泄露: %+v %v", rows, err)
		}
		var foreign model.Application
		if err = db.WithContext(ctx).First(&foreign, "uuid = ?", appB.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatal("跨项目详情必须不存在", err)
		}
		if r := db.WithContext(ctx).Model(&model.Application{}).Where("uuid = ?", appB.ID).Update("description", "不能修改"); r.Error != nil || r.RowsAffected != 0 {
			t.Fatalf("跨项目更新: %+v", r)
		}
		if r := db.WithContext(ctx).Where("uuid = ?", appB.ID).Delete(&model.Application{}); r.Error != nil || r.RowsAffected != 0 {
			t.Fatalf("跨项目删除: %+v", r)
		}
	}
	forged := model.Application{ID: "forged", Name: "伪造归属", ProjectID: "project-b"}
	if err = db.WithContext(a).Create(&forged).Error; !errors.Is(err, ErrProjectScope) {
		t.Fatal("禁止伪造创建归属", err)
	}
	run := model.PipelineRun{ID: "run-b", ApplicationID: appB.ID}
	if err = db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	if run.ProjectID != appB.ProjectID {
		t.Fatal("后台运行未继承应用项目")
	}
	build := model.BuildRun{ID: "build-b", ApplicationID: appB.ID, PipelineRunID: run.ID}
	if err = db.Create(&build).Error; err != nil {
		t.Fatal(err)
	}
	if build.ProjectID != appB.ProjectID {
		t.Fatal("制品生产未继承项目")
	}
}

func TestProjectSchemaDoesNotAdoptHistoricalResources(t *testing.T) {
	db, err := Open(context.Background(), config.Database{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "upgrade.db"), MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer Close(db)
	if err = Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&model.GitRepository{ID: "repository-original", Name: "历史仓库"}).Error; err != nil {
		t.Fatal(err)
	}
	app := model.Application{ID: "historical", Name: "历史应用", Description: "保持原样", RepositoryID: "repository-original", CreatedBy: "original-owner"}
	if err = db.Create(&app).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&app).Update("project_id", "").Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = migrateProjectSpaces(db); err != nil {
			t.Fatal(err)
		}
	}
	var stored model.Application
	if err = db.First(&stored, "uuid = ?", app.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Name != app.Name || stored.Description != app.Description || stored.RepositoryID != app.RepositoryID || stored.CreatedBy != app.CreatedBy || stored.ProjectID != "" {
		t.Fatalf("升级改写既有内容: %+v", stored)
	}
	var count int64
	db.Model(&model.Application{}).Count(&count)
	if count != 1 {
		t.Fatal("升级增删既有资源")
	}
}
