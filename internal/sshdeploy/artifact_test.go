package sshdeploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStageLocalArtifactValidatesDigestAndCommitsAtomically(t *testing.T) {
	workingDirectory := t.TempDir()
	content := []byte("immutable artifact payload")
	digest := stagedArtifactDigest(content)
	destination := filepath.Join(workingDirectory, ".edo", "artifacts", "bundle.tar.gz")
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("previous version"), 0o600); err != nil {
		t.Fatal(err)
	}

	staged, err := stageLocalArtifact(context.Background(), Input{
		WorkingDirectory: workingDirectory,
		Artifact:         strings.NewReader(string(content)),
		ArtifactName:     "bundle.tar.gz",
		ArtifactDigest:   digest,
	})
	if err != nil {
		t.Fatalf("暂存本地制品失败: %v", err)
	}
	if staged != destination {
		t.Fatalf("制品没有落入受控目录: got=%q want=%q", staged, destination)
	}
	stored, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(content) {
		t.Fatalf("原子替换后的制品内容不正确: %q", stored)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("暂存制品权限不安全: %o", info.Mode().Perm())
	}
	temporaryFiles, err := filepath.Glob(destination + ".tmp-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(temporaryFiles) != 0 {
		t.Fatalf("原子落盘后遗留临时文件: %v", temporaryFiles)
	}

	environment := map[string]string{"EDO_PIPELINE_RUN_ID": "run-1"}
	injected := withArtifactEnvironment(environment, staged, digest)
	if injected["EDO_ARTIFACT_PATH"] != staged || injected["EDO_ARTIFACT_DIGEST"] != digest {
		t.Fatalf("部署环境没有注入制品路径和摘要: %+v", injected)
	}
	if _, changed := environment["EDO_ARTIFACT_PATH"]; changed {
		t.Fatal("注入制品元数据时修改了调用方环境变量")
	}
	body, err := deploymentScriptInput(Input{Script: "printf deploy", Environment: injected})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "export EDO_ARTIFACT_PATH='"+staged+"'\n") {
		t.Fatalf("部署脚本输入缺少制品路径: %q", body)
	}
}

func TestStageLocalArtifactRejectsDigestMismatchWithoutPartialFile(t *testing.T) {
	workingDirectory := t.TempDir()
	destination := filepath.Join(workingDirectory, ".edo", "artifacts", "bad.tar.gz")
	_, err := stageLocalArtifact(context.Background(), Input{
		WorkingDirectory: workingDirectory,
		Artifact:         strings.NewReader("tampered"),
		ArtifactName:     "bad.tar.gz",
		ArtifactDigest:   stagedArtifactDigest([]byte("expected")),
	})
	if !errors.Is(err, ErrInvalidScript) {
		t.Fatalf("摘要不匹配未被拒绝: %v", err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("摘要不匹配后仍留下最终制品: %v", err)
	}
	temporaryFiles, globErr := filepath.Glob(destination + ".tmp-*")
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(temporaryFiles) != 0 {
		t.Fatalf("摘要不匹配后仍留下临时制品: %v", temporaryFiles)
	}
}

func TestStageLocalArtifactStopsWhenContextCanceled(t *testing.T) {
	workingDirectory := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := stageLocalArtifact(ctx, Input{
		WorkingDirectory: workingDirectory,
		Artifact:         strings.NewReader("payload"),
		ArtifactName:     "canceled.tar.gz",
		ArtifactDigest:   stagedArtifactDigest([]byte("payload")),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("上下文取消后仍继续暂存制品: %v", err)
	}
	temporaryFiles, globErr := filepath.Glob(filepath.Join(workingDirectory, ".edo", "artifacts", "canceled.tar.gz.tmp-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(temporaryFiles) != 0 {
		t.Fatalf("取消后仍留下临时制品: %v", temporaryFiles)
	}
}

func TestStagedArtifactPathsContainUntrustedName(t *testing.T) {
	workingDirectory := t.TempDir()
	destination, _, err := stagedArtifactPaths(workingDirectory, "../../release bundle.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(workingDirectory, ".edo", "artifacts", "release_bundle.tar.gz")
	if destination != expected {
		t.Fatalf("不可信制品名称未被限制在暂存目录: got=%q want=%q", destination, expected)
	}
}

func stagedArtifactDigest(content []byte) string {
	digest := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(digest[:])
}
