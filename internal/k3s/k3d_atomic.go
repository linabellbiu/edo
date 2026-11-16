package k3s

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"edo/internal/database"
	"edo/internal/kube"
	"edo/internal/model"
	"edo/internal/sshclient"
)

var errK3dInterrupted = errors.New("安装进程已中断，正在清理本次创建的 k3d 资源。")

// 外部创建不能参加 SQL 事务：先冻结可补偿的准确目标，Ready 后再原子接入。
func (s *Service) prepareK3dCreation(ctx context.Context, record *model.K3sInstallation, snapshot *savedInput) error {
	proof, err := s.prepareK3dCleanup(ctx, record, *snapshot)
	if err != nil {
		return err
	}
	snapshot.K3dCleanup = proof
	ciphertext, err := s.encodeK3dSnapshot(record.ID, *snapshot)
	if err != nil {
		return err
	}
	updated := s.db.WithContext(ctx).Model(&model.K3sInstallation{}).
		Where("uuid = ? AND status = ? AND lease_owner = ? AND lease_expires_at > ? AND cluster_id = ?", record.ID, "running", s.owner, time.Now().UTC(), "").
		Update("input_ciphertext", ciphertext)
	if updated.Error != nil || updated.RowsAffected != 1 {
		slog.ErrorContext(ctx, "保存 k3d 创建目标证明失败", "operation", "k3d_prepare_creation", "installation_id", record.ID, "err", updated.Error)
		return installationError("record", ErrHostChanged)
	}
	record.InputCiphertext = ciphertext
	return nil
}

func (s *Service) encodeK3dSnapshot(id string, snapshot savedInput) (string, error) {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	return s.secrets.Encrypt(string(encoded), []byte("k3s-installation:"+id))
}

func (s *Service) completeK3d(ctx context.Context, record *model.K3sInstallation, input kube.Input, hosts []Host) error {
	cluster, err := s.kubernetes.PrepareUnlinkedCreate(ctx, record.CreatedBy, input)
	if err != nil {
		return err
	}
	snapshot, err := s.decode(*record)
	if err != nil {
		return err
	}
	if err := s.recheckRuntime(ctx, *record, snapshot); err != nil {
		return err
	}
	now := time.Now().UTC()
	for index := range hosts {
		hosts[index].Status, hosts[index].Message = "succeeded", "k3d 容器集群已就绪"
		hosts[index].ProgressUpdatedAt = &now
	}
	encoded, err := json.Marshal(hosts)
	if err != nil {
		return err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 先占住任务行，失去租约的旧执行者不能注册或覆盖新执行者。
		updated := tx.Model(&model.K3sInstallation{}).
			Where("uuid = ? AND status = ? AND lease_owner = ? AND lease_expires_at > ? AND cluster_id = ?", record.ID, "running", s.owner, now, "").
			Updates(map[string]any{"status": "succeeded", "cluster_id": cluster.ID, "hosts": datatypes.JSON(encoded), "logs": record.Logs, "error_message": "", "finished_at": now, "updated_at": now, "lease_owner": "", "lease_expires_at": nil})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrHostChanged
		}
		if err := tx.Create(cluster).Error; err != nil {
			return err
		}
		return tx.Where("installation_id = ?", record.ID).Delete(&model.K3sInstallationHostLock{}).Error
	})
	if err != nil {
		slog.ErrorContext(ctx, "提交 k3d 集群和安装结果失败", "operation", "k3d_complete", "installation_id", record.ID, "err", err)
		// Commit 响应丢失不代表回滚；补偿前必须回读确定持久结果。
		checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var durable model.K3sInstallation
		if readErr := s.db.WithContext(checkCtx).First(&durable, "uuid = ?", record.ID).Error; readErr == nil && durable.Status == "succeeded" && durable.ClusterID == cluster.ID {
			*record = durable
			return nil
		}
		return installationError("record", err)
	}
	record.Status, record.ClusterID, record.Hosts = "succeeded", cluster.ID, datatypes.JSON(encoded)
	return nil
}

