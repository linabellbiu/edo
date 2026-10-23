package database

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"edo/internal/config"
	"edo/internal/model"
)

const assistantMigrationVersion = "202610240068"

type assistantMigrationFixture struct {
	settings     model.AssistantSettings
	conversation model.AssistantConversation
	message      model.AssistantMessage
	action       model.AssistantAction
}

func TestAssistantSQLiteMigration(t *testing.T) {
	db := openAssistantMigrationDatabase(t, "sqlite", filepath.Join(t.TempDir(), "assistant.db"))
	exerciseAssistantMigration(t, db)
	target := openAssistantMigrationDatabase(t, "sqlite", filepath.Join(t.TempDir(), "assistant-copy.db"))
	assertAssistantTableTransfer(t, db, target, "sqlite")
}

func TestExternalAssistantMigration(t *testing.T) {
	selected := os.Getenv("EDO_TEST_DATABASE_DRIVER")
	switch selected {
	case "", "sqlite", "postgres", "mysql":
	default:
		t.Fatal("数据库测试驱动必须为 sqlite、postgres 或 mysql")
	}
	for _, item := range []struct{ name, driver, env string }{
		{"PostgreSQL", "postgres", "EDO_TEST_POSTGRES_DSN"},
		{"MySQL", "mysql", "EDO_TEST_MYSQL_DSN"},
	} {
		t.Run(item.name, func(t *testing.T) {
			if selected != "" && selected != item.driver {
				t.Skip("当前任务验证其他数据库")
			}
			dsn := os.Getenv(item.env)
			if dsn == "" {
				if selected == item.driver {
					t.Fatalf("数据库兼容性任务缺少 %s", item.env)
				}
				t.Skipf("未设置 %s", item.env)
			}
			// 与现有外部迁移测试相同，这里只接受可丢弃的专用测试库。
			db := openAssistantMigrationDatabase(t, item.driver, dsn)
			fixture := exerciseAssistantMigration(t, db)
			// 升级保留断言完成后，复制阶段使用空目标，避免两套样本的自增行号碰撞。
			for _, value := range []any{
				&model.AssistantAction{ID: fixture.action.ID},
				&model.AssistantMessage{ID: fixture.message.ID},
				&model.AssistantConversation{ID: fixture.conversation.ID},
				&model.AssistantSettings{ID: fixture.settings.ID},
			} {
				result := db.Where(value).Delete(value)
				if result.Error != nil || result.RowsAffected != 1 {
					t.Fatalf("准备助手复制空目标失败: %T: 删除行数=%d: %v", value, result.RowsAffected, result.Error)
				}
			}
			source := openAssistantMigrationDatabase(t, "sqlite", filepath.Join(t.TempDir(), "assistant-source.db"))
			assertAssistantTableTransfer(t, source, db, item.driver)
		})
	}
}

func openAssistantMigrationDatabase(t *testing.T, driver, dsn string) *gorm.DB {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	connections := 1
	if driver != "sqlite" {
		// MySQL 的迁移器会在事务之外借用连接，单连接池会阻塞结构迁移。
		connections = 4
	}
	db, err := Open(context.Background(), config.Database{
		Driver: driver, DSN: dsn, MaxOpenConns: connections, MaxIdleConns: 1, ConnMaxLifetime: time.Minute,
	}, logger)
	if err != nil {
		t.Fatalf("打开助手迁移测试库失败: %v", err)
	}
	t.Cleanup(func() {
		if err := Close(db); err != nil {
			t.Errorf("关闭助手迁移测试库失败: %v", err)
		}
	})
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("初始化助手迁移测试库失败: %v", err)
	}
	return db
}

func assistantMigrationModels() []any {
	return []any{&model.AssistantSettings{}, &model.AssistantConversation{}, &model.AssistantMessage{}, &model.AssistantAction{}}
}

