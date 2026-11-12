package pipeline

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"edo/internal/config"
	"edo/internal/credential"
	"edo/internal/database"
	"edo/internal/deployment"
	"edo/internal/model"
	"edo/internal/repository"
	"edo/internal/sshdeploy"
)

func newComposeParameterTest(t *testing.T, files map[string]string) (*Service, *executionContext, context.Context) {
	t.Helper()
	if files == nil {
		files = map[string]string{"README.md": "隔离仓库\n"}
	}
	node := model.WorkflowNode{ID: "compose-parameters", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentParameters: "--env-file .env"}}
	snapshot := workflowSnapshot{SchemaVersion: model.WorkflowSchemaVersion, AuthorityID: "admin", Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "deploy", Tasks: []model.WorkflowNode{node}}}}
	s, db, _, run := newBuildExecutorTestRun(t, node, snapshot, files)
	s.repositories = repository.NewService(db, s.secrets, nil, buildExecutorCheckoutStub{files: files}, 4, repository.WithCheckoutDirectory(t.TempDir()))
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prepared := &executionContext{run: run, node: node, snapshot: snapshot, deploymentPlan: model.DeploymentPlan{Kind: model.DeploymentPlanComposeStack}, target: model.DeploymentTarget{WorkingDirectory: directory}, variables: map[string]string{"EDO_IMAGE_TAG": "3.22", "PRIORITY": "pipeline"}}
	if err := db.First(&prepared.component, "pipeline_run_id = ?", run.ID).Error; err != nil {
		t.Fatal(err)
	}
	ctx := database.WithDepartmentScope(t.Context(), database.DepartmentScope{ProjectID: run.ProjectID, DepartmentID: run.DepartmentID, UserID: "admin"})
	return s, prepared, ctx
}

func TestComposeParameterFilesStageReferencedFilesAndRespectPrecedence(t *testing.T) {
	content := "IMAGE=alpine:${EDO_IMAGE_TAG}\nVALUE=repository\nPRIORITY=file\nPRIVATE=private-canary\n"
	s, prepared, ctx := newComposeParameterTest(t, map[string]string{".env": content, "config/override.env": "VALUE=override\n", "unselected.env": "unused=true\n"})
	prepared.node.Config.DeploymentParameters = `--env-file .env --env-file config/override.env --env-file "${EDO_APP_CONFIG_PATH}/generated.env"`
	prepared.configuration = &deployment.ConfigurationSet{Files: map[string]string{"generated.env": "VALUE=generated\n"}}
	if err := s.prepareComposeParameterFiles(ctx, prepared); err != nil {
		t.Fatal(err)
	}
	if len(prepared.inputFiles) != 2 || prepared.composeEnvValues["VALUE"] != "generated" || prepared.composeEnvValues["IMAGE"] != "alpine:3.22" || composeInterpolationValues(prepared)["PRIORITY"] != "pipeline" {
		t.Fatal("引用文件、插值或覆盖顺序不正确")
	}
	file := prepared.inputFiles[0]
	body, err := io.ReadAll(file.Content)
	if err != nil || string(body) != content || file.Directory != prepared.target.WorkingDirectory || file.Path != ".env" || file.Digest != sha256Digest(body) {
		t.Fatal("仓库文件的原文、位置或摘要发生变化")
	}
	if strings.Contains(prepared.run.WorkflowSnapshot, "private-canary") || !strings.Contains(strings.Join(prepared.variableRedactions, "\n"), "private-canary") {
		t.Fatal("文件正文进入快照或没有纳入脱敏")
	}
	// 普通配置文件不会因为参与插值而自动注入每个容器。
	if _, exists := prepared.variables["PRIVATE"]; exists {
		t.Fatal("仓库 ENV 被错误提升为容器共享环境变量")
	}
}