func k3dFailureHosts(value datatypes.JSON, message string, now time.Time) datatypes.JSON {
	var hosts []Host
	if json.Unmarshal(value, &hosts) != nil {
		return value
	}
	for index := range hosts {
		hosts[index].Status, hosts[index].Message = "failed", message
		hosts[index].ProgressUpdatedAt = &now
	}
	encoded, _ := json.Marshal(hosts)
	return datatypes.JSON(encoded)
}

// finishK3d 只补偿仍归本执行者拥有的未成功任务；清理失败保留证明及主机锁供恢复重试。
func (s *Service) finishK3d(parent context.Context, record model.K3sInstallation, runErr error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 90*time.Second)
	defer cancel()
	ctx = database.WithDepartmentScope(ctx, database.DepartmentScope{ProjectID: record.ProjectID, DepartmentID: record.DepartmentID, UserID: record.CreatedBy})
	var current model.K3sInstallation
	if err := s.db.WithContext(ctx).First(&current, "uuid = ?", record.ID).Error; err != nil {
		slog.ErrorContext(ctx, "读取 k3d 补偿状态失败", "operation", "k3d_finish_read", "installation_id", record.ID, "err", err)
		return
	}
	if current.Status == "succeeded" || current.LeaseOwner != s.owner || (current.Status != "running" && current.Status != "failed") {
		return
	}
	snapshot, err := s.decode(current)
	if err != nil || snapshot.Input.Mode != "k3d" {
		slog.ErrorContext(ctx, "读取 k3d 补偿快照失败", "operation", "k3d_finish_decode", "installation_id", record.ID, "err", err)
		if err := s.deferInvalidK3dSnapshot(ctx, current, false); err != nil {
			slog.ErrorContext(ctx, "保留 k3d 异常快照任务失败", "operation", "k3d_invalid_snapshot", "installation_id", record.ID, "err", err)
		}
		return
	}
	if runErr == nil {
		runErr = installationError("record", ErrRemote)
	}
	if snapshot.K3dFailure == "" {
		snapshot.K3dFailure = safeFailure(runErr)
		if errors.Is(runErr, errK3dInterrupted) {
			snapshot.K3dFailure = "安装进程已中断。"
		}
	}
	message := snapshot.K3dFailure
	slog.ErrorContext(ctx, "k3d 安装失败，处理本次创建资源", "operation", "k3d_finish", "installation_id", record.ID, "err", runErr)
	// 旧版本曾提前接入的集群不能仅凭任务失败删除，保留锁等待明确核对。
	canCleanup := current.ClusterID == ""
	blockedMessage := " 已有集群连接，请核对后处理。"
	if canCleanup && snapshot.K3dCleanup == nil && k3dCreationMayHaveStarted(current.Hosts) {
		canCleanup = false
		blockedMessage = " 缺少创建归属记录，无法自动清理；请核对主机上的实际状态。"
	}
	if !canCleanup {
		message += blockedMessage
	} else if snapshot.K3dCleanup != nil {
		message += " 正在清理本次创建的 k3d 资源。"
	}
	if record.Logs != "" {
		current.Logs = record.Logs
	}
	now, lease := time.Now().UTC(), time.Now().UTC().Add(leaseTTL)
	failedHosts := current.Hosts
	current.Hosts = k3dFailureHosts(current.Hosts, message, now)
	if canCleanup && snapshot.K3dCleanup != nil && current.Status == "running" {
		var hosts []Host
		if json.Unmarshal(current.Hosts, &hosts) == nil {
			for index := range hosts {
				hosts[index].Status, hosts[index].Stage = "installing", "k3d_cleanup"
				hosts[index].StageStartedAt = &now
			}
			encoded, _ := json.Marshal(hosts)
			current.Hosts = datatypes.JSON(encoded)
		}
	}
	ciphertext, err := s.encodeK3dSnapshot(current.ID, snapshot)
	if err != nil {
		slog.ErrorContext(ctx, "保存 k3d 失败快照失败", "operation", "k3d_finish_encode", "installation_id", record.ID, "err", err)
		return
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updated := tx.Model(&model.K3sInstallation{}).
			Where("uuid = ? AND lease_owner = ? AND status = ? AND lease_expires_at > ?", current.ID, s.owner, current.Status, now).
			Updates(map[string]any{"error_message": message, "hosts": current.Hosts, "logs": current.Logs, "input_ciphertext": ciphertext, "updated_at": now, "lease_expires_at": lease})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrHostChanged
		}
		return tx.Model(&model.K3sInstallationHostLock{}).Where("installation_id = ?", current.ID).Update("expires_at", lease).Error
	})
	if err != nil {
		slog.ErrorContext(ctx, "保存 k3d 失败状态失败", "operation", "k3d_finish_claim", "installation_id", record.ID, "err", err)
		return
	}
	current.InputCiphertext = ciphertext
	var cleanupErr error
	if !canCleanup {
		cleanupErr = ErrHostChanged
	} else if snapshot.K3dCleanup != nil {
		cleanupErr = s.runK3dCompensation(ctx, &current, snapshot)
	}
	// 清理超时仍须提交明确失败和最后日志，不能使用已过期的命令上下文归档。
	finalCtx, stopFinal := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stopFinal()
	ctx = finalCtx
	message = snapshot.K3dFailure
	if !canCleanup {
		message += blockedMessage
	} else if cleanupErr != nil {
		slog.ErrorContext(ctx, "清理本次失败 k3d 资源失败", "operation", "k3d_compensation", "installation_id", record.ID, "err", cleanupErr)
		message += " 本次资源清理未完成，后台将重试；主机暂不可重复安装。"
	} else if snapshot.K3dCleanup != nil {
		message += " 本次创建的 k3d 资源已清理。"
	}
	now = time.Now().UTC()
	updates := map[string]any{"status": "failed", "error_message": message, "hosts": k3dFailureHosts(failedHosts, message, now), "logs": current.Logs, "finished_at": now, "updated_at": now}
	if cleanupErr == nil {
		snapshot.K3dCleanup = nil
		ciphertext, err = s.encodeK3dSnapshot(current.ID, snapshot)
		if err != nil {
			slog.ErrorContext(ctx, "保存 k3d 清理结果失败", "operation", "k3d_cleanup_encode", "installation_id", record.ID, "err", err)
			return
		}
		updates["input_ciphertext"], updates["lease_owner"], updates["lease_expires_at"] = ciphertext, "", nil
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updated := tx.Model(&model.K3sInstallation{}).Where("uuid = ? AND status = ? AND lease_owner = ?", current.ID, current.Status, s.owner).Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrHostChanged
		}
		if cleanupErr == nil {
			return tx.Where("installation_id = ?", current.ID).Delete(&model.K3sInstallationHostLock{}).Error
		}
		return nil
	})
	if err != nil {
		slog.ErrorContext(ctx, "提交 k3d 清理结果失败", "operation", "k3d_cleanup_finish", "installation_id", record.ID, "err", err)
	}
}

