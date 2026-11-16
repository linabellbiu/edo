package pipeline

import (
	"context"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"edo/internal/config"
	"edo/internal/deployment"
	"edo/internal/kube"
	"edo/internal/model"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// 使用真实 TLS 集群连接；请求进入后一直等待，直到测试明确释放。
func blockWorkflowCluster(t *testing.T, service *Service, clusterID string) (*atomic.Int32, <-chan struct{}, chan<- struct{}) {
	t.Helper()
	var calls atomic.Int32
	entered, release := make(chan struct{}, 8), make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-r.Context().Done():
			return
		case <-release:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Status","reason":"Forbidden","code":403}`)
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	configureWorkflowClusterServer(t, service, clusterID, server)
	return &calls, entered, release
}

func configureWorkflowClusterServer(t *testing.T, service *Service, clusterID string, server *httptest.Server) {
	t.Helper()
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	encoded, err := clientcmd.Write(clientcmdapi.Config{
		Clusters:       map[string]*clientcmdapi.Cluster{"blocked": {Server: server.URL, CertificateAuthorityData: certificate}},
		AuthInfos:      map[string]*clientcmdapi.AuthInfo{"blocked": {Token: "isolated-test-token"}},
		Contexts:       map[string]*clientcmdapi.Context{"blocked": {Cluster: "blocked", AuthInfo: "blocked"}},
		CurrentContext: "blocked",
	})
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := service.secrets.Encrypt(string(encoded), []byte("kubernetes_cluster:"+clusterID+":kubeconfig"))
	if err != nil {
		t.Fatal(err)
	}
	if err = service.db.Model(&model.KubernetesCluster{}).Where("uuid = ?", clusterID).
		Updates(map[string]any{"api_server": server.URL, "kubeconfig_ciphertext": ciphertext}).Error; err != nil {
		t.Fatal(err)
	}
	connection := kube.NewService(service.db, service.secrets, config.Runtime{ConnectTimeout: time.Second, RequestTimeout: 5 * time.Second})
	service.deployments = deployment.NewService(service.db, service.docker, connection, nil, nil, "", service.logger)
}

func TestWorkflowReadsDoNotWaitForUnavailableCluster(t *testing.T) {
	s, app, build, _, source, stages := newReusableKubernetesOverrideFixture(t)
	ctx := deliveryTestContext()
	saved, err := s.SaveWorkflow(ctx, app.ID, "admin", WorkflowInput{Name: app.Workflow.Name, Revision: app.Workflow.Revision, SchemaVersion: model.WorkflowSchemaVersion, Source: source, Stages: stages, Activate: true})
	if err != nil {
		t.Fatal(err)
	}
	// 已保存方案失效仍需在读取时呈现，不能用跳过全部校验换取快速响应。
	if err = s.db.Model(&model.BuildPlan{}).Where("uuid = ?", build.ID).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	calls, _, _ := blockWorkflowCluster(t, s, "override-cluster")
	for _, read := range []struct {
		name string
		call func(context.Context) (*WorkflowResult, error)
	}{
		{"application", func(ctx context.Context) (*WorkflowResult, error) {
			return s.GetApplicationWorkflow(ctx, app.ID, saved.Workflow.ID)
		}},
		{"single-workflow", func(ctx context.Context) (*WorkflowResult, error) { return s.GetWorkflow(ctx, app.ID) }},
	} {
		t.Run(read.name, func(t *testing.T) {
			bounded, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			result, err := read.call(bounded)
			if err != nil || bounded.Err() != nil || result == nil || result.Workflow.ID != saved.Workflow.ID {
				t.Fatalf("流水线读取等待了集群现场: result=%+v err=%v context=%v", result, err, bounded.Err())
			}
			if result.Valid || !hasWorkflowIssue(result.Issues, "missing_build_plan") {
				t.Fatalf("读取丢失实际任务配置异常: %+v", result.Issues)
			}
			if calls.Load() != 0 || hasWorkflowIssue(result.Issues, "kubernetes_template_target_invalid") {
				t.Fatalf("读取仍调用远程集群或猜测其状态: calls=%d issues=%+v", calls.Load(), result.Issues)
			}
		})
	}
}

func TestWorkflowValidationAndActivationKeepRemoteTargetChecks(t *testing.T) {
	for _, operation := range []string{"validate", "activate"} {
		t.Run(operation, func(t *testing.T) {
			s, app, _, _, source, stages := newReusableKubernetesOverrideFixture(t)
			ctx := deliveryTestContext()
			input := WorkflowInput{Name: app.Workflow.Name, Revision: app.Workflow.Revision, SchemaVersion: model.WorkflowSchemaVersion, Source: source, Stages: stages, Activate: true}
			saved, err := s.SaveWorkflow(ctx, app.ID, "admin", input)
			if err != nil {
				t.Fatal(err)
			}
			app, err = s.FindApplication(ctx, app.ID)
			if err != nil {
				t.Fatal(err)
			}
			input.Revision = saved.Workflow.Revision
			calls, entered, release := blockWorkflowCluster(t, s, "override-cluster")
			done := make(chan struct{})
			var result *WorkflowResult
			var operationErr error
			go func() {
				defer close(done)
				switch operation {
				case "validate":
					result, operationErr = s.ValidateApplicationWorkflow(ctx, app.ID, saved.Workflow.ID, input)
				case "activate":
					result, operationErr = s.SaveApplicationWorkflow(ctx, app.ID, saved.Workflow.ID, "admin", input)
				}
			}()
			select {
			case <-entered:
			case <-done:
				t.Fatalf("%s 未复查远程目标: result=%+v err=%v", operation, result, operationErr)
			case <-time.After(3 * time.Second):
				t.Fatal("未观察到远程目标检查")
			}
			select {
			case <-done:
				t.Fatal("远程目标未确认却已返回")
			default:
			}
			release <- struct{}{}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("集群返回后操作未结束")
			}
			if calls.Load() != 1 {
				t.Fatalf("远程检查次数异常: %d", calls.Load())
			}
			if result == nil || result.Valid || !hasWorkflowIssue(result.Issues, "kubernetes_template_target_invalid") {
				t.Fatalf("未保留远程目标异常: %+v %v", result, operationErr)
			}
			if operation == "activate" && !errors.Is(operationErr, ErrInvalidWorkflow) {
				t.Fatalf("不满足远程检查仍能启用: %v", operationErr)
			}
		})
	}
}

