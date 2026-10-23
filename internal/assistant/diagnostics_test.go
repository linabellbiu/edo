package assistant

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/deployment"
	"edo/internal/dockerengine"
	"edo/internal/kube"
	"edo/internal/logging"
	"edo/internal/model"
	"edo/internal/systemmetrics"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"gorm.io/datatypes"
	corev1 "k8s.io/api/core/v1"
)

func diagnosticTool(t *testing.T, s *Service, user *model.User, name string) tool.InvokableTool {
	t.Helper()
	for _, candidate := range s.tools(user, "diagnostic-test", func(Event) {}) {
		info, _ := candidate.Info(context.Background())
		if info.Name == name {
			return candidate.(tool.InvokableTool)
		}
	}
	t.Fatalf("排障工具未注册: %s", name)
	return nil
}

type diagnosticDeployment struct{ calls int }

func (d *diagnosticDeployment) RuntimeState(context.Context, string) (*deployment.RuntimeState, error) {
	d.calls++
	return &deployment.RuntimeState{ResourceID: "container-full-id", State: "exited", Count: 1}, nil
}

type diagnosticDocker struct {
	inspectCalls, logCalls int
	inspectError           error
	logs                   string
}

func (d *diagnosticDocker) InspectManagedContainer(_ context.Context, _, container, target, deployment, service string) (*dockerengine.ContainerDiagnostic, error) {
	d.inspectCalls++
	if container != "container-full-id" || target == "" || deployment == "" {
		return nil, errors.New("测试未使用部署绑定的实例")
	}
	return &dockerengine.ContainerDiagnostic{ID: container, State: "exited", OOMKilled: true, ExitCode: 137}, d.inspectError
}
func (d *diagnosticDocker) ContainerLogs(_ context.Context, _, container string, options dockerengine.ContainerLogOptions) (*dockerengine.ContainerLogStream, error) {
	d.logCalls++
	if options.Tail != 100 || options.Follow || !options.Timestamps || container != "container-full-id" {
		return nil, errors.New("测试日志请求范围错误")
	}
	return &dockerengine.ContainerLogStream{ReadCloser: io.NopCloser(strings.NewReader(d.logs)), TTY: true}, nil
}