func k3dCreationMayHaveStarted(value datatypes.JSON) bool {
	var hosts []Host
	if json.Unmarshal(value, &hosts) != nil {
		return true
	}
	for _, host := range hosts {
		switch host.Stage {
		case "k3d_create", "k3d_resources", "api", "node", "k3d_cleanup":
			return true
		}
	}
	return false
}

// 公开预览只能用于阻止不确定清理，绝不能据此重建删除证明。
func k3dRecordHint(record model.K3sInstallation) bool {
	var preview struct {
		Mode string `json:"mode"`
	}
	if json.Unmarshal(record.Preview, &preview) == nil && preview.Mode == "k3d" {
		return true
	}
	var hosts []Host
	_ = json.Unmarshal(record.Hosts, &hosts)
	for _, host := range hosts {
		if strings.HasPrefix(host.Stage, "k3d_") {
			return true
		}
	}
	return false
}

func (s *Service) deferInvalidK3dSnapshot(ctx context.Context, record model.K3sInstallation, recovering bool) error {
	now := time.Now().UTC()
	query := s.db.WithContext(ctx).Model(&model.K3sInstallation{}).Where("uuid = ? AND status = ? AND lease_owner = ?", record.ID, record.Status, record.LeaseOwner)
	if recovering {
		query = query.Where("lease_expires_at < ?", now)
	} else {
		query = query.Where("lease_owner = ? AND lease_expires_at > ?", s.owner, now)
	}
	message := "安装快照无法读取，无法确认本次资源归属；主机保持占用，等待后台重新核对。"
	return query.Updates(map[string]any{"status": "failed", "error_message": message, "hosts": k3dFailureHosts(record.Hosts, message, now), "lease_owner": s.owner, "lease_expires_at": now.Add(leaseTTL), "finished_at": now, "updated_at": now}).Error
}

