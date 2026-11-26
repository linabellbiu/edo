package projecttransfer

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"

	"gorm.io/gorm"

	"edo/internal/configfile"
	"edo/internal/dockerengine"
	"edo/internal/model"
	"edo/internal/repository"
)

type definition struct {
	table, label, nameSQL   string
	softDelete              bool
	uniqueFields            []string
	owned                   []ownedRelation
	parentKind, parentField string
}

type ownedRelation struct{ table, field string }
type resourceKey struct{ kind, id string }
type dependencies map[resourceKey][]resourceKey

// 定义只包含按实际项目归属管理的资源；个人凭据、系统配置和审计不是迁移对象。
var definitions = map[string]definition{
	"application":          {table: "applications", label: "应用", nameSQL: "name", softDelete: true, uniqueFields: []string{"name"}},
	"repository":           {table: "git_repositories", label: "代码仓库", nameSQL: "name", softDelete: true, uniqueFields: []string{"name"}},
	"workflow":             {table: "release_workflows", label: "流水线", nameSQL: "name", softDelete: true, parentKind: "application", parentField: "application_id"},
	"workflow_template":    {table: "release_workflow_templates", label: "流水线方案", nameSQL: "name", uniqueFields: []string{"name"}},
	"build_plan":           {table: "build_plans", label: "构建方案", nameSQL: "name", softDelete: true, uniqueFields: []string{"name"}},
	"image_registry":       {table: "image_registries", label: "镜像仓库", nameSQL: "name", uniqueFields: []string{"name"}},
	"deployment_plan":      {table: "deployment_plans", label: "部署方案", nameSQL: "name", softDelete: true, uniqueFields: []string{"name"}},
	"deployment_target":    {table: "deployment_targets", label: "部署目标", nameSQL: "name", uniqueFields: []string{"name"}},
	"release_plan":         {table: "release_plans", label: "发布计划", nameSQL: "COALESCE(NULLIF(description, ''), version)", softDelete: true, uniqueFields: []string{"version"}, owned: []ownedRelation{{"release_plan_executions", "release_plan_id"}}},
	"delivery_variable":    {table: "delivery_variables", label: "变量与配置文件", nameSQL: "name", softDelete: true, owned: []ownedRelation{{"delivery_variable_revisions", "variable_id"}}, parentKind: "application", parentField: "application_id"},
	"application_database": {table: "application_databases", label: "应用数据库", nameSQL: "name", softDelete: true, parentKind: "application", parentField: "application_id", owned: []ownedRelation{{"application_database_migrations", "database_id"}}},
	"environment":          {table: "environments", label: "主机组", nameSQL: "name", uniqueFields: []string{"name"}, owned: []ownedRelation{{"environment_release_policies", "environment_id"}}},
	"host":                 {table: "hosts", label: "主机", nameSQL: "name", uniqueFields: []string{"name"}},
	"cluster":              {table: "kubernetes_clusters", label: "Kubernetes 集群", nameSQL: "name", uniqueFields: []string{"name"}, owned: []ownedRelation{{"kubernetes_rbac_policies", "cluster_id"}}},
	"docker_endpoint":      {table: "docker_endpoints", label: "Docker 连接", nameSQL: "name", uniqueFields: []string{"name"}},
	"notification_channel": {table: "notification_channels", label: "通知渠道", nameSQL: "name", uniqueFields: []string{"name"}},
	"dns_provider_account": {table: "dns_provider_accounts", label: "DNS 厂商账号", nameSQL: "name", uniqueFields: []string{"name"}},
	"dns_domain":           {table: "dns_domains", label: "域名", nameSQL: "name"},
}

var externalDependencyLabels = map[string]string{
	"active_pipeline_run":        "未结束的流水线运行",
	"active_build":               "未结束的构建",
	"pending_repository_check":   "待回写的仓库检查",
	"delivery_default":           "项目默认部署设置",
	"environment_release_policy": "环境发布策略",
	"kubernetes_rbac_policy":     "集群访问授权",
	"kubernetes_role_grant":      "集群角色授权",
	"kubernetes_rbac_subject":    "集群用户授权",
	"monitor_rule":               "监控规则",
	"queued_notification":        "待发送通知",
	"scheduled_task":             "定时任务",
	"sql_change":                 "SQL 变更工单",
}

