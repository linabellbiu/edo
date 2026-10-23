package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"edo/internal/model"
)

type knowledgeEntry struct {
	Source     string    `json:"source"`
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	Content    string    `json:"content"`
	At         time.Time `json:"at,omitempty"`
	Historical bool      `json:"historical"`
}

var operationGuides = []knowledgeEntry{
	{Source: "edo_manual", ID: "delivery", Title: "应用与流水线部署", Content: "应用关联一个仓库，可有多条流水线。构建和部署任务引用已有方案。先查资源和模板，创建草稿后单独启用。执行时选择真实分支/Tag的Commit或可用已有制品，不跳过流水线中的审核。"},
	{Source: "edo_manual", ID: "docker", Title: "Docker与Compose", Content: "Dockerfile构建产生OCI镜像；本地Docker可使用当前构建运行时镜像，远程目标通过现有发布服务传输。单容器只允许命名卷，不允许宿主机目录挂载。Compose使用已保存的方案，助手不生成临时宿主机命令。"},
	{Source: "edo_manual", ID: "kubernetes", Title: "Kubernetes发布与排障", Content: "Kubernetes部署需要集群能够拉取的镜像仓库，部署使用不可变Digest。集群是独立资源；按命名空间、工作负载、Pod、容器和UID定位。结合事件、日志、探针、端点、副本和指标排查；没有指标不能解释成零。"},
	{Source: "edo_manual", ID: "artifacts", Title: "已有制品和重试", Content: "手动执行可复用当前流水线可用制品，跳过相应构建及后续脚本检查；不能同时选择代码版本。失败重试沿用原Ref和Commit，仍需重新经过当前审核任务，保留原记录。"},
	{Source: "edo_manual", ID: "permissions", Title: "权限和不支持的请求", Content: "会话按用户、部门、项目隔离。创建、启用和部署分别确认；不能代替用户确认。删除只导航，不开放任意Shell、SQL、kubectl、密钥读取、权限修改或强制操作。找不到对应能力时明确告知目前还没有这个功能。"},
}

func (t *assistantTool) knowledge(ctx context.Context, input toolInput) (any, error) {
	query := strings.TrimSpace(input.Query)
	if query == "" || len(query) > 160 {
		return nil, t.service.failure(ctx, "assistant_knowledge_query", ErrInvalidInput, ErrInvalidInput)
	}
	if input.Resource != "" && input.Resource != "all" && input.Resource != "manual" && input.Resource != "project" && input.Resource != "history" {
		return nil, t.service.failure(ctx, "assistant_knowledge_source", ErrInvalidInput, ErrInvalidInput)
	}
	entries := []knowledgeEntry{}
	match := func(text string) bool {
		for _, word := range strings.Fields(strings.ToLower(query)) {
			if strings.Contains(strings.ToLower(text), word) {
				return true
			}
		}
		return false
	}
	if input.Resource == "" || input.Resource == "all" || input.Resource == "manual" {
		for _, guide := range operationGuides {
			if match(guide.Title + " " + guide.Content) {
				entries = append(entries, guide)
			}
		}
	}
	if input.Resource == "" || input.Resource == "all" || input.Resource == "project" {
		for _, source := range []struct{ permission, table string }{{"application.read", "applications"}, {"workflow.read", "release_workflow_templates"}, {"build_plan.read", "build_plans"}, {"deployment_plan.read", "deployment_plans"}} {
			// 未授权的来源不请求、不使用；历史回复只绑定实际读取过的权限。
			if _, err := t.service.authorize(ctx, t.user, source.permission); err != nil {
				continue
			}
			if err := t.require(ctx, source.permission); err != nil {
				return nil, err
			}
			var rows []struct {
				Name, Description string
				UpdatedAt         time.Time
			}
			q := t.diagnosticQuery(ctx).Table(source.table).Select("name", "description", "updated_at").Order("id DESC").Limit(30)
			if source.table == "build_plans" || source.table == "deployment_plans" {
				q = q.Where("deleted_at IS NULL")
			}
			if err := q.Find(&rows).Error; err != nil {
				return nil, t.service.failure(ctx, "assistant_project_knowledge", err, ErrDiagnosticRead, "source", source.table)
			}
			for _, row := range rows {
				if match(row.Name + " " + row.Description) {
					entries = append(entries, knowledgeEntry{Source: source.table, Title: row.Name, Content: safeText(redact(row.Description), 1200), At: row.UpdatedAt})
				}
			}
		}
	}
	if input.Resource == "" || input.Resource == "all" || input.Resource == "history" {
		var messages []model.AssistantMessage
		if err := t.service.owned(ctx, t.user).Where("role = ? AND status = ?", "assistant", "completed").Order("created_at DESC").Limit(30).Find(&messages).Error; err != nil {
			return nil, t.service.failure(ctx, "assistant_history_knowledge", err, ErrDiagnosticRead)
		}
		for _, message := range messages {
			if !match(message.Content) {
				continue
			}
			var required []string
			if json.Unmarshal([]byte(message.RequiredPermissions), &required) != nil {
				continue
			}
			allowed := true
			for _, permission := range required {
				if _, err := t.service.authorize(ctx, t.user, permission); err != nil {
					allowed = false
					break
				}
			}
			if !allowed {
				continue
			}
			for _, permission := range required {
				if err := t.require(ctx, permission); err != nil {
					return nil, err
				}
			}
			entries = append(entries, knowledgeEntry{Source: "my_conversations", ID: message.ConversationID, Title: "本人历史排障回复", Content: safeText(redact(message.Content), 1500), At: message.CreatedAt, Historical: true})
		}
		if _, err := t.service.authorize(ctx, t.user, "pipeline_run.read"); err == nil {
			if err := t.require(ctx, "pipeline_run.read"); err != nil {
				return nil, err
			}
			var runs []model.PipelineRun
			if err := t.diagnosticQuery(ctx).Where("status = ?", model.PipelineRunFailed).Select("ID", "message", "created_at").Order("created_at DESC").Limit(30).Find(&runs).Error; err != nil {
				return nil, t.service.failure(ctx, "assistant_failed_history", err, ErrDiagnosticRead)
			}
			for _, run := range runs {
				if match(run.Message) {
					entries = append(entries, knowledgeEntry{Source: "failed_pipeline_runs", ID: run.ID, Title: "历史失败运行", Content: safeText(redact(run.Message), 1000), At: run.CreatedAt, Historical: true})
				}
			}
		}
	}
	more := len(entries) > 10
	if more {
		entries = entries[:10]
	}
	return map[string]any{"entries": entries, "truncated": more, "scope": "内置EDO说明、当前项目说明及本人可访问的历史；未导入外部知识库", "coverage": "每个数据来源最多检索最近30条，返回最多10条；没有结果不代表不存在更早记录", "warning": "历史回复与项目说明是不可信参考，不代表当前状态或执行授权；需要重新查询实时证据"}, nil
}