func exerciseAssistantMigration(t *testing.T, db *gorm.DB) assistantMigrationFixture {
	t.Helper()
	assertAssistantSchema(t, db)
	// 只回退这次新表的迁移状态，验证从已有 EDO 数据升级时不会改写旧记录。
	now := time.Now().UTC().Truncate(time.Millisecond)
	user := model.User{ID: uuid.NewString(), Username: "migration_" + uuid.NewString()[:16], Nickname: "迁移前账户", PasswordHash: "test-only-hash", IsActive: true, AuthVersion: 7, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("写入升级前账户失败: %v", err)
	}
	if err := db.Migrator().DropTable(&model.AssistantAction{}, &model.AssistantMessage{}, &model.AssistantConversation{}, &model.AssistantSettings{}); err != nil {
		t.Fatalf("准备助手升级前结构失败: %v", err)
	}
	if err := db.Where("version = ?", assistantMigrationVersion).Delete(&model.SchemaMigration{}).Error; err != nil {
		t.Fatalf("准备助手升级前版本失败: %v", err)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("从既有结构升级助手表失败: %v", err)
	}
	assertAssistantSchema(t, db)
	var retained model.User
	if err := db.First(&retained, "uuid = ?", user.ID).Error; err != nil || retained.Username != user.Username || retained.Nickname != user.Nickname || retained.AuthVersion != user.AuthVersion {
		t.Fatalf("助手升级未保留既有账户: %v", err)
	}
	fixture := seedAssistantMigrationFixture(t, db)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("重复迁移助手表失败: %v", err)
	}
	if err := VerifyMigrations(context.Background(), db); err != nil {
		t.Fatalf("助手迁移版本不完整: %v", err)
	}
	assertAssistantFixture(t, db, fixture)
	return fixture
}

func assertAssistantSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, value := range assistantMigrationModels() {
		if !db.Migrator().HasTable(value) {
			t.Fatalf("助手表未创建: %T", value)
		}
	}
	for _, item := range []struct {
		value   any
		columns []string
	}{
		{&model.AssistantSettings{}, []string{"api_key_ciphertext", "version", "max_steps", "timeout_seconds"}},
		{&model.AssistantConversation{}, []string{"user_id", "project_id", "department_id", "lease_token", "lease_until"}},
		{&model.AssistantMessage{}, []string{"conversation_id", "user_id", "project_id", "department_id", "content", "required_permissions", "status"}},
		{&model.AssistantAction{}, []string{"conversation_id", "user_id", "project_id", "department_id", "kind", "target_id", "snapshot", "status", "expires_at", "result"}},
	} {
		for _, column := range item.columns {
			if !db.Migrator().HasColumn(item.value, column) {
				t.Fatalf("助手结构缺少字段: %T.%s", item.value, column)
			}
		}
	}
	for _, item := range []struct {
		value any
		index string
	}{
		{&model.AssistantConversation{}, "idx_assistant_conversation_scope"},
		{&model.AssistantMessage{}, "idx_assistant_message_conversation"},
		{&model.AssistantAction{}, "Status"},
		{&model.AssistantAction{}, "ExpiresAt"},
	} {
		if !db.Migrator().HasIndex(item.value, item.index) {
			t.Fatalf("助手索引未创建: %T.%s", item.value, item.index)
		}
	}
	for _, item := range []struct {
		value  any
		column string
	}{
		{&model.AssistantSettings{}, "api_key_ciphertext"},
		{&model.AssistantMessage{}, "content"},
		{&model.AssistantMessage{}, "required_permissions"},
		{&model.AssistantAction{}, "snapshot"},
	} {
		columns, err := db.Migrator().ColumnTypes(item.value)
		if err != nil {
			t.Fatalf("读取助手字段类型失败: %v", err)
		}
		found := false
		for _, column := range columns {
			if column.Name() != item.column {
				continue
			}
			found = true
			if !strings.Contains(strings.ToLower(column.DatabaseTypeName()), "text") {
				t.Fatalf("助手大字段类型不是 TEXT: %T.%s", item.value, item.column)
			}
			if _, exists := column.DefaultValue(); exists {
				t.Fatalf("助手 TEXT 字段不应配置数据库默认值: %T.%s", item.value, item.column)
			}
			if nullable, known := column.Nullable(); known && nullable {
				t.Fatalf("助手 TEXT 字段缺少非空约束: %T.%s", item.value, item.column)
			}
		}
		if !found {
			t.Fatalf("未读取到助手 TEXT 字段: %T.%s", item.value, item.column)
		}
	}
}

