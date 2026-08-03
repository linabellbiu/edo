package database

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DefaultDepartmentID 是旧数据迁移和首次安装使用的确定性默认部门。
// 使用固定值可以让 SQLite、PostgreSQL、MySQL 以及数据库转移得到一致结果。
const DefaultDepartmentID = "00000000-0000-0000-0000-000000000001"

var ErrDepartmentScopeRequired = errors.New("当前账户尚未分配部门")

type DepartmentScope struct {
	UserID         string
	DepartmentID   string
	AllDepartments bool
}

type departmentScopeContextKey struct{}

// WithDepartmentScope 把已经通过身份校验的数据范围写入请求上下文。
// 只有超级管理员可以使用 AllDepartments；普通请求缺少 DepartmentID 时按无数据处理。
func WithDepartmentScope(ctx context.Context, scope DepartmentScope) context.Context {
	return context.WithValue(ctx, departmentScopeContextKey{}, scope)
}

func DepartmentScopeFromContext(ctx context.Context) (DepartmentScope, bool) {
	if ctx == nil {
		return DepartmentScope{}, false
	}
	scope, ok := ctx.Value(departmentScopeContextKey{}).(DepartmentScope)
	return scope, ok
}

func registerDepartmentScope(db *gorm.DB) error {
	filter := func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil {
			return
		}
		if tx.Statement.Schema.LookUpField("DepartmentID") == nil {
			return
		}
		scope, scoped := DepartmentScopeFromContext(tx.Statement.Context)
		if !scoped || scope.AllDepartments {
			return
		}
		if scope.DepartmentID == "" {
			tx.AddError(ErrDepartmentScopeRequired)
			return
		}
		tx.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Eq{
			Column: clause.Column{Table: clause.CurrentTable, Name: "department_id"},
			Value:  scope.DepartmentID,
		}}})
	}

	assign := func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.LookUpField("DepartmentID") == nil {
			return
		}
		scope, scoped := DepartmentScopeFromContext(tx.Statement.Context)
		if !scoped {
			return
		}
		if scope.DepartmentID == "" {
			tx.AddError(ErrDepartmentScopeRequired)
			return
		}
		if scope.AllDepartments {
			// 超级管理员可以显式创建其他部门的资源；未显式指定时仍归属
			// 管理员自己的部门，避免产生普通部门永远看不到的无归属数据。
			field := tx.Statement.Schema.LookUpField("DepartmentID")
			value := tx.Statement.ReflectValue
			for value.IsValid() && value.Kind() == reflect.Pointer {
				value = value.Elem()
			}
			switch value.Kind() {
			case reflect.Struct:
				if _, zero := field.ValueOf(tx.Statement.Context, value); !zero {
					return
				}
			case reflect.Slice, reflect.Array:
				for index := 0; index < value.Len(); index++ {
					item := value.Index(index)
					if _, zero := field.ValueOf(tx.Statement.Context, item); zero {
						tx.AddError(field.Set(tx.Statement.Context, item, scope.DepartmentID))
					}
				}
				return
			}
		}
		tx.Statement.SetColumn("DepartmentID", scope.DepartmentID)
	}

	if err := db.Callback().Create().Before("gorm:create").Register("edo:department_assign", assign); err != nil {
		return fmt.Errorf("注册部门创建范围失败: %w", err)
	}
	if err := db.Callback().Query().Before("gorm:query").Register("edo:department_query", filter); err != nil {
		return fmt.Errorf("注册部门查询范围失败: %w", err)
	}
	if err := db.Callback().Update().Before("gorm:update").Register("edo:department_update", filter); err != nil {
		return fmt.Errorf("注册部门更新范围失败: %w", err)
	}
	if err := db.Callback().Delete().Before("gorm:delete").Register("edo:department_delete", filter); err != nil {
		return fmt.Errorf("注册部门删除范围失败: %w", err)
	}
	if err := db.Callback().Row().Before("gorm:row").Register("edo:department_row", filter); err != nil {
		return fmt.Errorf("注册部门行查询范围失败: %w", err)
	}
	return nil
}
