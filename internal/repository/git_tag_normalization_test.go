package repository

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"edo/internal/config"
	"edo/internal/model"
)

func TestGitListRefsNormalizesAnnotatedTagCommit(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "remote")
	remote, err := git.PlainInit(directory, false)
	if err != nil {
		t.Fatal(err)
	}
	commit := commitVersionFile(t, remote, directory, "tag target\n", "标签目标提交")
	annotated, err := remote.CreateTag("v-annotated", commit, &git.CreateTagOptions{
		Tagger:  &object.Signature{Name: "EDO Test", Email: "edo@example.com", When: time.Now().UTC()},
		Message: "标签说明，不是新的提交",
	})
	if err != nil {
		t.Fatal(err)
	}
	if annotated.Hash() == commit {
		t.Fatal("夹具必须拥有不同的标签对象和提交身份")
	}
	if err := remote.Storer.SetReference(plumbing.NewHashReference(plumbing.NewTagReferenceName("v-lightweight"), commit)); err != nil {
		t.Fatal(err)
	}
	client := NewGitClient(config.Git{Timeout: 5 * time.Second})
	refs, err := client.ListRefs(context.Background(), model.GitRepository{CloneURL: directory, AuthType: model.GitAuthNone}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs.Branches) != 1 || refs.Branches[0].SHA != commit.String() || len(refs.Tags) != 2 {
		t.Fatalf("分支或轻量标签丢失，或暴露了重复peeled标签: branches=%+v tags=%+v", refs.Branches, refs.Tags)
	}
	for _, ref := range refs.Tags {
		if ref.SHA != commit.String() {
			t.Fatalf("公开Tag提交不是目标Commit: tag=%s got=%s tag_object=%s want_commit=%s", ref.Name, ref.SHA, annotated.Hash(), commit)
		}
		if ref.Name == "v-annotated" && ref.RawTagOID != annotated.Hash().String() {
			t.Fatal("没有保留进程内原始标签对象身份")
		}
	}
	encoded, err := json.Marshal(refs)
	if err != nil || strings.Contains(string(encoded), annotated.Hash().String()) || strings.Contains(string(encoded), "RawTagOID") {
		t.Fatal("公开引用不得暴露内部标签对象别名")
	}
}

func TestRefsToResultPeeledTagsAreOrderIndependentAndBounded(t *testing.T) {
	raw, commit := plumbing.NewHash(strings.Repeat("a", 40)), plumbing.NewHash(strings.Repeat("b", 40))
	refs := []*plumbing.Reference{
		plumbing.NewHashReference("refs/tags/v1^{}", commit),
		plumbing.NewHashReference("refs/heads/v1", commit),
		plumbing.NewHashReference("refs/tags/orphan^{}", commit),
		plumbing.NewHashReference("refs/tags/v-lightweight", commit),
		plumbing.NewHashReference("refs/tags/v1", raw),
		plumbing.NewHashReference("refs/tags/v1^{}", commit),
	}
	result, err := refsToResult(refs)
	if err != nil || len(result.Branches) != 1 || len(result.Tags) != 2 || result.Tags[0].Name != "v-lightweight" || result.Tags[1].Name != "v1" || result.Tags[1].SHA != commit.String() || result.Tags[1].RawTagOID != raw.String() {
		t.Fatalf("peeled记录没有按准确引用合并排序: result=%+v err=%v", result, err)
	}
	refs = append(refs, plumbing.NewHashReference("refs/tags/v1^{}", plumbing.NewHash(strings.Repeat("c", 40))))
	if _, err := refsToResult(refs); err == nil {
		t.Fatal("冲突的peeled目标不能被顺序覆盖")
	}
	if _, err := refsToResult(make([]*plumbing.Reference, maxRemoteRefs+1)); err == nil {
		t.Fatal("不得放宽原远端引用数量上限")
	}
}
