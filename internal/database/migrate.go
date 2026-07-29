package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"zrt/internal/model"
)

type migration struct {
	version string
	up      func(*gorm.DB) error
}

var migrations = []migration{
	{
		version: "202607230001",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.Job{}, &model.OutboxEvent{})
		},
	},
	{
		version: "202607230002",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.User{})
		},
	},
	{
		version: "202607230003",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(
				&model.Role{},
				&model.RolePermission{},
				&model.UserRole{},
				&model.AuditLog{},
			)
		},
	},
	{
		version: "202607230004",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.Job{})
		},
	},
	{
		version: "202607230005",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.GitRepository{}, &model.GitWebhookDelivery{})
		},
	},
	{
		version: "202607230006",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.DockerEndpoint{}, &model.KubernetesCluster{})
		},
	},
	{
		version: "202607230007",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.DeploymentTarget{}, &model.DeploymentRecord{})
		},
	},
	{
		version: "202607230008",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.Configuration{}, &model.ConfigurationRevision{})
		},
	},
	{
		version: "202607230009",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.NotificationChannel{}, &model.Notification{})
		},
	},
	{
		version: "202607230010",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.ScheduledTask{}, &model.MonitorRule{}, &model.MonitorCheck{})
		},
	},
	{
		version: "202607230011",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.Job{})
		},
	},
	{
		version: "202607230012",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.User{})
		},
	},
	{
		version: "202607230013",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(
				&model.BuildPlan{}, &model.ImageRegistry{}, &model.DeploymentPlan{},
				&model.Application{}, &model.PipelineRun{},
			)
		},
	},
	{
		version: "202607230014",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.IdentityProvider{}, &model.ExternalIdentity{})
		},
	},
	{
		version: "202607230015",
		up: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(
				&model.Application{}, &model.ApplicationEnvironment{},
				&model.ReleaseWorkflow{}, &model.PipelineRun{}, &model.PipelineRunApproval{},
			); err != nil {
				return err
			}
			return backfillApplicationEnvironments(tx)
		},
	},
	{
		version: "202607230016",
		up: func(tx *gorm.DB) error {
			return dropOptionalDeliveryConstraints(tx)
		},
	},
	{
		version: "202607240017",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(
				&model.ReleaseWorkflowTemplate{}, &model.Application{}, &model.ReleaseWorkflow{},
			)
		},
	},
	{
		version: "202607240018",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(
				&model.UserPermission{}, &model.GitCredential{}, &model.GitRepository{},
			)
		},
	},
	{
		version: "202607240019",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.DNSProviderAccount{}, &model.DNSDomain{})
		},
	},
	{
		version: "202607270020",
		up: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(
				&model.GitRepository{}, &model.Application{}, &model.ApplicationRepository{}, &model.ApplicationRepositoryObservation{},
				&model.PipelineRun{}, &model.PipelineRunRepository{},
			); err != nil {
				return err
			}
			return backfillApplicationRepositories(tx)
		},
	},
	{
		version: "202607270021",
		up:      migrateReleasePlanning,
	},
	{
		version: "202607270022",
		up:      migrateDeploymentEnvironmentFields,
	},
	{
		version: "202607270023",
		up:      migratePipelineExecutionFields,
	},
	{
		version: "202607270024",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.PipelineRun{})
		},
	},
	{
		version: "202607270025",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.PipelineRunLog{})
		},
	},
	{
		version: "202607270026",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.ReleaseGroupApplication{})
		},
	},
	{
		version: "202607270027",
		up:      migrateRepositoryObservationWatchKeys,
	},
	{
		version: "202607280028",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.PipelineRun{})
		},
	},
	{
		version: "202607280029",
		up:      migrateDeploymentApprovalsToWorkflow,
	},
	{
		version: "202607280030",
		up:      migrateHostsAndEnvironments,
	},
	{
		version: "202607280031",
		up:      migrateSSHDeploymentTargets,
	},
	{
		version: "202607290032",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(
				&model.PipelineRun{}, &model.ReleasePlanExecution{}, &model.ReleasePlanExecutionItem{},
			)
		},
	},
	{
		version: "202607290033",
		up:      migrateDeploymentPlanTargets,
	},
	{
		version: "202607290034",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.ReleasePlan{})
		},
	},
	{
		version: "202607290035",
		up:      migrateManualReleaseNodesToTriggerEvents,
	},
	{
		version: "202607290036",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.ReleasePlan{})
		},
	},
	{
		version: "202607290037",
		up:      backfillDeploymentPlanTargets,
	},
	{
		version: "202607290038",
		up: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.BuildPlan{})
		},
	},
	{
		version: "202607290039",
		up:      migrateEnvironmentHosts,
	},
}