func TestWorkflowDraftAndTaskSaveDoNotProbeOtherTargets(t *testing.T) {
	for _, taskID := range []string{"", "build"} {
		t.Run("task="+taskID, func(t *testing.T) {
			s, app, _, _, source, stages := newReusableKubernetesOverrideFixture(t)
			ctx := deliveryTestContext()
			saved, err := s.SaveWorkflow(ctx, app.ID, "admin", WorkflowInput{Name: app.Workflow.Name, Revision: app.Workflow.Revision, SchemaVersion: model.WorkflowSchemaVersion, Source: source, Stages: stages, Activate: true})
			if err != nil {
				t.Fatal(err)
			}
			calls, _, _ := blockWorkflowCluster(t, s, "override-cluster")
			bounded, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			input := WorkflowInput{Name: saved.Workflow.Name, Revision: saved.Workflow.Revision, SchemaVersion: model.WorkflowSchemaVersion, Source: source, Stages: cloneWorkflowStages(stages), SaveOnly: true, TaskID: taskID}
			input.Stages[0].Tasks[0].Name = "已修改构建任务"
			result, err := s.SaveApplicationWorkflow(bounded, app.ID, saved.Workflow.ID, "admin", input)
			if err != nil || bounded.Err() != nil || result == nil || result.Workflow.Revision != saved.Workflow.Revision+1 || !result.Workflow.IsActive {
				t.Fatalf("独立保存等待其他任务或改变启用状态: result=%+v err=%v context=%v", result, err, bounded.Err())
			}
			if calls.Load() != 0 || result.Workflow.Stages[0].Tasks[0].Name != input.Stages[0].Tasks[0].Name {
				t.Fatalf("配置未保存或触发了远程目标探测: calls=%d", calls.Load())
			}
		})
	}
}

