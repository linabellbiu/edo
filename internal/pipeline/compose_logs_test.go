package pipeline

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"edo/internal/config"
	"edo/internal/configfile"
	"edo/internal/database"
	"edo/internal/dockerengine"
	"edo/internal/model"
	"edo/internal/sshdeploy"
)

func executeComposeLogFailure(t *testing.T, directory, content string, settings model.WorkflowNodeConfig) (string, sshdeploy.Result, error) {
	t.Helper()
	node := model.WorkflowNode{ID: "compose-log-task", Name: "日志部署", Type: model.WorkflowNodeDeploy, Config: settings}
	snapshot := workflowSnapshot{SchemaVersion: model.WorkflowSchemaVersion, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "deploy", Tasks: []model.WorkflowNode{node}}}}
	s, db, _, run := newBuildExecutorTestRun(t, node, snapshot, nil)
	capability := model.HostCapability{HostID: model.BuiltinLocalHostID, Kind: model.HostCapabilityLocalExec, Status: model.HostCapabilityReady}
	if err := db.Where("host_id = ? AND kind = ?", capability.HostID, capability.Kind).Assign(map[string]any{"status": model.HostCapabilityReady}).FirstOrCreate(&capability).Error; err != nil {
		t.Fatal(err)
	}
	prepared := &executionContext{node: node, deploymentPlan: model.DeploymentPlan{ComposeYAML: content}, variableRedactions: []string{"first-private-key-line\nsecond-private-key-line"}, variables: map[string]string{"DEPLOY_PASSWORD": "deployment-private-value"}}
	prepared.variableRedactions = append(prepared.variableRedactions, composeRegistryLogRedactions(dockerengine.RegistryAuth{Username: "registry-user", Credential: "registry-private-value"})...)
	ctx := withPipelineNodeLog(t.Context(), node.ID, node.Name)
	output := s.newExecutionLogWriter(ctx, run.ID, "deploy", "Docker Compose", composeExecutionLogRedactions(prepared)...)
	script, err := composeStackScript(content, settings, directory, 90, "")
	if err != nil {
		t.Fatal(err)
	}
	runner := sshdeploy.NewService(db, s.secrets, config.Runtime{})
	result, executionErr := runner.RunHostDeploymentScript(ctx, sshdeploy.Input{HostID: model.BuiltinLocalHostID, WorkingDirectory: directory, Script: script, Timeout: 30 * time.Second, Environment: map[string]string{"EDO_DEPLOYMENT_ID": "failure-detail", "EDO_DEPLOYMENT_TARGET_ID": "log-target", "EDO_COMMIT_SHA": strings.Repeat("a", 40)}, Stdout: output, Stderr: output})
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	logs, _, err := s.ListNodeRunLogs(ctx, run.ID, node.ID, 0, 200)
	if err != nil || len(logs) == 0 {
		t.Fatal("失败详情未进入任务持久日志", err)
	}
	var text strings.Builder
	for _, line := range logs {
		if line.NodeID != node.ID || line.Stage != "deploy" {
			t.Fatal("失败日志归属错误")
		}
		text.WriteString(line.Message)
	}
	return text.String(), result, executionErr
}