func migrateEnvironmentHosts(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&model.EnvironmentHost{}); err != nil {
		return err
	}
	if !tx.Migrator().HasTable(&model.Host{}) || !tx.Migrator().HasColumn(&model.Host{}, "EnvironmentID") {
		return nil
	}
	var legacy []struct {
		HostID        string    `gorm:"column:host_id"`
		EnvironmentID string    `gorm:"column:environment_id"`
		CreatedAt     time.Time `gorm:"column:created_at"`
	}
	if err := tx.Model(&model.Host{}).
		Select("id AS host_id", "environment_id", "created_at").
		Where("environment_id <> ''").
		Scan(&legacy).Error; err != nil {
		return err
	}
	for i := range legacy {
		createdAt := legacy[i].CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		membership := model.EnvironmentHost{
			EnvironmentID: legacy[i].EnvironmentID,
			HostID:        legacy[i].HostID,
			CreatedAt:     createdAt,
		}
		if err := tx.Where(
			"environment_id = ? AND host_id = ?", membership.EnvironmentID, membership.HostID,
		).FirstOrCreate(&membership).Error; err != nil {
			return err
		}
	}
	// 旧单值列必须清空，避免后续代码或外部工具把它误认为权威关系。
	return tx.Model(&model.Host{}).Where("environment_id <> ''").Update("environment_id", "").Error
}

func migrateDeploymentPlanTargets(tx *gorm.DB) error {
	if err := addColumns(tx, &model.DeploymentPlan{}, []string{"DeploymentTargetID"}); err != nil {
		return err
	}
	return addIndexes(tx, &model.DeploymentPlan{}, []string{"DeploymentTargetID"})
}

// backfillDeploymentPlanTargets 恢复聚合部署方案上线前保存在应用上的目标关联。
// 只有一个方案能唯一确定一个历史目标且部署类型一致时才回填，避免把共享方案错误指向某个应用的环境。
func backfillDeploymentPlanTargets(tx *gorm.DB) error {
	type legacyBinding struct {
		DeploymentPlanID   string
		DeploymentTargetID string
	}
	var bindings []legacyBinding
	if err := tx.Model(&model.Application{}).
		Select("release_plan_id AS deployment_plan_id, deployment_target_id").
		Where("release_plan_id <> ? AND deployment_target_id <> ?", "", "").
		Group("release_plan_id, deployment_target_id").
		Scan(&bindings).Error; err != nil {
		return fmt.Errorf("读取部署方案历史目标关联失败: %w", err)
	}
	byPlan := make(map[string][]string)
	for _, binding := range bindings {
		byPlan[binding.DeploymentPlanID] = append(byPlan[binding.DeploymentPlanID], binding.DeploymentTargetID)
	}
	for planID, targetIDs := range byPlan {
		if len(targetIDs) != 1 {
			continue
		}
		var plan model.DeploymentPlan
		if err := tx.Select("id", "kind", "deployment_target_id").First(&plan, "id = ?", planID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return fmt.Errorf("读取待恢复部署方案失败: %w", err)
		}
		if plan.DeploymentTargetID != "" {
			continue
		}
		var target model.DeploymentTarget
		if err := tx.Select("id", "platform").First(&target, "id = ?", targetIDs[0]).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return fmt.Errorf("读取待恢复部署目标失败: %w", err)
		}
		if deploymentPlanPlatform(plan.Kind) != target.Platform {
			continue
		}
		if err := tx.Model(&model.DeploymentPlan{}).
			Where("id = ? AND deployment_target_id = ?", plan.ID, "").
			Update("deployment_target_id", target.ID).Error; err != nil {
			return fmt.Errorf("恢复部署方案目标关联失败: %w", err)
		}
	}
	return nil
}

func deploymentPlanPlatform(kind model.DeploymentPlanKind) model.DeploymentPlatform {
	switch kind {
	case model.DeploymentPlanScript:
		return model.DeploymentSSH
	case model.DeploymentPlanHelm:
		return model.DeploymentKubernetes
	case model.DeploymentPlanDocker, model.DeploymentPlanCompose:
		return model.DeploymentDocker
	default:
		return ""
	}
}