func TestWorkflowSnapshotDefersTemplateConflictToExecution(t *testing.T) {
	s, app, _, _, source, stages := newReusableKubernetesOverrideFixture(t)
	ctx := deliveryTestContext()
	if _, err := s.SaveWorkflow(ctx, app.ID, "admin", WorkflowInput{Name: app.Workflow.Name, Revision: app.Workflow.Revision, SchemaVersion: model.WorkflowSchemaVersion, Source: source, Stages: stages, Activate: true}); err != nil {
		t.Fatal(err)
	}
	app, err := s.FindApplication(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	var nodes, objects, writes atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			writes.Add(1)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/api/v1/nodes":
			nodes.Add(1)
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"NodeList","items":[{"metadata":{"name":"platform"},"status":{"nodeInfo":{"architecture":"amd64","operatingSystem":"linux"}}}]}`)
		case "/api/v1/namespaces/default":
			objects.Add(1)
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"default","uid":"namespace","resourceVersion":"1"}}`)
		case "/apis/apps/v1/namespaces/default/deployments/api-a":
			objects.Add(1)
			_, _ = io.WriteString(w, `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api-a","namespace":"default","uid":"foreign","resourceVersion":"1"}}`)
		default:
			objects.Add(1)
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"Status","reason":"NotFound","code":404}`)
		}
	}))
	t.Cleanup(server.Close)
	configureWorkflowClusterServer(t, s, "override-cluster", server)
	run, err := s.newResolvedWorkflowRun(ctx, app, app.Workflow, app.Workflow.Source, "manual", "refs/heads/main", strings.Repeat("a", 40), "admin", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := parseWorkflowSnapshot(run)
	if err != nil || snapshot.TaskFailures["deploy"] != "" || objects.Load() != 0 || nodes.Load() != 1 {
		t.Fatalf("创建运行仍预读模板现场，或丢失真实平台准备: err=%v objects=%d nodes=%d", err, objects.Load(), nodes.Load())
	}
	if err = s.db.Create(run).Error; err != nil {
		t.Fatal(err)
	}
	frozen := snapshot.DeploymentTargets["deploy"]
	target := model.DeploymentTarget{ID: frozen.ID, ProjectID: run.ProjectID, DepartmentID: run.DepartmentID, Name: frozen.Name, Platform: frozen.Platform, RuntimeID: frozen.RuntimeID, EnvironmentID: frozen.EnvironmentID, Namespace: frozen.Namespace, WorkloadName: frozen.WorkloadName, ContainerName: frozen.ContainerName, RolloutTimeout: frozen.RolloutTimeout}
	// 调用 Worker 使用的真实部署入口，核验冲突在任何写入前仍然阻断。
	record, err := s.deployments.RequestSnapshotAndRun(ctx, "admin", target, deployment.RequestInput{TargetID: target.ID, ApplicationID: app.ID, ApplicationName: app.Name, PipelineRunID: run.ID, WorkflowNodeID: "deploy", DeploymentPlanID: snapshot.DeploymentPlans["deploy"].ID, PlanKind: model.DeploymentPlanKubernetes, Image: "registry.example.test/api@sha256:" + strings.Repeat("a", 64)})
	if !errors.Is(err, kube.ErrDeploymentTemplateConflict) || record == nil || record.Status != model.DeploymentFailed || record.ErrorCode != "kubernetes_target_conflict" || objects.Load() == 0 || writes.Load() != 0 {
		t.Fatalf("延后现场检查放宽了执行边界: record=%+v err=%v objects=%d writes=%d", record, err, objects.Load(), writes.Load())
	}
}
