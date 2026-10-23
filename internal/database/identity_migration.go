package database

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"gorm.io/gorm"
)

type identityTable struct {
	name    string
	value   any
	columns []string
	keys    []string
	renamed bool
	hasID   bool
}

// migrateRowIdentities 必须早于模型 AutoMigrate：SQLite 不能直接追加自增主键，
// 字符串主键也不能先被 ORM 当成整数改写。已有资源标识只改列名，不改值。
func migrateRowIdentities(ctx context.Context, db *gorm.DB) (err error) {
	defer func() {
		if err != nil {
			slog.ErrorContext(ctx, "升级数据库标识失败", "operation", "database_identity_migrate", "driver", db.Dialector.Name(), "err", err)
		}
	}()
	return db.WithContext(ctx).Connection(func(conn *gorm.DB) error {
		switch conn.Dialector.Name() {
		case "postgres":
			if err := conn.Exec("SELECT pg_advisory_lock(hashtext(?))", "edo_row_identity_upgrade").Error; err != nil {
				return err
			}
			defer conn.WithContext(context.Background()).Exec("SELECT pg_advisory_unlock(hashtext(?))", "edo_row_identity_upgrade")
		case "mysql":
			var acquired int
			if err := conn.Raw("SELECT GET_LOCK(?, 30)", "edo_row_identity_upgrade").Scan(&acquired).Error; err != nil {
				return err
			}
			if acquired != 1 {
				return fmt.Errorf("数据库升级正在执行，请稍后重试")
			}
			defer conn.WithContext(context.Background()).Exec("SELECT RELEASE_LOCK(?)", "edo_row_identity_upgrade")
		}
		var tables []identityTable
		for _, value := range persistedIdentityModels {
			stmt := &gorm.Statement{DB: conn}
			if err := stmt.Parse(value); err != nil {
				return err
			}
			if !conn.Migrator().HasTable(stmt.Table) {
				continue
			}
			columns, err := conn.Migrator().ColumnTypes(stmt.Table)
			if err != nil {
				return err
			}
			table := identityTable{name: stmt.Table, value: value}
			complete := false
			for _, column := range columns {
				table.columns = append(table.columns, column.Name())
				primary, _ := column.PrimaryKey()
				if primary {
					table.keys = append(table.keys, column.Name())
				}
				if column.Name() == "id" {
					table.hasID = true
					kind := strings.ToLower(column.DatabaseTypeName())
					complete = primary && (strings.Contains(kind, "int") || kind == "integer")
				}
			}

			if conn.Dialector.Name() == "sqlite" {
				var info []struct {
					Name string
					PK   int `gorm:"column:pk"`
				}
				if err := conn.Raw("PRAGMA table_info(" + quoteIdentity(conn, table.name) + ")").Scan(&info).Error; err != nil {
					return err
				}
				table.keys = nil
				complete = false
				for _, field := range info {
					if field.PK > 0 {
						table.keys = append(table.keys, field.Name)
					}
					if field.Name == "id" && field.PK > 0 {
						for _, column := range columns {
							if column.Name() == "id" {
								complete = strings.Contains(strings.ToLower(column.DatabaseTypeName()), "int")
							}
						}
					}
				}
			}
			if complete {
				continue
			}
			if table.hasID && stmt.Schema.FieldsByDBName["uuid"] == nil {
				return fmt.Errorf("数据表 %s 的主键结构不符合升级要求", table.name)
			}
			if len(table.keys) == 0 {
				return fmt.Errorf("数据表 %s 缺少可验证的原主键", table.name)
			}
			tables = append(tables, table)
		}
		if len(tables) == 0 {
			if conn.Dialector.Name() == "mysql" && conn.Migrator().HasTable("edo_identity_upgrade_journal") {
				return migrateServerIdentities(conn, nil)
			}
			return nil
		}
		slog.InfoContext(ctx, "开始升级数据库行号与资源标识", "operation", "database_identity_migrate", "driver", db.Dialector.Name(), "tables", len(tables))
		var err error
		switch conn.Dialector.Name() {
		case "sqlite":
			err = migrateSQLiteIdentities(conn, tables)
		case "postgres", "mysql":
			err = migrateServerIdentities(conn, tables)
		default:
			err = fmt.Errorf("当前数据库不支持主键升级")
		}
		return err
	})
}

