package repository

import (
	"errors"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

func TestValidateCloneURL(t *testing.T) {
	valid := []string{
		"https://github.com/example/repo.git",
		"ssh://git@git.example.com:2222/team/repo.git",
		"git@git.example.com:team/repo.git",
	}
	for _, value := range valid {
		if _, err := validateCloneURL(value, false); err != nil {
			t.Fatalf("合法仓库地址被拒绝 %q: %v", value, err)
		}
	}
	invalid := []string{
		"file:///etc/passwd", "/srv/repo", "https://user:password@example.com/repo.git", "--upload-pack=evil",
	}
	for _, value := range invalid {
		if _, err := validateCloneURL(value, false); !errors.Is(err, ErrInvalidRepository) {
			t.Fatalf("非法仓库地址未被拒绝 %q: %v", value, err)
		}
	}
	if _, err := validateCloneURL("http://git.example.com/repo.git", false); !errors.Is(err, ErrInsecureRepository) {
		t.Fatalf("HTTP 地址未要求显式确认: %v", err)
	}
}

func TestRefsToResult(t *testing.T) {
	result, err := refsToResult([]*plumbing.Reference{
		plumbing.NewHashReference(plumbing.NewBranchReferenceName("main"), plumbing.NewHash("0123456789012345678901234567890123456789")),
		plumbing.NewHashReference(plumbing.NewTagReferenceName("v1.0.0"), plumbing.NewHash("abcdefabcdefabcdefabcdefabcdefabcdefabcd")),
	})
	if err != nil {
		t.Fatalf("转换 Git 引用失败: %v", err)
	}
	if len(result.Branches) != 1 || result.Branches[0].Name != "main" || len(result.Tags) != 1 || result.Tags[0].Name != "v1.0.0" {
		t.Fatalf("转换 Git 引用失败: %+v", result)
	}
}
