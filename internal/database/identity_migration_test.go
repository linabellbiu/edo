package database

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"edo/internal/config"
	"edo/internal/model"
)

func identityTestDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	driver := os.Getenv("EDO_ID_TEST_DRIVER")
	dsn := os.Getenv("EDO_ID_TEST_DSN")
	if driver == "" {
		driver = "sqlite"
		dsn = filepath.Join(t.TempDir(), "identity.db")
	}
	if driver != "sqlite" && dsn == "" {
		t.Fatal("独立数据库主键测试缺少连接配置")
	}
	db, err := Open(context.Background(), config.Database{Driver: driver, DSN: dsn, MaxOpenConns: 4, MaxIdleConns: 2, ConnMaxLifetime: time.Hour}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Close(db) })
	tables, err := db.Migrator().GetTables()
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		if !strings.HasPrefix(table, "sqlite_") {
			t.Fatalf("主键测试必须使用空的独立数据库，发现数据表 %s", table)
		}
	}
	return db
}

func TestRowIdentitiesFresh(t *testing.T) {
	db := identityTestDatabase(t)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := verifyRowIdentities(db); err != nil {
		t.Fatal(err)
	}
	assertIdentityInsert(t, db)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
}

// frozenIdentityValue 复现此次变更前的数据库布局；不复用当前主键标签，避免升级测试只验证新建库。
func frozenIdentityValue(value any) (any, error) {
	original := reflect.ValueOf(value).Elem()
	typ := original.Type()
	keys := map[string][]string{
		"RolePermission": {"RoleID", "Permission"}, "UserRole": {"UserID", "RoleID"}, "UserPermission": {"UserID", "Permission"},
		"EnvironmentHost": {"EnvironmentID", "HostID"}, "HostCapability": {"HostID", "Kind"}, "ProjectMember": {"ProjectID", "UserID"},
		"ReleaseGroupDependency": {"ReleaseGroupID", "DependsOnGroupID"}, "SchemaMigration": {"Version"},
	}
	var fields []reflect.StructField
	var values []reflect.Value
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Name == "RowID" || field.PkgPath != "" {
			continue
		}
		settings := schema.ParseTagSetting(field.Tag.Get("gorm"), ";")
		if settings["FOREIGNKEY"] != "" || settings["-"] != "" {
			continue
		}
		tags := []string{}
		for _, tag := range strings.Split(field.Tag.Get("gorm"), ";") {
			lower := strings.ToLower(tag)
			if strings.HasPrefix(lower, "uniqueindex") || strings.HasPrefix(lower, "index") || lower == "primarykey" || lower == "column:uuid" {
				continue
			}
			if tag != "" {
				tags = append(tags, tag)
			}
		}
		primary := field.Name == "ID"
		if selected, ok := keys[typ.Name()]; ok {
			primary = false
			for _, name := range selected {
				if field.Name == name {
					primary = true
				}
			}
		}
		if primary {
			tags = append(tags, "primaryKey")
		}
		field.Tag = reflect.StructTag(`gorm:"` + strings.Join(tags, ";") + `"`)
		fields = append(fields, field)
		values = append(values, original.Field(i))
	}
	copy := reflect.New(reflect.StructOf(fields))
	for i, value := range values {
		copy.Elem().Field(i).Set(value)
	}
	return copy.Interface(), nil
}

