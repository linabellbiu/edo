package projecttransfer

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"gorm.io/gorm"

	"edo/internal/model"
)

type historyFilter struct {
	table string
	where string
	args  []any
	count int64
}

type historyPlan struct {
	rows         []historyFilter
	variableKeys map[string]string
}

func (p historyPlan) Fingerprint() []int64 {
	counts := make([]int64, 0, len(p.rows))
	for _, row := range p.rows {
		counts = append(counts, row.count)
	}
	return counts
}

func (p *historyPlan) track(tx *gorm.DB, source, table, where string, args ...any) (int64, error) {
	var count int64
	if err := scopedRows(tx, table, source, where, args...).Count(&count).Error; err != nil {
		return 0, fmt.Errorf("检查相关记录 %s: %w", table, err)
	}
	p.rows = append(p.rows, historyFilter{table, where, args, count})
	return count, nil
}

func scopedRows(tx *gorm.DB, table, source, where string, args ...any) *gorm.DB {
	return tx.Table(table).Where("project_id = ?", source).Where(where, args...)
}

func buildHistory(tx *gorm.DB, p *Plan, selected map[resourceKey]bool, add func(resourceKey, string)) error {
	source := p.SourceProjectID
	ids := map[string][]string{}
	for k := range selected {
		ids[k.kind] = append(ids[k.kind], k.id)
	}
	for k := range ids {
		slices.Sort(ids[k])
	}
	for _, kind := range kinds() {
		if len(ids[kind]) == 0 {
			continue
		}
		for _, child := range definitions[kind].owned {
			if _, err := p.history.track(tx, source, child.table, child.field+" IN ?", ids[kind]); err != nil {
				return err
			}
		}
	}
	check := func(kind, table, key, where, message string, args ...any) error {
		if len(ids[kind]) == 0 {
			return nil
		}
		var found []string
		if err := scopedRows(tx, table, source, key+" IN ?", ids[kind]).Where(where, args...).Distinct(key).Pluck(key, &found).Error; err != nil {
			return fmt.Errorf("检查资源使用状态 %s: %w", table, err)
		}
		for _, id := range found {
			add(resourceKey{kind, id}, message)
		}
		return nil
	}
	if err := check("application", "pipeline_runs", "application_id", "status NOT IN ?", "应用仍有未结束的流水线运行", []model.PipelineRunStatus{model.PipelineRunSucceeded, model.PipelineRunFailed, model.PipelineRunSkipped, model.PipelineRunCanceled}); err != nil {
		return err
	}
	if err := check("application", "build_runs", "application_id", "status IN ?", "应用仍有排队或运行中的构建", []model.BuildRunStatus{model.BuildRunStatusQueued, model.BuildRunStatusRunning}); err != nil {
		return err
	}
	for _, r := range []struct{ kind, key string }{
		{"application", "application_id"}, {"deployment_plan", "deployment_plan_id"}, {"deployment_target", "target_id"}, {"host", "host_id"}, {"cluster", "runtime_id"}, {"docker_endpoint", "runtime_id"}, {"environment", "environment_id"},
	} {
		if err := check(r.kind, "deployment_records", r.key, "(status IN ? OR (status = ? AND runtime_deleted_at IS NULL))", "仍关联部署实例或未结束的部署，请先解除关联或结束部署",
			[]model.DeploymentStatus{model.DeploymentQueued, model.DeploymentRunning, model.DeploymentAwaitingApproval}, model.DeploymentSucceeded); err != nil {
			return err
		}
	}
	if err := check("release_plan", "release_plan_executions", "release_plan_id", "status IN ?", "发布计划仍有未结束的执行", []model.ReleasePlanExecutionStatus{model.ReleasePlanExecutionPending, model.ReleasePlanExecutionRunning}); err != nil {
		return err
	}
	if err := check("application", "applications", "uuid", "sync_status = ?", "应用正在同步代码仓库，请等待同步结束", model.ApplicationSyncChecking); err != nil {
		return err
	}
	if err := check("application", "sql_changes", "application_id", "deleted_at IS NULL", "应用仍有关联的 SQL 变更工单，需先处理工单"); err != nil {
		return err
	}
	if err := check("cluster", "k3s_installations", "cluster_id", "TRUE", "此集群有 EDO 安装或卸载记录，暂不支持移动"); err != nil {
		return err
	}
	if err := check("host", "k3s_installation_host_locks", "host_id", "TRUE", "主机有进行中的集群安装或卸载任务"); err != nil {
		return err
	}
	if len(ids["delivery_variable"]) > 0 {
		var variables []model.DeliveryVariable
		if err := scopedRows(tx, "delivery_variables", source, "uuid IN ?", ids["delivery_variable"]).Select("uuid", "application_id", "environment_id", "usage", "name").Find(&variables).Error; err != nil {
			return err
		}
		keys := map[string]string{}
		p.history.variableKeys = make(map[string]string, len(variables))
		for _, v := range variables {
			key := model.DeliveryVariableScopeKey(p.TargetProjectID, v.ApplicationID, v.EnvironmentID, v.Usage, v.Name)
			keys[key] = v.ID
			p.history.variableKeys[v.ID] = key
		}
		wanted := make([]string, 0, len(keys))
		for k := range keys {
			wanted = append(wanted, k)
		}
		var conflicts []string
		if err := tx.Table("delivery_variables").Where("scope_key IN ?", wanted).Pluck("scope_key", &conflicts).Error; err != nil {
			return err
		}
		for _, key := range conflicts {
			add(resourceKey{"delivery_variable", keys[key]}, "目标项目已有相同名称和作用范围的配置")
		}
	}
	if len(ids["application"]) > 0 {
		apps := ids["application"]
		// 通过应用归属包含已归档流水线和早期缺少 workflow_id 的运行。
		runWhere := "pipeline_run_id IN (SELECT uuid FROM pipeline_runs WHERE project_id = ? AND application_id IN ?)"
		for _, table := range []string{"pipeline_node_runs", "pipeline_check_reports", "repository_check_deliveries"} {
			if _, err := p.history.track(tx, source, table, runWhere, source, apps); err != nil {
				return err
			}
		}
		var pending int64
		if err := scopedRows(tx, "repository_check_deliveries", source, runWhere, source, apps).Where("status = ?", "pending").Count(&pending).Error; err != nil {
			return err
		}
		if pending > 0 {
			add(resourceKey{}, "仍有待回写的仓库检查结果，请等待后台处理")
		}
		for _, table := range []string{"artifacts", "build_runs", "deployment_records"} {
			if _, err := p.history.track(tx, source, table, "application_id IN ?", apps); err != nil {
				return err
			}
		}
		// Job 的执行快照含冻结的项目范围，原运营审计保留；运行本身和日志仍随应用移动。
		count, err := p.history.track(tx, source, "pipeline_runs", "application_id IN ?", apps)
		if err != nil {
			return err
		}
		p.HistoryCount = count
	}
	return nil
}