// migrateManualReleaseNodesToTriggerEvents 只规范化仍可编辑的当前流水线和方案。
// 已创建运行的 WorkflowSnapshot 是不可变执行依据，发布计划执行项也必须继续引用原始入口。
func migrateManualReleaseNodesToTriggerEvents(tx *gorm.DB) error {
	templateRevisions := make(map[string]uint64)
	if tx.Migrator().HasTable(&model.ReleaseWorkflowTemplate{}) {
		var templates []model.ReleaseWorkflowTemplate
		if err := tx.Find(&templates).Error; err != nil {
			return fmt.Errorf("读取待迁移流水线方案失败: %w", err)
		}
		for i := range templates {
			nodes, edges, _, changed := migrateManualReleaseGraph(templates[i].Nodes, templates[i].Edges)
			if !changed {
				continue
			}
			nodesJSON, err := json.Marshal(nodes)
			if err != nil {
				return fmt.Errorf("序列化流水线方案节点迁移数据失败: %w", err)
			}
			edgesJSON, err := json.Marshal(edges)
			if err != nil {
				return fmt.Errorf("序列化流水线方案连线迁移数据失败: %w", err)
			}
			nextRevision := templates[i].Revision + 1
			result := tx.Model(&model.ReleaseWorkflowTemplate{}).
				Where("id = ? AND revision = ?", templates[i].ID, templates[i].Revision).
				Updates(map[string]any{
					"nodes": string(nodesJSON), "edges": string(edgesJSON),
					"revision": nextRevision, "updated_at": time.Now().UTC(),
				})
			if result.Error != nil {
				return fmt.Errorf("更新流水线方案手动触发配置失败: %w", result.Error)
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("更新流水线方案手动触发配置失败: 方案版本已经变化")
			}
			templateRevisions[templates[i].ID] = nextRevision
		}
	}

	if !tx.Migrator().HasTable(&model.ReleaseWorkflow{}) {
		return nil
	}
	var workflows []model.ReleaseWorkflow
	if err := tx.Find(&workflows).Error; err != nil {
		return fmt.Errorf("读取待迁移应用流水线失败: %w", err)
	}
	for i := range workflows {
		nodes, edges, sourceRemap, graphChanged := migrateManualReleaseGraph(workflows[i].Nodes, workflows[i].Edges)
		templateRevision, templateChanged := templateRevisions[workflows[i].WorkflowTemplateID]
		if !graphChanged && !templateChanged {
			continue
		}
		nodesJSON, err := json.Marshal(nodes)
		if err != nil {
			return fmt.Errorf("序列化应用流水线节点迁移数据失败: %w", err)
		}
		edgesJSON, err := json.Marshal(edges)
		if err != nil {
			return fmt.Errorf("序列化应用流水线连线迁移数据失败: %w", err)
		}
		updates := map[string]any{
			"nodes": string(nodesJSON), "edges": string(edgesJSON),
			"revision": workflows[i].Revision + 1, "updated_at": time.Now().UTC(),
		}
		if templateChanged {
			updates["workflow_template_revision"] = templateRevision
		}
		result := tx.Model(&model.ReleaseWorkflow{}).
			Where("id = ? AND revision = ?", workflows[i].ID, workflows[i].Revision).
			Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("更新应用流水线手动触发配置失败: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("更新应用流水线手动触发配置失败: 流水线版本已经变化")
		}
		if err := remapBlockedManualRuns(tx, workflows[i].ApplicationID, sourceRemap); err != nil {
			return err
		}
	}
	return nil
}

func migrateManualReleaseGraph(
	nodes []model.WorkflowNode,
	edges []model.WorkflowEdge,
) ([]model.WorkflowNode, []model.WorkflowEdge, map[string]string, bool) {
	migratedNodes := append([]model.WorkflowNode(nil), nodes...)
	for i := range migratedNodes {
		migratedNodes[i].Config.Events = append([]string(nil), migratedNodes[i].Config.Events...)
	}
	migratedEdges := append([]model.WorkflowEdge(nil), edges...)
	nodeIndex := make(map[string]int, len(migratedNodes))
	incomingCount := make(map[string]int, len(migratedNodes))
	outgoing := make(map[string][]model.WorkflowEdge, len(migratedNodes))
	for i := range migratedNodes {
		nodeIndex[migratedNodes[i].ID] = i
	}
	for i := range migratedEdges {
		incomingCount[migratedEdges[i].Target]++
		outgoing[migratedEdges[i].Source] = append(outgoing[migratedEdges[i].Source], migratedEdges[i])
	}

	remap := make(map[string]string)
	changed := false
	for i := range migratedNodes {
		if migratedNodes[i].Type != model.WorkflowNodeManualRelease {
			continue
		}
		manualEdges := outgoing[migratedNodes[i].ID]
		if len(manualEdges) == 1 && incomingCount[migratedNodes[i].ID] == 0 {
			if targetIndex, ok := nodeIndex[manualEdges[0].Target]; ok && migratedNodes[targetIndex].Type == model.WorkflowNodeTrigger {
				if !workflowEventExists(migratedNodes[targetIndex].Config.Events, "manual") {
					migratedNodes[targetIndex].Config.Events = append(migratedNodes[targetIndex].Config.Events, "manual")
				}
				remap[migratedNodes[i].ID] = migratedNodes[targetIndex].ID
				changed = true
				continue
			}
		}
		// 没有可折叠的代码触发下游时保留节点 ID 和全部连线，只改变入口的表达方式。
		migratedNodes[i].Type = model.WorkflowNodeTrigger
		migratedNodes[i].Config.Events = []string{"manual"}
		changed = true
	}
	if !changed {
		return migratedNodes, migratedEdges, remap, false
	}
	if len(remap) == 0 {
		return migratedNodes, migratedEdges, remap, true
	}

	keptNodes := make([]model.WorkflowNode, 0, len(migratedNodes)-len(remap))
	for i := range migratedNodes {
		if _, removed := remap[migratedNodes[i].ID]; removed {
			continue
		}
		keptNodes = append(keptNodes, migratedNodes[i])
	}
	keptEdges := make([]model.WorkflowEdge, 0, len(migratedEdges)-len(remap))
	for i := range migratedEdges {
		source, sourceRemapped := remap[migratedEdges[i].Source]
		target, targetRemapped := remap[migratedEdges[i].Target]
		if !sourceRemapped {
			source = migratedEdges[i].Source
		}
		if !targetRemapped {
			target = migratedEdges[i].Target
		}
		if source == target {
			continue
		}
		edge := migratedEdges[i]
		edge.Source, edge.Target = source, target
		keptEdges = append(keptEdges, edge)
	}
	return keptNodes, keptEdges, remap, true
}

