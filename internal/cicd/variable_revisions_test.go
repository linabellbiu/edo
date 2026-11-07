package cicd

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"edo/internal/database"
	"edo/internal/model"
)

func TestConfigurationHistoryPreservesVersionsAndPaginatesMetadata(t *testing.T) {
	s, ctx := variableFixture(t)
	author := model.User{ID: uuid.NewString(), Username: "config_editor", Nickname: "配置编辑者", DepartmentID: database.DefaultDepartmentID}
	if err := s.db.Create(&author).Error; err != nil {
		t.Fatal(err)
	}
	input := VariableInput{Name: "orders_settings", Kind: "config_yaml", Usage: "deploy", IsActive: true, Value: pointer("port: 8080\n")}
	item, err := s.SaveVariable(ctx, author.ID, "", input)
	if err != nil {
		t.Fatal(err)
	}
	for version, value := range []string{"port: 9090\n", "port: 9091\n"} {
		input.ExpectedVersion, input.Value = uint64(version+1), pointer(value)
		if _, err = s.SaveVariable(ctx, author.ID, item.ID, input); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ListVariableRevisions(ctx, item.ID, 1, 2)
	if err != nil || page.Total != 3 || page.CurrentVersion != 3 || len(page.Revisions) != 2 || page.Revisions[0].Version != 3 || page.Revisions[1].Version != 2 {
		t.Fatalf("历史分页或版本错误: %#v %v", page, err)
	}
	if page.Revisions[0].Value != nil || page.Revisions[0].CreatedByName != author.Nickname {
		t.Fatal("历史列表应仅包含有归属的元数据")
	}
	second, err := s.ListVariableRevisions(ctx, item.ID, 2, 2)
	if err != nil || len(second.Revisions) != 1 || second.Revisions[0].Version != 1 {
		t.Fatalf("较早版本丢失: %v", err)
	}
	old, err := s.ReadVariableRevision(ctx, item.ID, 1, false)
	if err != nil || old.Value == nil || *old.Value != "port: 8080\n" || old.Hidden {
		t.Fatalf("读取历史原文错误: %v", err)
	}
	current, err := s.ReadVariableRevision(ctx, item.ID, 3, false)
	if err != nil || current.Value == nil || *current.Value != "port: 9091\n" {
		t.Fatalf("读取当前正文错误: %v", err)
	}
}

func TestConfigurationHistorySecretsRequireExplicitRevealAcrossAllVersions(t *testing.T) {
	s, ctx := variableFixture(t)
	input := VariableInput{Name: "production_settings", Kind: "config_env", Usage: "deploy", IsActive: true, Value: pointer("TOKEN=old-history-canary\n")}
	item, err := s.SaveVariable(ctx, "author", "", input)
	if err != nil {
		t.Fatal(err)
	}
	input.IsSecret, input.ExpectedVersion, input.Value = true, 1, pointer("TOKEN=new-history-canary\n")
	if _, err = s.SaveVariable(ctx, "author", item.ID, input); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListVariableRevisions(ctx, item.ID, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(page)
	if strings.Contains(string(encoded), "history-canary") {
		t.Fatal("历史元数据泄漏正文")
	}
	for _, version := range []uint64{1, 2} {
		view, readErr := s.ReadVariableRevision(ctx, item.ID, version, false)
		if readErr != nil || view.Value != nil || !view.Hidden || !view.IsSecret {
			t.Fatalf("密钥保护未覆盖历史版本: %v", readErr)
		}
		view, readErr = s.ReadVariableRevision(ctx, item.ID, version, true)
		if readErr != nil || view.Hidden || view.Value == nil || !strings.Contains(*view.Value, "history-canary") {
			t.Fatalf("显式读取未返回指定版本: %v", readErr)
		}
	}
	scope, _ := scopeFrom(ctx)
	scope.ProjectID = uuid.NewString()
	other := database.WithDepartmentScope(ctx, scope)
	if _, err = s.ReadVariableRevision(other, item.ID, 2, true); !errors.Is(err, ErrVariableUnavailable) {
		t.Fatalf("跨项目读取未拒绝: %v", err)
	}
	if _, err = s.ListVariableRevisions(other, item.ID, 1, 20); !errors.Is(err, ErrVariableUnavailable) {
		t.Fatalf("跨项目历史未隔离: %v", err)
	}
	if err = s.db.Model(&model.DeliveryVariableRevision{}).Where("variable_id = ? AND version = 2", item.ID).Update("ciphertext", "invalid-ciphertext-canary").Error; err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadVariableRevision(ctx, item.ID, 2, true); !errors.Is(err, ErrStorage) || strings.Contains(err.Error(), "canary") {
		t.Fatalf("解密失败未安全返回: %v", err)
	}
	if err = s.DeleteVariable(ctx, item.ID, 2); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadVariableRevision(ctx, item.ID, 1, true); !errors.Is(err, ErrVariableUnavailable) {
		t.Fatal("删除后的历史不应绕过资源边界")
	}
}

func TestConfigurationHistoryRejectsInvalidPaginationAndMissingRevision(t *testing.T) {
	s, ctx := variableFixture(t)
	for _, input := range [][2]int{{0, 20}, {1, 0}, {1, 51}, {100001, 20}} {
		if _, err := s.ListVariableRevisions(ctx, "irrelevant", input[0], input[1]); !errors.Is(err, ErrInvalidVariable) {
			t.Fatal("非法分页未拒绝")
		}
	}
	if _, err := s.ReadVariableRevision(ctx, "irrelevant", 0, false); !errors.Is(err, ErrInvalidVariable) {
		t.Fatal("版本零未拒绝")
	}
	item, err := s.SaveVariable(ctx, "author", "", VariableInput{Name: "empty_config", Kind: "config_text", Usage: "deploy", IsActive: true, Value: pointer("")})
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.ReadVariableRevision(ctx, item.ID, 1, false)
	if err != nil || view.Value == nil || *view.Value != "" {
		t.Fatal("空正文不能被视为未读取")
	}
	if _, err = s.ReadVariableRevision(ctx, item.ID, 99, true); !errors.Is(err, ErrVariableUnavailable) {
		t.Fatal("不存在的版本未拒绝")
	}
}