func (d dependencies) add(from resourceKey, kind, id string) {
	to := resourceKey{kind, id}
	if from.id != "" && to.id != "" && from != to {
		d[from] = append(d[from], to)
	}
}

type edgeRow struct{ SourceID, RequiredID string }

// 简单关系直接按实际列/关联表读取，不对整个 JSON 做字符串匹配。
// 仅保留仍能影响配置或执行的引用；运行中的任务和不可变历史由迁移边界单独处理。
type directRelation struct {
	from, to, table, requiredColumn, where string
	deleted                                bool
}

var directRelations = []directRelation{
	{"application", "repository", "applications", "repository_id", "", true},
	{"workflow", "application", "release_workflows", "application_id", "", true},
	{"workflow", "workflow_template", "release_workflows", "workflow_template_id", "", true},
	{"build_plan", "image_registry", "build_plans", "image_registry_id", "", true},
	{"deployment_plan", "deployment_target", "deployment_plans", "deployment_target_id", "", true},
	{"delivery_variable", "application", "delivery_variables", "application_id", "", true},
	{"delivery_variable", "environment", "delivery_variables", "environment_id", "", true},
	{"application_database", "application", "application_databases", "application_id", "", true},
	{"application_database", "environment", "application_databases", "environment_id", "", true},
	{"environment", "environment", "environments", "parent_id", "", false},
	{"docker_endpoint", "host", "docker_endpoints", "host_id", "", false},
	{"dns_domain", "dns_provider_account", "dns_domains", "account_id", "", false},
	{"delivery_default", "workflow_template", "delivery_defaults", "workflow_template_id", "", false},
	{"delivery_default", "deployment_plan", "delivery_defaults", "deployment_plan_id", "", false},
	{"environment_release_policy", "environment", "environment_release_policies", "environment_id", "is_active = TRUE", false},
	{"kubernetes_rbac_policy", "cluster", "kubernetes_rbac_policies", "cluster_id", "(enabled = TRUE OR desired_enabled = TRUE OR status <> 'disabled')", false},
	{"kubernetes_role_grant", "cluster", "kubernetes_role_grants", "cluster_id", "", false},
	{"kubernetes_rbac_subject", "cluster", "kubernetes_rbac_subjects", "cluster_id", "", false},
	{"monitor_rule", "notification_channel", "monitor_rules", "notification_channel_id", "", false},
	{"queued_notification", "notification_channel", "notifications", "channel_id", "status = 'queued'", false},
	{"sql_change", "application", "sql_changes", "application_id", "success_at IS NULL AND status <> 'succeeded'", true},
	{"sql_change", "application_database", "sql_changes", "database_id", "success_at IS NULL AND status <> 'succeeded'", true},
	{"sql_change", "environment", "sql_changes", "environment_id", "success_at IS NULL AND status <> 'succeeded'", true},
	{"active_build", "build_plan", "build_runs", "build_plan_id", "status IN ('queued', 'running')", false},
	{"active_build", "repository", "build_runs", "repository_id", "status IN ('queued', 'running')", false},
	{"pending_repository_check", "repository", "repository_check_deliveries", "repository_id", "status = 'pending'", false},
}

// 指定实际项目集合；使用显式范围，避免继承请求当前项目的默认过滤。
func transferTable(tx *gorm.DB, projectIDs []string, table string, softDelete bool) *gorm.DB {
	query := tx.Session(&gorm.Session{NewDB: true}).Table(table).Where(table+".project_id IN ?", projectIDs)
	if softDelete {
		query = query.Where(table + ".deleted_at IS NULL")
	}
	return query
}