func TestComposeFailureDetailsReachTaskLogsWithoutChangingFailureFlow(t *testing.T) {
	for _, tc := range []struct {
		phase, diagnostic, summary string
	}{
		{"success", "Compose 部署与镜像验收完成", "开始启动 Compose 服务"},
		{"config", "invalid compose configuration", "EDO Compose 配置或变量校验失败（退出码 17）"},
		{"pull", "pull access denied for registry.example.com/web:fixture", "镜像拉取失败，请检查镜像及主机网络（退出码 18）"},
		{"large", "pull access denied for registry.example.com/web:fixture", "仅显示最后 64 KiB"},
		{"startup", "container failed to start", "Compose 启动失败（退出码 19）"},
		{"rollback", "previous image restore failed", "恢复上一镜像也失败"},
		{"health", "curl: (22) health endpoint returned 500", "健康检查地址访问失败（退出码 22）"},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			directory, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if tc.phase == "success" {
				directory = filepath.Join(directory, "path with 'quotes'")
				if err := os.Mkdir(directory, 0700); err != nil {
					t.Fatal(err)
				}
			}
			bin := filepath.Join(directory, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			fixture := `#!/bin/sh
set -eu
phase=__PHASE__
trace=__TRACE__
up=__UP__
failed=__FAILED__
printf '%s\n' "$*" >>"$trace"
if [ "$1" = inspect ]; then
 case "$3" in *RestartCount*) echo 0;; *State.Status*) echo 'running 0 ';; *) echo 'sha256:__IMAGE_ID__';; esac
 exit 0
fi
if [ "$1" = image ]; then echo 'sha256:__IMAGE_ID__'; exit 0; fi
for argument in "$@"; do
 case "$argument" in
 config) if [ "$phase" = config ]; then echo 'invalid compose configuration' >&2; exit 17; fi; exit 0;;
 ps) if [ -f "$up" ]; then echo test-container; fi; exit 0;;
 pull)
  case "$phase" in
   pull|large)
    if [ "$phase" = large ]; then printf '%s\n' __NOISE__ >&2; fi
    printf '%s\n' 'pull access denied for registry.example.com/web:fixture' 'deployment-private-value registry-private-value inline-private-value __BASIC__' 'first-private-key-line' 'second-private-key-line' >&2
    exit 18;;
  esac
  exit 0;;
 up)
  case "$phase" in
   startup) echo 'container failed to start' >&2; exit 19;;
   rollback)
    if [ -f "$failed" ]; then echo 'previous image restore failed' >&2; exit 29; fi
    touch "$failed"; echo 'container failed to start' >&2; exit 19;;
  esac
  touch "$up"; exit 0;;
 esac
done
exit 99
`
			basic := base64.StdEncoding.EncodeToString([]byte("registry-user:registry-private-value"))
			fixture = strings.NewReplacer("__PHASE__", configfile.Quote(tc.phase), "__TRACE__", configfile.Quote(filepath.Join(directory, "trace")), "__UP__", configfile.Quote(filepath.Join(directory, "up")), "__FAILED__", configfile.Quote(filepath.Join(directory, "failed")), "__IMAGE_ID__", strings.Repeat("b", 64), "__NOISE__", configfile.Quote(strings.Repeat("discarded-private-noise", 4000)), "__BASIC__", basic).Replace(fixture)
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fixture), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, "curl"), []byte("#!/bin/sh\necho 'curl: (22) health endpoint returned 500' >&2\nexit 22\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			settings := model.WorkflowNodeConfig{Compose: &model.ComposeDeploymentSettings{ProjectName: "failure-logs", TagVariable: "${EDO_IMAGE_TAG}"}}
			if tc.phase == "health" {
				settings.Compose.HealthURL = "http://127.0.0.1/health"
			}
			content := "services:\n  web:\n    image: registry.example.com/web:fixture\n    environment:\n      DB_PASSWORD: inline-private-value\n"
			if tc.phase == "rollback" {
				state := filepath.Join(directory, ".edo", "compose", "failure-logs")
				if err := os.MkdirAll(state, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(state, "current.yml"), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			text, result, err := executeComposeLogFailure(t, directory, content, settings)
			failed := tc.phase != "success"
			if failed && (!errors.Is(err, sshdeploy.ErrCommandFailed) || result.ExitCode != 1) || !failed && (err != nil || result.ExitCode != 0) || !strings.Contains(text, tc.diagnostic) || !strings.Contains(text, tc.summary) {
				t.Fatalf("执行失败详情或原失败状态未保留: result=%+v err=%v logs=%s", result, err, text)
			}
			if !strings.Contains(text, "执行命令： 'docker' 'compose' '--ansi' 'never' '--project-name' 'failure-logs'") || !strings.Contains(text, "'--project-directory' "+composeShellLiteral(directory)) || !strings.Contains(text, "'--file' '.edo/compose/failure-logs/failure-detail/compose.yml' 'config' '--quiet'") {
				t.Fatal("日志没有显示实际完整的配置校验命令", text)
			}
			if tc.phase == "success" && (!strings.Contains(text, "'up' '-d' '--no-build' '--pull' 'never' '--wait' '--wait-timeout' '60'") || !strings.Contains(text, "生成 Compose 文件：")) {
				t.Fatal("实际部署命令或生成文件未进入日志", text)
			}
			for _, secret := range []string{"deployment-private-value", "registry-private-value", "inline-private-value", "first-private-key-line", "second-private-key-line", basic, "discarded-private-noise"} {
				if strings.Contains(text, secret) {
					t.Fatal("错误详情包含敏感内容或已截断的早期长行")
				}
			}
			if len(text) > 70*1024 {
				t.Fatal("失败详情超过单次回传边界")
			}
			trace, err := os.ReadFile(filepath.Join(directory, "trace"))
			if err != nil {
				t.Fatal(err)
			}
			if (tc.phase == "config" || tc.phase == "pull" || tc.phase == "large") && strings.Contains(string(trace), " up ") {
				t.Fatal("准备失败后仍执行了 up")
			}
		})
	}
}

