package legacyimport

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"zrt/internal/auth"
	"zrt/internal/model"
	"zrt/internal/secret"
)

var (
	ErrNotLegacyDatabase = errors.New("来源数据库不是可识别的 ZRT 旧版数据库")
	ErrSecretsRequired   = errors.New("迁移配置数据前必须设置 ZRT_SECRETS_KEY")
)

type Stat struct {
	Created  int `json:"created"`
	Existing int `json:"existing"`
	Skipped  int `json:"skipped"`
	Planned  int `json:"planned,omitempty"`
}

type Report struct {
	DryRun                       bool     `json:"dry_run"`
	Users                        Stat     `json:"users"`
	Roles                        Stat     `json:"roles"`
	UserRoles                    Stat     `json:"user_roles"`
	Configurations               Stat     `json:"configurations"`
	Repositories                 Stat     `json:"repositories"`
	PasswordsRequireReset        int      `json:"passwords_require_reset"`
	RolePermissionsRequireReview int      `json:"role_permissions_require_review"`
	HostRecordsOmitted           int64    `json:"host_records_omitted"`
	UnsafeSchedulesOmitted       int64    `json:"unsafe_schedules_omitted"`
	ScriptMonitorsOmitted        int64    `json:"script_monitors_omitted"`
	LegacyDeploymentsOmitted     int64    `json:"legacy_deployments_omitted"`
	Warnings                     []string `json:"warnings,omitempty"`
}

type Importer struct {
	source      *gorm.DB
	destination *gorm.DB
	secrets     *secret.Manager
	dryRun      bool
	warnings    []string
}

func New(source, destination *gorm.DB, secrets *secret.Manager, dryRun bool) *Importer {
	return &Importer{source: source, destination: destination, secrets: secrets, dryRun: dryRun}
}

func (i *Importer) Run(ctx context.Context) (Report, error) {
	report := Report{DryRun: i.dryRun}
	if !i.source.Migrator().HasTable("users") {
		return report, ErrNotLegacyDatabase
	}
	roleIDs, err := i.importRoles(ctx, &report)
	if err != nil {
		return report, err
	}
	userIDs, err := i.importUsers(ctx, &report)
	if err != nil {
		return report, err
	}
	if err := i.importUserRoles(ctx, userIDs, roleIDs, &report); err != nil {
		return report, err
	}
	if err := i.importConfigurations(ctx, userIDs, &report); err != nil {
		return report, err
	}
	if err := i.importRepositories(ctx, userIDs, &report); err != nil {
		return report, err
	}
	report.HostRecordsOmitted, err = i.tableCount(ctx, "hosts")
	if err != nil {
		return report, err
	}
	report.UnsafeSchedulesOmitted, err = i.tableCount(ctx, "tasks")
	if err != nil {
		return report, err
	}
	report.ScriptMonitorsOmitted, err = i.tableCount(ctx, "detections")
	if err != nil {
		return report, err
	}
	report.LegacyDeploymentsOmitted, err = i.tableCount(ctx, "deploys")
	if err != nil {
		return report, err
	}
	report.Warnings = i.warnings
	return report, nil
}

type legacyUser struct {
	ID          int64
	Username    string
	Nickname    string
	IsSuperuser bool    `gorm:"column:is_supper"`
	IsActive    bool    `gorm:"column:is_active"`
	LastLogin   *string `gorm:"column:last_login"`
	CreatedAt   *string `gorm:"column:created_at"`
	DeletedAt   *string `gorm:"column:deleted_at"`
}

func (legacyUser) TableName() string { return "users" }