func discoverDependencies(tx *gorm.DB, projectIDs []string) (dependencies, error) {
	graph := dependencies{}
	if len(projectIDs) == 0 {
		return graph, nil
	}
	for _, relation := range directRelations {
		query := transferTable(tx, projectIDs, relation.table, relation.deleted).
			Select("uuid AS source_id, " + relation.requiredColumn + " AS required_id").
			Where(relation.requiredColumn + " <> ''")
		if relation.where != "" {
			query = query.Where(relation.where)
		}
		var rows []edgeRow
		if err := query.Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("读取 %s 的迁移依赖: %w", relation.table, err)
		}
		for _, row := range rows {
			graph.add(resourceKey{relation.from, row.SourceID}, relation.to, row.RequiredID)
		}
	}
	for _, relation := range []struct{ from, to, sql string }{
		{"application", "repository", `SELECT a.uuid AS source_id, ar.repository_id AS required_id FROM applications a JOIN application_repositories ar ON ar.application_id = a.uuid WHERE a.project_id IN ? AND a.deleted_at IS NULL`},
		{"release_plan", "application", `SELECT rp.uuid AS source_id, ga.application_id AS required_id FROM release_plans rp JOIN release_groups rg ON rg.release_plan_id = rp.uuid JOIN release_group_applications ga ON ga.release_group_id = rg.uuid WHERE rp.project_id IN ? AND rp.deleted_at IS NULL`},
		{"environment", "host", `SELECT e.uuid AS source_id, eh.host_id AS required_id FROM environments e JOIN environment_hosts eh ON eh.environment_id = e.uuid WHERE e.project_id IN ?`},
		{"host", "cluster", `SELECT h.uuid AS source_id, hc.runtime_id AS required_id FROM hosts h JOIN host_capabilities hc ON hc.host_id = h.uuid WHERE h.project_id IN ? AND hc.kind = 'kubernetes' AND hc.runtime_id <> ''`},
		{"host", "docker_endpoint", `SELECT h.uuid AS source_id, hc.runtime_id AS required_id FROM hosts h JOIN host_capabilities hc ON hc.host_id = h.uuid WHERE h.project_id IN ? AND hc.kind = 'docker' AND hc.runtime_id <> ''`},
		{"application", "image_registry", `SELECT v.application_id AS source_id, v.image_registry_id AS required_id FROM artifacts v JOIN applications a ON a.uuid = v.application_id AND a.project_id = v.project_id WHERE v.project_id IN ? AND v.status = 'available' AND v.storage_kind = 'registry' AND v.image_registry_id <> '' AND a.deleted_at IS NULL`},
		{"application", "deployment_plan", `SELECT a.uuid AS source_id, dp.uuid AS required_id FROM applications a JOIN deployment_instance_controls c ON c.application_id = a.uuid JOIN deployment_plans dp ON dp.uuid = c.deployment_plan_id WHERE a.project_id IN ? AND a.deleted_at IS NULL AND dp.deleted_at IS NULL`},
		{"application", "deployment_target", `SELECT a.uuid AS source_id, dt.uuid AS required_id FROM applications a JOIN deployment_instance_controls c ON c.application_id = a.uuid JOIN deployment_targets dt ON dt.uuid = c.target_id WHERE a.project_id IN ? AND a.deleted_at IS NULL`},
	} {
		var rows []edgeRow
		if err := tx.Session(&gorm.Session{NewDB: true}).Raw(relation.sql, projectIDs).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("读取 %s 到 %s 的迁移依赖: %w", relation.from, relation.to, err)
		}
		for _, row := range rows {
			if relation.to == "docker_endpoint" && dockerengine.IsLocalEndpointID(row.RequiredID) {
				continue
			}
			graph.add(resourceKey{relation.from, row.SourceID}, relation.to, row.RequiredID)
		}
	}
	if err := graph.discoverConfigurations(tx, projectIDs); err != nil {
		return nil, err
	}
	if err := graph.discoverRunDependencies(tx, projectIDs); err != nil {
		return nil, err
	}
	for key, edges := range graph {
		slices.SortFunc(edges, func(a, b resourceKey) int {
			if n := cmp.Compare(a.kind, b.kind); n != 0 {
				return n
			}
			return cmp.Compare(a.id, b.id)
		})
		graph[key] = slices.Compact(edges)
	}
	return graph, nil
}

