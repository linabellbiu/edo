package pipeline

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"edo/internal/access"
	"edo/internal/artifact"
	"edo/internal/cicd"
	"edo/internal/database"
	"edo/internal/dockerengine"
	"edo/internal/model"
	"edo/internal/repository"
	"edo/internal/testutil"
)

func TestPipelineInjectsFrozenVariablesAndRedactsActualValue(t *testing.T) {
	node := parallelShell("test")
	snapshot := workflowSnapshot{SchemaVersion: 1, ExecutionVersion: 2, Source: model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger}, Stages: []model.WorkflowStage{{ID: "ci", Tasks: []model.WorkflowNode{node}}}}
	s, db, _, run := newBuildExecutorTestRun(t, snapshot.Source, snapshot, map[string]string{"code.txt": "source"})
	actor := model.User{ID: "delivery-authority", Username: "delivery_authority", IsSuperuser: true, IsActive: true}
	if err := db.Create(&actor).Error; err != nil {
		t.Fatal(err)
	}
	permissions, err := access.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	s.ConfigureDeliveryAccess(permissions)
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: run.ProjectID, DepartmentID: run.DepartmentID, UserID: actor.ID})
	value := "private-actual-variable"
	input := cicd.VariableInput{Name: "TOKEN_WITHOUT_HINT", Kind: "env", Usage: "build", Value: &value, IsSecret: true, IsActive: true}
	variable, err := s.Delivery().SaveVariable(ctx, actor.ID, "", input)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := s.Delivery().FreezeVariables(ctx, run.ProjectID, run.ApplicationID, "", "build", run.Ref, []string{input.Name})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.AuthorityID = actor.ID
	snapshot.VariableBindings = map[string][]model.DeliveryVariableBinding{node.ID: bindings}
	raw, _ := json.Marshal(snapshot)
	if strings.Contains(string(raw), value) {
		t.Fatal("运行快照保存了密钥明文")
	}
	if err = db.Model(&model.PipelineRun{}).Where("uuid = ?", run.ID).Update("workflow_snapshot", string(raw)).Error; err != nil {
		t.Fatal(err)
	}
	replacement := "replacement-private-value"
	input.Value = &replacement
	input.ExpectedVersion = variable.Version
	if _, err = s.Delivery().SaveVariable(ctx, actor.ID, variable.ID, input); err != nil {
		t.Fatal(err)
	}
	s.scriptRunner = parallelScriptRunner(func(_ context.Context, in dockerengine.ScriptContainerInput) (dockerengine.ScriptContainerResult, error) {
		if in.Environment["TOKEN_WITHOUT_HINT"] != value {
			t.Fatal("没有注入运行创建时的变量版本")
		}
		_, _ = in.Stdout.Write([]byte("value=" + value + "\n"))
		return dockerengine.ScriptContainerResult{}, nil
	})
	if _, err = s.AdvanceRun(ctx, run.ID, actor.ID, ""); err != nil {
		t.Fatal(err)
	}
	nodes, err := s.ListNodeRuns(ctx, run.ID)
	if err != nil || len(nodes) != 1 {
		t.Fatal(err)
	}
	if err = s.ExecuteBuildTask(ctx, BuildTaskPayload{PipelineRunID: run.ID, WorkflowNodeID: node.ID}, nodes[0].JobID); err != nil {
		t.Fatal(err)
	}
	var logs []model.PipelineRunLog
	if err = db.Where("pipeline_run_id = ?", run.ID).Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(logs)
	if strings.Contains(string(encoded), value) || !strings.Contains(string(encoded), "已脱敏") {
		t.Fatal("日志没有按密钥实际值脱敏")
	}
}

type secretBuildCheckoutStub struct {
	buildExecutorCheckoutStub
	refs repository.RefResult
}

func (stub secretBuildCheckoutStub) ListRefs(context.Context, model.GitRepository, string) (repository.RefResult, error) {
	return stub.refs, nil
}