func TestRowIdentitiesUpgrade(t *testing.T) {
	db := identityTestDatabase(t)
	for _, value := range persistedIdentityModels {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(value); err != nil {
			t.Fatal(err)
		}
		old, err := frozenIdentityValue(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Table(stmt.Table).AutoMigrate(old); err != nil {
			t.Fatalf("建立升级前表 %s: %v", stmt.Table, err)
		}
	}
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	for _, version := range migrations {
		if version.version == "202610240070" {
			continue
		}
		if err := db.Exec("INSERT INTO schema_migrations(version,applied_at) VALUES (?,?)", version.version, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	userID := "00000000-0000-0000-0000-000000000101"
	appID := "00000000-0000-0000-0000-000000000102"
	seed := []any{
		&model.User{ID: userID, Username: "identity-upgrade", Nickname: "保留账户", PasswordHash: "test-hash-preserved", DepartmentID: DefaultDepartmentID, IsActive: true, CreatedAt: now, UpdatedAt: now},
		&model.Application{ID: appID, Name: "保留应用", ProjectID: DefaultProjectID(DefaultDepartmentID), DepartmentID: DefaultDepartmentID, IsActive: true, CreatedAt: now, UpdatedAt: now},
		&model.PipelineRun{ID: "ffffffff-0000-0000-0000-000000000103", ApplicationID: appID, ProjectID: DefaultProjectID(DefaultDepartmentID), Status: model.PipelineRunFailed, CreatedAt: now, UpdatedAt: now},
		&model.PipelineRun{ID: "00000000-0000-0000-0000-000000000104", ApplicationID: appID, ProjectID: DefaultProjectID(DefaultDepartmentID), Status: model.PipelineRunSucceeded, CreatedAt: now.Add(time.Minute), UpdatedAt: now},
		&model.RolePermission{RoleID: userID, Permission: "pipeline_run.read", CreatedAt: now},
	}
	for _, value := range seed {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(value); err != nil {
			t.Fatal(err)
		}
		old, err := frozenIdentityValue(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Table(stmt.Table).Create(old).Error; err != nil {
			t.Fatal(err)
		}
	}
	keyType := "integer PRIMARY KEY AUTOINCREMENT"
	if db.Dialector.Name() == "postgres" {
		keyType = "bigserial PRIMARY KEY"
	}
	if db.Dialector.Name() == "mysql" {
		keyType = "bigint unsigned PRIMARY KEY AUTO_INCREMENT"
	}
	if err := db.Exec("CREATE TABLE identity_test_link(id " + keyType + ", user_uuid varchar(36) NOT NULL, CONSTRAINT identity_link_user FOREIGN KEY(user_uuid) REFERENCES users(id))").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO identity_test_link(user_uuid) VALUES (?)", userID).Error; err != nil {
		t.Fatal(err)
	}
	if db.Dialector.Name() == "mysql" {
		if err := db.Migrator().RenameColumn("users", "id", "uuid"); err != nil {
			t.Fatal(err)
		}
		interruptIdentityConstraints(t, db)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var user model.User
	if err := db.First(&user, "uuid = ?", userID).Error; err != nil {
		t.Fatal(err)
	}
	if user.RowID == 0 || user.ID != userID || user.PasswordHash != "test-hash-preserved" {
		t.Fatalf("账户字段变化: %+v", user)
	}
	var runs []model.PipelineRun
	if err := db.Preload("Application").Order("id ASC").Find(&runs).Error; err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].ID != "ffffffff-0000-0000-0000-000000000103" || runs[0].RowID >= runs[1].RowID || runs[0].Application.ID != appID || runs[1].Status != model.PipelineRunSucceeded {
		t.Fatalf("运行顺序或 UUID 关联变化: %+v", runs)
	}
	var links int64
	if err := db.Table("identity_test_link AS link").Joins("JOIN users ON users.uuid=link.user_uuid").Count(&links).Error; err != nil || links != 1 {
		t.Fatalf("资源关联丢失: count=%d err=%v", links, err)
	}
	if err := db.Exec("INSERT INTO identity_test_link(user_uuid) VALUES (?)", "missing-user").Error; !errors.Is(err, gorm.ErrForeignKeyViolated) {
		t.Fatalf("升级后外键约束丢失: %v", err)
	}
	var permission model.RolePermission
	if err := db.First(&permission, "role_id = ? AND permission = ?", userID, "pipeline_run.read").Error; err != nil || permission.RowID == 0 {
		t.Fatalf("关联表记录丢失: %+v %v", permission, err)
	}
	before := runs[1].RowID
	if db.Dialector.Name() == "mysql" {
		interruptIdentityConstraints(t, db)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var same model.PipelineRun
	if err := db.First(&same, "uuid = ?", runs[1].ID).Error; err != nil || same.RowID != before {
		t.Fatalf("重复迁移改写行号: %+v %v", same, err)
	}
	assertIdentityInsert(t, db)
}

// 模拟 MySQL DDL 已提交、进程在恢复外键前中断；再次升级必须从数据库记录恢复约束。
func interruptIdentityConstraints(t *testing.T, db *gorm.DB) {
	t.Helper()
	keys, err := identityForeignKeys(db, []identityTable{{name: "users"}})
	if err != nil || len(keys) != 1 {
		t.Fatalf("读取升级恢复夹具失败: %+v %v", keys, err)
	}
	if _, err := persistIdentityForeignKeys(db, keys); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ALTER TABLE identity_test_link DROP FOREIGN KEY identity_link_user").Error; err != nil {
		t.Fatal(err)
	}
}

func assertIdentityInsert(t *testing.T, db *gorm.DB) {
	t.Helper()
	var maximum uint64
	if err := db.Model(&model.User{}).Select("COALESCE(MAX(id),0)").Scan(&maximum).Error; err != nil {
		t.Fatal(err)
	}
	user := model.User{ID: "00000000-0000-0000-0000-000000000201", Username: "identity-next", Nickname: "后续写入", PasswordHash: "test-hash", DepartmentID: DefaultDepartmentID, IsActive: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if user.RowID <= maximum {
		t.Fatalf("数据库未继续生成自增行号: previous=%d next=%d", maximum, user.RowID)
	}
	var stored model.User
	if err := db.First(&stored, "uuid = ?", user.ID).Error; err != nil || stored.RowID != user.RowID {
		t.Fatalf("UUID 定位失败: %+v %v", stored, err)
	}
}

func TestIdentityDDLRetainsExpressions(t *testing.T) {
	fields, err := identityDDLFields("CREATE TABLE sample (`id` varchar(36) PRIMARY KEY, note text DEFAULT 'a,b', CHECK(length(note)>0), CONSTRAINT fk FOREIGN KEY (`note`) REFERENCES x(`uuid`))")
	if err != nil || len(fields) != 4 {
		t.Fatalf("字段和约束拆分错误: %v %v", fields, err)
	}
	if _, err := identityDDLFields("CREATE TABLE sample (note text DEFAULT 'missing)"); err == nil {
		t.Fatal("未拒绝不完整定义")
	}
}

func TestRowIdentityModelContract(t *testing.T) {
	db := identityTestDatabase(t)
	for _, value := range persistedIdentityModels {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(value); err != nil {
			t.Fatal(err)
		}
		primary := stmt.Schema.PrimaryFields
		if len(primary) != 1 || primary[0].DBName != "id" || !primary[0].AutoIncrement || primary[0].DataType != schema.Uint {
			t.Fatalf("数据表 %s 必须只有数据库生成的整数主键", stmt.Table)
		}
		for name, relation := range stmt.Schema.Relationships.Relations {
			for _, reference := range relation.References {
				if reference.PrimaryKey != nil && reference.PrimaryKey.DBName == "id" {
					t.Fatalf("数据表 %s 的关联 %s 使用了自增行号", stmt.Table, name)
				}
			}
		}
	}
}

func TestRowIdentityTransfer(t *testing.T) {
	target := identityTestDatabase(t)
	if err := Migrate(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	source := openTransferTestDatabase(t, "identity-transfer-source")
	user := model.User{RowID: 1000, ID: "00000000-0000-0000-0000-000000000301", Username: "identity-transfer-user", Nickname: "迁移账户", PasswordHash: "test-transfer-hash", IsActive: true}
	role := model.Role{ID: "00000000-0000-0000-0000-000000000302", Name: "identity-transfer-role", DisplayName: "迁移角色"}
	membership := model.UserRole{UserID: user.ID, RoleID: role.ID}
	for _, value := range []any{&user, &role, &membership} {
		if err := source.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureEmptyTransferTarget(target); err != nil {
		t.Fatal(err)
	}
	if err := copyDatabaseSnapshot(context.Background(), source, target, target.Dialector.Name(), func(int, int, int64) {}); err != nil {
		t.Fatal(err)
	}
	var copied model.User
	if err := target.First(&copied, "uuid = ?", user.ID).Error; err != nil || copied.RowID != 1000 || copied.PasswordHash != user.PasswordHash {
		t.Fatalf("复制改写了行号或账户内容: %+v %v", copied, err)
	}
	var joined int64
	if err := target.Model(&model.UserRole{}).Joins("JOIN users ON users.uuid=user_roles.user_id").Joins("JOIN roles ON roles.uuid=user_roles.role_id").Where("user_roles.user_id = ?", user.ID).Count(&joined).Error; err != nil || joined != 1 {
		t.Fatalf("跨库 UUID 关联丢失: %d %v", joined, err)
	}
	assertIdentityInsert(t, target)
}
