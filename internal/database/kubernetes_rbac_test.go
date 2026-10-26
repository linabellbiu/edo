package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"edo/internal/config"
	"edo/internal/model"
)

func TestKubernetesRBACMigrationCreatesNoAutomaticAuthorization(t *testing.T) {
	db, err := Open(context.Background(), config.Database{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "rbac.db"), MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer Close(db)
	for range 2 {
		if err := Migrate(context.Background(), db); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []any{&model.KubernetesRBACPolicy{}, &model.KubernetesRoleGrant{}, &model.KubernetesRBACSubject{}} {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("missing RBAC table for %T", table)
		}
		var count int64
		if err := db.Model(table).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("migration granted access or enabled policy for %T", table)
		}
		if !db.Migrator().HasColumn(table, "id") || !db.Migrator().HasColumn(table, "uuid") {
			t.Fatalf("RBAC model does not use shared row/uuid identity convention: %T", table)
		}
	}
	if err := VerifyMigrations(context.Background(), db); err != nil {
		t.Fatal(err)
	}
}