func diagnosticDeploymentFixture(t *testing.T) (*Service, *model.User, context.Context, model.DeploymentRecord, *diagnosticDeployment, *diagnosticDocker) {
	t.Helper()
	s, db, user, ctx := assistantFixture(t)
	project := database.DefaultProjectID(user.DepartmentID)
	app := model.Application{ID: newID(), Name: "checkout", RepositoryID: newID(), DepartmentID: user.DepartmentID, ProjectID: project}
	if err := db.WithContext(ctx).Create(&model.GitRepository{ID: app.RepositoryID, Name: "checkout-repo", CloneURL: "https://example.invalid/checkout.git", DepartmentID: user.DepartmentID, ProjectID: project}).Error; err != nil {
		t.Fatal(err)
	}
	endpoint := model.DockerEndpoint{ID: newID(), Name: "runtime", Host: "unix:///unused.sock", DepartmentID: user.DepartmentID, ProjectID: project, IsActive: true}
	row := model.DeploymentRecord{ID: newID(), ApplicationID: app.ID, TargetID: newID(), RuntimeID: endpoint.ID, Platform: model.DeploymentDocker, DeploymentPlanKind: model.DeploymentPlanDocker, Status: model.DeploymentSucceeded, DepartmentID: user.DepartmentID, ProjectID: project}
	for _, value := range []any{&app, &endpoint, &row} {
		if err := db.WithContext(ctx).Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	runtime := &diagnosticDeployment{}
	docker := &diagnosticDocker{logs: "2026-10-24T01:00:00Z fatal: out of memory\npassword=private-log-value"}
	s.deps.Deployments, s.deps.Docker = runtime, docker
	return s, user, ctx, row, runtime, docker
}

func TestDiagnosticDockerLogsStayScopedAndRedacted(t *testing.T) {
	s, user, ctx, row, runtime, docker := diagnosticDeploymentFixture(t)
	output, err := diagnosticTool(t, s, user, "deployment_logs").InvokableRun(ctx, `{"deployment_id":"`+row.ID+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"out of memory", "oom_killed", "collected_at", "read_only"} {
		if !strings.Contains(output, want) {
			t.Fatalf("排障证据缺失 %s: %s", want, output)
		}
	}
	if strings.Contains(output, "private-log-value") || runtime.calls != 1 || docker.logCalls != 1 {
		t.Fatal("日志泄露或读取次数错误")
	}
	output, err = diagnosticTool(t, s, user, "project_resources").InvokableRun(ctx, `{"resource":"applications","query":"`+row.ApplicationID+`"}`)
	if err != nil || !strings.Contains(output, row.ApplicationID) {
		t.Fatalf("按ID定位应用失败: %s %v", output, err)
	}
	docker.inspectError = dockerengine.ErrDiagnosticIdentity
	if _, err := diagnosticTool(t, s, user, "deployment_logs").InvokableRun(ctx, `{"deployment_id":"`+row.ID+`"}`); !errors.Is(err, ErrDiagnosticRead) || docker.logCalls != 1 {
		t.Fatal("身份不符时仍读取容器日志")
	}
	row.ProjectID = newID()
	if err := s.deps.Database.Model(&row).Select("project_id").Updates(&row).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := diagnosticTool(t, s, user, "deployment_logs").InvokableRun(ctx, `{"deployment_id":"`+row.ID+`"}`); !errors.Is(err, ErrNotFound) || runtime.calls != 2 {
		t.Fatalf("跨项目调用了运行时: %v", err)
	}
}

func TestDiagnosticScriptsAndExpiredSessionsNeverReadRuntime(t *testing.T) {
	s, user, ctx, row, runtime, _ := diagnosticDeploymentFixture(t)
	if err := s.deps.Database.Model(&row).Update("deployment_plan_kind", model.DeploymentPlanScript).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := diagnosticTool(t, s, user, "deployment_diagnostics").InvokableRun(ctx, `{"deployment_id":"`+row.ID+`"}`); !errors.Is(err, ErrForbidden) || runtime.calls != 0 {
		t.Fatal("排障调用了脚本状态检查")
	}
	ctx = WithSessionAuthorization(ctx, func() bool { return false })
	if _, err := diagnosticTool(t, s, user, "project_tasks").InvokableRun(ctx, `{}`); !errors.Is(err, ErrForbidden) {
		t.Fatal("过期会话仍可读取")
	}
}

func TestDiagnosticResourceSearchAndTasksDoNotExposePayload(t *testing.T) {
	s, db, user, ctx := assistantFixture(t)
	project := database.DefaultProjectID(user.DepartmentID)
	for _, app := range []model.Application{
		{ID: newID(), Name: "checkout", ProjectID: project, DepartmentID: user.DepartmentID},
		{ID: newID(), Name: "other-project", ProjectID: newID(), DepartmentID: user.DepartmentID},
	} {
		app.RepositoryID = newID()
		if err := db.Create(&model.GitRepository{ID: app.RepositoryID, Name: app.Name + "-repo", CloneURL: "https://example.invalid/repo.git", DepartmentID: app.DepartmentID, ProjectID: app.ProjectID}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&app).Error; err != nil {
			t.Fatal(err)
		}
	}
	job := model.Job{ID: newID(), Kind: "build", Status: model.JobFailed, ProjectID: project, DepartmentID: user.DepartmentID, Subject: "private-subject", Payload: datatypes.JSON(`{"raw":"private-payload"}`), ErrorMessage: "password=private-error-value", MaxAttempts: 4}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`{"resource":"applications","query":"checkout"}`, `{"resource":"applications"}`} {
		output, err := diagnosticTool(t, s, user, "project_resources").InvokableRun(ctx, query)
		if err != nil || !strings.Contains(output, "checkout") || strings.Contains(output, "other-project") {
			t.Fatalf("资源搜索范围错误: %s %v", output, err)
		}
	}
	output, err := diagnosticTool(t, s, user, "project_resources").InvokableRun(ctx, `{"resource":"applications","query":"%"}`)
	if err != nil || strings.Contains(output, "checkout") {
		t.Fatal("搜索通配符未转义")
	}
	output, err = diagnosticTool(t, s, user, "project_tasks").InvokableRun(ctx, `{"job_id":"`+job.ID+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []string{"private-subject", "private-payload", "private-error-value"} {
		if strings.Contains(output, hidden) {
			t.Fatalf("任务泄露 %s", hidden)
		}
	}
	if _, err := diagnosticTool(t, s, user, "project_tasks").InvokableRun(ctx, `{"job_id":"missing"}`); !errors.Is(err, ErrNotFound) {
		t.Fatal("指定任务不存在时未说明")
	}
}

type diagnosticKube struct {
	object            map[string]any
	logs              string
	reads, logCalls   int
	previous, replace bool
	afterLog          func()
}

func (k *diagnosticKube) ListResources(context.Context, string, string, string, string) (*kube.ResourceList, error) {
	return nil, errors.New("测试未配置列表")
}
func (k *diagnosticKube) ResourceEvents(context.Context, string, string, string, string, string) ([]corev1.Event, error) {
	return nil, nil
}
func (k *diagnosticKube) GetResource(_ context.Context, _, _, _, _ string, reveal bool) (map[string]any, error) {
	k.reads++
	if reveal {
		return nil, errors.New("不应读取密钥")
	}
	if k.replace && k.reads > 1 {
		return map[string]any{"metadata": map[string]any{"uid": "replacement"}}, nil
	}
	return k.object, nil
}
func (k *diagnosticKube) ResourceDiagnostics(context.Context, string, string, string, string, string) (*kube.ResourceDiagnostics, error) {
	return &kube.ResourceDiagnostics{UID: "pod-uid", State: "partial", Errors: map[string]error{"metrics": errors.New("private SDK endpoint")}}, nil
}
func (k *diagnosticKube) PodLogSnapshot(_ context.Context, _, namespace, pod, container string, tail int64, previous bool) (io.ReadCloser, error) {
	k.logCalls++
	if namespace != "test" || pod != "api-1" || container != "api" || tail != 100 {
		return nil, errors.New("Pod日志范围错误")
	}
	k.previous = previous
	if k.afterLog != nil {
		k.afterLog()
	}
	return io.NopCloser(strings.NewReader(k.logs)), nil
}

func TestDiagnosticPodLogsCheckIdentityBeforeAndAfterRead(t *testing.T) {
	s, db, user, ctx := assistantFixture(t)
	cluster := model.KubernetesCluster{ID: newID(), Name: "test", IsActive: true, DepartmentID: user.DepartmentID, ProjectID: database.DefaultProjectID(user.DepartmentID)}
	if err := db.Create(&cluster).Error; err != nil {
		t.Fatal(err)
	}
	reader := &diagnosticKube{object: map[string]any{"kind": "Pod", "metadata": map[string]any{"uid": "pod-uid"}, "spec": map[string]any{"containers": []any{map[string]any{"name": "api"}}}}, logs: "previous: OOMKilled\nAuthorization: Bearer private-pod-token"}
	s.deps.Kubernetes = reader
	partial, err := diagnosticTool(t, s, user, "kubernetes_diagnostics").InvokableRun(ctx, `{"cluster_id":"`+cluster.ID+`","resource":"pods","namespace":"test","name":"api-1","uid":"pod-uid"}`)
	if err != nil || !strings.Contains(partial, "metrics") || strings.Contains(partial, "private SDK endpoint") {
		t.Fatalf("关联诊断缺失信息或泄露内部错误: %s %v", partial, err)
	}
	reader.reads = 0
	input := `{"cluster_id":"` + cluster.ID + `","namespace":"test","name":"api-1","uid":"pod-uid","container":"api","previous":true}`
	output, err := diagnosticTool(t, s, user, "kubernetes_pod_logs").InvokableRun(ctx, input)
	if err != nil || !reader.previous || reader.reads != 2 || !strings.Contains(output, "OOMKilled") || strings.Contains(output, "private-pod-token") {
		t.Fatalf("Pod日志异常: %s %v", output, err)
	}
	reader.reads, reader.replace = 0, true
	if output, err := diagnosticTool(t, s, user, "kubernetes_pod_logs").InvokableRun(ctx, input); !errors.Is(err, ErrActionState) || output != "" {
		t.Fatal("同名Pod重建后仍输出旧日志")
	}
	reader.reads, reader.replace = 0, false
	if _, err := diagnosticTool(t, s, user, "kubernetes_pod_logs").InvokableRun(ctx, strings.Replace(input, "pod-uid", "wrong-uid", 1)); !errors.Is(err, ErrActionState) || reader.logCalls != 2 {
		t.Fatal("错误UID仍访问日志")
	}
	active := true
	reader.afterLog = func() { active = false }
	guarded := WithSessionAuthorization(ctx, func() bool { return active })
	if output, err := diagnosticTool(t, s, user, "kubernetes_pod_logs").InvokableRun(guarded, input); !errors.Is(err, ErrForbidden) || output != "" {
		t.Fatal("读取期间退出后仍返回日志")
	}
}

type diagnosticMetrics struct{ calls int }

func (m *diagnosticMetrics) Snapshot(context.Context) systemmetrics.Snapshot {
	m.calls++
	return systemmetrics.Snapshot{Unavailable: []string{"queue"}}
}

func TestDiagnosticPlatformNeedsSeparatePlatformPermission(t *testing.T) {
	s, db, admin, adminCtx := assistantFixture(t)
	user, ctx := ordinaryAssistantUser(t, s, db, admin, adminCtx)
	metrics := &diagnosticMetrics{}
	logger, logs := logging.NewRuntime("info")
	t.Cleanup(func() { _ = logs.Close() })
	s.deps.Metrics, s.deps.RuntimeLogs = metrics, logs
	logger.Error("连接失败 password=private-platform-password", "operation", "test_diagnostic", "request_id", "req-1", "api_key", "private-field")
	if _, err := diagnosticTool(t, s, user, "platform_diagnostics").InvokableRun(ctx, `{}`); !errors.Is(err, ErrForbidden) || metrics.calls != 0 {
		t.Fatal("普通项目成员读取了平台日志")
	}
	if err := s.deps.Access.SetUserPermissions(adminCtx, user.ID, access.UserPermissionOverrides{Allow: []string{"assistant.read", "assistant.execute", "monitor.read", access.PermissionAssistantPlatformRead}}); err != nil {
		t.Fatal(err)
	}
	output, err := diagnosticTool(t, s, user, "platform_diagnostics").InvokableRun(ctx, `{"query":"test_diagnostic"}`)
	if err != nil || metrics.calls != 1 || !strings.Contains(output, "req-1") {
		t.Fatalf("已授权平台排障失败: %s %v", output, err)
	}
	if strings.Contains(output, "private-platform-password") || strings.Contains(output, "private-field") {
		t.Fatal("平台日志泄露")
	}
}

func TestDiagnosticLogBoundsAndKubernetesProjection(t *testing.T) {
	for _, input := range []string{"postgres://user:private-dsn-password@db/app", "redis://user:private-dsn-password@cache:6379", "mysqluser:private-dsn-password@tcp(db:3306)/app"} {
		if strings.Contains(redact(input), "private-dsn-password") {
			t.Fatal("排障日志泄露数据库连接密码")
		}
	}
	var frame bytes.Buffer
	header := make([]byte, 8)
	header[0] = 1
	binary.BigEndian.PutUint32(header[4:], 5)
	frame.Write(header)
	frame.WriteString("hello")
	text, partial, err := decodeDiagnosticLogs(frame.Bytes(), false)
	if err != nil || partial || text != "hello" {
		t.Fatal("Docker日志解复用失败")
	}
	if _, _, err := decodeDiagnosticLogs(frame.Bytes()[:10], false); err == nil {
		t.Fatal("未识别破损帧")
	}
	binary.BigEndian.PutUint32(header[4:], 0xffffffff)
	if _, _, err := decodeDiagnosticLogs(header, false); err == nil {
		t.Fatal("允许超大帧分配")
	}
	text, truncated := diagnosticLogTail(strings.Repeat("前", 4000)+" password=hidden-log-secret", false)
	if !truncated || len(text) > 8192 || strings.Contains(text, "hidden-log-secret") {
		t.Fatal("日志限长或脱敏失败")
	}
	object := map[string]any{"kind": "Pod", "spec": map[string]any{"containers": []any{map[string]any{"name": "api", "image": "api:1", "env": []any{map[string]any{"value": "private-env"}}, "startupProbe": map[string]any{"httpGet": map[string]any{"path": "/health", "port": 8080, "httpHeaders": []any{"private-header"}}}}}}}
	encoded, _ := json.Marshal(resourceSummary(object))
	if !strings.Contains(string(encoded), "/health") || strings.Contains(string(encoded), "private-") {
		t.Fatalf("探针投影不正确: %s", encoded)
	}
	related := &kube.ResourceDiagnostics{Endpoints: make([]kube.ServiceEndpoint, 30), Revisions: []kube.DeploymentRevision{{Pods: make([]kube.RevisionPod, 25)}}}
	bounded, cut := boundedKubeDiagnostics(related)
	if !cut || len(bounded.Endpoints) != 20 || len(bounded.Revisions[0].Pods) != 20 || len(related.Revisions[0].Pods) != 25 {
		t.Fatal("关联诊断没有限长或修改了原结果")
	}
}

func TestDiagnosticEinoUsesRealToolEvidenceWithoutActions(t *testing.T) {
	s, user, ctx, row, _, _ := diagnosticDeploymentFixture(t)
	saveTestSettings(t, s, user, ctx, "fake-diagnostic-key")
	conversation, err := s.CreateConversation(ctx, user, "")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.newModel = func(context.Context, model.AssistantSettings, string) (einomodel.BaseChatModel, error) {
		return &fakeModel{respond: func(_ context.Context, messages []*schema.Message) (*schema.Message, error) {
			calls++
			steps := []schema.FunctionCall{
				{Name: "project_resources", Arguments: `{"resource":"applications","query":"checkout"}`},
				{Name: "application_deployments", Arguments: `{"application_id":"` + row.ApplicationID + `"}`},
				{Name: "deployment_logs", Arguments: `{"deployment_id":"` + row.ID + `"}`},
			}
			if calls <= len(steps) {
				return schema.AssistantMessage("", []schema.ToolCall{{ID: newID(), Type: "function", Function: steps[calls-1]}}), nil
			}
			found := false
			for _, message := range messages {
				if message.Role == schema.Tool && strings.Contains(message.Content, "out of memory") {
					found = true
				}
				if strings.Contains(message.Content, "private-log-value") {
					t.Error("模型收到日志秘密")
				}
			}
			if !found {
				t.Error("模型没有收到真实排障工具结果")
			}
			return schema.AssistantMessage("日志显示 out of memory，容器 OOMKilled；建议检查内存限制，尚未修改资源。", nil), nil
		}}, nil
	}
	if err := s.Chat(ctx, user, conversation.ID, "应用刚才退出了，帮我查日志", nil); err != nil {
		t.Fatal(err)
	}
	detail, err := s.Conversation(ctx, user, conversation.ID)
	if err != nil || calls != 4 || len(detail.Actions) != 0 || len(detail.Messages) != 2 || !strings.Contains(detail.Messages[1].Content, "OOMKilled") {
		t.Fatalf("排障链路不完整: %+v %v", detail, err)
	}
}