func moveRows(tx *gorm.DB, p Plan) error {
	source, target := p.SourceProjectID, p.TargetProjectID
	// 先处理用源项目条件查询的历史，再移动流水线运行和根资源。
	for _, r := range p.history.rows {
		if r.count == 0 {
			continue
		}
		result := scopedRows(tx, r.table, source, r.where, r.args...).Update("project_id", target)
		if result.Error != nil {
			return fmt.Errorf("转移相关记录 %s: %w", r.table, result.Error)
		}
		if result.RowsAffected != r.count {
			return ErrConfirmation
		}
	}
	ids := rootIDs(p.records)
	for _, kind := range kinds() {
		if len(ids[kind]) == 0 {
			continue
		}
		d := definitions[kind]
		values := map[string]any{"project_id": target}
		if kind == "delivery_variable" {
			var expr strings.Builder
			expr.WriteString("CASE uuid")
			args := make([]any, 0, len(ids[kind])*2)
			for _, id := range ids[kind] {
				key, ok := p.history.variableKeys[id]
				if !ok {
					return ErrConfirmation
				}
				expr.WriteString(" WHEN ? THEN ?")
				args = append(args, id, key)
			}
			expr.WriteString(" ELSE scope_key END")
			values["scope_key"] = gorm.Expr(expr.String(), args...)
		}
		result := rootQuery(tx, d, source, d.parentKind != "").Where("uuid IN ?", ids[kind]).Updates(values)
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrDuplicatedKey) {
				return ErrConflict
			}
			return fmt.Errorf("移动项目资源 %s: %w", kind, result.Error)
		}
		if result.RowsAffected != int64(len(ids[kind])) {
			return ErrConfirmation
		}
	}
	return nil
}
