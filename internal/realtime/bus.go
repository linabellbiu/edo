package realtime

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/model"
)

// Bus 只传递失效通知；业务数据及日志仍从数据库按原权限读取。
// Pub/Sub 不保证补发，因此订阅建立、重连和低频校准都必须重新读取快照。
type Bus struct {
	db           *gorm.DB
	client       *redis.Client
	prefix       string
	logger       *slog.Logger
	ctx          context.Context
	cancel       context.CancelFunc
	done         chan struct{}
	queue        chan change
	lastOverflow atomic.Int64
}

type change struct {
	resource, id string
	scope        database.DepartmentScope
}

func New(db *gorm.DB, client *redis.Client, prefix string, logger *slog.Logger) *Bus {
	ctx, cancel := context.WithCancel(context.Background())
	bus := &Bus{db: db, client: client, prefix: fmt.Sprintf("%s:db%d:", prefix, client.Options().DB), logger: logger, ctx: ctx, cancel: cancel, done: make(chan struct{}), queue: make(chan change, 256)}
	go bus.run()
	return bus
}

func (b *Bus) Changed(ctx context.Context, resource, id string) {
	switch resource {
	case "applications", "application_sync", "pipeline_runs", "release_plans", "deployments", "deployment_plans":
		b.enqueue(ctx, resource, id)
	}
}

func (b *Bus) Close() { b.cancel(); <-b.done }

func (b *Bus) enqueue(ctx context.Context, resource, id string) {
	if b == nil {
		return
	}
	scope, _ := database.DepartmentScopeFromContext(ctx)
	select {
	case <-b.ctx.Done():
	case b.queue <- change{resource: resource, id: id, scope: scope}:
	default:
		now := time.Now().Unix()
		previous := b.lastOverflow.Load()
		if now-previous >= 60 && b.lastOverflow.CompareAndSwap(previous, now) {
			b.logger.Warn("实时通知缓冲已满，将由低频同步补齐", "operation", "realtime_overflow")
		}
	}
}

func (b *Bus) run() {
	defer close(b.done)
	for {
		select {
		case <-b.ctx.Done():
			return
		case first := <-b.queue:
			// 合并同一资源的突发输出，Redis 故障不得阻塞构建和发布。
			batch := map[change]struct{}{first: {}}
			timer := time.NewTimer(100 * time.Millisecond)
		collect:
			for len(batch) < 256 {
				select {
				case <-b.ctx.Done():
					timer.Stop()
					return
				case next := <-b.queue:
					batch[next] = struct{}{}
				case <-timer.C:
					break collect
				}
			}
			timer.Stop()
			for item := range batch {
				if b.ctx.Err() != nil {
					return
				}
				ctx := database.WithDepartmentScope(b.ctx, item.scope)
				// 无请求范围的 Worker 使用资源自身已冻结的部门。
				if item.scope == (database.DepartmentScope{}) {
					ctx = b.ctx
				}
				if item.resource == "logs" {
					b.publishLogs(ctx, item.id)
				} else {
					b.publishChanged(ctx, item.resource, item.id)
				}
			}
		}
	}
}

func (b *Bus) publishChanged(ctx context.Context, resource, id string) {
	if b == nil {
		return
	}
	var record any
	switch resource {
	case "applications", "application_sync":
		record = &model.Application{}
	case "pipeline_runs":
		record = &model.PipelineRun{}
	case "release_plans":
		record = &model.ReleasePlan{}
	case "deployments":
		record = &model.DeploymentRecord{}
	case "deployment_plans":
		record = &model.DeploymentPlan{}
	default:
		return
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	scope, _ := database.DepartmentScopeFromContext(ctx)
	departmentID := scope.DepartmentID
	projectID := scope.ProjectID
	if projectID == "" && departmentID != "" {
		projectID = database.DefaultProjectID(departmentID)
	}
	if id != "" {
		// 软删除后仍从原资源读取冻结的部门，不能按管理员当前部门推断。
		var row struct{ DepartmentID, ProjectID string }
		if err := b.db.WithContext(ctx).Unscoped().Model(record).Select("department_id", "project_id").Where("uuid = ?", id).Take(&row).Error; err != nil {
			if err != gorm.ErrRecordNotFound {
				b.logger.Warn("读取实时通知归属失败", "operation", "realtime_scope", "resource", resource, "resource_id", id, "err", err)
			}
			return
		}
		departmentID = row.DepartmentID
		projectID = row.ProjectID
	}
	if departmentID == "" {
		return
	}
	pipe := b.client.Pipeline()
	if projectID != "" {
		pipe.Publish(ctx, b.prefix+"project:"+projectID, resource)
	}
	pipe.Publish(ctx, b.prefix+"department:"+departmentID, resource)
	pipe.Publish(ctx, b.prefix+"administrators", resource)
	if _, err := pipe.Exec(ctx); err != nil {
		b.logger.Warn("发送状态变更通知失败", "operation", "realtime_publish", "resource", resource, "resource_id", id, "err", err)
	}
	if resource == "pipeline_runs" && id != "" {
		b.publishLogs(ctx, id)
	}
}

func (b *Bus) LogsChanged(ctx context.Context, runID string) {
	if runID != "" {
		b.enqueue(ctx, "logs", runID)
	}
}

func (b *Bus) publishLogs(ctx context.Context, runID string) {
	if b == nil || runID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := b.client.Publish(ctx, b.prefix+"logs:"+runID, "changed").Err(); err != nil {
		b.logger.Warn("发送日志变更通知失败", "operation", "realtime_log_publish", "pipeline_run_id", runID, "err", err)
	}
}

func (b *Bus) SubscribeProject(ctx context.Context) (*redis.PubSub, error) {
	scope, _ := database.DepartmentScopeFromContext(ctx)
	if scope.ProjectID == "" {
		return nil, fmt.Errorf("项目范围未指定")
	}
	return b.subscribe(ctx, "project:"+scope.ProjectID)
}

func (b *Bus) SubscribeState(ctx context.Context, departmentID string, administrator bool) (*redis.PubSub, error) {
	channel := "department:" + departmentID
	if administrator {
		channel = "administrators"
	}
	return b.subscribe(ctx, channel)
}

// 调用方必须在订阅前校验运行的部门归属与读取权限。
func (b *Bus) SubscribeLogs(ctx context.Context, runID string) (*redis.PubSub, error) {
	return b.subscribe(ctx, "logs:"+runID)
}

func (b *Bus) subscribe(ctx context.Context, channel string) (*redis.PubSub, error) {
	if b == nil {
		return nil, fmt.Errorf("实时通知服务未配置")
	}
	subscription := b.client.Subscribe(ctx, b.prefix+channel)
	readyCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := subscription.Receive(readyCtx); err != nil {
		_ = subscription.Close()
		return nil, err
	}
	return subscription, nil
}
