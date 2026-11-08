package configuration

import (
	"context"
	"errors"
	"testing"

	"edo/internal/database"
	"edo/internal/model"
)

func TestGitHubLogoDefaultsVisibleAndPersistsGloballyWithRevision(t *testing.T) {
	s := newConfigurationTestService(t)
	initial, err := s.GetGitHubLogoSettings(context.Background())
	if err != nil || !initial.Enabled || initial.Version != 0 {
		t.Fatal(initial, err)
	}
	hidden, err := s.UpdateGitHubLogoSettings(context.Background(), "admin", false, 0)
	if err != nil || hidden.Enabled || hidden.Version != 1 {
		t.Fatal(hidden, err)
	}
	if _, err = s.UpdateGitHubLogoSettings(context.Background(), "admin", true, 0); !errors.Is(err, ErrVersionConflict) {
		t.Fatal("旧版本不能覆盖新设置", err)
	}
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{DepartmentID: "another-department", ProjectID: "another-project", UserID: "viewer"})
	reopened := NewService(s.db, s.secrets)
	saved, err := reopened.GetGitHubLogoSettings(ctx)
	if err != nil || saved != hidden {
		t.Fatal("设置未持久化或不属于全站", saved, err)
	}
	visible, err := s.UpdateGitHubLogoSettings(context.Background(), "admin", true, hidden.Version)
	if err != nil || !visible.Enabled || visible.Version != 2 {
		t.Fatal(visible, err)
	}
	var item model.Configuration
	if err = s.db.Where("namespace = ? AND key = ?", systemNamespace, githubLogoSettingKey).First(&item).Error; err != nil {
		t.Fatal(err)
	}
	revisions, err := s.Revisions(context.Background(), item.ID, 10)
	if err != nil || len(revisions) != 2 {
		t.Fatal("设置修订没有保存", revisions, err)
	}
}