func (i *Importer) importUsers(ctx context.Context, report *Report) (map[int64]string, error) {
	var rows []legacyUser
	if err := i.source.WithContext(ctx).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取旧用户失败: %w", err)
	}
	result := make(map[int64]string, len(rows))
	for _, row := range rows {
		if row.DeletedAt != nil && strings.TrimSpace(*row.DeletedAt) != "" {
			report.Users.Skipped++
			continue
		}
		username := normalizeUsername(row.Username, row.ID)
		if username != strings.ToLower(strings.TrimSpace(row.Username)) {
			i.addWarning(fmt.Sprintf("旧用户 %d 的用户名已规范化为 %s", row.ID, username))
		}
		id := legacyID("user", row.ID)
		var existing model.User
		err := i.destination.WithContext(ctx).Where("id = ? OR username = ?", id, username).First(&existing).Error
		if err == nil {
			result[row.ID] = existing.ID
			report.Users.Existing++
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("检查旧用户迁移状态失败: %w", err)
		}
		result[row.ID] = id
		report.PasswordsRequireReset++
		if i.dryRun {
			report.Users.Planned++
			continue
		}
		passwordHash, err := auth.HashPassword(uuid.NewString() + uuid.NewString())
		if err != nil {
			return nil, fmt.Errorf("生成迁移账户占位密码失败: %w", err)
		}
		createdAt := parseLegacyTime(row.CreatedAt, time.Now().UTC())
		user := model.User{
			ID: id, Username: username, Nickname: normalizeNickname(row.Nickname, username),
			PasswordHash: passwordHash, IsActive: false, IsSuperuser: row.IsSuperuser,
			LastLoginAt: parseOptionalLegacyTime(row.LastLogin), CreatedAt: createdAt, UpdatedAt: createdAt,
		}
		if err := i.destination.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&user).Error; err != nil {
				return err
			}
			return tx.Model(&model.User{}).Where("id = ?", user.ID).UpdateColumn("is_active", false).Error
		}); err != nil {
			return nil, fmt.Errorf("写入迁移用户失败: %w", err)
		}
		report.Users.Created++
	}
	return result, nil
}

type legacyRole struct {
	ID          int64
	Name        string
	Description *string `gorm:"column:desc"`
	PagePerms   *string `gorm:"column:page_perms"`
	DeployPerms *string `gorm:"column:deploy_perms"`
	GroupPerms  *string `gorm:"column:group_perms"`
	CreatedAt   *string `gorm:"column:created_at"`
}

func (legacyRole) TableName() string { return "roles" }

func (i *Importer) importRoles(ctx context.Context, report *Report) (map[int64]string, error) {
	result := make(map[int64]string)
	if !i.source.Migrator().HasTable("roles") {
		return result, nil
	}
	var rows []legacyRole
	if err := i.source.WithContext(ctx).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取旧角色失败: %w", err)
	}
	for _, row := range rows {
		name := normalizeRoleName(row.Name, row.ID)
		id := legacyID("role", row.ID)
		if nonEmpty(row.PagePerms) || nonEmpty(row.DeployPerms) || nonEmpty(row.GroupPerms) {
			report.RolePermissionsRequireReview++
		}
		var existing model.Role
		err := i.destination.WithContext(ctx).Where("id = ? OR name = ?", id, name).First(&existing).Error
		if err == nil {
			result[row.ID] = existing.ID
			report.Roles.Existing++
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("检查旧角色迁移状态失败: %w", err)
		}
		result[row.ID] = id
		if i.dryRun {
			report.Roles.Planned++
			continue
		}
		now := parseLegacyTime(row.CreatedAt, time.Now().UTC())
		role := model.Role{
			ID: id, Name: name, DisplayName: normalizeDisplayName(row.Name, name),
			Description: truncate(optionalString(row.Description), 255), CreatedAt: now, UpdatedAt: now,
		}
		if err := i.destination.WithContext(ctx).Create(&role).Error; err != nil {
			return nil, fmt.Errorf("写入迁移角色失败: %w", err)
		}
		report.Roles.Created++
	}
	return result, nil
}

type legacyUserRole struct {
	UserID int64 `gorm:"column:user_id"`
	RoleID int64 `gorm:"column:role_id"`
}

func (i *Importer) importUserRoles(ctx context.Context, users, roles map[int64]string, report *Report) error {
	if !i.source.Migrator().HasTable("user_role_rel") {
		return nil
	}
	var rows []legacyUserRole
	if err := i.source.WithContext(ctx).Table("user_role_rel").Find(&rows).Error; err != nil {
		return fmt.Errorf("读取旧用户角色关系失败: %w", err)
	}
	for _, row := range rows {
		userID, userOK := users[row.UserID]
		roleID, roleOK := roles[row.RoleID]
		if !userOK || !roleOK {
			report.UserRoles.Skipped++
			continue
		}
		var count int64
		if err := i.destination.WithContext(ctx).Model(&model.UserRole{}).
			Where("user_id = ? AND role_id = ?", userID, roleID).Count(&count).Error; err != nil {
			return fmt.Errorf("检查用户角色迁移状态失败: %w", err)
		}
		if count > 0 {
			report.UserRoles.Existing++
			continue
		}
		if i.dryRun {
			report.UserRoles.Planned++
			continue
		}
		if err := i.destination.WithContext(ctx).Create(&model.UserRole{
			UserID: userID, RoleID: roleID, CreatedAt: time.Now().UTC(),
		}).Error; err != nil {
			return fmt.Errorf("写入用户角色关系失败: %w", err)
		}
		report.UserRoles.Created++
	}
	return nil
}

