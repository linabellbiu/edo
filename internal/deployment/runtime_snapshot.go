package deployment

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"edo/internal/cache"
	"edo/internal/database"
	"edo/internal/kube"
	"edo/internal/model"
	"gorm.io/gorm"
)

// CachedRuntimeState 只读取后台展示快照，运行操作的预检仍核对实际实例。
func (s *Service) CachedRuntimeState(ctx context.Context, id string) (*RuntimeState, cache.SnapshotStatus, error) {
	record, err := s.currentDeployment(ctx, id)
	if err != nil {
		return nil, cache.SnapshotStatus{}, err
	}
	if record.Platform == model.DeploymentKubernetes && s.kube != nil {
		state, stamp, err := s.kube.CachedDeploymentState(ctx, record.RuntimeID, record.Namespace, record.WorkloadName, record.KubernetesUID)
		if err != nil || state == nil {
			return nil, stamp, err
		}
		return kubernetesRuntimeState(id, *state), stamp, nil
	}
	key, err := s.runtimeSnapshotKey(ctx, record)
	if err != nil {
		s.logger.DebugContext(ctx, "无法确认运行实例缓存范围", "operation", "deployment_runtime_snapshot_scope", "deployment_id", id, "err", err)
		return nil, cache.SnapshotStatus{State: cache.SnapshotUnavailable}, nil
	}
	state, status := s.runtimeSnapshots.Get(key, time.Now())
	return state, status, nil
}

func (s *Service) RunRuntimeSnapshotSync(ctx context.Context) error {
	return cache.RunSnapshotWorkers(ctx, 15*time.Second, "deployment_runtime_snapshots", 8, func(ctx context.Context, yield func(string, model.DeploymentRecord) bool) error {
		// 同一项目内只采集当前成功实例，旧记录的应用从运行记录解析。
		current := s.db.WithContext(ctx).Table("deployment_records AS d").
			Select("MAX(d.id)").Joins("LEFT JOIN pipeline_runs AS r ON r.uuid = d.pipeline_run_id AND r.project_id = d.project_id AND r.department_id = d.department_id").
			Where("d.status = ? AND d.pipeline_run_id <> '' AND d.deployment_plan_id <> '' AND COALESCE(NULLIF(d.application_id, ''), r.application_id, '') <> ''", model.DeploymentSucceeded).
			Where("EXISTS (SELECT 1 FROM projects WHERE projects.uuid = d.project_id AND projects.department_id = d.department_id AND projects.is_active = ?)", true).
			Group("d.department_id, d.project_id, d.deployment_plan_id, d.target_id, COALESCE(NULLIF(d.application_id, ''), r.application_id, '')")
		for cursor := uint64(0); ctx.Err() == nil; {
			var records []model.DeploymentRecord
			err := s.db.WithContext(ctx).Where("id IN (?) AND id > ? AND runtime_deleted_at IS NULL", current, cursor).
				Order("id").Limit(100).Find(&records).Error
			if err != nil {
				return err
			}
			for _, record := range records {
				if !yield(record.ID, record) {
					return nil
				}
				cursor = record.RowID
			}
			if len(records) < 100 {
				return nil
			}
		}
		return nil
	}, func(ctx context.Context, record model.DeploymentRecord) {
		request, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		request = database.WithDepartmentScope(request, database.DepartmentScope{ProjectID: record.ProjectID, DepartmentID: record.DepartmentID})
		if record.ApplicationID == "" {
			var run model.PipelineRun
			if err := s.db.WithContext(request).Select("application_id").First(&run, "uuid = ?", record.PipelineRunID).Error; err != nil {
				s.logger.WarnContext(request, "无法解析旧部署实例的应用", "operation", "deployment_runtime_snapshot_application", "deployment_id", record.ID, "err", err)
				return
			}
			record.ApplicationID = run.ApplicationID
		}
		if err := s.checkConfiguredLocation(request, &record); err != nil {
			if !errors.Is(err, ErrDeploymentNotCurrent) && request.Err() == nil {
				s.logger.WarnContext(request, "无法确认实例的当前部署位置", "operation", "deployment_snapshot_location", "deployment_id", record.ID, "err", err)
			}
			return
		}
		if record.Platform == model.DeploymentKubernetes {
			s.saveKubernetesObservation(request, &record)
			return
		}
		key, err := s.runtimeSnapshotKey(request, &record)
		if err != nil || !s.runtimeSnapshots.Due(key, time.Now(), 30*time.Second, 15*time.Second) {
			return
		}
		state, err := s.readRuntimeState(request, &record)
		now := time.Now().UTC()
		if err != nil || state == nil {
			s.runtimeSnapshots.Fail(key, now)
			if err != nil && ctx.Err() == nil {
				s.logger.WarnContext(request, "后台采集部署实例状态失败", "operation", "deployment_runtime_snapshot_collect", "deployment_id", record.ID, "err", err)
			}
			return
		}
		if ctx.Err() == nil {
			s.runtimeSnapshots.Put(key, state, now)
			s.saveObservation(request, &record, key, state, now)
		}
	})
}

