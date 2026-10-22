package database

import (
	"edo/internal/model"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"reflect"
	"strings"
	"time"
)

var ErrProjectScope = errors.New("项目数据范围无效")

func DefaultProjectID(departmentID string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("edo:department-project:"+departmentID)).String()
}

var projectModels = []any{&model.AuditLog{}, &model.Job{}, &model.OutboxEvent{}, &model.GitRepository{}, &model.DockerEndpoint{}, &model.KubernetesCluster{}, &model.DeploymentTarget{}, &model.DeploymentRecord{}, &model.NotificationChannel{}, &model.Notification{}, &model.ScheduledTask{}, &model.MonitorRule{}, &model.BuildRun{}, &model.Artifact{}, &model.ReleasePlan{}, &model.ReleasePlanExecution{}, &model.ReleaseWorkflow{}, &model.ReleaseWorkflowTemplate{}, &model.BuildPlan{}, &model.ImageRegistry{}, &model.DeploymentPlan{}, &model.Application{}, &model.PipelineRun{}, &model.DNSProviderAccount{}, &model.DNSDomain{}, &model.Environment{}, &model.Host{}}

func EnsureDepartmentProject(tx *gorm.DB, departmentID string) error {
	now := time.Now().UTC()
	project := model.Project{ID: DefaultProjectID(departmentID), DepartmentID: departmentID, Name: "默认项目", Description: "部门的初始项目空间", IsActive: true, IsDefault: true, CreatedAt: now, UpdatedAt: now}
	return tx.Where("id = ?", project.ID).FirstOrCreate(&project).Error
}

func migrateProjectSpaces(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&model.Organization{}, &model.Project{}, &model.ProjectMember{}); err != nil {
		return err
	}
	if err := addColumns(tx, &model.Department{}, []string{"ParentID"}); err != nil {
		return err
	}
	organization := model.Organization{ID: DefaultDepartmentID, Name: "我的公司", Description: "", UpdatedAt: time.Now().UTC()}
	if err := tx.Where("id = ?", organization.ID).FirstOrCreate(&organization).Error; err != nil {
		return err
	}
	var departments []model.Department
	if err := tx.Find(&departments).Error; err != nil {
		return err
	}
	for _, department := range departments {
		if err := EnsureDepartmentProject(tx, department.ID); err != nil {
			return err
		}
	}
	for _, value := range projectModels {
		if err := addColumns(tx, value, []string{"ProjectID"}); err != nil {
			return err
		}
		statement := &gorm.Statement{DB: tx}
		if err := statement.Parse(value); err != nil {
			return err
		}
		if err := addIndexes(tx, value, []string{"ProjectID"}); err != nil {
			return err
		}
	}

	for _, item := range departmentScopedUniqueIndexes {
		projectIndex := strings.Replace(item.newIndex, "_department_", "_project_", 1)
		if !tx.Migrator().HasIndex(item.model, projectIndex) {
			if err := tx.Migrator().CreateIndex(item.model, projectIndex); err != nil {
				return fmt.Errorf("创建项目唯一索引失败: %w", err)
			}
		}
		if tx.Migrator().HasIndex(item.model, item.newIndex) {
			if err := tx.Migrator().DropIndex(item.model, item.newIndex); err != nil {
				return fmt.Errorf("移除旧部门唯一索引失败: %w", err)
			}
		}
	}
	return nil
}

