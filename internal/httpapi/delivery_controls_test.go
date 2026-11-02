package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/repository"
)

func TestDeliveryControlHTTPRequiresProjectAndHidesSecrets(t *testing.T) {
	f := newStreamBoundaryFixture(t)
	call := func(method, path, project string, payload any) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(body))
		req.AddCookie(f.cookie)
		req.Header.Set("Content-Type", "application/json")
		if project != "" {
			req.Header.Set("X-EDO-Project-ID", project)
		}
		response := httptest.NewRecorder()
		f.router.ServeHTTP(response, req)
		return response
	}
	for _, path := range []string{"/delivery-variables", "/application-databases", "/environment-policies"} {
		if response := call(http.MethodGet, path, "", nil); response.Code != 400 {
			t.Fatalf("接口未要求项目: %s %d", path, response.Code)
		}
	}
	response := call(http.MethodPost, "/delivery-variables", f.project.ID, map[string]any{"name": "PRIVATE_TOKEN", "kind": "env", "usage": "build", "is_secret": true, "is_active": true, "value": "secret-for-http-boundary"})
	if response.Code != 200 || bytes.Contains(response.Body.Bytes(), []byte("secret-for-http-boundary")) {
		t.Fatalf("密钥创建失败或回显明文: %d %s", response.Code, response.Body.String())
	}
	response = call(http.MethodGet, "/delivery-variables", f.project.ID, nil)
	if response.Code != 200 || bytes.Contains(response.Body.Bytes(), []byte("secret-for-http-boundary")) {
		t.Fatal("密钥列表回显明文")
	}
	response = call(http.MethodGet, "/delivery-variables", "inaccessible-project", nil)
	if response.Code != 403 {
		t.Fatal("跨项目请求未被拒绝")
	}
}