type legacyEnvironment struct {
	ID   int64
	Name string
	Key  string
	Prod bool
}

func (legacyEnvironment) TableName() string { return "environments" }

type legacyNamedObject struct {
	ID  int64
	Key string
}

type legacyConfiguration struct {
	ID          int64
	Type        string
	ObjectID    int64 `gorm:"column:o_id"`
	Key         string
	Environment int64 `gorm:"column:env_id"`
	Value       *string
	UpdatedAt   *string `gorm:"column:updated_at"`
	UpdatedByID int64   `gorm:"column:updated_by_id"`
}

func (legacyConfiguration) TableName() string { return "configs" }

func (i *Importer) importConfigurations(ctx context.Context, users map[int64]string, report *Report) error {
	if !i.source.Migrator().HasTable("configs") {
		return nil
	}
	var rows []legacyConfiguration
	if err := i.source.WithContext(ctx).Order("id ASC").Find(&rows).Error; err != nil {
		return fmt.Errorf("读取旧配置失败: %w", err)
	}
	if len(rows) > 0 && (i.secrets == nil || !i.secrets.Available()) && !i.dryRun {
		return ErrSecretsRequired
	}
	environments, err := i.loadEnvironments(ctx)
	if err != nil {
		return err
	}
	apps, err := i.loadNamedObjects(ctx, "apps")
	if err != nil {
		return err
	}
	services, err := i.loadNamedObjects(ctx, "services")
	if err != nil {
		return err
	}
	for _, row := range rows {
		environment, ok := environments[row.Environment]
		if !ok {
			report.Configurations.Skipped++
			continue
		}
		objects, prefix := apps, "app"
		if row.Type == "src" {
			objects, prefix = services, "service"
		} else if row.Type != "app" {
			report.Configurations.Skipped++
			continue
		}
		object, ok := objects[row.ObjectID]
		if !ok {
			report.Configurations.Skipped++
			continue
		}
		namespace := normalizeNamespace(prefix + "-" + object.Key)
		key := normalizeConfigKey(row.Key, row.ID)
		var count int64
		if err := i.destination.WithContext(ctx).Model(&model.Configuration{}).Where(
			"namespace = ? AND environment = ? AND key = ?", namespace, environment, key,
		).Count(&count).Error; err != nil {
			return fmt.Errorf("检查配置迁移状态失败: %w", err)
		}
		if count > 0 {
			report.Configurations.Existing++
			continue
		}
		if i.dryRun {
			report.Configurations.Planned++
			continue
		}
		id := legacyID("configuration", row.ID)
		value := optionalString(row.Value)
		ciphertext, err := i.secrets.Encrypt(value, []byte("configuration:"+id+":value"))
		if err != nil {
			return fmt.Errorf("加密迁移配置失败: %w", err)
		}
		actorID := users[row.UpdatedByID]
		if actorID == "" {
			actorID = fallbackActorID(users)
		}
		now := parseLegacyTime(row.UpdatedAt, time.Now().UTC())
		item := model.Configuration{
			ID: id, Namespace: namespace, Environment: environment, Key: key,
			SecretCiphertext: ciphertext, IsSecret: true, Version: 1, IsActive: true,
			CreatedBy: actorID, UpdatedBy: actorID, CreatedAt: now, UpdatedAt: now,
		}
		revision := model.ConfigurationRevision{
			ID: legacyID("configuration-revision", row.ID), ConfigurationID: id, Version: 1,
			Namespace: namespace, Environment: environment, Key: key, SecretCiphertext: ciphertext,
			IsSecret: true, IsActive: true, ChangedBy: actorID, CreatedAt: now,
		}
		if err := i.destination.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&item).Error; err != nil {
				return err
			}
			return tx.Create(&revision).Error
		}); err != nil {
			return fmt.Errorf("写入迁移配置失败: %w", err)
		}
		report.Configurations.Created++
	}
	return nil
}

