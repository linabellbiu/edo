package dockerengine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"edo/internal/cicd"
	"edo/internal/config"
	"edo/internal/model"
)

func TestDeliverySandboxReportsSecretsAndCacheIntegration(t *testing.T) {
	if os.Getenv("EDO_TEST_DOCKER_INTEGRATION") != "1" {
		t.Skip("未启用真实 Docker 集成测试")
	}
	service := NewService(nil, nil, config.Runtime{ConnectTimeout: 10 * time.Second, RequestTimeout: 30 * time.Second})
	source := t.TempDir()
	cache := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	var output bytes.Buffer
	result, err := service.RunScriptContainer(ctx, ScriptContainerInput{Image: "alpine:3.22", Script: "test \"$(cat \"$FILE_SECRET\")\" = private-file-value\nmkdir -p node_modules\nprintf cached > node_modules/cache.txt\nprintf '<testsuite><testcase name=\"one\"/></testsuite>' > junit.xml\n", SourceDirectory: source, WorkingDirectory: ".", FileVariables: map[string]string{"FILE_SECRET": "private-file-value"}, ReportPaths: []string{"junit.xml"}, CachePaths: []string{"node_modules"}, CacheOutputDirectory: cache, Timeout: time.Minute, Stdout: &output, Stderr: &output})
	if err != nil {
		t.Fatalf("实际隔离容器失败: %v %s", err, output.String())
	}
	if _, err = cicd.ParseReport("junit", result.Reports["junit.xml"]); err != nil {
		t.Fatal("未回收检查报告")
	}
	if data, e := os.ReadFile(filepath.Join(result.CachePaths["node_modules"], "cache.txt")); e != nil || string(data) != "cached" {
		t.Fatal("未回收依赖缓存")
	}
	result, err = service.RunScriptContainer(ctx, ScriptContainerInput{Image: "alpine:3.22", Script: "printf '<testsuite><testcase name=\"bad\"><failure message=\"failed\"/></testcase></testsuite>' > failed.xml\nexit 7\n", SourceDirectory: source, WorkingDirectory: ".", ReportPaths: []string{"failed.xml"}, Timeout: time.Minute})
	var exit *ScriptContainerExitError
	if !errors.As(err, &exit) || exit.ExitCode != 7 {
		t.Fatal("失败命令没有保留退出码")
	}
	report, e := cicd.ParseReport("junit", result.Reports["failed.xml"])
	if e != nil || report.Summary.Failed != 1 {
		t.Fatal("失败命令的报告没有被收集")
	}
}
func TestDeliveryCheckToolsIntegration(t *testing.T) {
	if os.Getenv("EDO_TEST_DELIVERY_TOOLS") != "1" {
		t.Skip("未启用真实检查工具集成测试")
	}
	service := NewService(nil, nil, config.Runtime{ConnectTimeout: 15 * time.Second, RequestTimeout: time.Minute})
	for _, tool := range []string{"sqlfluff", "gitleaks", "trivy"} {
		t.Run(tool, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
			defer cancel()
			source := t.TempDir()
			if err := os.Mkdir(filepath.Join(source, "checks"), 0700); err != nil {
				t.Fatal(err)
			}
			fixtures := map[string]string{"query.sql": "select * from users where id=1;\n", "config.env": "GITHUB_TOKEN=ghp_R8n3kL7Xa2Qc9Nv4Ys6Fb1Wd5Hj0Pm8Te3Zu\n", "Dockerfile": "FROM alpine:3.22\nUSER root\n"}
			for name, value := range fixtures {
				if err := os.WriteFile(filepath.Join(source, "checks", name), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			node := model.WorkflowNode{Type: model.WorkflowNodeShell, Config: model.WorkflowNodeConfig{Check: &model.WorkflowCheckConfig{Tool: tool, Dialect: "mysql", Path: "checks"}, TimeoutSeconds: 240}}
			if err := cicd.PrepareCheck(&node); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			result, err := service.RunScriptContainer(ctx, ScriptContainerInput{Image: node.Config.RuntimeImage, Script: node.Config.Script, SourceDirectory: source, WorkingDirectory: ".", ReportPaths: node.Config.Reports[0].Paths, Timeout: 4 * time.Minute, Stdout: &output, Stderr: &output})
			var exit *ScriptContainerExitError
			if err != nil && !errors.As(err, &exit) {
				t.Fatalf("检查工具运行失败: %v %s", err, output.String())
			}
			report, parseErr := cicd.ParseReport(node.Config.Reports[0].Format, result.Reports[node.Config.Reports[0].Paths[0]])
			if parseErr != nil {
				t.Fatalf("检查工具没有生成标准报告: %v %s", parseErr, output.String())
			}
			if tool != "trivy" && report.Summary.Issues == 0 {
				t.Fatalf("问题样本未被检查工具发现: %s", output.String())
			}
		})
	}
}
