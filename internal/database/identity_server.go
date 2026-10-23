package database

import (
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

type identityForeignKey struct{ TableName, Name, Definition string }

func migrateServerIdentities(db *gorm.DB, tables []identityTable) error {
	run := func(tx *gorm.DB) error {
		for i := range tables {
			if err := renameResourceIdentity(tx, &tables[i]); err != nil {
				return err
			}
		}
		constraints, err := identityForeignKeys(tx, tables)
		if err != nil {
			return err
		}
		if tx.Dialector.Name() == "mysql" {
			constraints, err = persistIdentityForeignKeys(tx, constraints)
			if err != nil {
				return err
			}
		}
		for _, key := range constraints {
			if !tx.Migrator().HasConstraint(key.TableName, key.Name) {
				continue
			}
			action := " DROP CONSTRAINT "
			if tx.Dialector.Name() == "mysql" {
				action = " DROP FOREIGN KEY "
			}
			if err := tx.Exec("ALTER TABLE " + quoteIdentity(tx, key.TableName) + action + quoteIdentity(tx, key.Name)).Error; err != nil {
				return err
			}
		}
		for _, table := range tables {
			if err := replaceServerIdentity(tx, table); err != nil {
				return fmt.Errorf("升级数据表 %s 失败: %w", table.name, err)
			}
		}
		for _, key := range constraints {
			if tx.Migrator().HasConstraint(key.TableName, key.Name) {
				continue
			}
			if err := tx.Exec("ALTER TABLE " + quoteIdentity(tx, key.TableName) + " ADD CONSTRAINT " + quoteIdentity(tx, key.Name) + " " + key.Definition).Error; err != nil {
				return err
			}
		}
		if tx.Dialector.Name() == "mysql" {
			return tx.Exec("DROP TABLE edo_identity_upgrade_journal").Error
		}
		return nil
	}
	if db.Dialector.Name() == "postgres" {
		return db.Transaction(run)
	}
	// MySQL DDL 会提交事务；每张表保留原 UUID 唯一约束，失败后仍可重新执行升级。
	return run(db)
}

// MySQL 的 DDL 不支持整段回滚，先持久化约束定义，进程中断后仍能恢复。
// 此临时表仅保存当前数据库的结构信息，升级完成后移除。
func persistIdentityForeignKeys(db *gorm.DB, keys []identityForeignKey) ([]identityForeignKey, error) {
	if err := db.Exec("CREATE TABLE IF NOT EXISTS edo_identity_upgrade_journal (id bigint unsigned NOT NULL AUTO_INCREMENT PRIMARY KEY, marker varchar(32) NOT NULL UNIQUE, payload longtext NOT NULL)").Error; err != nil {
		return nil, err
	}
	var payload string
	if err := db.Raw("SELECT payload FROM edo_identity_upgrade_journal WHERE marker = ?", "foreign_keys").Scan(&payload).Error; err != nil {
		return nil, err
	}
	if payload != "" {
		var saved []identityForeignKey
		if err := json.Unmarshal([]byte(payload), &saved); err != nil {
			return nil, fmt.Errorf("主键升级恢复记录无效: %w", err)
		}
		return saved, nil
	}
	data, err := json.Marshal(keys)
	if err != nil {
		return nil, err
	}
	if err := db.Exec("INSERT INTO edo_identity_upgrade_journal(marker,payload) VALUES (?,?)", "foreign_keys", string(data)).Error; err != nil {
		return nil, err
	}
	return keys, nil
}
func identityForeignKeys(db *gorm.DB, tables []identityTable) ([]identityForeignKey, error) {
	names := make([]string, len(tables))
	for i, table := range tables {
		names[i] = table.name
	}
	var result []identityForeignKey
	if db.Dialector.Name() == "postgres" {
		err := db.Raw(`SELECT child.relname AS table_name, c.conname AS name, pg_get_constraintdef(c.oid) AS definition
   FROM pg_constraint c JOIN pg_class child ON child.oid=c.conrelid JOIN pg_namespace ns ON ns.oid=child.relnamespace
   JOIN pg_class parent ON parent.oid=c.confrelid
   WHERE c.contype='f' AND ns.nspname=current_schema() AND parent.relname IN ?`, names).Scan(&result).Error
		return result, err
	}
	var keys []struct{ TableName, ConstraintName string }
	if err := db.Raw(`SELECT DISTINCT TABLE_NAME AS table_name, CONSTRAINT_NAME AS constraint_name FROM information_schema.KEY_COLUMN_USAGE
  WHERE TABLE_SCHEMA=DATABASE() AND REFERENCED_TABLE_NAME IN ?`, names).Scan(&keys).Error; err != nil {
		return nil, err
	}
	for _, key := range keys {
		rows, err := db.Raw("SHOW CREATE TABLE " + quoteIdentity(db, key.TableName)).Rows()
		if err != nil {
			return nil, err
		}
		var name, ddl string
		if rows.Next() {
			err = rows.Scan(&name, &ddl)
		} else {
			err = fmt.Errorf("未读取到外键所属表")
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
		fields, err := identityDDLFields(ddl)
		if err != nil {
			return nil, err
		}
		prefix := "CONSTRAINT " + quoteIdentity(db, key.ConstraintName) + " "
		found := false
		for _, field := range fields {
			if strings.HasPrefix(field, prefix) {
				result = append(result, identityForeignKey{TableName: key.TableName, Name: key.ConstraintName, Definition: strings.TrimPrefix(field, prefix)})
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("未读取到完整资源关联约束")
		}
	}
	return result, nil
}
func replaceServerIdentity(db *gorm.DB, table identityTable) error {
	quoted := quoteIdentity(db, table.name)
	uniqueName := "ux_edo_identity_" + table.name
	if !db.Migrator().HasIndex(table.name, uniqueName) {
		if err := db.Exec("CREATE UNIQUE INDEX " + quoteIdentity(db, uniqueName) + " ON " + quoted + " (" + identityColumns(db, table.keys) + ")").Error; err != nil {
			return err
		}
	}
	if !table.hasID {
		kind := "bigint"
		if db.Dialector.Name() == "mysql" {
			kind += " unsigned"
		}
		if err := db.Exec("ALTER TABLE " + quoted + " ADD COLUMN " + quoteIdentity(db, "id") + " " + kind).Error; err != nil {
			return err
		}
	}
	var matches []string
	for _, key := range table.keys {
		matches = append(matches, quoted+"."+quoteIdentity(db, key)+" = numbered."+quoteIdentity(db, key))
	}
	numbered := "SELECT " + identityColumns(db, table.keys) + ", ROW_NUMBER() OVER (ORDER BY " + identityOrder(db, table) + ") AS edo_row_number FROM " + quoted
	var statement string
	if db.Dialector.Name() == "postgres" {
		statement = "WITH numbered AS (" + numbered + ") UPDATE " + quoted + " SET id=numbered.edo_row_number FROM numbered WHERE " + strings.Join(matches, " AND ")
	} else {
		statement = "UPDATE " + quoted + " INNER JOIN (" + numbered + ") AS numbered ON " + strings.Join(matches, " AND ") + " SET " + quoted + ".id=numbered.edo_row_number"
	}
	if err := db.Exec(statement).Error; err != nil {
		return err
	}
	if db.Dialector.Name() == "mysql" {
		return db.Exec("ALTER TABLE " + quoted + " DROP PRIMARY KEY, MODIFY COLUMN id bigint unsigned NOT NULL AUTO_INCREMENT, ADD PRIMARY KEY (id)").Error
	}
	var primary string
	if err := db.Raw("SELECT conname FROM pg_constraint WHERE contype='p' AND conrelid=to_regclass(?)", table.name).Scan(&primary).Error; err != nil {
		return err
	}
	if primary == "" {
		return fmt.Errorf("未找到原主键约束")
	}
	sequence := table.name + "_id_seq"
	if err := db.Exec("CREATE SEQUENCE IF NOT EXISTS " + quoteIdentity(db, sequence) + " OWNED BY " + quoted + ".id").Error; err != nil {
		return err
	}
	if err := db.Exec("ALTER TABLE " + quoted + " DROP CONSTRAINT " + quoteIdentity(db, primary) + ", ALTER COLUMN id SET NOT NULL, ALTER COLUMN id SET DEFAULT nextval('" + sequence + "'), ADD PRIMARY KEY (id)").Error; err != nil {
		return err
	}
	return db.Exec("SELECT setval(?::regclass, GREATEST(COALESCE(MAX(id),0),1), COALESCE(MAX(id),0)>0) FROM "+quoted, sequence).Error
}