func (i *Importer) loadEnvironments(ctx context.Context) (map[int64]model.EnvironmentType, error) {
	result := make(map[int64]model.EnvironmentType)
	if !i.source.Migrator().HasTable("environments") {
		return result, nil
	}
	var rows []legacyEnvironment
	if err := i.source.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取旧环境失败: %w", err)
	}
	for _, row := range rows {
		value := strings.ToLower(row.Key + " " + row.Name)
		switch {
		case row.Prod:
			result[row.ID] = model.EnvironmentProduction
		case strings.Contains(value, "staging"), strings.Contains(value, "stage"),
			strings.Contains(value, "uat"), strings.Contains(value, "test"), strings.Contains(value, "pre"):
			result[row.ID] = model.EnvironmentStaging
		default:
			result[row.ID] = model.EnvironmentDevelopment
		}
	}
	return result, nil
}

func (i *Importer) loadNamedObjects(ctx context.Context, table string) (map[int64]legacyNamedObject, error) {
	result := make(map[int64]legacyNamedObject)
	if !i.source.Migrator().HasTable(table) {
		return result, nil
	}
	var rows []legacyNamedObject
	if err := i.source.WithContext(ctx).Table(table).Select("id", "key").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取旧配置归属对象失败: %w", err)
	}
	for _, row := range rows {
		result[row.ID] = row
	}
	return result, nil
}

type legacyRepository struct {
	DeployID int64  `gorm:"column:deploy_id"`
	CloneURL string `gorm:"column:git_repo"`
}

func (i *Importer) importRepositories(ctx context.Context, users map[int64]string, report *Report) error {
	unique := make(map[string]int64)
	for _, table := range []string{"deploy_extend1", "deploy_extend3"} {
		if !i.source.Migrator().HasTable(table) {
			continue
		}
		var rows []legacyRepository
		if err := i.source.WithContext(ctx).Table(table).Select("deploy_id", "git_repo").Find(&rows).Error; err != nil {
			return fmt.Errorf("读取旧 Git 仓库失败: %w", err)
		}
		for _, row := range rows {
			cloneURL := strings.TrimSpace(row.CloneURL)
			if cloneURL != "" {
				if _, exists := unique[cloneURL]; !exists {
					unique[cloneURL] = row.DeployID
				}
			}
		}
	}
	for cloneURL, sourceID := range unique {
		provider, valid := classifyRepository(cloneURL)
		if !valid {
			report.Repositories.Skipped++
			continue
		}
		id := legacyStringID("repository", cloneURL)
		var count int64
		if err := i.destination.WithContext(ctx).Model(&model.GitRepository{}).
			Where("id = ? OR clone_url = ?", id, cloneURL).Count(&count).Error; err != nil {
			return fmt.Errorf("检查 Git 仓库迁移状态失败: %w", err)
		}
		if count > 0 {
			report.Repositories.Existing++
			continue
		}
		if i.dryRun {
			report.Repositories.Planned++
			continue
		}
		now := time.Now().UTC()
		repository := model.GitRepository{
			ID: id, Name: repositoryName(cloneURL, sourceID), Provider: provider, CloneURL: cloneURL,
			AuthType: model.GitAuthNone, IsActive: false, CreatedBy: fallbackActorID(users),
			CreatedAt: now, UpdatedAt: now,
		}
		if err := i.destination.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&repository).Error; err != nil {
				return err
			}
			return tx.Model(&model.GitRepository{}).Where("id = ?", repository.ID).UpdateColumn("is_active", false).Error
		}); err != nil {
			return fmt.Errorf("写入迁移 Git 仓库失败: %w", err)
		}
		report.Repositories.Created++
	}
	return nil
}

func classifyRepository(raw string) (model.GitProvider, bool) {
	host := ""
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" || parsed.Fragment != "" || parsed.User != nil {
			return "", false
		}
		scheme := strings.ToLower(parsed.Scheme)
		if scheme != "https" && scheme != "http" && scheme != "ssh" {
			return "", false
		}
		host = strings.ToLower(parsed.Hostname())
	} else {
		parts := strings.SplitN(raw, ":", 2)
		if len(parts) != 2 || !strings.Contains(parts[0], "@") || strings.ContainsAny(raw, " \t\r\n") {
			return "", false
		}
		host = strings.ToLower(strings.SplitN(parts[0], "@", 2)[1])
	}
	switch {
	case host == "github.com":
		return model.GitProviderGitHub, true
	case strings.Contains(host, "gitlab"):
		return model.GitProviderGitLab, true
	case strings.Contains(host, "gitea"):
		return model.GitProviderGitea, true
	case host == "gitee.com":
		return model.GitProviderGitee, true
	default:
		return model.GitProviderGeneric, true
	}
}