// 普通配置换源后，未结束运行仍从已固定的仓库、变量和运行目标取数据。
func (d dependencies) discoverRunDependencies(tx *gorm.DB, projectIDs []string) error {
	terminal := []model.PipelineRunStatus{model.PipelineRunSucceeded, model.PipelineRunFailed, model.PipelineRunCanceled, model.PipelineRunSkipped}
	var links []struct {
		RunID, ApplicationID, RepositoryID, BuildPlanID, ImageRegistryID, DeploymentPlanID string
		Status                                                                             model.PipelineRunStatus
	}
	if err := tx.Session(&gorm.Session{NewDB: true}).Raw(`SELECT pr.pipeline_run_id AS run_id, pipeline_runs.application_id, pipeline_runs.status,
		pr.repository_id, pr.build_plan_id, pr.image_registry_id, pr.release_plan_id AS deployment_plan_id
		FROM pipeline_run_repositories pr JOIN pipeline_runs ON pipeline_runs.uuid = pr.pipeline_run_id
		WHERE pipeline_runs.project_id IN ? AND (pipeline_runs.status NOT IN ? OR
		(pipeline_runs.status_writeback = TRUE AND pipeline_runs.repository_check_state <> `+repository.ExpectedCheckStateSQL+`))`, projectIDs, terminal).Scan(&links).Error; err != nil {
		return fmt.Errorf("读取运行固定的仓库依赖: %w", err)
	}
	for _, link := range links {
		if slices.Contains(terminal, link.Status) {
			key := resourceKey{"pending_repository_check", link.RunID}
			d.add(key, "application", link.ApplicationID)
			d.add(key, "repository", link.RepositoryID)
			continue
		}
		key := resourceKey{"active_pipeline_run", link.RunID}
		d.add(key, "application", link.ApplicationID)
		d.add(key, "repository", link.RepositoryID)
		d.add(key, "build_plan", link.BuildPlanID)
		d.add(key, "image_registry", link.ImageRegistryID)
		d.add(key, "deployment_plan", link.DeploymentPlanID)
	}
	var runs []model.PipelineRun
	if err := transferTable(tx, projectIDs, "pipeline_runs", false).Select("uuid, application_id, workflow_id, workflow_snapshot").
		Where("status NOT IN ? AND workflow_snapshot <> ''", terminal).Scan(&runs).Error; err != nil {
		return fmt.Errorf("读取未结束的流水线快照: %w", err)
	}
	for _, run := range runs {
		// 只解析快照中用于定位已经冻结的资源字段，不依赖执行脚本或凭据正文。
		var snapshot struct {
			Source                model.WorkflowNode                         `json:"source"`
			Stages                []model.WorkflowStage                      `json:"stages"`
			VariableBindings      map[string][]model.DeliveryVariableBinding `json:"variable_bindings"`
			ConfigurationBindings map[string][]model.DeploymentConfigBinding `json:"configuration_bindings"`
			BuildPlans            map[string]struct {
				ID              string `json:"id"`
				ImageRegistryID string `json:"image_registry_id"`
			} `json:"build_plans"`
			DeploymentPlans map[string]struct {
				ID           string `json:"id"`
				SourcePlanID string `json:"source_plan_id"`
			} `json:"deployment_plans"`
			DeploymentTargets map[string]model.DeploymentTarget `json:"deployment_targets"`
			RegistryInputs    map[string][]struct {
				RegistryID string `json:"registry_id"`
			} `json:"registry_inputs"`
		}
		if err := json.Unmarshal([]byte(run.WorkflowSnapshot), &snapshot); err != nil {
			return fmt.Errorf("解析未结束的流水线快照: %w", err)
		}
		key := resourceKey{"active_pipeline_run", run.ID}
		d.add(key, "workflow", run.WorkflowID)
		d.addStages(key, nil, run.ApplicationID, snapshot.Source, snapshot.Stages)
		for _, bindings := range snapshot.VariableBindings {
			for _, binding := range bindings {
				d.add(key, "delivery_variable", binding.VariableID)
			}
		}
		for _, bindings := range snapshot.ConfigurationBindings {
			for _, binding := range bindings {
				d.add(key, "delivery_variable", binding.VariableID)
			}
		}
		for _, build := range snapshot.BuildPlans {
			d.add(key, "build_plan", build.ID)
			d.add(key, "image_registry", build.ImageRegistryID)
		}
		for _, plan := range snapshot.DeploymentPlans {
			d.add(key, "deployment_plan", plan.ID)
			d.add(key, "deployment_plan", plan.SourcePlanID)
		}
		for _, target := range snapshot.DeploymentTargets {
			d.add(key, "deployment_target", target.ID)
			d.addTarget(key, &target)
		}
		for _, inputs := range snapshot.RegistryInputs {
			for _, input := range inputs {
				d.add(key, "image_registry", input.RegistryID)
			}
		}
	}
	return nil
}

