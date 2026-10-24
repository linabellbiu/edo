package cicd

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"edo/internal/model"
	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"gorm.io/gorm"
)

var ErrInvalidDatabase = errors.New("应用数据库配置无效，请检查连接和适用范围")
var ErrDatabaseConnect = errors.New("无法连接应用数据库，请检查地址、账号和访问权限")
var ErrMigrationState = errors.New("数据库变更已执行、正在执行或需要人工核对，不能重复执行")
var ErrMigrationChanged = errors.New("已登记的迁移脚本发生变化，请新增迁移文件")
var ErrMigrationFailed = errors.New("数据库变更失败，请核对变更记录后处理")

type DatabaseInput struct {
	Name            string  `json:"name"`
	ApplicationID   string  `json:"application_id"`
	EnvironmentID   string  `json:"environment_id"`
	Driver          string  `json:"driver"`
	Host            string  `json:"host"`
	Port            int     `json:"port"`
	Database        string  `json:"database"`
	Username        string  `json:"username"`
	Password        *string `json:"password"`
	TLSMode         string  `json:"tls_mode"`
	IsActive        bool    `json:"is_active"`
	ExpectedVersion uint64  `json:"expected_version"`
}

func (s *Service) ListDatabases(ctx context.Context) ([]model.ApplicationDatabase, error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, s.failure(ctx, "application_database_scope", err, err)
	}
	items := []model.ApplicationDatabase{}
	err = s.db.WithContext(ctx).Where("project_id = ?", scope.ProjectID).Order("id DESC").Find(&items).Error
	if err != nil {
		return nil, s.failure(ctx, "application_database_list", err, ErrStorage)
	}
	return items, nil
}
func (s *Service) FindDatabase(ctx context.Context, projectID, appID, id string) (*model.ApplicationDatabase, error) {
	var item model.ApplicationDatabase
	err := s.db.WithContext(ctx).Where("uuid = ? AND project_id = ? AND application_id = ? AND is_active = ?", id, projectID, appID, true).First(&item).Error
	if err != nil {
		return nil, s.failure(ctx, "application_database_find", err, ErrInvalidDatabase)
	}
	return &item, nil
}
func (s *Service) SaveDatabase(ctx context.Context, actorID, id string, in DatabaseInput, testOnly bool) (*model.ApplicationDatabase, error) {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return nil, s.failure(ctx, "application_database_scope", err, err)
	}
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 128 || len(in.Host) == 0 || len(in.Host) > 253 || strings.ContainsAny(in.Host, "/\\\n\r\x00") || in.Port < 1 || in.Port > 65535 || in.Database == "" || len(in.Database) > 128 || in.Username == "" || len(in.Username) > 128 || (in.Driver != "mysql" && in.Driver != "postgres") || (in.TLSMode != "disable" && in.TLSMode != "verify-full") || (in.Password != nil && len(*in.Password) > 65536) {
		return nil, s.failure(ctx, "application_database_validate", ErrInvalidDatabase, ErrInvalidDatabase)
	}
	var app model.Application
	if e := s.db.WithContext(ctx).First(&app, "uuid = ? AND project_id = ?", in.ApplicationID, scope.ProjectID).Error; e != nil {
		return nil, s.failure(ctx, "application_database_application", e, ErrInvalidDatabase)
	}
	var env model.Environment
	if e := s.db.WithContext(ctx).First(&env, "uuid = ? AND project_id = ?", in.EnvironmentID, scope.ProjectID).Error; e != nil {
		return nil, s.failure(ctx, "application_database_environment", e, ErrInvalidDatabase)
	}
	item := model.ApplicationDatabase{ID: uuid.NewString(), ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID, CreatedBy: actorID, Version: 1}
	password := ""
	if id != "" {
		if e := s.db.WithContext(ctx).First(&item, "uuid = ? AND project_id = ?", id, scope.ProjectID).Error; e != nil {
			return nil, s.failure(ctx, "application_database_read", e, ErrInvalidDatabase)
		}
		if item.Version != in.ExpectedVersion {
			return nil, s.failure(ctx, "application_database_version", ErrVariableConflict, ErrVariableConflict)
		}
		if item.ApplicationID != in.ApplicationID || item.EnvironmentID != in.EnvironmentID {
			return nil, s.failure(ctx, "application_database_identity", ErrInvalidDatabase, ErrInvalidDatabase)
		}
		password, err = s.secrets.Decrypt(item.Ciphertext, []byte("application-database:"+item.ID))
		if err != nil {
			return nil, s.failure(ctx, "application_database_decrypt", err, ErrStorage)
		}
		item.Version++
	} else if in.ExpectedVersion != 0 || in.Password == nil {
		return nil, s.failure(ctx, "application_database_create", ErrInvalidDatabase, ErrInvalidDatabase)
	}
	if in.Password != nil {
		password = *in.Password
	}
	fingerprint := digest(in.Driver, strings.ToLower(in.Host), strconv.Itoa(in.Port), in.Database)
	if id != "" && item.Fingerprint != fingerprint {
		return nil, s.failure(ctx, "application_database_target_immutable", ErrInvalidDatabase, ErrInvalidDatabase)
	}
	item.ApplicationID, item.EnvironmentID, item.Name, item.Driver, item.Host, item.Port, item.Database, item.Username, item.TLSMode, item.IsActive, item.UpdatedBy = in.ApplicationID, in.EnvironmentID, in.Name, in.Driver, in.Host, in.Port, in.Database, in.Username, in.TLSMode, in.IsActive, actorID
	item.Fingerprint = fingerprint
	connection, err := OpenApplicationDatabase(ctx, item, password)
	if err != nil {
		return nil, s.failure(ctx, "application_database_connect", err, ErrDatabaseConnect)
	}
	_ = connection.Close()
	if testOnly {
		return &item, nil
	}
	item.Ciphertext, err = s.secrets.Encrypt(password, []byte("application-database:"+item.ID))
	if err != nil {
		return nil, s.failure(ctx, "application_database_encrypt", err, ErrStorage)
	}
	if id == "" {
		err = s.db.WithContext(ctx).Create(&item).Error
	} else {
		result := s.db.WithContext(ctx).Model(&model.ApplicationDatabase{}).Where("uuid = ? AND project_id = ? AND version = ?", id, scope.ProjectID, in.ExpectedVersion).Select("*").Omit("id", "uuid", "project_id", "department_id", "created_at", "created_by").Updates(&item)
		err = result.Error
		if err == nil && result.RowsAffected != 1 {
			err = ErrVariableConflict
		}
	}
	if err != nil {
		return nil, s.failure(ctx, "application_database_save", err, safeControlError(err, ErrInvalidDatabase))
	}
	return &item, nil
}
func (s *Service) DeleteDatabase(ctx context.Context, id string, version uint64) error {
	scope, err := scopeFrom(ctx)
	if err != nil {
		return s.failure(ctx, "application_database_scope", err, err)
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r := tx.Model(&model.ApplicationDatabase{}).Where("uuid = ? AND project_id = ? AND version = ?", id, scope.ProjectID, version).Updates(map[string]any{"is_active": false, "version": version + 1})
		if r.Error != nil {
			return s.failure(ctx, "application_database_delete", r.Error, ErrStorage)
		}
		if r.RowsAffected != 1 {
			return s.failure(ctx, "application_database_delete_version", ErrVariableConflict, ErrVariableConflict)
		}
		if e := tx.Delete(&model.ApplicationDatabase{}, "uuid = ?", id).Error; e != nil {
			return s.failure(ctx, "application_database_delete", e, ErrStorage)
		}
		return nil
	})
}
func OpenApplicationDatabase(ctx context.Context, item model.ApplicationDatabase, password string) (*sql.DB, error) {
	var driver, dsn string
	if item.Driver == "mysql" {
		cfg := mysql.NewConfig()
		cfg.User = item.Username
		cfg.Passwd = password
		cfg.Net = "tcp"
		cfg.Addr = net.JoinHostPort(item.Host, strconv.Itoa(item.Port))
		cfg.DBName = item.Database
		cfg.ParseTime = true
		cfg.Timeout = 5 * time.Second
		cfg.ReadTimeout = 30 * time.Second
		cfg.WriteTimeout = 30 * time.Second
		if item.TLSMode == "verify-full" {
			cfg.TLSConfig = "true"
		}
		cfg.Params = map[string]string{"time_zone": "'+08:00'"}
		driver, dsn = "mysql", cfg.FormatDSN()
	} else {
		u := url.URL{Scheme: "postgres", Host: net.JoinHostPort(item.Host, strconv.Itoa(item.Port)), Path: item.Database, User: url.UserPassword(item.Username, password)}
		q := url.Values{"sslmode": {item.TLSMode}, "connect_timeout": {"5"}, "timezone": {"Asia/Shanghai"}}
		u.RawQuery = q.Encode()
		driver, dsn = "pgx", u.String()
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	ping, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err = db.PingContext(ping); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}
