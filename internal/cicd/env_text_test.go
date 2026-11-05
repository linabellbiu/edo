package cicd

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"edo/internal/model"
)

func TestEnvTextFrozenSecretAndFile(t *testing.T) {
	s, ctx := variableFixture(t)
	scope, _ := scopeFrom(ctx)
	text := "TOKEN=private$literal\nEMPTY=\n"
	in := VariableInput{Name: "PROD_ENV", Kind: "env_text", Usage: "deploy", Value: &text, IsSecret: true, IsActive: true}
	v, err := s.SaveVariable(ctx, "owner", "", in)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := s.FreezeVariables(ctx, scope.ProjectID, "", "", "deploy", "refs/heads/main", []string{in.Name})
	if err != nil {
		t.Fatal(err)
	}
	in.Value, in.ExpectedVersion = pointer("TOKEN=new"), v.Version
	if _, err = s.SaveVariable(ctx, "owner", v.ID, in); err != nil {
		t.Fatal(err)
	}
	values, _, redactions, err := s.ResolveVariables(ctx, scope.ProjectID, "refs/heads/main", bindings)
	if err != nil || values["TOKEN"] != "private$literal" || values["EMPTY"] != "" || len(redactions) != 2 {
		t.Fatalf("未固定正文或逐项脱敏: %#v %v", values, err)
	}
	encoded, _ := json.Marshal(bindings)
	if strings.Contains(string(encoded), "private") {
		t.Fatal("快照包含正文")
	}
	in.Name, in.Kind, in.Value, in.ExpectedVersion = ".prod.env", "env_file", &text, 0
	file, err := s.SaveVariable(ctx, "owner", "", in)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.FreezeVariables(ctx, scope.ProjectID, "", "", "deploy", "refs/heads/main", []string{in.Name})
	if err != nil {
		t.Fatal(err)
	}
	values, files, _, err := s.ResolveVariables(ctx, scope.ProjectID, "refs/heads/main", b)
	if err != nil || len(values) != 0 || !strings.Contains(files[".prod.env"], "TOKEN=private$literal") {
		t.Fatalf("文件未独立解析: %v", err)
	}
	var revision model.DeliveryVariableRevision
	if err = s.db.WithContext(ctx).Where("variable_id = ?", file.ID).First(&revision).Error; err != nil || revision.Value != "" || revision.Ciphertext == "" {
		t.Fatal("文件正文未加密")
	}
	if err = s.DeleteVariable(ctx, file.ID, file.Version); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.ResolveVariables(ctx, scope.ProjectID, "refs/heads/main", b); !errors.Is(err, ErrVariableUnavailable) {
		t.Fatal("撤销文件仍可注入")
	}
}

func TestEnvTextInvalidDoesNotPersist(t *testing.T) {
	s, ctx := variableFixture(t)
	for _, candidate := range []VariableInput{
		{Name: "PROD", Kind: "env_text", Usage: "deploy", Value: pointer("A=private\nA=second")},
		{Name: "PROD", Kind: "env_text", Usage: "build", Value: pointer("A=private")},
		{Name: "../.env", Kind: "env_file", Usage: "deploy", Value: pointer("A=private")},
	} {
		if _, err := s.SaveVariable(ctx, "owner", "", candidate); !errors.Is(err, ErrInvalidVariable) || strings.Contains(err.Error(), "private") {
			t.Fatalf("配置未安全拒绝: %v", err)
		}
	}
	items, err := s.ListVariables(ctx)
	if err != nil || len(items) != 0 {
		t.Fatal("无效输入留下记录")
	}
}