func TestBuildExecutionPersistsFrozenPlanWithoutResolvedSecrets(t *testing.T) {
	service, db, secrets, repositoryID := newPipelineTestService(t)
	const actorID = "build-secret-authority"
	const commit = "0123456789abcdef0123456789abcdef01234567"
	const envValue = "EDO_UNIT_WORKER_ENV_CANARY_8077a28e"
	const fileMarker = "EDO_UNIT_WORKER_FILE_CANARY_a30729bf"
	const publicOutput = "public build output\n"
	fileValue := fileMarker + "\n第二行公开合成文件\n"
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{
		ProjectID: database.DefaultProjectID(database.DefaultDepartmentID), DepartmentID: database.DefaultDepartmentID, UserID: actorID,
	})
	actor := model.User{ID: actorID, Username: "build_secret_authority", DepartmentID: database.DefaultDepartmentID, IsSuperuser: true, IsActive: true}
	if err := db.Create(&actor).Error; err != nil {
		t.Fatal(err)
	}
	permissions, err := access.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	service.ConfigureDeliveryAccess(permissions)
	service.repositories = repository.NewService(db, secrets, nil, secretBuildCheckoutStub{
		buildExecutorCheckoutStub: buildExecutorCheckoutStub{files: map[string]string{"source.txt": "public source"}},
		refs:                      repository.RefResult{Branches: []repository.GitRef{{Name: "main", SHA: commit}}},
	}, 4)
	store, err := artifact.NewService(db, t.TempDir(), 1024*1024, service.logger)
	if err != nil {
		t.Fatal(err)
	}
	service.ConfigureArtifacts(store)
	plan, err := service.CreateBuildPlan(ctx, actorID, BuildPlanInput{
		Name: "密钥持久化脚本构建", Kind: model.BuildPlanScript, Script: "printf public > result.txt",
		WorkingDirectory: ".", ArtifactPath: "result.txt", RuntimeImage: model.DefaultRuntimeImage, TimeoutSeconds: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	app, err := service.CreateApplication(ctx, actorID, ApplicationInput{Name: "build_secret_persistence", RepositoryID: repositoryID})
	if err != nil {
		t.Fatal(err)
	}
	variables := map[string]model.DeliveryVariable{}
	for _, input := range []struct{ name, kind, value string }{
		{"BUILD_ENV_CANARY", "env", envValue}, {"BUILD_FILE_CANARY", "file", fileValue},
	} {
		variable, saveErr := service.Delivery().SaveVariable(ctx, actorID, "", cicd.VariableInput{
			Name: input.name, Kind: input.kind, Usage: "build", Value: &input.value,
			IsSecret: true, IsActive: true, ApplicationID: app.ID, ProtectedRefs: []string{"refs/heads/main"},
		})
		if saveErr != nil || variable == nil || variable.ID == "" || variable.Version != 1 {
			t.Fatalf("正常保存构建密钥失败: %v", saveErr)
		}
		variables[input.name] = variable.DeliveryVariable
	}
	workflow, err := service.GetWorkflow(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	source := model.WorkflowNode{ID: "source", Type: model.WorkflowNodeTrigger, Name: "代码源", Config: model.WorkflowNodeConfig{Branch: "main", Events: []string{"manual"}}}
	build := model.WorkflowNode{ID: "build", Type: model.WorkflowNodeBuild, Name: "构建", Config: model.WorkflowNodeConfig{
		BuildPlanID: plan.ID, VariableNames: []string{"BUILD_ENV_CANARY", "BUILD_FILE_CANARY"},
	}}
	saved, err := service.SaveWorkflow(ctx, app.ID, actorID, WorkflowInput{
		SchemaVersion: model.WorkflowSchemaVersion, Name: workflow.Workflow.Name, Revision: workflow.Workflow.Revision, Activate: true,
		Source: source, Stages: []model.WorkflowStage{{ID: "build-stage", Name: "构建", Tasks: []model.WorkflowNode{build}}},
	})
	if err != nil || !saved.Valid || !saved.Workflow.IsActive {
		t.Fatalf("正常启用构建流水线失败: %v", err)
	}
	run, err := service.PrepareWorkflowRun(ctx, app.ID, saved.Workflow.ID, actorID)
	if err != nil || run.Status != model.PipelineRunBlocked {
		t.Fatalf("正常准备构建运行失败: %v", err)
	}
	run, err = service.ExecuteRunSelection(ctx, run.ID, actorID, "refs/heads/main", commit, source.ID, "")
	if err != nil || run.Status != model.PipelineRunRunning || run.ExecutionJobID == "" {
		t.Fatalf("正常执行未产生排队任务: %v", err)
	}
	jobID := run.ExecutionJobID
	runnerCalls := 0
	service.scriptRunner = parallelScriptRunner(func(callCtx context.Context, in dockerengine.ScriptContainerInput) (dockerengine.ScriptContainerResult, error) {
		runnerCalls++
		if callCtx.Err() != nil || in.Environment["BUILD_ENV_CANARY"] != envValue || in.FileVariables["BUILD_FILE_CANARY"] != fileValue || len(in.FileVariables) != 1 {
			return dockerengine.ScriptContainerResult{}, errors.New("构建执行器未收到准确的 env/file 密钥")
		}
		if in.ArtifactPath != "result.txt" || in.OutputDirectory == "" || in.Image != model.DefaultRuntimeImage {
			return dockerengine.ScriptContainerResult{}, errors.New("构建执行器缺少正常输出配置")
		}
		output := filepath.Join(in.OutputDirectory, "result.txt")
		if writeErr := os.WriteFile(output, []byte(publicOutput), 0o600); writeErr != nil {
			return dockerengine.ScriptContainerResult{}, writeErr
		}
		return dockerengine.ScriptContainerResult{ArtifactPath: output}, nil
	})
	// 只替换外部脚本容器执行；运行、变量冻结和 BuildRun 均由正常服务流程写入。
	if err = service.ExecuteBuildTask(ctx, BuildTaskPayload{PipelineRunID: run.ID, WorkflowNodeID: build.ID}, jobID); err != nil {
		t.Fatalf("正常执行构建任务失败: %v", err)
	}
	if runnerCalls != 1 {
		t.Fatal("构建执行器没有恰好执行一次，无法证明密钥实际注入")
	}
	var storedRun model.PipelineRun
	if err = db.First(&storedRun, "uuid = ?", run.ID).Error; err != nil || storedRun.Status != model.PipelineRunSucceeded || storedRun.ArtifactID == "" {
		t.Fatalf("构建运行未成功关联实际制品: %v", err)
	}
	stored, file, err := store.OpenDownload(ctx, storedRun.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	content, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || string(content) != publicOutput || stored.BuildRunID == "" || stored.PipelineRunID != run.ID {
		t.Fatal("实际持久化制品没有准确的公开内容或 BuildRun 关联")
	}
	var buildRun model.BuildRun
	if err = db.First(&buildRun, "uuid = ?", stored.BuildRunID).Error; err != nil {
		t.Fatal(err)
	}
	if buildRun.Status != model.BuildRunStatusSucceeded || buildRun.ProducerKind != model.BuildRunProducerScript ||
		buildRun.PipelineRunID != run.ID || buildRun.BuildPlanID != plan.ID || buildRun.WorkflowNodeID != build.ID ||
		buildRun.ApplicationID != app.ID || buildRun.RepositoryID != repositoryID || buildRun.CreatedBy != actorID {
		t.Fatal("BuildRun 未准确关联本次正常执行")
	}
	assertNoCanary := func(label, raw string) {
		t.Helper()
		for _, value := range []string{envValue, fileMarker, fileValue, base64.StdEncoding.EncodeToString([]byte(envValue)), base64.StdEncoding.EncodeToString([]byte(fileValue))} {
			if strings.Contains(raw, value) {
				t.Fatalf("%s 含合成密钥内容", label)
			}
		}
	}
	// 原始数据库字段不会受模型 json:"-" 隐藏；快照内容和摘要都必须精确匹配冻结方案。
	assertNoCanary("BuildRun 原始 PlanSnapshot", buildRun.PlanSnapshot)
	assertNoCanary("PipelineRun 原始 WorkflowSnapshot", storedRun.WorkflowSnapshot)
	expectedPlan, err := json.Marshal(buildPlanSnapshot(*plan))
	if err != nil || buildRun.PlanSnapshot == "" || buildRun.PlanSnapshot != string(expectedPlan) || buildRun.PlanDigest != sha256Digest(expectedPlan) {
		t.Fatal("BuildRun 没有持久化准确的冻结构建方案及摘要")
	}
	var frozen workflowSnapshot
	if err = json.Unmarshal([]byte(storedRun.WorkflowSnapshot), &frozen); err != nil || len(frozen.VariableBindings) != 1 || len(frozen.VariableBindings[build.ID]) != 2 {
		t.Fatal("运行快照缺少两个正常冻结的密钥引用")
	}
	seen := map[string]bool{}
	for _, binding := range frozen.VariableBindings[build.ID] {
		variable, ok := variables[binding.Name]
		if !ok || seen[binding.Name] || binding.VariableID != variable.ID || binding.Version != 1 || binding.Kind != variable.Kind || !binding.IsSecret {
			t.Fatal("运行快照未保留准确的密钥 UUID、版本及类型")
		}
		seen[binding.Name] = true
	}
	var jobs []model.Job
	if err = db.Find(&jobs).Error; err != nil || len(jobs) != 1 || jobs[0].ID != jobID || len(jobs[0].Payload) == 0 {
		t.Fatalf("缺少本次正常执行的唯一原始 Job: %v", err)
	}
	assertNoCanary("Job 原始 payload", string(jobs[0].Payload))
	var payload BuildTaskPayload
	if json.Unmarshal(jobs[0].Payload, &payload) != nil || payload.PipelineRunID != run.ID || payload.WorkflowNodeID != build.ID {
		t.Fatal("Job 未准确引用本次运行和构建节点")
	}
	var events []model.OutboxEvent
	if err = db.Find(&events).Error; err != nil || len(events) != 1 || events[0].AggregateID != jobID || len(events[0].Payload) == 0 {
		t.Fatalf("缺少本次构建任务的唯一原始 Outbox: %v", err)
	}
	assertNoCanary("Outbox 原始 payload", string(events[0].Payload))
	var envelope struct {
		JobID   string           `json:"job_id"`
		Payload BuildTaskPayload `json:"payload"`
	}
	if json.Unmarshal(events[0].Payload, &envelope) != nil || envelope.JobID != jobID || envelope.Payload != payload {
		t.Fatal("Outbox 未准确引用本次 Job 及安全 payload")
	}
	for _, query := range []any{&model.BuildRun{}, &model.Artifact{}} {
		var count int64
		if err = db.Model(query).Count(&count).Error; err != nil || count != 1 {
			t.Fatal("正常执行没有恰好持久化一个 BuildRun 和制品")
		}
	}
}

func TestDeliveryPermissionUsesCurrentProjectRoleAfterRevocation(t *testing.T) {
	for _, candidate := range []string{"移除角色权限", "移除项目成员", "个人明确拒绝", "停用项目", "停用用户"} {
		t.Run(candidate, func(t *testing.T) {
			db := testutil.NewDatabase(t)
			projectID := database.DefaultProjectID(database.DefaultDepartmentID)
			actor := model.User{ID: "delivery-role-actor", Username: "delivery_role_actor", DepartmentID: database.DefaultDepartmentID, IsActive: true}
			role := model.Role{ID: "delivery-variable-role", Name: "delivery_variable_role", DisplayName: "交付变量角色"}
			for _, item := range []any{
				&actor, &role,
				&model.RolePermission{RoleID: role.ID, Permission: "delivery_variable.execute"},
				&model.ProjectMember{ProjectID: projectID, UserID: actor.ID, RoleID: role.ID},
			} {
				if err := db.Create(item).Error; err != nil {
					t.Fatal(err)
				}
			}
			permissions, err := access.NewService(db)
			if err != nil {
				t.Fatal(err)
			}
			service := NewService(db, nil, nil)
			service.ConfigureDeliveryAccess(permissions)
			ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: projectID, DepartmentID: actor.DepartmentID})
			if err = service.CheckDeliveryPermission(ctx, actor.ID, "delivery_variable.execute"); err != nil {
				t.Fatalf("有效项目角色不能使用变量: %v", err)
			}
			switch candidate {
			case "移除角色权限":
				err = db.Where("role_id = ? AND permission = ?", role.ID, "delivery_variable.execute").Delete(&model.RolePermission{}).Error
			case "移除项目成员":
				err = db.Where("project_id = ? AND user_id = ?", projectID, actor.ID).Delete(&model.ProjectMember{}).Error
			case "个人明确拒绝":
				err = db.Create(&model.UserPermission{UserID: actor.ID, Permission: "delivery_variable.execute", Effect: model.PermissionDeny}).Error
			case "停用项目":
				err = db.Model(&model.Project{}).Where("uuid = ?", projectID).Update("is_active", false).Error
			case "停用用户":
				err = db.Model(&model.User{}).Where("uuid = ?", actor.ID).Update("is_active", false).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			// 故意不重载 Casbin，模拟权限刚撤销、后台消息随后到达。
			if stale, err := permissions.HasPermission(ctx, &actor, "delivery_variable.execute"); err != nil || !stale {
				t.Fatalf("夹具没有保留过期缓存授权: %v", err)
			}
			if err = service.CheckDeliveryPermission(ctx, actor.ID, "delivery_variable.execute"); !errors.Is(err, cicd.ErrExecutionAuthorization) {
				t.Fatalf("后台执行沿用了过期项目角色: %v", err)
			}
		})
	}
}

func TestDeliveryPermissionRequiresRoleInTheSelectedProject(t *testing.T) {
	db := testutil.NewDatabase(t)
	projectID := database.DefaultProjectID(database.DefaultDepartmentID)
	actor := model.User{ID: "delivery-project-actor", Username: "delivery_project_actor", DepartmentID: database.DefaultDepartmentID, IsActive: true}
	role := model.Role{ID: "delivery-project-writer", Name: "delivery_project_writer", DisplayName: "变量编辑角色"}
	other := model.Project{ID: "delivery-project-other", DepartmentID: actor.DepartmentID, Name: "其他交付项目", IsActive: true}
	for _, item := range []any{&actor, &role, &other, &model.RolePermission{RoleID: role.ID, Permission: "delivery_variable.update"}, &model.ProjectMember{ProjectID: projectID, UserID: actor.ID, RoleID: role.ID}} {
		if err := db.Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	permissions, err := access.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db, nil, nil)
	service.ConfigureDeliveryAccess(permissions)
	ctx := database.WithDepartmentScope(context.Background(), database.DepartmentScope{ProjectID: projectID, DepartmentID: actor.DepartmentID})
	if err = service.CheckDeliveryPermission(ctx, actor.ID, "delivery_variable.update"); err != nil {
		t.Fatal(err)
	}
	foreign := database.WithDepartmentScope(ctx, database.DepartmentScope{ProjectID: other.ID, DepartmentID: actor.DepartmentID})
	if err = service.CheckDeliveryPermission(foreign, actor.ID, "delivery_variable.update"); !errors.Is(err, cicd.ErrExecutionAuthorization) {
		t.Fatalf("一个项目的角色权限用于修改其他项目: %v", err)
	}
	if err = service.CheckDeliveryPermission(context.Background(), actor.ID, "delivery_variable.update"); !errors.Is(err, cicd.ErrExecutionAuthorization) {
		t.Fatalf("未选项目仍可修改交付配置: %v", err)
	}
}