// 仅选取依赖相关的字段，不读取凭据、密文、变量正文或运行输出。
func (d dependencies) discoverConfigurations(tx *gorm.DB, projectIDs []string) error {
	var targets []model.DeploymentTarget
	if err := transferTable(tx, projectIDs, "deployment_targets", false).
		Select("uuid, platform, environment_id, host_id, host_ids, runtime_id").Scan(&targets).Error; err != nil {
		return fmt.Errorf("读取部署目标依赖: %w", err)
	}
	for _, target := range targets {
		d.addTarget(resourceKey{"deployment_target", target.ID}, &target)
	}

	var variables []model.DeliveryVariable
	if err := transferTable(tx, projectIDs, "delivery_variables", true).
		Select("uuid, project_id, application_id, environment_id, name, kind, usage, auto_deploy").Scan(&variables).Error; err != nil {
		return fmt.Errorf("读取交付变量依赖: %w", err)
	}
	varIndex := make(map[string]map[string][]model.DeliveryVariable)
	for _, variable := range variables {
		if configfile.ValidKind(variable.Kind) {
			continue
		}
		if varIndex[variable.ProjectID] == nil {
			varIndex[variable.ProjectID] = make(map[string][]model.DeliveryVariable)
		}
		varIndex[variable.ProjectID][variable.Name] = append(varIndex[variable.ProjectID][variable.Name], variable)
	}

	var plans []model.DeploymentPlan
	if err := transferTable(tx, projectIDs, "deployment_plans", true).
		Select("uuid, project_id, execution_config").Scan(&plans).Error; err != nil {
		return fmt.Errorf("读取部署方案配置依赖: %w", err)
	}
	for _, plan := range plans {
		if plan.ExecutionConfig != nil {
			d.addNodeConfig(resourceKey{"deployment_plan", plan.ID}, varIndex[plan.ProjectID], "", model.WorkflowNodeDeploy, plan.ExecutionConfig.NodeConfig())
		}
	}

	var workflows []model.ReleaseWorkflow
	if err := transferTable(tx, projectIDs, "release_workflows", true).
		Select("uuid, project_id, application_id, source, stages").Scan(&workflows).Error; err != nil {
		return fmt.Errorf("读取流水线配置依赖: %w", err)
	}
	for _, workflow := range workflows {
		d.addStages(resourceKey{"workflow", workflow.ID}, varIndex[workflow.ProjectID], workflow.ApplicationID, workflow.Source, workflow.Stages)
	}

	var templates []model.ReleaseWorkflowTemplate
	if err := transferTable(tx, projectIDs, "release_workflow_templates", false).
		Select("uuid, project_id, source, stages").Scan(&templates).Error; err != nil {
		return fmt.Errorf("读取流水线方案配置依赖: %w", err)
	}
	for _, template := range templates {
		d.addStages(resourceKey{"workflow_template", template.ID}, varIndex[template.ProjectID], "", template.Source, template.Stages)
	}

	var schedules []model.ScheduledTask
	if err := transferTable(tx, projectIDs, "scheduled_tasks", false).
		Select("uuid, payload").Where("action = ?", model.ScheduleNotification).Scan(&schedules).Error; err != nil {
		return fmt.Errorf("读取定时通知依赖: %w", err)
	}
	for _, schedule := range schedules {
		var payload struct {
			ChannelID string `json:"channel_id"`
		}
		if err := json.Unmarshal(schedule.Payload, &payload); err != nil {
			return fmt.Errorf("解析定时通知依赖: %w", err)
		}
		d.add(resourceKey{"scheduled_task", schedule.ID}, "notification_channel", payload.ChannelID)
	}
	return nil
}

func (d dependencies) addStages(key resourceKey, vars map[string][]model.DeliveryVariable, applicationID string, source model.WorkflowNode, stages []model.WorkflowStage) {
	d.addNodeConfig(key, vars, applicationID, source.Type, source.Config)
	for _, stage := range stages {
		for _, task := range stage.Tasks {
			// 停用任务的保存配置仍可恢复，不裁剪其引用。
			d.addNodeConfig(key, vars, applicationID, task.Type, task.Config)
		}
	}
}