func TestBuildSecretHTTPPersistsOnlyVersionReferencesAndSafeAudit(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	f := newReleasePlanExecutionHTTPFixture(t, commit)
	defer f.close()
	permissions, err := access.NewService(f.db)
	if err != nil {
		t.Fatal(err)
	}
	f.pipelines.ConfigureDeliveryAccess(permissions)
	repo, _, err := f.repositories.Create(context.Background(), "admin", repository.Input{
		Name: "构建密钥持久化测试仓库", Provider: model.GitProviderGeneric,
		CloneURL: "https://example.invalid/build-secret-fixture.git", DefaultBranch: "main", AuthType: model.GitAuthNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	app, workflowID, sourceID, _ := createReleasePlanExecutionHTTPApplication(t, f.pipelines, f.db, repo.ID)
	cookie := loginForDepartmentTest(t, f.router, "admin")
	const envValue = "EDO_UNIT_ENV_CANARY_79c2d87fd6"
	const fileMarker = "EDO_UNIT_FILE_CANARY_c1bdf5203a"
	fileValue := fileMarker + "\n第二行公开合成文件\n"
	assertNoCanary := func(label, raw string) {
		t.Helper()
		for _, value := range []string{envValue, fileMarker, fileValue,
			base64.StdEncoding.EncodeToString([]byte(envValue)), base64.StdEncoding.EncodeToString([]byte(fileValue))} {
			if strings.Contains(raw, value) {
				t.Fatalf("%s 含合成密钥内容", label)
			}
		}
	}
	// 通过正常请求提交密钥，记录准确请求对应的原始审计；不执行任何构建器或 Worker。
	wantAudits := map[string]string{}
	variables := map[string]model.DeliveryVariable{}
	for _, input := range []struct{ name, kind, value string }{
		{"BUILD_ENV_CANARY", "env", envValue}, {"BUILD_FILE_CANARY", "file", fileValue},
	} {
		response := performJSONRequest(t, f.router, http.MethodPost, "/api/v1/delivery-variables", map[string]any{
			"name": input.name, "kind": input.kind, "usage": "build", "value": input.value,
			"is_secret": true, "is_active": true, "application_id": app.ID, "protected_refs": []string{"refs/heads/main"},
		}, cookie)
		var payload struct {
			Variable model.DeliveryVariable `json:"variable"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload.Variable.ID == "" || payload.Variable.Version != 1 {
			t.Fatalf("正常创建构建密钥失败: status=%d", response.Code)
		}
		assertNoCanary("创建密钥响应", response.Body.String())
		variables[input.name] = payload.Variable
		wantAudits[response.Header().Get("X-Request-ID")] = "delivery_variable.create"
		var revision model.DeliveryVariableRevision
		if err = f.db.Where("variable_id = ? AND version = ?", payload.Variable.ID, 1).First(&revision).Error; err != nil {
			t.Fatal(err)
		}
		if !revision.IsSecret || revision.Value != "" || revision.Ciphertext == "" {
			t.Fatal("构建密钥修订没有仅以密文保存内容")
		}
		assertNoCanary("变量修订密文", revision.Ciphertext)
	}
	workflow, err := f.pipelines.GetWorkflow(context.Background(), app.ID)
	if err != nil {
		t.Fatal(err)
	}
	bound := false
	for i := range workflow.Workflow.Stages {
		for j := range workflow.Workflow.Stages[i].Tasks {
			node := &workflow.Workflow.Stages[i].Tasks[j]
			if node.ID == "build" && node.Type == model.WorkflowNodeBuild {
				node.Config.VariableNames = []string{"BUILD_ENV_CANARY", "BUILD_FILE_CANARY"}
				bound = true
			}
		}
	}
	if !bound {
		t.Fatal("正常构建夹具缺少 Dockerfile 构建节点")
	}
	saved := performJSONRequest(t, f.router, http.MethodPut, "/api/v1/applications/"+app.ID+"/workflows/"+workflowID, map[string]any{
		"schema_version": workflow.Workflow.SchemaVersion, "name": workflow.Workflow.Name, "revision": workflow.Workflow.Revision,
		"activate": true, "source": workflow.Workflow.Source, "stages": workflow.Workflow.Stages,
	}, cookie)
	if saved.Code != http.StatusOK {
		t.Fatalf("正常保存构建密钥引用失败: status=%d", saved.Code)
	}
	wantAudits[saved.Header().Get("X-Request-ID")] = "workflow.update"
	prepared := performJSONRequest(t, f.router, http.MethodPost, "/api/v1/applications/"+app.ID+"/workflows/"+workflowID+"/pipeline-runs", nil, cookie)
	var preparedBody struct {
		Run model.PipelineRun `json:"pipeline_run"`
	}
	if prepared.Code != http.StatusCreated || json.Unmarshal(prepared.Body.Bytes(), &preparedBody) != nil || preparedBody.Run.ID == "" {
		t.Fatalf("正常准备流水线失败: status=%d", prepared.Code)
	}
	wantAudits[prepared.Header().Get("X-Request-ID")] = "pipeline.prepare"
	executed := performJSONRequest(t, f.router, http.MethodPost, "/api/v1/pipeline-runs/"+preparedBody.Run.ID+"/execute", map[string]any{
		"source_node_id": sourceID, "ref": "refs/heads/main", "commit_sha": commit,
	}, cookie)
	if executed.Code != http.StatusOK {
		t.Fatalf("正常提交构建任务失败: status=%d", executed.Code)
	}
	assertNoCanary("运行执行响应", executed.Body.String())
	wantAudits[executed.Header().Get("X-Request-ID")] = "workflow_run.execute"
	var run model.PipelineRun
	if err = f.db.Where("uuid = ?", preparedBody.Run.ID).First(&run).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != model.PipelineRunRunning || run.ExecutionJobID == "" || run.WorkflowSnapshot == "" {
		t.Fatal("没有持久化实际排队运行及其快照")
	}
	// 必须检查原始持久化字符串；Marshal(PipelineRun) 会被 json:"-" 隐藏快照而产生假绿。
	assertNoCanary("原始 workflow_snapshot", run.WorkflowSnapshot)
	var snapshot struct {
		Bindings map[string][]map[string]json.RawMessage `json:"variable_bindings"`
	}
	if err = json.Unmarshal([]byte(run.WorkflowSnapshot), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Bindings) != 1 || len(snapshot.Bindings["build"]) != 2 {
		t.Fatal("持久化快照缺少两个真实构建变量引用")
	}
	seen := map[string]bool{}
	for _, raw := range snapshot.Bindings["build"] {
		if len(raw) != 5 {
			t.Fatal("变量快照不只是固定版本引用字段")
		}
		for _, key := range []string{"name", "variable_id", "version", "kind", "is_secret"} {
			if _, ok := raw[key]; !ok {
				t.Fatal("变量快照引用字段不完整")
			}
		}
		encoded, _ := json.Marshal(raw)
		var binding model.DeliveryVariableBinding
		if err = json.Unmarshal(encoded, &binding); err != nil {
			t.Fatal(err)
		}
		variable, ok := variables[binding.Name]
		if !ok || seen[binding.Name] || binding.VariableID != variable.ID || binding.Version != 1 || binding.Kind != variable.Kind || !binding.IsSecret {
			t.Fatal("持久化快照没有固定准确的变量 UUID、版本及类型")
		}
		seen[binding.Name] = true
	}
	var nodeRuns []model.PipelineNodeRun
	if err = f.db.Where("pipeline_run_id = ?", run.ID).Find(&nodeRuns).Error; err != nil || len(nodeRuns) == 0 {
		t.Fatalf("缺少本次运行的任务关联: %v", err)
	}
	jobIDs := make([]string, 0, len(nodeRuns))
	for _, node := range nodeRuns {
		if node.JobID == "" {
			t.Fatal("运行节点缺少排队任务 UUID")
		}
		jobIDs = append(jobIDs, node.JobID)
	}
	var jobs []model.Job
	if err = f.db.Where("uuid IN ?", jobIDs).Find(&jobs).Error; err != nil || len(jobs) != len(jobIDs) {
		t.Fatalf("正常执行没有持久化可核验的 Job: %v", err)
	}
	for _, job := range jobs {
		assertNoCanary("Job原始payload", string(job.Payload))
		var payload struct {
			RunID string `json:"pipeline_run_id"`
		}
		if job.Status != model.JobPending || json.Unmarshal(job.Payload, &payload) != nil || payload.RunID != run.ID {
			t.Fatal("Job未准确引用本次排队运行")
		}
		var events []model.OutboxEvent
		if err = f.db.Where("aggregate_id = ?", job.ID).Find(&events).Error; err != nil || len(events) != 1 || len(events[0].Payload) == 0 {
			t.Fatalf("缺少对应Outbox消息: %v", err)
		}
		event := events[0]
		assertNoCanary("Outbox原始payload", string(event.Payload))
		var envelope struct {
			JobID   string          `json:"job_id"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(event.Payload, &envelope) != nil || envelope.JobID != job.ID || !bytes.Equal(envelope.Payload, job.Payload) {
			t.Fatal("Outbox没有引用准确Job及原始安全payload")
		}
	}
	var audits []model.AuditLog
	if err = f.db.Find(&audits).Error; err != nil {
		t.Fatal(err)
	}
	matched := map[string]int{}
	if len(wantAudits) != 5 {
		t.Fatal("五次正常业务请求没有独立请求标识")
	}
	for _, entry := range audits {
		actorID := ""
		if entry.ActorUserID != nil {
			actorID = *entry.ActorUserID
		}
		for _, raw := range []string{entry.ID, entry.DepartmentID, entry.ProjectID, actorID, entry.Action, entry.ResourceType, entry.ResourceID, string(entry.Result), entry.RequestID, entry.ClientIP, entry.UserAgent, string(entry.Metadata)} {
			assertNoCanary("审计原始持久化字段", raw)
		}
		if action, ok := wantAudits[entry.RequestID]; ok {
			if entry.RequestID == "" || actorID != run.CreatedBy || entry.Action != action || entry.Result != model.AuditSucceeded || entry.ProjectID != database.DefaultProjectID(database.DefaultDepartmentID) {
				t.Fatal("正常业务请求未对应成功的项目审计")
			}
			matched[entry.RequestID]++
		}
	}
	for id := range wantAudits {
		if id == "" || matched[id] != 1 {
			t.Fatal("缺少与正常请求一一对应的持久化审计")
		}
	}
	var builds int64
	if err = f.db.Model(&model.BuildRun{}).Count(&builds).Error; err != nil || builds != 0 {
		t.Fatal("本用例不应执行构建器或写入真实构建结果")
	}
}