func quoteIdentity(db *gorm.DB, name string) string {
	var result strings.Builder
	db.Dialector.QuoteTo(&result, name)
	return result.String()
}
func identityColumns(db *gorm.DB, names []string) string {
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = quoteIdentity(db, name)
	}
	return strings.Join(parts, ",")
}
func identityOrder(db *gorm.DB, table identityTable) string {
	var result []string
	for _, name := range []string{"created_at", "applied_at"} {
		for _, column := range table.columns {
			if name == column {
				result = append(result, quoteIdentity(db, name)+" ASC")
				break
			}
		}
	}
	for _, name := range table.keys {
		result = append(result, quoteIdentity(db, name)+" ASC")
	}
	return strings.Join(result, ",")
}
func renameResourceIdentity(db *gorm.DB, table *identityTable) error {
	if !table.hasID {
		return nil
	}
	for _, key := range table.keys {
		if key != "id" {
			continue
		}
		var empty int64
		if err := db.Table(table.name).Where("id IS NULL OR id = ?", "").Count(&empty).Error; err != nil {
			return err
		}
		if empty != 0 {
			return fmt.Errorf("数据表 %s 含空资源标识，已停止升级", table.name)
		}
		if err := db.Migrator().RenameColumn(table.name, "id", "uuid"); err != nil {
			return err
		}
		for i, column := range table.columns {
			if column == "id" {
				table.columns[i] = "uuid"
			}
		}
		for i, key := range table.keys {
			if key == "id" {
				table.keys[i] = "uuid"
			}
		}
		table.renamed = true
		table.hasID = false
		return nil
	}
	return nil
}

// verifyRowIdentities 防止新表或后续迁移重新把 UUID、组合字段设为数据库主键。
func verifyRowIdentities(db *gorm.DB) error {
	for _, value := range persistedIdentityModels {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(value); err != nil {
			return err
		}
		columns, err := db.Migrator().ColumnTypes(value)
		if err != nil {
			return err
		}
		count := 0
		for _, column := range columns {
			primary, _ := column.PrimaryKey()
			if !primary {
				continue
			}
			count++
			kind := strings.ToLower(column.DatabaseTypeName())
			if column.Name() != "id" || !strings.Contains(kind, "int") {
				return fmt.Errorf("数据表 %s 尚未使用整数主键", stmt.Table)
			}
		}
		if count != 1 {
			return fmt.Errorf("数据表 %s 的主键结构不正确", stmt.Table)
		}
		if err := verifyIdentityGeneration(db, stmt.Table); err != nil {
			return err
		}
	}
	return nil
}

func verifyIdentityGeneration(db *gorm.DB, table string) error {
	var generated bool
	switch db.Dialector.Name() {
	case "sqlite":
		var ddl string
		if err := db.Raw("SELECT sql FROM sqlite_schema WHERE type='table' AND name=?", table).Scan(&ddl).Error; err != nil {
			return err
		}
		generated = identityAutoIncrement.MatchString(ddl)
	case "postgres":
		if err := db.Raw("SELECT pg_get_serial_sequence(?, 'id') IS NOT NULL", table).Scan(&generated).Error; err != nil {
			return err
		}
	case "mysql":
		if err := db.Raw("SELECT COUNT(*) > 0 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND COLUMN_NAME='id' AND EXTRA LIKE '%auto_increment%'", table).Scan(&generated).Error; err != nil {
			return err
		}
	}
	if !generated {
		return fmt.Errorf("数据表 %s 的整数主键未配置自增", table)
	}
	return nil
}
