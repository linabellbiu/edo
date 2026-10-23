package database

import (
	"fmt"
	"regexp"
	"strings"

	"gorm.io/gorm"
)

// identityDDLFields 只拆分本次升级需要保留的建表定义，不解释或重写业务表达式。
// 引号、转义和括号内的逗号必须原样保留，CHECK、外键及默认值随表一并复制。
func identityDDLFields(ddl string) ([]string, error) {
	start, end := strings.IndexByte(ddl, '('), strings.LastIndexByte(ddl, ')')
	if start < 0 || end <= start {
		return nil, fmt.Errorf("建表定义缺少字段列表")
	}
	body := ddl[start+1 : end]
	var fields []string
	depth, begin := 0, 0
	var quote byte
	for i := 0; i < len(body); i++ {
		c := body[i]
		if quote != 0 {
			if c == quote {
				if i+1 < len(body) && body[i+1] == quote {
					i++
				} else {
					quote = 0
				}
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
		case '[':
			quote = ']'
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("建表定义括号不完整")
			}
		case ',':
			if depth == 0 {
				fields = append(fields, strings.TrimSpace(body[begin:i]))
				begin = i + 1
			}
		}
	}
	if quote != 0 || depth != 0 {
		return nil, fmt.Errorf("建表定义引号或括号不完整")
	}
	fields = append(fields, strings.TrimSpace(body[begin:]))
	return fields, nil
}

var identityPrimaryKey = regexp.MustCompile(`(?i)\bPRIMARY\s+KEY\b`)
var identityAutoIncrement = regexp.MustCompile(`(?i)\s+AUTOINCREMENT\b`)

func migrateSQLiteIdentities(db *gorm.DB, tables []identityTable) error {
	var foreignKeys, legacy int
	if err := db.Raw("PRAGMA foreign_keys").Scan(&foreignKeys).Error; err != nil {
		return err
	}
	if err := db.Raw("PRAGMA legacy_alter_table").Scan(&legacy).Error; err != nil {
		return err
	}
	if err := db.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
		return err
	}
	defer db.Exec(fmt.Sprintf("PRAGMA foreign_keys = %d", foreignKeys))
	defer db.Exec(fmt.Sprintf("PRAGMA legacy_alter_table = %d", legacy))
	return db.Transaction(func(tx *gorm.DB) error {
		// 先改全部资源列名，让 SQLite 同步改写其他表的外键和视图，再重建主键。
		for i := range tables {
			if err := renameResourceIdentity(tx, &tables[i]); err != nil {
				return err
			}
		}
		if err := tx.Exec("PRAGMA legacy_alter_table = ON").Error; err != nil {
			return err
		}
		for _, table := range tables {
			if err := rebuildSQLiteIdentity(tx, table); err != nil {
				return fmt.Errorf("升级数据表 %s 失败: %w", table.name, err)
			}
		}
		var violations []map[string]any
		if err := tx.Raw("PRAGMA foreign_key_check").Scan(&violations).Error; err != nil {
			return err
		}
		if len(violations) > 0 {
			return fmt.Errorf("主键升级后资源关联校验未通过")
		}
		return nil
	})
}
func rebuildSQLiteIdentity(db *gorm.DB, table identityTable) error {
	var ddl string
	if err := db.Raw("SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = ?", table.name).Scan(&ddl).Error; err != nil {
		return err
	}
	fields, err := identityDDLFields(ddl)
	if err != nil {
		return err
	}
	var objects []struct{ SQL string }
	if err := db.Raw("SELECT sql FROM sqlite_schema WHERE tbl_name = ? AND type IN ('index','trigger') AND sql IS NOT NULL", table.name).Scan(&objects).Error; err != nil {
		return err
	}
	next := []string{"`id` integer PRIMARY KEY AUTOINCREMENT"}
	for _, field := range fields {
		if identityPrimaryKey.MatchString(field) {
			// 复合主键的唯一性必须保留；资源 UUID 同样保持唯一。
			if strings.HasPrefix(strings.ToUpper(field), "PRIMARY") || strings.HasPrefix(strings.ToUpper(field), "CONSTRAINT") {
				field = identityPrimaryKey.ReplaceAllString(field, "UNIQUE")
				if strings.HasPrefix(strings.ToUpper(field), "UNIQUE") {
					field = "CONSTRAINT " + quoteIdentity(db, "ux_edo_identity_"+table.name) + " " + field
				}
			} else {
				field = identityAutoIncrement.ReplaceAllString(field, "")
				field = identityPrimaryKey.ReplaceAllString(field, "UNIQUE NOT NULL")
			}
		}
		next = append(next, field)
	}
	temp := table.name + "__edo_identity"
	if db.Migrator().HasTable(temp) {
		return fmt.Errorf("发现未完成的主键升级临时表，已停止操作")
	}
	if err := db.Exec("CREATE TABLE " + quoteIdentity(db, temp) + " (" + strings.Join(next, ",") + ")").Error; err != nil {
		return err
	}
	columns := identityColumns(db, table.columns)
	if err := db.Exec("INSERT INTO " + quoteIdentity(db, temp) + " (" + columns + ") SELECT " + columns + " FROM " + quoteIdentity(db, table.name) + " ORDER BY " + identityOrder(db, table)).Error; err != nil {
		return err
	}
	var oldCount, newCount int64
	if err := db.Table(table.name).Count(&oldCount).Error; err != nil {
		return err
	}
	if err := db.Table(temp).Count(&newCount).Error; err != nil {
		return err
	}
	if oldCount != newCount {
		return fmt.Errorf("复制前后记录数不一致")
	}
	for _, pair := range [][2]string{{table.name, temp}, {temp, table.name}} {
		var different int
		sql := "SELECT EXISTS(SELECT " + columns + " FROM " + quoteIdentity(db, pair[0]) + " EXCEPT SELECT " + columns + " FROM " + quoteIdentity(db, pair[1]) + ")"
		if err := db.Raw(sql).Scan(&different).Error; err != nil {
			return err
		}
		if different != 0 {
			return fmt.Errorf("复制前后字段内容不一致")
		}
	}
	if err := db.Exec("DROP TABLE " + quoteIdentity(db, table.name)).Error; err != nil {
		return err
	}
	if err := db.Exec("ALTER TABLE " + quoteIdentity(db, temp) + " RENAME TO " + quoteIdentity(db, table.name)).Error; err != nil {
		return err
	}
	for _, object := range objects {
		if err := db.Exec(object.SQL).Error; err != nil {
			return err
		}
	}
	return nil
}