func registerProjectScope(db *gorm.DB) error {
	filter := func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.LookUpField("ProjectID") == nil {
			return
		}
		// 成员关联不是业务资源，由项目管理服务按目标项目显式校验。
		if tx.Statement.Table == "project_members" {
			return
		}
		scope, ok := DepartmentScopeFromContext(tx.Statement.Context)
		if !ok || scope.ProjectID == "" {
			return
		}
		tx.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Eq{Column: clause.Column{Table: clause.CurrentTable, Name: "project_id"}, Value: scope.ProjectID}}})
	}
	assign := func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Table == "project_members" {
			return
		}
		field := tx.Statement.Schema.LookUpField("ProjectID")
		if field == nil {
			return
		}
		scope, _ := DepartmentScopeFromContext(tx.Statement.Context)
		var assignOne func(reflect.Value)
		assignOne = func(value reflect.Value) {
			for value.IsValid() && value.Kind() == reflect.Pointer {
				value = value.Elem()
			}
			if !value.IsValid() {
				return
			}
			if value.Kind() == reflect.Slice || value.Kind() == reflect.Array {
				for i := 0; i < value.Len(); i++ {
					assignOne(value.Index(i))
				}
				return
			}
			if value.Kind() != reflect.Struct {
				tx.AddError(ErrProjectScope)
				return
			}
			raw, _ := field.ValueOf(tx.Statement.Context, value)
			projectID, _ := raw.(string)
			if scope.ProjectID != "" {
				if projectID != "" && projectID != scope.ProjectID {
					tx.AddError(ErrProjectScope)
					return
				}
				projectID = scope.ProjectID
			}
			if projectID == "" {
				// 后台创建的派生记录必须跟随父资源，不使用执行者当前项目推断。
				for _, parent := range []struct{ field, table string }{{"ApplicationID", "applications"}, {"PipelineRunID", "pipeline_runs"}, {"RunID", "pipeline_runs"}, {"ReleasePlanID", "release_plans"}, {"BuildPlanID", "build_plans"}, {"TargetID", "deployment_targets"}, {"AggregateID", "jobs"}} {
					parentField := tx.Statement.Schema.LookUpField(parent.field)
					if parentField == nil {
						continue
					}
					if !tx.Migrator().HasColumn(parent.table, "project_id") {
						continue
					}
					raw, _ := parentField.ValueOf(tx.Statement.Context, value)
					id, _ := raw.(string)
					if id == "" {
						continue
					}
					var row struct{ ProjectID string }
					query := tx.Session(&gorm.Session{NewDB: true}).Table(parent.table).Select("project_id").Where("id = ?", id).Take(&row)
					if query.Error == nil && row.ProjectID != "" {
						projectID = row.ProjectID
						break
					}
					if query.Error != nil && !errors.Is(query.Error, gorm.ErrRecordNotFound) {
						tx.AddError(query.Error)
						return
					}
				}
			}
			if projectID == "" {
				department := tx.Statement.Schema.LookUpField("DepartmentID")
				if department == nil {
					tx.AddError(ErrProjectScope)
					return
				}
				raw, _ := department.ValueOf(tx.Statement.Context, value)
				id, _ := raw.(string)
				if id == "" {
					id = scope.DepartmentID
				}
				if id == "" {
					id = DefaultDepartmentID
				}
				projectID = DefaultProjectID(id)
			}
			tx.AddError(field.Set(tx.Statement.Context, value, projectID))
		}
		assignOne(tx.Statement.ReflectValue)
	}
	if err := db.Callback().Create().Before("gorm:create").After("edo:department_assign").Register("edo:project_assign", assign); err != nil {
		return fmt.Errorf("注册项目创建范围失败: %w", err)
	}
	for _, item := range []struct {
		name     string
		register func(func(*gorm.DB)) error
	}{
		{"query", func(fn func(*gorm.DB)) error {
			return db.Callback().Query().Before("gorm:query").Register("edo:project_query", fn)
		}},
		{"update", func(fn func(*gorm.DB)) error {
			return db.Callback().Update().Before("gorm:update").Register("edo:project_update", fn)
		}},
		{"delete", func(fn func(*gorm.DB)) error {
			return db.Callback().Delete().Before("gorm:delete").Register("edo:project_delete", fn)
		}},
		{"row", func(fn func(*gorm.DB)) error {
			return db.Callback().Row().Before("gorm:row").Register("edo:project_row", fn)
		}},
	} {
		if err := item.register(filter); err != nil {
			return fmt.Errorf("注册项目%s范围失败: %w", item.name, err)
		}
	}
	return nil
}