func TestComposeFailureDetailsFromRealComposeCLI(t *testing.T) {
	if os.Getenv("EDO_TEST_DOCKER_INTEGRATION") != "1" {
		t.Skip("未启用真实 Docker Compose CLI 验证")
	}
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings := model.WorkflowNodeConfig{DeploymentParameters: "--env-file missing.env", Compose: &model.ComposeDeploymentSettings{ProjectName: "failure-logs", TagVariable: "${EDO_IMAGE_TAG}"}}
	text, result, err := executeComposeLogFailure(t, directory, "services:\n  web:\n    image: alpine:3.22\n", settings)
	if !errors.Is(err, sshdeploy.ErrCommandFailed) || result.ExitCode != 1 || !strings.Contains(text, "missing.env") || !strings.Contains(text, "Compose 执行错误详情") {
		t.Fatal("真实 Compose 配置错误未进入任务日志", result, err, text)
	}
	if _, err := os.Stat(filepath.Join(directory, ".edo", "compose", "failure-logs", "current.yml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("配置校验失败仍替换了当前 Compose")
	}
}

func TestComposeLogRedactsListEnvironmentAndMultilineFragments(t *testing.T) {
	prepared := &executionContext{deploymentPlan: model.DeploymentPlan{ComposeYAML: "services:\n  web:\n    image: alpine:3.22\n    environment: [API_TOKEN=list-private-value, DISPLAY_NAME=visible]\n"}, variableRedactions: []string{"line-one\r\nline-two\r\n"}}
	values := composeExecutionLogRedactions(prepared)
	text := string(redactLogBytes([]byte("list-private-value line-two visible"), normalizeLogRedactions(values)))
	if strings.Contains(text, "list-private-value") || strings.Contains(text, "line-two") || !strings.Contains(text, "visible") {
		t.Fatal("列表环境变量或截断后的多行片段未正确脱敏")
	}
}

func TestComposeImagePreparationFailureDetailsReachTaskLog(t *testing.T) {
	node := model.WorkflowNode{ID: "image-log-task", Type: model.WorkflowNodeDeploy, Config: model.WorkflowNodeConfig{DeploymentRegistryID: "registry-log-test", Compose: &model.ComposeDeploymentSettings{}}}
	snapshot := workflowSnapshot{SchemaVersion: model.WorkflowSchemaVersion, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "deploy", Tasks: []model.WorkflowNode{node}}}}
	s, db, _, run := newBuildExecutorTestRun(t, node, snapshot, nil)
	registry := model.ImageRegistry{ID: node.Config.DeploymentRegistryID, ProjectID: database.DefaultProjectID(database.DefaultDepartmentID), Endpoint: "https://registry.example.com", IsActive: true}
	if err := db.Create(&registry).Error; err != nil {
		t.Fatal(err)
	}
	prepared := &executionContext{run: run, node: node, variables: map[string]string{"SECRET": "PrivateImageValue"}}
	_, err := s.resolveConfiguredComposeImages(t.Context(), prepared, "services:\n  web:\n    image: registry.example.com/${SECRET}:v1\n")
	if !errors.Is(err, ErrDeploymentInputs) {
		t.Fatal("镜像校验错误分类发生变化", err)
	}
	logs, _, err := s.ListNodeRunLogs(t.Context(), run.ID, node.ID, 0, 100)
	if err != nil || len(logs) != 1 || logs[0].Level != "error" || !strings.Contains(logs[0].Message, "must be lowercase") || strings.Contains(logs[0].Message, "PrivateImageValue") {
		t.Fatal("镜像准备阶段缺少安全的任务错误详情", logs, err)
	}
}
