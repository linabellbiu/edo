package pipeline

import (
	"context"
	"time"

	"edo/internal/cicd"
	"github.com/bsm/redislock"
)

type deliveryLockManager interface {
	Obtain(context.Context, string, time.Duration, *redislock.Options) (*redislock.Lock, error)
}

func (s *Service) ConfigureDeliveryLocks(locks deliveryLockManager, prefix string) {
	s.deliveryLocks, s.deliveryLockPrefix = locks, prefix
}
func (s *Service) executeDatabaseTask(ctx context.Context, prepared *buildExecutionContext, workspace string) error {
	config := prepared.node.Config.Migration
	if config == nil || s.deliveryLocks == nil {
		return ErrPipelineExecutionUnavailable
	}
	if err := s.requireDeliveryPermission(ctx, prepared.snapshot.AuthorityID, "application_database.execute"); err != nil {
		return err
	}
	directory, err := secureWorkspacePath(workspace, config.Directory)
	if err != nil {
		return err
	}
	lock, err := s.deliveryLocks.Obtain(ctx, s.deliveryLockPrefix+":"+config.Fingerprint, 30*time.Second, &redislock.Options{RetryStrategy: redislock.NoRetry()})
	if err != nil {
		s.logger.Error("获取数据库变更锁失败", "operation", "database_migration_lock", "run_id", prepared.run.ID, "err", err)
		return cicd.ErrMigrationState
	}
	execution, cancel := context.WithTimeout(ctx, time.Duration(prepared.node.Config.TimeoutSeconds)*time.Second)
	defer cancel()
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-finished:
				return
			case <-execution.Done():
				return
			case <-ticker.C:
				if err := lock.Refresh(execution, 30*time.Second, nil); err != nil {
					s.logger.Error("续期数据库变更锁失败", "operation", "database_migration_lock_refresh", "run_id", prepared.run.ID, "err", err)
					cancel()
					return
				}
			}
		}
	}()
	defer func() {
		release, c := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer c()
		if e := lock.Release(release); e != nil {
			s.logger.Warn("释放数据库变更锁失败", "operation", "database_migration_unlock", "err", e)
		}
	}()
	target, err := s.Delivery().FindDatabase(execution, prepared.run.ProjectID, prepared.run.ApplicationID, config.DatabaseID)
	if err != nil {
		return err
	}
	if err := s.validateReleasePolicy(execution, prepared.run, prepared.node, prepared.snapshot, target.EnvironmentID); err != nil {
		return err
	}
	if err = s.updateExecutionPhase(ctx, &prepared.run, "database_migration", "正在执行已审核的数据库变更"); err != nil {
		return err
	}
	return s.Delivery().ExecuteMigration(execution, prepared.run, prepared.node.ID, *config, directory)
}