func (d dependencies) addNodeConfig(key resourceKey, vars map[string][]model.DeliveryVariable, applicationID string, nodeType model.WorkflowNodeType, config model.WorkflowNodeConfig) {
	d.add(key, "build_plan", config.BuildPlanID)
	d.add(key, "build_plan", config.ArtifactBuildPlanID)
	d.add(key, "deployment_plan", config.DeploymentPlanID)
	d.add(key, "image_registry", config.DeploymentRegistryID)
	d.add(key, "environment", config.VariableEnvironmentID)
	d.add(key, "host", config.ExecutionHostID)
	if config.InlineBuild != nil {
		d.add(key, "image_registry", config.InlineBuild.ImageRegistryID)
	}
	if config.InlineDeploymentPlan != nil {
		d.addTarget(key, config.InlineDeploymentPlan.DeploymentTarget)
		if execution := config.InlineDeploymentPlan.ExecutionConfig; execution != nil {
			d.addNodeConfig(key, vars, applicationID, nodeType, execution.NodeConfig())
		}
	}
	if override := config.DeploymentOverride; override != nil {
		if override.DeploymentTarget != nil {
			// 现有覆盖协议允许省略平台，仍按 Kubernetes 选定集群解析。
			target := *override.DeploymentTarget
			target.Platform = model.DeploymentKubernetes
			d.addTarget(key, &target)
		}
		if override.DeploymentRegistryID != nil {
			d.add(key, "image_registry", *override.DeploymentRegistryID)
		}
		if override.DeploymentInputs != nil {
			d.addInputs(key, *override.DeploymentInputs)
		}
		if snapshot := override.PlanSnapshot; snapshot != nil {
			d.add(key, "deployment_target", snapshot.DeploymentTargetID)
			d.addTarget(key, snapshot.DeploymentTarget)
			if snapshot.ExecutionConfig != nil {
				d.addNodeConfig(key, vars, applicationID, nodeType, snapshot.ExecutionConfig.NodeConfig())
			}
		}
	}
	for _, rule := range config.Notifications {
		d.add(key, "notification_channel", rule.ChannelID)
	}
	if config.Migration != nil {
		d.add(key, "application_database", config.Migration.DatabaseID)
	}
	for _, file := range config.ConfigurationFiles {
		d.add(key, "delivery_variable", file.ID)
	}
	if config.ComposeSource != nil {
		d.add(key, "delivery_variable", config.ComposeSource.ConfigurationID)
	}
	d.addInputs(key, config.DeploymentInputs)
	usage := "build"
	if nodeType == model.WorkflowNodeDeploy {
		usage = "deploy"
	}
	for _, name := range config.VariableNames {
		for _, variable := range vars[name] {
			if variable.Usage == usage && (variable.ApplicationID == "" || variable.ApplicationID == applicationID) &&
				(variable.EnvironmentID == "" || nodeType == model.WorkflowNodeDeploy || variable.EnvironmentID == config.VariableEnvironmentID) {
				d.add(key, "delivery_variable", variable.ID)
			}
		}
	}
}

func (d dependencies) addInputs(key resourceKey, inputs []model.DeploymentInput) {
	for _, input := range inputs {
		d.add(key, "delivery_variable", input.ConfigurationID)
		d.add(key, "image_registry", input.RegistryID)
		d.add(key, "build_plan", input.ArtifactBuildPlanID)
	}
}

func (d dependencies) addTarget(key resourceKey, target *model.DeploymentTarget) {
	if target == nil {
		return
	}
	d.add(key, "environment", target.EnvironmentID)
	d.add(key, "host", target.HostID)
	for _, id := range target.HostIDs {
		d.add(key, "host", id)
	}
	switch target.Platform {
	case model.DeploymentKubernetes:
		d.add(key, "cluster", target.RuntimeID)
	case model.DeploymentDocker:
		if dockerengine.IsLocalEndpointID(target.RuntimeID) {
			d.add(key, "host", model.BuiltinLocalHostID)
		} else {
			d.add(key, "docker_endpoint", target.RuntimeID)
		}
	}
}