func TestComposeParameterLogsPreserveImagesAndRedactCredentials(t *testing.T) {
	content := "IMAGE_NAMESPACE=edo-demo\nIMAGE_TAG=v0.0.1\nREGISTRY_PASSWORD=registry-private-value\nPRIVATE_KEY=\"key-fragment-one\nkey-fragment-two\"\n"
	for _, generated := range []bool{false, true} {
		name := "repository"
		if generated {
			name = "configuration"
		}
		t.Run(name, func(t *testing.T) {
			s, prepared, ctx := newComposeParameterTest(t, map[string]string{".env": content})
			if generated {
				prepared.node.Config.DeploymentParameters = `--env-file "${EDO_APP_CONFIG_PATH}/generated.env"`
				prepared.configuration = &deployment.ConfigurationSet{Files: map[string]string{"generated.env": content}}
			}
			if err := s.prepareComposeParameterFiles(ctx, prepared); err != nil {
				t.Fatal(err)
			}
			ctx = withPipelineNodeLog(ctx, prepared.node.ID, prepared.node.Name)
			output := s.newExecutionLogWriter(ctx, prepared.run.ID, "deploy", "Docker Compose", composeExecutionLogRedactions(prepared)...)
			image := "edo-demo/edo-demo-web:v0.0.1"
			for _, chunk := range []string{"Image " + image + " Error pull access denied\nregistry-pri", "vate-value key-fragment-one key-fragment-two\n", content} {
				if _, err := io.WriteString(output, chunk); err != nil {
					t.Fatal(err)
				}
			}
			if err := output.Close(); err != nil {
				t.Fatal(err)
			}
			logs, _, err := s.ListNodeRunLogs(ctx, prepared.run.ID, prepared.node.ID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			var text strings.Builder
			for _, line := range logs {
				text.WriteString(line.Message)
			}
			if !strings.Contains(text.String(), image+" Error pull access denied") {
				t.Fatal("镜像地址或拉取错误被误脱敏")
			}
			for _, secret := range []string{"registry-private-value", "key-fragment-one", "key-fragment-two", "IMAGE_NAMESPACE=edo-demo"} {
				if strings.Contains(text.String(), secret) {
					t.Fatal("凭据、多行密钥或文件正文进入任务日志")
				}
			}
		})
	}
}

func TestComposeParameterFilesAllowHostFilesAndRequireExecutionAuthority(t *testing.T) {
	s, prepared, ctx := newComposeParameterTest(t, nil)
	if err := s.prepareComposeParameterFiles(ctx, prepared); err != nil || len(prepared.inputFiles) != 0 {
		t.Fatal("不存在于仓库的文件应留给执行主机检查", err)
	}
	probe := &composeDetectionCheckout{files: map[string]string{".env": "VALUE=canary\n"}}
	s.repositories = repository.NewService(s.db, s.secrets, nil, probe, 4, repository.WithCheckoutDirectory(t.TempDir()))
	if err := s.prepareComposeParameterFiles(context.Background(), prepared); err == nil || probe.calls != 0 {
		t.Fatal("缺少执行授权时读取了代码文件")
	}
}

func TestComposeParameterConfigurationPrefixUsesFrozenDirectory(t *testing.T) {
	prepared := &executionContext{snapshot: workflowSnapshot{ApplicationConfigDirectory: "~/edo/original/config"}, node: model.WorkflowNode{Config: model.WorkflowNodeConfig{DeploymentParameters: `--env-file "${EDO_APP_CONFIG_PATH}/existing.env"`}}}
	prefix, err := composeParameterConfigurationPrefix(prepared)
	if err != nil || !strings.Contains(prefix, "EDO_APP_CONFIG_PATH=") || !strings.Contains(prefix, "original") {
		t.Fatal("显式配置目录引用没有导出冻结目录", err)
	}
	prepared.node.Config.DeploymentParameters = "--env-file .env"
	if prefix, err := composeParameterConfigurationPrefix(prepared); err != nil || prefix != "" {
		t.Fatal("仓库相对路径被强加配置目录", err)
	}
}

func TestComposeParameterFileRejectsLinksDirectoriesOversizeAndInvalidText(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "file.env"), []byte("VALUE=ok\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file.env", filepath.Join(directory, "link.env")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(directory, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "large.env"), bytes.Repeat([]byte("a"), maximumComposeParameterFileBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "nul.env"), []byte("VALUE=\x00"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{"link.env", "linked/missing.env", ".", "large.env", "nul.env"} {
		if _, _, err := readComposeParameterFile(root, name); err == nil {
			t.Fatalf("接受不安全的参数文件: %s", name)
		}
	}
	s, prepared, ctx := newComposeParameterTest(t, map[string]string{".env": "INVALID KEY=private-parse-canary\n"})
	if err := s.prepareComposeParameterFiles(ctx, prepared); err == nil || strings.Contains(err.Error(), "private-parse-canary") {
		t.Fatal("无效文件未拒绝或错误包含正文")
	}
}

func TestComposeParameterFilesRealGitAndTransfer(t *testing.T) {
	s, prepared, ctx := newComposeParameterTest(t, nil)
	directory := t.TempDir()
	remote, err := git.PlainInit(directory, false)
	if err != nil {
		t.Fatal(err)
	}
	tree, _ := remote.Worktree()
	commit := func(value string) plumbing.Hash {
		t.Helper()
		if err := os.WriteFile(filepath.Join(directory, ".env"), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := tree.Add(".env"); err != nil {
			t.Fatal(err)
		}
		hash, err := tree.Commit("验收 ENV", &git.CommitOptions{Author: &object.Signature{Name: "EDO", Email: "test@example.invalid", When: time.Now()}})
		if err != nil {
			t.Fatal(err)
		}
		return hash
	}
	content := "IMAGE=alpine:3.22\nMESSAGE=original-canary\n"
	pinned := commit(content)
	head := commit("IMAGE=alpine:latest\nMESSAGE=changed-branch\n")
	if err := remote.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName("main"), head)); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&model.GitRepository{}).Where("uuid = ?", prepared.component.RepositoryID).Update("clone_url", directory).Error; err != nil {
		t.Fatal(err)
	}
	s.repositories = repository.NewService(s.db, s.secrets, credential.NewService(s.db, s.secrets), repository.NewGitClient(config.Git{Timeout: 10 * time.Second}), 4, repository.WithCheckoutDirectory(t.TempDir()))
	prepared.run.CommitSHA = pinned.String()
	if err := s.prepareComposeParameterFiles(ctx, prepared); err != nil {
		t.Fatal(err)
	}
	if len(prepared.inputFiles) != 1 || prepared.composeEnvValues["MESSAGE"] != "original-canary" {
		t.Fatal("没有固定本次 Commit 的文件")
	}
	capability := model.HostCapability{HostID: model.BuiltinLocalHostID, Kind: model.HostCapabilityLocalExec, Status: model.HostCapabilityReady}
	if err := s.db.Where("host_id = ? AND kind = ?", capability.HostID, capability.Kind).Assign(map[string]any{"status": model.HostCapabilityReady}).FirstOrCreate(&capability).Error; err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	runner := sshdeploy.NewService(s.db, s.secrets, config.Runtime{})
	result, err := runner.RunHostDeploymentScript(ctx, sshdeploy.Input{HostID: model.BuiltinLocalHostID, WorkingDirectory: prepared.target.WorkingDirectory, Script: "cat .env\n", Files: prepared.inputFiles, Timeout: 30 * time.Second, Stdout: &output})
	if err != nil || result.ExitCode != 0 || output.String() != content {
		t.Fatal("真实文件传输后不能从执行工作目录读取固定 Commit 内容", err)
	}
	info, err := os.Stat(filepath.Join(prepared.target.WorkingDirectory, ".env"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("仓库 ENV 文件权限不是 0600", err)
	}
	if os.Getenv("EDO_TEST_DOCKER_INTEGRATION") == "1" {
		yaml := "services:\n  api:\n    image: ${IMAGE}\n    environment:\n      MESSAGE: ${MESSAGE}\n"
		if err := os.WriteFile(filepath.Join(prepared.target.WorkingDirectory, "compose.yaml"), []byte(yaml), 0600); err != nil {
			t.Fatal(err)
		}
		arguments, err := configurationComposeArguments(prepared.node.Config)
		if err != nil {
			t.Fatal(err)
		}
		command := exec.Command("sh", "-se")
		command.Dir = prepared.target.WorkingDirectory
		command.Stdin = strings.NewReader("docker compose" + arguments + " -f compose.yaml config --format json")
		body, err := command.CombinedOutput()
		if err != nil || !bytes.Contains(body, []byte("alpine:3.22")) || !bytes.Contains(body, []byte("original-canary")) {
			t.Fatal("真实 Compose 无法读取工作目录中的仓库 ENV 文件", err)
		}
	}
}