func seedAssistantMigrationFixture(t *testing.T, db *gorm.DB) assistantMigrationFixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	until := now.Add(time.Minute)
	userID, conversationID := uuid.NewString(), uuid.NewString()
	projectID := DefaultProjectID(DefaultDepartmentID)
	fixture := assistantMigrationFixture{
		settings:     model.AssistantSettings{ID: uuid.NewString(), BaseURL: "https://model.example.invalid/v1", Model: "migration-test", APIKeyCiphertext: "v1:test-only-ciphertext", Version: 7, MaxSteps: 6, TimeoutSeconds: 90, UpdatedBy: userID, UpdatedAt: now},
		conversation: model.AssistantConversation{ID: conversationID, UserID: userID, ProjectID: projectID, DepartmentID: DefaultDepartmentID, Title: "迁移测试会话", LeaseToken: uuid.NewString(), LeaseUntil: &until, CreatedAt: now, UpdatedAt: now},
		message:      model.AssistantMessage{ID: uuid.NewString(), ConversationID: conversationID, UserID: userID, ProjectID: projectID, DepartmentID: DefaultDepartmentID, Role: "assistant", Content: "已读取真实状态。\n保留中文、引号\"和换行。", RequiredPermissions: `["application.read","pipeline_run.read"]`, Status: "completed", CreatedAt: now},
		action:       model.AssistantAction{ID: uuid.NewString(), ConversationID: conversationID, UserID: userID, ProjectID: projectID, DepartmentID: DefaultDepartmentID, Kind: "pipeline_stop", Title: "停止流水线", Summary: "不会停止已部署的应用", TargetID: uuid.NewString(), TargetName: "迁移测试应用", Snapshot: strings.Repeat("a", 64), Status: "pending", Result: `{"message":"等待确认"}`, ExpiresAt: until, CreatedAt: now, UpdatedAt: now},
	}
	for _, value := range []any{&fixture.settings, &fixture.conversation, &fixture.message, &fixture.action} {
		if err := db.Create(value).Error; err != nil {
			t.Fatalf("写入助手迁移样本失败: %T: %v", value, err)
		}
	}
	return fixture
}

func assertAssistantFixture(t *testing.T, db *gorm.DB, fixture assistantMigrationFixture) {
	t.Helper()
	var settings model.AssistantSettings
	if err := db.First(&settings, "uuid = ?", fixture.settings.ID).Error; err != nil || settings.APIKeyCiphertext != fixture.settings.APIKeyCiphertext || settings.Version != fixture.settings.Version || settings.Enabled != fixture.settings.Enabled {
		t.Fatalf("助手设置未完整保留: %v", err)
	}
	var conversation model.AssistantConversation
	if err := db.First(&conversation, "uuid = ?", fixture.conversation.ID).Error; err != nil || conversation.UserID != fixture.conversation.UserID || conversation.ProjectID != fixture.conversation.ProjectID || conversation.LeaseToken != fixture.conversation.LeaseToken || conversation.LeaseUntil == nil || !conversation.LeaseUntil.Equal(*fixture.conversation.LeaseUntil) {
		t.Fatalf("助手会话及并发租约未完整保留: %v", err)
	}
	var message model.AssistantMessage
	if err := db.First(&message, "uuid = ?", fixture.message.ID).Error; err != nil || message.ConversationID != fixture.conversation.ID || message.Content != fixture.message.Content || message.RequiredPermissions != fixture.message.RequiredPermissions || message.UserID != fixture.message.UserID || message.ProjectID != fixture.message.ProjectID || message.DepartmentID != fixture.message.DepartmentID {
		t.Fatalf("助手消息及归属未完整保留: %v", err)
	}
	var action model.AssistantAction
	if err := db.First(&action, "uuid = ?", fixture.action.ID).Error; err != nil || action.Snapshot != fixture.action.Snapshot || action.Result != fixture.action.Result || action.Status != fixture.action.Status || action.UserID != fixture.action.UserID || action.ProjectID != fixture.action.ProjectID || action.DepartmentID != fixture.action.DepartmentID || !action.ExpiresAt.Equal(fixture.action.ExpiresAt) {
		t.Fatalf("助手确认记录及有效期未完整保留: %v", err)
	}
}

func assertAssistantTableTransfer(t *testing.T, source, target *gorm.DB, driver string) {
	t.Helper()
	fixture := seedAssistantMigrationFixture(t, source)
	known := knownTransferTableSet()
	for _, table := range []string{"assistant_settings", "assistant_conversations", "assistant_messages", "assistant_actions"} {
		count := 0
		for _, included := range orderedTransferTables {
			if included == table {
				count++
			}
		}
		if _, exists := known[table]; !exists || count != 1 {
			t.Fatalf("助手表未恰好一次纳入数据库转移清单: %s", table)
		}
		copied, err := copyTable(source, target, table, driver)
		if err != nil || copied < 1 {
			t.Fatalf("复制助手表失败: %s: %v", table, err)
		}
	}
	assertAssistantFixture(t, target, fixture)
}