// 只从已有集群后台缓存留存状态，并以发布时的 UID 识别工作负载。
func (s *Service) saveKubernetesObservation(ctx context.Context, record *model.DeploymentRecord) {
	if s.kube == nil || record.KubernetesUID == "" {
		return
	}
	ctx = kube.WithInternalClusterAccess(ctx, "resource-inventory")
	state, status, err := s.kube.CachedDeploymentState(ctx, record.RuntimeID, record.Namespace, record.WorkloadName, record.KubernetesUID)
	if err != nil {
		s.logger.DebugContext(ctx, "工作负载历史状态暂不可用", "operation", "deployment_observation_collect", "deployment_id", record.ID, "err", err)
		return
	}
	if state != nil && status.CollectedAt != nil {
		s.saveObservation(ctx, record, state.ObservationScope, kubernetesRuntimeState(record.ID, *state), *status.CollectedAt)
	}
}

func (s *Service) saveObservation(ctx context.Context, record *model.DeploymentRecord, key string, state *RuntimeState, at time.Time) {
	if record.ObservedAt != nil && !record.ObservedAt.Before(at) {
		return
	}
	// 更新观察记录不能改变发布的 updated_at，也不能让迟到的采样覆盖较新结果。
	err := s.db.WithContext(ctx).Model(&model.DeploymentRecord{}).
		Where("uuid = ? AND updated_at = ? AND runtime_deleted_at IS NULL AND (observed_at IS NULL OR observed_at < ?)", record.ID, record.UpdatedAt, at).
		UpdateColumns(map[string]any{"observed_at": at, "observed_scope": fmt.Sprintf("%x", sha256.Sum256([]byte(key))), "observed_state": state.State, "observed_running": state.Running}).Error
	if err != nil {
		s.logger.WarnContext(ctx, "留存部署运行状态失败", "operation", "deployment_observation_save", "deployment_id", record.ID, "err", err)
	}
}

type LocationObservation struct {
	CollectedAt *time.Time `json:"collected_at"`
	State       string     `json:"state,omitempty"`
	Running     bool       `json:"running"`
}

// LocationObservation 只返回成功采集并持久化的最小历史摘要，不启动运行检查。
func (s *Service) LocationObservation(ctx context.Context, id string) (LocationObservation, error) {
	var record model.DeploymentRecord
	if err := s.db.WithContext(ctx).Where("uuid = ? AND status = ? AND pipeline_run_id <> '' AND deployment_plan_id <> ''", id, model.DeploymentSucceeded).First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return LocationObservation{}, ErrDeploymentNotFound
		}
		return LocationObservation{}, err
	}
	if record.ObservedAt == nil {
		return LocationObservation{}, nil
	}
	var key string
	var err error
	if record.Platform == model.DeploymentKubernetes {
		if s.kube == nil || record.KubernetesUID == "" {
			return LocationObservation{}, nil
		}
		key, err = s.kube.DeploymentObservationScope(ctx, record.RuntimeID, record.Namespace, record.WorkloadName)
	} else {
		key, err = s.runtimeSnapshotKey(ctx, &record)
	}
	if err != nil {
		return LocationObservation{}, err
	}
	if record.ObservedScope != fmt.Sprintf("%x", sha256.Sum256([]byte(key))) {
		return LocationObservation{}, nil
	}
	return LocationObservation{CollectedAt: record.ObservedAt, State: record.ObservedState, Running: record.ObservedRunning}, nil
}

func (s *Service) runtimeSnapshotKey(ctx context.Context, record *model.DeploymentRecord) (string, error) {
	fields := []string{record.ProjectID, record.DepartmentID, record.ID, record.UpdatedAt.UTC().Format(time.RFC3339Nano)}
	endpointID := record.RuntimeID
	if record.HostID != "" {
		var host model.Host
		if err := s.db.WithContext(ctx).Select("uuid", "project_id", "department_id", "is_active", "updated_at").First(&host, "uuid = ?", record.HostID).Error; err != nil {
			return "", err
		}
		if !host.IsActive || host.ProjectID != record.ProjectID || host.DepartmentID != record.DepartmentID {
			return "", ErrRuntimeStateUnavailable
		}
		fields = append(fields, host.ID, host.UpdatedAt.UTC().Format(time.RFC3339Nano))
		if record.DeploymentPlanKind == model.DeploymentPlanComposeStack || isCustomScriptRuntime(record) && endpointID != "" {
			var capability model.HostCapability
			if err := s.db.WithContext(ctx).First(&capability, "host_id = ? AND kind = ?", record.HostID, model.HostCapabilityDocker).Error; err != nil {
				return "", err
			}
			if record.DeploymentPlanKind == model.DeploymentPlanComposeStack {
				endpointID = capability.RuntimeID
			} else if endpointID != capability.RuntimeID {
				return "", ErrRuntimeStateUnavailable
			}
			fields = append(fields, fmt.Sprint(capability.UseSudo))
		}
	}
	if endpointID != "" {
		if s.docker == nil {
			return "", errors.New("Docker 服务不可用")
		}
		endpoint, err := s.docker.Find(ctx, endpointID)
		if err != nil {
			return "", err
		}
		if endpoint.ProjectID != record.ProjectID || endpoint.DepartmentID != record.DepartmentID || !endpoint.IsActive {
			return "", ErrRuntimeStateUnavailable
		}
		fields = append(fields, endpoint.ID, endpoint.UpdatedAt.UTC().Format(time.RFC3339Nano))
	}
	return strings.Join(fields, "\x00"), nil
}