func (s *Service) runK3dCompensation(ctx context.Context, record *model.K3sInstallation, snapshot savedInput) error {
	redactor := newInstallationLogRedactor(installationLogSecrets(snapshot.Input, sshclient.Bundle{}))
	var pending strings.Builder
	lastFlush := time.Time{}
	flush := func() error {
		if pending.Len() == 0 {
			return nil
		}
		record.Logs = appendInstallationLog(record.Logs, pending.String())
		pending.Reset()
		updated := s.db.WithContext(ctx).Model(&model.K3sInstallation{}).Where("uuid = ? AND status = ? AND lease_owner = ?", record.ID, record.Status, s.owner).Update("logs", record.Logs)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrHostChanged
		}
		lastFlush = time.Now()
		return nil
	}
	writer := &installationDiagnostics{onLine: func(line string) error {
		if line = redactor.line(line); line != "" {
			pending.WriteString(line + "\n")
		}
		if pending.Len() > 32*1024 || time.Since(lastFlush) > 500*time.Millisecond {
			return flush()
		}
		return nil
	}, onFlush: flush}
	err := s.cleanupFailedK3d(ctx, record, snapshot, snapshot.K3dCleanup, writer)
	return writer.failure(err)
}

// recovery 不重跑 create；旧本地进程的最长执行窗口结束后才接管补偿。
func (s *Service) recoverK3d(ctx context.Context, record model.K3sInstallation, now time.Time) (bool, error) {
	if record.Status == "pending" {
		return false, nil
	}
	snapshot, err := s.decode(record)
	if record.Action != "create" {
		return false, nil
	}
	if err != nil || snapshot.Input.Mode != "k3d" {
		if !k3dRecordHint(record) {
			return false, nil
		}
		slog.ErrorContext(ctx, "恢复 k3d 任务时快照不可用", "operation", "k3d_recover_decode", "installation_id", record.ID, "err", err)
		return true, s.deferInvalidK3dSnapshot(ctx, record, true)
	}
	if snapshot.K3dCleanup != nil && record.Status == "running" && record.StartedAt != nil && record.StartedAt.Add(executionTimeout).After(now) {
		waitUntil := record.StartedAt.Add(executionTimeout)
		message := "安装进程已中断，正在等待原创建进程退出，随后自动清理本次资源。"
		var hosts []Host
		_ = json.Unmarshal(record.Hosts, &hosts)
		for index := range hosts {
			hosts[index].Status, hosts[index].Stage, hosts[index].Message = "installing", "k3d_cleanup", message
			hosts[index].StageStartedAt, hosts[index].ProgressUpdatedAt = &now, &now
		}
		encoded, _ := json.Marshal(hosts)
		return true, s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			updated := tx.Model(&model.K3sInstallation{}).
				Where("uuid = ? AND status = ? AND lease_owner = ? AND lease_expires_at < ?", record.ID, record.Status, record.LeaseOwner, now).
				Updates(map[string]any{"lease_owner": s.owner, "lease_expires_at": waitUntil, "error_message": message, "hosts": datatypes.JSON(encoded), "updated_at": now})
			if updated.Error != nil || updated.RowsAffected != 1 {
				return updated.Error
			}
			return tx.Model(&model.K3sInstallationHostLock{}).Where("installation_id = ?", record.ID).Update("expires_at", waitUntil).Error
		})
	}
	lease := now.Add(leaseTTL)
	updated := s.db.WithContext(ctx).Model(&model.K3sInstallation{}).
		Where("uuid = ? AND status = ? AND lease_owner = ? AND lease_expires_at < ?", record.ID, record.Status, record.LeaseOwner, now).
		Updates(map[string]any{"lease_owner": s.owner, "lease_expires_at": lease})
	if updated.Error != nil {
		return true, updated.Error
	}
	if updated.RowsAffected == 0 {
		return true, nil
	}
	record.LeaseOwner, record.LeaseExpiresAt = s.owner, &lease
	s.finishK3d(ctx, record, errK3dInterrupted)
	return true, nil
}