func workflowEventExists(events []string, expected string) bool {
	for i := range events {
		if events[i] == expected {
			return true
		}
	}
	return false
}

func remapBlockedManualRuns(tx *gorm.DB, applicationID string, sourceRemap map[string]string) error {
	if len(sourceRemap) == 0 || !tx.Migrator().HasTable(&model.PipelineRun{}) {
		return nil
	}
	for sourceID, targetID := range sourceRemap {
		result := tx.Model(&model.PipelineRun{}).
			Where(
				"application_id = ? AND workflow_snapshot = ? AND status = ? AND current_node_id = ?",
				applicationID, "", model.PipelineRunBlocked, sourceID,
			).
			UpdateColumn("current_node_id", targetID)
		if result.Error != nil {
			return fmt.Errorf("迁移待执行流水线的手动触发入口失败: %w", result.Error)
		}
	}
	return nil
}

func migrateSSHDeploymentTargets(tx *gorm.DB) error {
	// 旧版本的 SQLite 表可能保留与当前模型不同的列约束。这里仅添加本次功能需要的列，
	// 避免 AutoMigrate 重建整表时因历史脏数据或旧默认值导致升级失败。
	if err := addColumns(tx, &model.DeploymentTarget{}, []string{
		"EnvironmentID", "HostID", "WorkingDirectory",
	}); err != nil {
		return err
	}
	if err := addColumns(tx, &model.DeploymentRecord{}, []string{
		"EnvironmentID", "HostID", "WorkingDirectory", "DeploymentPlanID", "DeploymentPlanKind",
		"CommandScript", "CommandDigest", "CommandTimeout", "CommandExitCode",
	}); err != nil {
		return err
	}
	if err := addColumns(tx, &model.PipelineRunRepository{}, []string{
		"DeploymentPlanKind", "DeploymentPlanScript", "DeploymentPlanTimeoutSeconds", "DeploymentPlanDigest",
	}); err != nil {
		return err
	}
	if err := addIndexes(tx, &model.DeploymentTarget{}, []string{"EnvironmentID", "HostID"}); err != nil {
		return err
	}
	if err := addIndexes(tx, &model.DeploymentRecord{}, []string{"EnvironmentID", "HostID", "DeploymentPlanID"}); err != nil {
		return err
	}
	var components []model.PipelineRunRepository
	if err := tx.Where("release_plan_id <> '' AND deployment_plan_digest = ''").Find(&components).Error; err != nil {
		return err
	}
	for i := range components {
		var plan model.DeploymentPlan
		if err := tx.First(&plan, "id = ?", components[i].DeploymentPlanID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return err
		}
		if err := tx.Model(&model.PipelineRunRepository{}).Where("id = ?", components[i].ID).Updates(map[string]any{
			"deployment_plan_kind":            plan.Kind,
			"deployment_plan_script":          plan.Script,
			"deployment_plan_timeout_seconds": plan.TimeoutSeconds,
			"deployment_plan_digest":          model.DeploymentPlanExecutionDigest(plan.Kind, plan.Script, plan.TimeoutSeconds),
		}).Error; err != nil {
			return err
		}
	}

	var targets []model.DeploymentTarget
	if err := tx.Where("platform = ? AND host_id = '' AND runtime_id <> ''", model.DeploymentDocker).Find(&targets).Error; err != nil {
		return err
	}
	for i := range targets {
		var endpoint model.DockerEndpoint
		if err := tx.Select("host_id").First(&endpoint, "id = ?", targets[i].RuntimeID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return err
		}
		if endpoint.HostID != "" {
			if err := tx.Model(&model.DeploymentTarget{}).Where("id = ?", targets[i].ID).Update("host_id", endpoint.HostID).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func addColumns(tx *gorm.DB, value any, fields []string) error {
	if !tx.Migrator().HasTable(value) {
		return tx.AutoMigrate(value)
	}
	for _, field := range fields {
		if tx.Migrator().HasColumn(value, field) {
			continue
		}
		if err := tx.Migrator().AddColumn(value, field); err != nil {
			return err
		}
	}
	return nil
}

func addIndexes(tx *gorm.DB, value any, fields []string) error {
	for _, field := range fields {
		if tx.Migrator().HasIndex(value, field) {
			continue
		}
		if err := tx.Migrator().CreateIndex(value, field); err != nil {
			return err
		}
	}
	return nil
}

func migrateHostsAndEnvironments(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&model.Environment{}, &model.Host{}, &model.HostCapability{}); err != nil {
		return err
	}
	if !tx.Migrator().HasColumn(&model.DockerEndpoint{}, "HostID") {
		if err := tx.Migrator().AddColumn(&model.DockerEndpoint{}, "HostID"); err != nil {
			return err
		}
	}
	if !tx.Migrator().HasIndex(&model.DockerEndpoint{}, "idx_docker_endpoints_host_id") {
		if err := tx.Migrator().CreateIndex(&model.DockerEndpoint{}, "HostID"); err != nil {
			return err
		}
	}

	now := time.Now().UTC()
	localHost := model.Host{
		ID: model.BuiltinLocalHostID, Name: "本地", Mode: model.HostModeLocal,
		SSHPort: 22, IsBuiltin: true, IsActive: true, CreatedBy: "system",
		CreatedAt: now, UpdatedAt: now,
	}
	createLocalHost := tx.Where("id = ?", localHost.ID).FirstOrCreate(&localHost)
	if createLocalHost.Error != nil {
		return createLocalHost.Error
	}
	// 只迁移历史版本写入的精确默认名，不覆盖管理员后续设置的自定义名称。
	if localHost.Name == "ZRT 本机" {
		if err := tx.Model(&model.Host{}).Where("id = ? AND name = ?", localHost.ID, "ZRT 本机").
			Updates(map[string]any{"name": "本地", "updated_at": now}).Error; err != nil {
			return err
		}
		localHost.Name = "本地"
	}
	// Docker 能力只是新安装的初始选择。之后是否启用由用户管理，
	// 重跑迁移不得把已经关闭的能力静默恢复。
	if createLocalHost.RowsAffected == 0 {
		return migrateLegacySSHDockerHosts(tx, now)
	}
	localCapability := model.HostCapability{
		HostID: localHost.ID, Kind: model.HostCapabilityDocker, RuntimeID: "zrt-local-docker",
		Status: model.HostCapabilityReady, CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.Create(&localCapability).Error; err != nil {
		return err
	}
	return migrateLegacySSHDockerHosts(tx, now)
}

func migrateLegacySSHDockerHosts(tx *gorm.DB, now time.Time) error {
	var endpoints []model.DockerEndpoint
	if err := tx.Where("host_id = ''").Find(&endpoints).Error; err != nil {
		return err
	}
	for i := range endpoints {
		endpoint := &endpoints[i]
		parsed, err := url.Parse(endpoint.Host)
		if err != nil || parsed.Scheme != "ssh" || parsed.User == nil ||
			parsed.User.Username() == "" || parsed.Hostname() == "" {
			continue
		}
		port := 22
		if parsed.Port() != "" {
			value, err := strconv.ParseUint(parsed.Port(), 10, 16)
			if err != nil || value == 0 {
				continue
			}
			port = int(value)
		}

		// 旧凭据仍由 DockerEndpoint 使用 endpoint ID 作为 AAD 解密。数据库迁移没有密钥上下文，
		// 因此这里只建立主机归属，不复制或重加密密文，也不猜测认证方式与 sudo 配置。
		host := model.Host{
			ID: endpoint.ID, Name: endpoint.Name, Mode: model.HostModeSSH,
			Address: parsed.Hostname(), SSHPort: port, SSHUsername: parsed.User.Username(),
			SSHHostKeyFingerprint: endpoint.SSHHostKeyFingerprint,
			IsActive:              endpoint.IsActive, CreatedBy: endpoint.CreatedBy,
			CreatedAt: endpoint.CreatedAt, UpdatedAt: endpoint.UpdatedAt,
		}
		var count int64
		if err := tx.Model(&model.Host{}).Where("id = ?", host.ID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			if err := tx.Create(&host).Error; err != nil {
				return err
			}
		}
		capability := model.HostCapability{
			HostID: host.ID, Kind: model.HostCapabilityDocker, RuntimeID: endpoint.ID,
			Status: model.HostCapabilityUnchecked, CreatedAt: endpoint.CreatedAt, UpdatedAt: endpoint.UpdatedAt,
		}
		if err := tx.Where("host_id = ? AND kind = ?", capability.HostID, capability.Kind).
			FirstOrCreate(&capability).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.DockerEndpoint{}).Where("id = ? AND host_id = ''", endpoint.ID).
			Update("host_id", host.ID).Error; err != nil {
			return err
		}
	}
	return nil
}

func migrateDeploymentApprovalsToWorkflow(tx *gorm.DB) error {
	if !tx.Migrator().HasTable(&model.DeploymentRecord{}) {
		return nil
	}
	now := time.Now().UTC()
	// 环境级隐式审批已取消。旧记录不能自动放行，否则升级本身会触发外部发布副作用。
	return tx.Model(&model.DeploymentRecord{}).
		Where("status = ?", model.DeploymentAwaitingApproval).
		Updates(map[string]any{
			"status": model.DeploymentCanceled, "error_code": "legacy_environment_approval_removed",
			"error_message": "历史待审批发布已取消，请重新执行", "finished_at": now, "updated_at": now,
		}).Error
}

func migrateRepositoryObservationWatchKeys(tx *gorm.DB) error {
	// 旧表按环境保存一个游标，无法同时监听同一环境下的多个自定义分支、PR 和 Tag。
	// 先移除旧唯一索引，再增加按触发节点、事件和具体 Ref 生成的监听键。
	if !tx.Migrator().HasTable(&model.ApplicationRepositoryObservation{}) {
		return tx.AutoMigrate(&model.ApplicationRepositoryObservation{})
	}
	if tx.Migrator().HasIndex(&model.ApplicationRepositoryObservation{}, "idx_repository_environment") {
		if err := tx.Migrator().DropIndex(&model.ApplicationRepositoryObservation{}, "idx_repository_environment"); err != nil {
			return err
		}
	}
	for _, field := range []string{"WatchKey", "SourceNodeID", "Event"} {
		if !tx.Migrator().HasColumn(&model.ApplicationRepositoryObservation{}, field) {
			if err := tx.Migrator().AddColumn(&model.ApplicationRepositoryObservation{}, field); err != nil {
				return err
			}
		}
	}
	var observations []model.ApplicationRepositoryObservation
	if err := tx.Find(&observations).Error; err != nil {
		return err
	}
	for i := range observations {
		if observations[i].WatchKey != "" {
			continue
		}
		// 保留旧观察记录用于升级后的基线兼容；首次扫描会将它绑定到准确的监听键。
		if err := tx.Model(&observations[i]).Update("watch_key", "legacy:"+observations[i].ID).Error; err != nil {
			return err
		}
	}
	if !tx.Migrator().HasIndex(&model.ApplicationRepositoryObservation{}, "idx_repository_watch") {
		return tx.Migrator().CreateIndex(&model.ApplicationRepositoryObservation{}, "idx_repository_watch")
	}
	return nil
}

func migratePipelineExecutionFields(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&model.PipelineRun{}, &model.PipelineRunRepository{}, &model.DeploymentRecord{}); err != nil {
		return err
	}
	if err := backfillPipelineRunRegistries(tx); err != nil {
		return err
	}
	if err := backfillApplicationWorkflowTargets(tx); err != nil {
		return err
	}
	// 旧实现只推进节点状态，从未创建发布记录；不能继续把这类历史记录展示为真实发布成功。
	return tx.Model(&model.PipelineRun{}).
		Where("status = ? AND current_node_id <> '' AND deployment_id = ''", model.PipelineRunSucceeded).
		Updates(map[string]any{
			"status": model.PipelineRunFailed, "stage": "execution_missing",
			"message": "历史流水线未执行真实构建和发布，请重新运行", "updated_at": time.Now().UTC(),
		}).Error
}

func backfillPipelineRunRegistries(tx *gorm.DB) error {
	var rows []struct {
		ID              string
		ImageRegistryID string
	}
	if err := tx.Table("pipeline_run_repositories AS component").
		Select("component.id, application.image_registry_id").
		Joins("JOIN pipeline_runs AS run ON run.id = component.pipeline_run_id").
		Joins("JOIN applications AS application ON application.id = run.application_id").
		Where("component.image_registry_id = '' AND application.image_registry_id <> ''").
		Scan(&rows).Error; err != nil {
		return err
	}
	for i := range rows {
		if err := tx.Model(&model.PipelineRunRepository{}).Where("id = ?", rows[i].ID).
			Update("image_registry_id", rows[i].ImageRegistryID).Error; err != nil {
			return err
		}
	}
	return nil
}

func backfillApplicationWorkflowTargets(tx *gorm.DB) error {
	if !tx.Migrator().HasTable(&model.ReleaseWorkflow{}) || !tx.Migrator().HasTable(&model.ReleaseWorkflowTemplate{}) {
		return nil
	}
	var workflows []model.ReleaseWorkflow
	if err := tx.Where("workflow_template_id <> ''").Find(&workflows).Error; err != nil {
		return err
	}
	for i := range workflows {
		workflow := &workflows[i]
		var template model.ReleaseWorkflowTemplate
		if err := tx.First(&template, "id = ?", workflow.WorkflowTemplateID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				continue
			}
			return err
		}
		templateNodes := make(map[string]model.WorkflowNode, len(template.Nodes))
		for j := range template.Nodes {
			templateNodes[template.Nodes[j].ID] = template.Nodes[j]
		}
		changed := false
		for j := range workflow.Nodes {
			node := &workflow.Nodes[j]
			if node.Type != model.WorkflowNodeDeploy || node.Config.DeploymentTargetID != "" {
				continue
			}
			templateNode, ok := templateNodes[node.ID]
			if !ok || templateNode.Config.DeploymentTargetID == "" {
				continue
			}
			node.Config.DeploymentTargetID = templateNode.Config.DeploymentTargetID
			changed = true
			if err := tx.Model(&model.ApplicationEnvironment{}).
				Where("application_id = ? AND key = ? AND deployment_target_id = ''", workflow.ApplicationID, node.Config.Environment).
				Update("deployment_target_id", node.Config.DeploymentTargetID).Error; err != nil {
				return err
			}
		}
		if !changed {
			continue
		}
		workflow.Revision++
		workflow.WorkflowTemplateRevision = template.Revision
		workflow.UpdatedAt = time.Now().UTC()
		nodes, err := json.Marshal(workflow.Nodes)
		if err != nil {
			return fmt.Errorf("序列化应用流水线迁移数据失败: %w", err)
		}
		if err := tx.Model(workflow).Updates(map[string]any{
			"nodes": string(nodes), "revision": workflow.Revision,
			"workflow_template_revision": workflow.WorkflowTemplateRevision,
			"updated_at":                 workflow.UpdatedAt,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

func migrateDeploymentEnvironmentFields(tx *gorm.DB) error {
	if err := migrateDockerSSHCredentialColumn(tx); err != nil {
		return err
	}
	// 仅补新增字段，避免 SQLite 因历史字段类型差异重建整表。
	columns := []struct {
		model any
		field string
	}{
		{&model.DockerEndpoint{}, "SSHHostKeyFingerprint"},
		{&model.DeploymentTarget{}, "Description"},
	}
	for _, column := range columns {
		if tx.Migrator().HasColumn(column.model, column.field) {
			continue
		}
		if err := tx.Migrator().AddColumn(column.model, column.field); err != nil {
			return err
		}
	}
	return nil
}

func migrateDockerSSHCredentialColumn(tx *gorm.DB) error {
	const column = "ssh_credential_ciphertext"
	if !tx.Migrator().HasColumn(&model.DockerEndpoint{}, column) {
		switch tx.Dialector.Name() {
		case "sqlite":
			// SQLite 给已有表新增非空列时必须提供默认值；该默认值仅属于旧库升级结构。
			return tx.Exec("ALTER TABLE docker_endpoints ADD COLUMN ssh_credential_ciphertext text NOT NULL DEFAULT ''").Error
		case "mysql", "postgres":
			// 先允许 NULL，避免已有连接记录导致新增非空列失败，随后统一回填并收紧约束。
			if err := tx.Exec("ALTER TABLE docker_endpoints ADD COLUMN ssh_credential_ciphertext text NULL").Error; err != nil {
				return err
			}
		default:
			return tx.Migrator().AddColumn(&model.DockerEndpoint{}, "SSHCredentialCiphertext")
		}
	}

	switch tx.Dialector.Name() {
	case "mysql":
		if err := tx.Exec("UPDATE docker_endpoints SET ssh_credential_ciphertext = '' WHERE ssh_credential_ciphertext IS NULL").Error; err != nil {
			return err
		}
		return tx.Exec("ALTER TABLE docker_endpoints MODIFY COLUMN ssh_credential_ciphertext text NOT NULL").Error
	case "postgres":
		if err := tx.Exec("UPDATE docker_endpoints SET ssh_credential_ciphertext = '' WHERE ssh_credential_ciphertext IS NULL").Error; err != nil {
			return err
		}
		return tx.Exec("ALTER TABLE docker_endpoints ALTER COLUMN ssh_credential_ciphertext SET NOT NULL").Error
	default:
		return nil
	}
}

func migrateReleasePlanning(tx *gorm.DB) error {
	if tx.Migrator().HasTable("release_plans") && !tx.Migrator().HasTable("deployment_plans") {
		if err := tx.Migrator().RenameTable("release_plans", "deployment_plans"); err != nil {
			return err
		}
		// PostgreSQL 和 SQLite 改表名后会保留旧索引名；先同步索引名，避免新发布计划表创建同名索引失败。
		for _, suffix := range []string{"name", "kind", "is_active", "created_by"} {
			oldName, newName := "idx_release_plans_"+suffix, "idx_deployment_plans_"+suffix
			if !tx.Migrator().HasIndex("deployment_plans", oldName) {
				continue
			}
			if tx.Migrator().HasIndex("deployment_plans", newName) {
				if err := tx.Migrator().DropIndex("deployment_plans", oldName); err != nil {
					return err
				}
				continue
			}
			if err := tx.Migrator().RenameIndex("deployment_plans", oldName, newName); err != nil {
				return err
			}
		}
	}
	if err := tx.AutoMigrate(
		&model.DeploymentPlan{}, &model.Application{}, &model.ApplicationEnvironment{},
		&model.PipelineRunRepository{}, &model.ReleasePlan{}, &model.ReleaseGroup{},
		&model.ReleaseGroupApplication{}, &model.ReleaseGroupDependency{},
	); err != nil {
		return err
	}
	return moveRepositoryPlansToApplications(tx)
}

func backfillApplicationRepositories(tx *gorm.DB) error {
	var applications []model.Application
	if err := tx.Find(&applications).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	for i := range applications {
		application := applications[i]
		if application.RepositoryID == "" {
			continue
		}
		var count int64
		if err := tx.Model(&model.ApplicationRepository{}).
			Where("application_id = ? AND repository_id = ?", application.ID, application.RepositoryID).
			Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			link := model.ApplicationRepository{
				ID: uuid.NewString(), ApplicationID: application.ID, RepositoryID: application.RepositoryID,
				SortOrder: 0, LastObservedRef: application.LastObservedRef,
				LastObservedCommit: application.LastObservedCommit, LastCheckedAt: application.LastCheckedAt,
				CreatedAt: now, UpdatedAt: now,
			}
			if err := tx.Create(&link).Error; err != nil {
				return err
			}
		}
	}

	var runs []model.PipelineRun
	if err := tx.Find(&runs).Error; err != nil {
		return err
	}
	for i := range runs {
		run := runs[i]
		var count int64
		if err := tx.Model(&model.PipelineRunRepository{}).Where("pipeline_run_id = ?", run.ID).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		var application model.Application
		if err := tx.First(&application, "id = ?", run.ApplicationID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				continue
			}
			return err
		}
		if application.RepositoryID == "" {
			continue
		}
		status := model.PipelineRunRepositoryPending
		if run.CommitSHA != "" {
			status = model.PipelineRunRepositoryReady
		}
		component := model.PipelineRunRepository{
			ID: uuid.NewString(), PipelineRunID: run.ID, RepositoryID: application.RepositoryID,
			SortOrder: 0, Ref: run.Ref, CommitSHA: run.CommitSHA,
			BuildPlanID: application.BuildPlanID, ImageRegistryID: application.ImageRegistryID,
			DeploymentPlanID: application.DeploymentPlanID,
			Status:           status, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt,
		}
		if err := tx.Create(&component).Error; err != nil {
			return err
		}
	}
	return nil
}

func dropOptionalDeliveryConstraints(tx *gorm.DB) error {
	constraints := []struct {
		model any
		names []string
	}{
		{&model.Application{}, []string{"BuildPlan", "ImageRegistry", "DeploymentPlan", "DeploymentTarget"}},
		{&model.ApplicationEnvironment{}, []string{"DeploymentPlan", "DeploymentTarget"}},
	}
	for _, item := range constraints {
		for _, name := range item.names {
			if tx.Migrator().HasConstraint(item.model, name) {
				if err := tx.Migrator().DropConstraint(item.model, name); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func backfillApplicationEnvironments(tx *gorm.DB) error {
	var applications []model.Application
	if err := tx.Find(&applications).Error; err != nil {
		return err
	}
	for i := range applications {
		var count int64
		if err := tx.Model(&model.ApplicationEnvironment{}).
			Where("application_id = ?", applications[i].ID).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		now := time.Now().UTC()
		environment := model.ApplicationEnvironment{
			ID: applications[i].ID + "-dev", ApplicationID: applications[i].ID,
			Key: "dev", Name: "开发环境", Branch: applications[i].Branch,
			PollEnabled: applications[i].PollEnabled, WatchPush: applications[i].WatchPush,
			WatchPullRequest: applications[i].WatchPullRequest, WatchTags: applications[i].WatchTags,
			TagPattern: applications[i].TagPattern, DeploymentPlanID: applications[i].DeploymentPlanID,
			DeploymentTargetID: applications[i].DeploymentTargetID,
			LastObservedRef:    applications[i].LastObservedRef,
			LastObservedCommit: applications[i].LastObservedCommit,
			LastCheckedAt:      applications[i].LastCheckedAt, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&environment).Error; err != nil {
			return err
		}
	}
	return nil
}

func moveRepositoryPlansToApplications(tx *gorm.DB) error {
	if !tx.Migrator().HasColumn("git_repositories", "build_plan_id") &&
		!tx.Migrator().HasColumn("git_repositories", "release_plan_id") {
		return nil
	}
	type legacyRepositoryPlan struct {
		ID               string `gorm:"column:id"`
		BuildPlanID      string `gorm:"column:build_plan_id"`
		DeploymentPlanID string `gorm:"column:release_plan_id"`
	}
	var legacy []legacyRepositoryPlan
	if err := tx.Table("git_repositories").Select("id", "build_plan_id", "release_plan_id").Scan(&legacy).Error; err != nil {
		return err
	}
	for _, repository := range legacy {
		updates := map[string]any{}
		if repository.BuildPlanID != "" {
			updates["build_plan_id"] = gorm.Expr("CASE WHEN build_plan_id = '' THEN ? ELSE build_plan_id END", repository.BuildPlanID)
		}
		if repository.DeploymentPlanID != "" {
			updates["release_plan_id"] = gorm.Expr("CASE WHEN release_plan_id = '' THEN ? ELSE release_plan_id END", repository.DeploymentPlanID)
		}
		if len(updates) == 0 {
			continue
		}
		if err := tx.Model(&model.Application{}).Where("repository_id = ?", repository.ID).Updates(updates).Error; err != nil {
			return err
		}
	}
	return nil
}

func Migrate(ctx context.Context, db *gorm.DB) error {
	if err := db.WithContext(ctx).AutoMigrate(&model.SchemaMigration{}); err != nil {
		return fmt.Errorf("初始化迁移记录失败: %w", err)
	}

	for _, item := range migrations {
		var count int64
		if err := db.WithContext(ctx).Model(&model.SchemaMigration{}).
			Where("version = ?", item.version).Count(&count).Error; err != nil {
			return fmt.Errorf("读取迁移版本 %s 失败: %w", item.version, err)
		}
		if count > 0 {
			continue
		}
		if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := item.up(tx); err != nil {
				return err
			}
			return tx.Create(&model.SchemaMigration{Version: item.version, AppliedAt: time.Now().UTC()}).Error
		}); err != nil {
			return fmt.Errorf("执行迁移版本 %s 失败: %w", item.version, err)
		}
	}
	return nil
}

func VerifyMigrations(ctx context.Context, db *gorm.DB) error {
	if !db.Migrator().HasTable(&model.SchemaMigration{}) {
		return fmt.Errorf("数据库尚未初始化，请先执行 zrt migrate")
	}
	for _, item := range migrations {
		var count int64
		if err := db.WithContext(ctx).Model(&model.SchemaMigration{}).
			Where("version = ?", item.version).Count(&count).Error; err != nil {
			return fmt.Errorf("检查数据库迁移版本 %s 失败: %w", item.version, err)
		}
		if count == 0 {
			return fmt.Errorf("数据库迁移版本 %s 尚未执行，请先执行 zrt migrate", item.version)
		}
	}
	return nil
}