func (i *Importer) tableCount(ctx context.Context, table string) (int64, error) {
	if !i.source.Migrator().HasTable(table) {
		return 0, nil
	}
	var count int64
	if err := i.source.WithContext(ctx).Table(table).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("统计不迁移的旧数据失败: %w", err)
	}
	return count, nil
}

var identifierParts = regexp.MustCompile(`[^a-z0-9_.-]+`)
var roleParts = regexp.MustCompile(`[^a-z0-9_-]+`)
var namespaceParts = regexp.MustCompile(`[^a-z0-9_-]+`)
var configKeyParts = regexp.MustCompile(`[^A-Z0-9_]+`)

func normalizeUsername(value string, id int64) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = identifierParts.ReplaceAllString(value, "_")
	value = strings.Trim(value, "_.-")
	if value == "" || value[0] < 'a' || value[0] > 'z' {
		value = "user_" + strconv.FormatInt(id, 10) + "_" + value
	}
	value = truncate(value, 32)
	value = strings.TrimRight(value, "_.-")
	if len(value) < 3 {
		value += "_" + strconv.FormatInt(id, 10)
	}
	return truncate(value, 32)
}

func normalizeNickname(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	return truncate(value, 64)
}

func normalizeRoleName(value string, id int64) string {
	value = roleParts.ReplaceAllString(strings.ToLower(strings.TrimSpace(value)), "_")
	value = strings.Trim(value, "_-")
	if value == "" || value[0] < 'a' || value[0] > 'z' {
		value = "role_" + value
	}
	return truncate("legacy_"+value+"_"+strconv.FormatInt(id, 10), 64)
}

func normalizeDisplayName(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	return truncate(value, 64)
}

func normalizeNamespace(value string) string {
	value = namespaceParts.ReplaceAllString(strings.ToLower(strings.TrimSpace(value)), "-")
	value = strings.Trim(value, "-_")
	if value == "" || value[0] < 'a' || value[0] > 'z' {
		value = "legacy-" + value
	}
	if len(value) < 2 {
		value += "-config"
	}
	return truncate(value, 64)
}

func normalizeConfigKey(value string, id int64) string {
	value = configKeyParts.ReplaceAllString(strings.ToUpper(strings.TrimSpace(value)), "_")
	value = strings.TrimRight(value, "_")
	if value == "" {
		value = "LEGACY_CONFIG_" + strconv.FormatInt(id, 10)
	}
	return truncate(value, 128)
}

func repositoryName(raw string, sourceID int64) string {
	value := strings.TrimSuffix(strings.TrimSpace(raw), ".git")
	value = strings.TrimRight(value, "/")
	if index := strings.LastIndexAny(value, "/:"); index >= 0 {
		value = value[index+1:]
	}
	value = identifierParts.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-. ")
	if len(value) < 2 {
		value = "repository"
	}
	return truncate("legacy-"+value+"-"+strconv.FormatInt(sourceID, 10), 128)
}

func parseLegacyTime(value *string, fallback time.Time) time.Time {
	if value == nil {
		return fallback
	}
	text := strings.TrimSpace(*value)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, text, time.Local); err == nil {
			return parsed.UTC()
		}
	}
	return fallback
}

func parseOptionalLegacyTime(value *string) *time.Time {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	parsed := parseLegacyTime(value, time.Time{})
	if parsed.IsZero() {
		return nil
	}
	return &parsed
}

func legacyID(kind string, id int64) string {
	return legacyStringID(kind, strconv.FormatInt(id, 10))
}

func legacyStringID(kind, value string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("zrt:legacy:"+kind+":"+value)).String()
}

func fallbackActorID(users map[int64]string) string {
	var smallest int64
	for id := range users {
		if smallest == 0 || id < smallest {
			smallest = id
		}
	}
	if smallest != 0 {
		return users[smallest]
	}
	return legacyStringID("actor", "system")
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func nonEmpty(value *string) bool { return value != nil && strings.TrimSpace(*value) != "" }

func truncate(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

func (i *Importer) addWarning(message string) {
	if len(i.warnings) < 50 {
		i.warnings = append(i.warnings, message)
	}
}
