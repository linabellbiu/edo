package pipeline

import (
	"context"
	"errors"
	"log/slog"

	"gorm.io/gorm"
)

var errWorkflowSnapshotUnavailable = errors.New("流水线运行配置暂时无法读取，请稍后重试")

func (s *Service) readWorkflowSnapshotResource(ctx context.Context, query *gorm.DB, value any) error {
	err := query.First(value).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		s.logger.ErrorContext(ctx, "读取流水线快照资源失败", "operation", "workflow_snapshot_resource", "err", err)
		return errWorkflowSnapshotUnavailable
	}
	return err
}

// 运行快照按任务冻结异常；原始诊断仅进入服务日志，不阻断独立任务。
func (s *Service) recordWorkflowTaskFailure(ctx context.Context, failures map[string]string, nodeID, operation, message string, err error) {
	if s.logger != nil {
		s.logger.Log(ctx, slog.LevelError, "冻结流水线任务失败", "operation", operation, "node_id", nodeID, "err", err)
	}
	if failures[nodeID] == "" {
		failures[nodeID] = message
	}
}
