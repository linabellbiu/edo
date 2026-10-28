package repository

import (
	"context"
	"errors"
	"testing"

	"edo/internal/credential"
	"edo/internal/model"
)

func TestRepositoryDestinationChangeCannotReuseOtherOwnersPersonalToken(t *testing.T) {
	service, db := newRepositoryTestService(t)
	ctx := context.Background()
	synthetic := "synthetic-personal-git-token"
	ownerCredential, err := service.credentials.Create(ctx, "token-owner", credential.Input{Name: "owner token", Provider: model.GitProviderGeneric, AuthType: model.GitAuthToken, Secret: &synthetic})
	if err != nil {
		t.Fatal(err)
	}
	input := Input{Name: "shared-repository", Provider: model.GitProviderGeneric, CloneURL: "https://original.invalid/team/repo.git", AuthType: model.GitAuthToken, CredentialID: &ownerCredential.ID}
	repo, _, err := service.Create(ctx, "token-owner", input)
	if err != nil {
		t.Fatal(err)
	}
	input.CredentialID = nil
	input.CloneURL = "https://new-destination.invalid/team/repo.git"
	if _, _, err = service.Update(ctx, "different-editor", repo.ID, input); !errors.Is(err, ErrCredentialRebindRequired) {
		t.Fatalf("地址变更仍继承他人个人凭据: %v", err)
	}
	var stored model.GitRepository
	if err = db.First(&stored, "uuid = ?", repo.ID).Error; err != nil || stored.CloneURL != repo.CloneURL || stored.CredentialID == nil || *stored.CredentialID != ownerCredential.ID {
		t.Fatalf("被拒绝的更新改变原地址或凭据: err=%v", err)
	}
	input.CredentialID = &ownerCredential.ID
	if _, _, err = service.Update(ctx, "different-editor", repo.ID, input); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("显式提供他人凭据引用仍被接受: %v", err)
	}
	input.CredentialID = nil
	input.CloneURL, input.Name, input.DefaultBranch = "https://ORIGINAL.invalid:443/team/other.git", "renamed repository", "develop"
	if _, _, err = service.Update(ctx, "different-editor", repo.ID, input); err != nil {
		t.Fatalf("相同认证目的地的普通修改不应要求读取原凭据: %v", err)
	}
	newSecret := "synthetic-editor-git-token"
	editorCredential, err := service.credentials.Create(ctx, "different-editor", credential.Input{Name: "editor token", Provider: model.GitProviderGeneric, AuthType: model.GitAuthToken, Secret: &newSecret})
	if err != nil {
		t.Fatal(err)
	}
	input.CloneURL, input.CredentialID = "https://new-destination.invalid/team/repo.git", &editorCredential.ID
	updated, _, err := service.Update(ctx, "different-editor", repo.ID, input)
	if err != nil || updated.CredentialID == nil || *updated.CredentialID != editorCredential.ID {
		t.Fatalf("更换为本人显式选择的凭据失败: err=%v", err)
	}
}

func TestRepositoryDestinationChangeCannotReuseHiddenOrProviderAPICredential(t *testing.T) {
	for _, target := range []string{"https://other.invalid/team/repo.git", "http://original.invalid/team/repo.git", "https://original.invalid:8443/team/repo.git"} {
		t.Run(target, func(t *testing.T) {
			service, _ := newRepositoryTestService(t)
			ctx := context.Background()
			synthetic := "synthetic-hidden-git-token"
			input := Input{Name: "hidden-token-repo", Provider: model.GitProviderGeneric, CloneURL: "https://original.invalid/team/repo.git", AuthType: model.GitAuthToken, Credential: &synthetic}
			repo, _, err := service.Create(ctx, "owner", input)
			if err != nil {
				t.Fatal(err)
			}
			input.CloneURL, input.Credential, input.AllowInsecureHTTP = target, nil, true
			if _, _, err = service.Update(ctx, "owner", repo.ID, input); !errors.Is(err, ErrCredentialRebindRequired) {
				t.Fatalf("隐藏凭据可隐式发送到新认证目的地: %v", err)
			}
			input.Credential = &synthetic
			if _, _, err = service.Update(ctx, "owner", repo.ID, input); err != nil {
				t.Fatalf("重新提供凭据后不能变更认证目的地: %v", err)
			}
		})
	}
	service, _ := newRepositoryTestService(t)
	ctx := context.Background()
	synthetic := "synthetic-provider-api-token"
	api, err := service.credentials.Create(ctx, "owner", credential.Input{Name: "provider API", Provider: model.GitProviderGitLab, AuthType: model.GitAuthToken, Secret: &synthetic})
	if err != nil {
		t.Fatal(err)
	}
	input := Input{Name: "api-bound-repo", Provider: model.GitProviderGitLab, CloneURL: "https://original.invalid/team/repo.git", AuthType: model.GitAuthNone, APICredentialID: &api.ID}
	repo, _, err := service.Create(ctx, "owner", input)
	if err != nil {
		t.Fatal(err)
	}
	input.CloneURL, input.APICredentialID = "https://new-destination.invalid/team/repo.git", nil
	if _, _, err = service.Update(ctx, "owner", repo.ID, input); !errors.Is(err, ErrCredentialRebindRequired) {
		t.Fatalf("平台 API 凭据随新地址隐式复用: %v", err)
	}
	input.APICredentialID = &api.ID
	if _, _, err = service.Update(ctx, "owner", repo.ID, input); err != nil {
		t.Fatalf("本人明确重绑平台 API 凭据失败: %v", err)
	}
}

func TestRepositoryCredentialDestinationKeepsEquivalentSSHOrigin(t *testing.T) {
	for _, test := range []struct {
		before, after string
		rebind        bool
	}{
		{"git@original.invalid:team/a.git", "ssh://git@original.invalid:22/team/b.git", false},
		{"ssh://git@original.invalid/a.git", "ssh://other@original.invalid/a.git", true},
		{"ssh://git@original.invalid/a.git", "ssh://git@original.invalid:2222/a.git", true},
		{"https://original.invalid/a.git", "https://original.invalid:443/b.git", false},
	} {
		t.Run(test.after, func(t *testing.T) {
			existing := &model.GitRepository{CloneURL: test.before, Provider: model.GitProviderGeneric}
			if got := credentialsNeedRebinding(existing, Input{CloneURL: test.after, Provider: model.GitProviderGeneric}); got != test.rebind {
				t.Fatalf("认证目的地判定不符: got=%v want=%v", got, test.rebind)
			}
		})
	}
}
