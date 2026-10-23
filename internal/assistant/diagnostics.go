package assistant

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"time"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/dockerengine"
	"edo/internal/logging"
	"edo/internal/model"
	"github.com/moby/moby/api/pkg/stdcopy"
	"gorm.io/gorm"
)

const maxDiagnosticRead = 64 * 1024

var ErrDiagnosticRead = errors.New("暂时无法读取排障信息，请检查资源状态与连接")

func (t *assistantTool) diagnosticQuery(ctx context.Context) *gorm.DB {
	scope, _ := database.DepartmentScopeFromContext(ctx)
	return t.service.deps.Database.WithContext(ctx).Where("project_id = ? AND department_id = ?", scope.ProjectID, scope.DepartmentID)
}

func (t *assistantTool) diagnostics(ctx context.Context, input toolInput) (any, error) {
	if len(input.Query) > 160 || strings.ContainsAny(input.Query, "\r\n\x00") {
		return nil, t.service.failure(ctx, "assistant_diagnostic_arguments", ErrInvalidInput, ErrInvalidInput)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var result any
	var err error
	switch t.name {
	case "project_resources":
		result, err = t.findResources(ctx, input)
	case "project_tasks":
		result, err = t.taskDiagnostics(ctx, input)
	case "application_deployments":
		result, err = t.applicationDeployments(ctx, input)
	case "deployment_diagnostics", "deployment_logs":
		result, err = t.deploymentDiagnostics(ctx, input)
	case "kubernetes_pod_logs", "kubernetes_diagnostics":
		result, err = t.kubeDiagnostics(ctx, input)
	case "platform_diagnostics":
		result, err = t.platformDiagnostics(ctx, input)
	default:
		return nil, t.service.failure(ctx, "assistant_diagnostic_whitelist", ErrForbidden, ErrForbidden)
	}
	if err != nil {
		return nil, err
	}
	if err := t.service.authorizeUsed(ctx, t.user); err != nil {
		return nil, err
	}
	return map[string]any{"source": map[string]any{"tool": t.name, "collected_at": time.Now().UTC(), "read_only": true}, "data": result}, nil
}

func (t *assistantTool) findResources(ctx context.Context, input toolInput) (any, error) {
	var value any
	permission := ""
	columns := []string{"ID", "name", "is_active", "updated_at"}
	var identity any
	switch input.Resource {
	case "applications":
		value = &[]model.Application{}
		identity = &model.Application{ID: strings.TrimSpace(input.Query)}
		permission = "application.read"
	case "hosts":
		value = &[]model.Host{}
		identity = &model.Host{ID: strings.TrimSpace(input.Query)}
		permission = "cluster.read"
		columns = append(columns, "mode", "architecture")
	case "clusters":
		value = &[]model.KubernetesCluster{}
		identity = &model.KubernetesCluster{ID: strings.TrimSpace(input.Query)}
		permission = "cluster.read"
	case "domains":
		value = &[]model.DNSDomain{}
		identity = &model.DNSDomain{ID: strings.TrimSpace(input.Query)}
		permission = "dns.read"
	default:
		return nil, t.service.failure(ctx, "assistant_resource_type", ErrInvalidInput, ErrInvalidInput)
	}
	if err := t.require(ctx, permission); err != nil {
		return nil, err
	}
	query := t.diagnosticQuery(ctx).Select(columns).Order("name ASC").Limit(20)
	if text := strings.TrimSpace(input.Query); text != "" {
		// 参数化匹配并屏蔽LIKE通配符，模型不能把搜索改成无界枚举。
		text = strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(text)
		match := t.service.deps.Database.Session(&gorm.Session{NewDB: true}).Where("name LIKE ? ESCAPE '!'", "%"+text+"%").Or(identity)
		query = query.Where(match)
	}
	if err := query.Find(value).Error; err != nil {
		return nil, t.service.failure(ctx, "assistant_resource_find", err, ErrDiagnosticRead, "resource", input.Resource)
	}
	// 白名单投影，模型结构中存在的凭据、脚本和配置字段一律不序列化。
	rows := []map[string]any{}
	switch items := value.(type) {
	case *[]model.Application:
		for _, v := range *items {
			rows = append(rows, map[string]any{"id": v.ID, "name": v.Name, "active": v.IsActive, "updated_at": v.UpdatedAt})
		}
	case *[]model.Host:
		for _, v := range *items {
			var caps []model.HostCapability
			if err := t.service.deps.Database.WithContext(ctx).Where("host_id = ?", v.ID).Select("kind", "status", "version", "updated_at").Limit(8).Find(&caps).Error; err != nil {
				return nil, t.service.failure(ctx, "assistant_host_capabilities", err, ErrDiagnosticRead)
			}
			rows = append(rows, map[string]any{"id": v.ID, "name": v.Name, "active": v.IsActive, "mode": v.Mode, "architecture": v.Architecture, "capabilities": caps, "updated_at": v.UpdatedAt})
		}
	case *[]model.KubernetesCluster:
		for _, v := range *items {
			rows = append(rows, map[string]any{"id": v.ID, "name": v.Name, "active": v.IsActive, "updated_at": v.UpdatedAt})
		}
	case *[]model.DNSDomain:
		for _, v := range *items {
			rows = append(rows, map[string]any{"id": v.ID, "name": v.Name, "active": v.IsActive, "updated_at": v.UpdatedAt, "dns_resolution_checked": false})
		}
	}
	return map[string]any{"resources": rows, "limit": 20, "possibly_more": len(rows) == 20}, nil
}

func (t *assistantTool) taskDiagnostics(ctx context.Context, input toolInput) (any, error) {
	if err := t.require(ctx, "task.read"); err != nil {
		return nil, err
	}
	var rows []model.Job
	q := t.diagnosticQuery(ctx).Select("ID", "kind", "status", "attempt", "max_attempts", "error_code", "error_message", "created_at", "updated_at", "started_at", "finished_at").Order("id DESC").Limit(20)
	if input.JobID != "" {
		q = q.Where(&model.Job{ID: input.JobID})
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, t.service.failure(ctx, "assistant_tasks", err, ErrDiagnosticRead)
	}
	if input.JobID != "" && len(rows) == 0 {
		return nil, t.service.failure(ctx, "assistant_task_scope", gorm.ErrRecordNotFound, ErrNotFound)
	}
	items := []map[string]any{}
	for _, r := range rows {
		items = append(items, map[string]any{"id": r.ID, "kind": r.Kind, "status": r.Status, "attempt": r.Attempt, "max_attempts": r.MaxAttempts, "error_code": r.ErrorCode, "error_message": redact(r.ErrorMessage), "created_at": r.CreatedAt, "updated_at": r.UpdatedAt, "started_at": r.StartedAt, "finished_at": r.FinishedAt})
	}
	return map[string]any{"tasks": items, "limit": 20}, nil
}

func (t *assistantTool) applicationDeployments(ctx context.Context, input toolInput) (any, error) {
	for _, p := range []string{"application.read", "deployment.read"} {
		if err := t.require(ctx, p); err != nil {
			return nil, err
		}
	}
	var app model.Application
	if input.ApplicationID == "" {
		return nil, t.service.failure(ctx, "assistant_application_id", ErrInvalidInput, ErrInvalidInput)
	}
	if err := t.diagnosticQuery(ctx).Select("ID", "name").First(&app, &model.Application{ID: input.ApplicationID}).Error; err != nil {
		return nil, t.service.failure(ctx, "assistant_application_scope", err, ErrNotFound)
	}
	var rows []model.DeploymentRecord
	if err := t.diagnosticQuery(ctx).Where("application_id = ?", app.ID).Select("ID", "application_id", "pipeline_run_id", "platform", "runtime_id", "namespace", "workload_name", "container_name", "status", "image_display", "error_code", "error_message", "created_at").Order("id DESC").Limit(20).Find(&rows).Error; err != nil {
		return nil, t.service.failure(ctx, "assistant_application_deployments", err, ErrDiagnosticRead)
	}
	items := []map[string]any{}
	for _, r := range rows {
		items = append(items, map[string]any{"id": r.ID, "application_id": r.ApplicationID, "pipeline_run_id": r.PipelineRunID, "platform": r.Platform, "runtime_id": r.RuntimeID, "namespace": r.Namespace, "workload": r.WorkloadName, "container": r.ContainerName, "status": r.Status, "image": r.ImageDisplay, "error_code": r.ErrorCode, "error_message": redact(r.ErrorMessage), "created_at": r.CreatedAt})
	}
	return map[string]any{"application_id": app.ID, "application_name": app.Name, "deployments": items, "live_state_checked": false}, nil
}

func (t *assistantTool) deploymentDiagnostics(ctx context.Context, input toolInput) (any, error) {
	if err := t.require(ctx, "deployment.read"); err != nil {
		return nil, err
	}
	if input.DeploymentID == "" || t.service.deps.Deployments == nil {
		return nil, t.service.failure(ctx, "assistant_deployment_input", ErrInvalidInput, ErrDiagnosticRead)
	}
	var row model.DeploymentRecord
	if err := t.diagnosticQuery(ctx).First(&row, &model.DeploymentRecord{ID: input.DeploymentID}).Error; err != nil {
		return nil, t.service.failure(ctx, "assistant_deployment_scope", err, ErrNotFound)
	}
	permission := "cluster.read"
	if row.Platform == model.DeploymentKubernetes {
		permission = "kubernetes.workload.read"
	}
	if err := t.require(ctx, permission); err != nil {
		return nil, err
	}
	// 宿主机脚本状态可能执行已保存的检查命令，不属于助手的只读能力。
	if row.DeploymentPlanKind == model.DeploymentPlanScript {
		return nil, t.service.failure(ctx, "assistant_script_runtime_denied", ErrForbidden, ErrForbidden)
	}
	if row.Platform == model.DeploymentDocker {
		var endpoint model.DockerEndpoint
		if row.RuntimeID == "" {
			return nil, t.service.failure(ctx, "assistant_runtime_missing", ErrNotFound, ErrNotFound)
		}
		if err := t.diagnosticQuery(ctx).Select("ID").Where("is_active = ?", true).First(&endpoint, &model.DockerEndpoint{ID: row.RuntimeID}).Error; err != nil {
			return nil, t.service.failure(ctx, "assistant_runtime_scope", err, ErrNotFound)
		}
	}
	state, err := t.service.deps.Deployments.RuntimeState(ctx, row.ID)
	if err != nil {
		return nil, t.service.failure(ctx, "assistant_runtime_state", err, ErrDiagnosticRead, "deployment_id", row.ID)
	}
	if row.Platform == model.DeploymentKubernetes {
		if t.name == "deployment_logs" {
			return nil, t.service.failure(ctx, "assistant_kubernetes_log_target", ErrInvalidInput, errors.New("请先查询工作负载的 Pod 和容器，再读取 Pod 日志"))
		}
		return map[string]any{"deployment_id": row.ID, "runtime_id": row.RuntimeID, "state": state}, nil
	}
	if state == nil || state.ResourceID == "" || t.service.deps.Docker == nil {
		return nil, t.service.failure(ctx, "assistant_container_unavailable", ErrDiagnosticRead, ErrDiagnosticRead)
	}
	// RuntimeState按部署快照解析ID；再次检查托管标签，拒绝同名替换的外部容器。
	service := ""
	if row.DeploymentPlanKind == model.DeploymentPlanCompose {
		service = row.ComposeService
	}
	container, err := t.service.deps.Docker.InspectManagedContainer(ctx, row.RuntimeID, state.ResourceID, row.TargetID, row.ID, service)
	if err != nil {
		return nil, t.service.failure(ctx, "assistant_container_identity", err, ErrDiagnosticRead, "deployment_id", row.ID, "container_id", state.ResourceID)
	}
	result := map[string]any{"deployment_id": row.ID, "runtime_id": row.RuntimeID, "state": state, "container": container}
	if t.name != "deployment_logs" {
		return result, nil
	}
	if err := t.service.authorizeUsed(ctx, t.user); err != nil {
		return nil, err
	}
	stream, err := t.service.deps.Docker.ContainerLogs(ctx, row.RuntimeID, container.ID, dockerengine.ContainerLogOptions{Tail: 100, Follow: false, Timestamps: true})
	if err != nil {
		return nil, t.service.failure(ctx, "assistant_container_logs", err, ErrDiagnosticRead, "deployment_id", row.ID, "container_id", container.ID)
	}
	defer stream.Close()
	raw, err := io.ReadAll(io.LimitReader(stream, maxDiagnosticRead+1))
	if err != nil {
		return nil, t.service.failure(ctx, "assistant_container_log_read", err, ErrDiagnosticRead)
	}
	text, partial, err := decodeDiagnosticLogs(raw, stream.TTY)
	if err != nil {
		return nil, t.service.failure(ctx, "assistant_container_log_decode", err, ErrDiagnosticRead)
	}
	result["logs"], result["truncated"] = diagnosticLogTail(text, partial || len(raw) > maxDiagnosticRead)
	if state.Count > 1 {
		result["selection_note"] = "该服务有多个容器，本次只读取已解析实例的日志"
	}
	return result, nil
}

func decodeDiagnosticLogs(raw []byte, tty bool) (string, bool, error) {
	if tty {
		return string(raw), false, nil
	}
	valid := 0
	for valid+8 <= len(raw) {
		size := int(binary.BigEndian.Uint32(raw[valid+4 : valid+8]))
		if raw[valid] > 2 || raw[valid+1] != 0 || raw[valid+2] != 0 || raw[valid+3] != 0 || size > maxDiagnosticRead {
			return "", false, ErrDiagnosticRead
		}
		if valid+8+size > len(raw) {
			break
		}
		valid += 8 + size
	}
	if valid != len(raw) && len(raw) <= maxDiagnosticRead {
		return "", false, ErrDiagnosticRead
	}
	var out strings.Builder
	if _, err := stdcopy.StdCopy(&out, &out, bytes.NewReader(raw[:valid])); err != nil {
		return "", false, err
	}
	return out.String(), valid != len(raw), nil
}

func diagnosticLogTail(text string, truncated bool) (string, bool) {
	text = strings.ToValidUTF8(redact(text), "")
	if len(text) > 8192 {
		text = strings.ToValidUTF8(text[len(text)-8192:], "")
		truncated = true
	}
	return text, truncated
}

func (t *assistantTool) platformDiagnostics(ctx context.Context, input toolInput) (any, error) {
	for _, p := range []string{access.PermissionAssistantPlatformRead, "monitor.read"} {
		if err := t.require(ctx, p); err != nil {
			return nil, err
		}
	}
	if t.service.deps.Metrics == nil || t.service.deps.RuntimeLogs == nil {
		return nil, t.service.failure(ctx, "assistant_platform_unavailable", ErrDiagnosticRead, ErrDiagnosticRead)
	}
	snapshot := t.service.deps.Metrics.Snapshot(ctx)
	entries, more, err := t.service.deps.RuntimeLogs.List(logging.Query{Limit: 30, Text: input.Query})
	if err != nil {
		return nil, t.service.failure(ctx, "assistant_platform_logs", err, ErrDiagnosticRead)
	}
	rows := []map[string]any{}
	for _, e := range entries {
		fields := map[string]string{}
		for _, key := range []string{"request_id", "operation", "project_id", "resource_id", "pipeline_run_id", "deployment_id", "job_id", "err", "error_code"} {
			if value, ok := e.Fields[key]; ok {
				fields[key] = safeText(redact(value), 512)
			}
		}
		rows = append(rows, map[string]any{"id": e.ID, "created_at": e.CreatedAt, "level": e.Level, "operation": e.Operation, "message": safeText(redact(e.Message), 768), "fields": fields})
	}
	return map[string]any{"metrics": snapshot, "system_logs": rows, "more_logs": more, "scope": "EDO平台，不是应用日志"}, nil
}
