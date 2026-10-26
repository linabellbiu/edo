package k3s

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"

	"edo/internal/database"
	"edo/internal/kube"
	"edo/internal/model"
)

// Run is an application-lifecycle worker, not a request-scoped goroutine. A
// process restart fails expired leases instead of replaying privileged installs.
func (s *Service) Run(ctx context.Context) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.recoverExpired(ctx); err != nil {
			slog.ErrorContext(ctx, "清理过期 K3s 安装任务失败", "operation", "k3s_lease_recovery", "err", err)
		}
		record, err := s.claim(ctx)
		if err != nil {
			slog.ErrorContext(ctx, "读取 K3s 安装任务失败", "operation", "k3s_claim", "err", err)
		} else if record != nil {
			s.runInstallation(ctx, *record)
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) recoverExpired(ctx context.Context) error {
	now := time.Now().UTC()
	var records []model.K3sInstallation
	if err := s.db.WithContext(ctx).Where("(status = ? AND lease_expires_at < ?) OR (status = ? AND updated_at < ?)", "running", now, "pending", now.Add(-executionTimeout)).Find(&records).Error; err != nil {
		return err
	}
	for _, record := range records {
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			query := tx.Model(&model.K3sInstallation{}).Where("uuid = ? AND status = ?", record.ID, record.Status)
			if record.Status == "running" {
				query = query.Where("lease_expires_at < ?", now)
			} else {
				query = query.Where("updated_at < ?", now.Add(-executionTimeout))
			}
			updated := query.Updates(map[string]any{"status": "failed", "error_message": "安装进程已中断，请先检查主机上的实际安装状态，再重新操作。", "finished_at": now, "updated_at": now, "lease_owner": "", "lease_expires_at": nil})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected == 0 {
				return nil
			}
			return tx.Where("installation_id = ?", record.ID).Delete(&model.K3sInstallationHostLock{}).Error
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) claim(ctx context.Context) (*model.K3sInstallation, error) {
	var record model.K3sInstallation
	if err := s.db.WithContext(ctx).Where("status = ?", "pending").Order("created_at ASC").First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	now := time.Now().UTC()
	lease := now.Add(leaseTTL)
	updated := s.db.WithContext(ctx).Model(&model.K3sInstallation{}).Where("uuid = ? AND status = ?", record.ID, "pending").Updates(map[string]any{"status": "running", "started_at": now, "updated_at": now, "lease_owner": s.owner, "lease_expires_at": lease})
	if updated.Error != nil {
		return nil, updated.Error
	}
	if updated.RowsAffected == 0 {
		return nil, nil
	}
	record.Status = "running"
	record.StartedAt = &now
	record.LeaseOwner = s.owner
	record.LeaseExpiresAt = &lease
	return &record, nil
}

func (s *Service) runInstallation(parent context.Context, record model.K3sInstallation) {
	ctx, cancel := context.WithTimeout(parent, executionTimeout)
	defer cancel()
	// Scope is reconstructed from the saved project plus a fresh actor lookup;
	// no administrator flag from a browser request is trusted by the worker.
	var actor model.User
	if err := s.db.WithContext(ctx).First(&actor, "uuid = ?", record.CreatedBy).Error; err != nil {
		s.finish(parent, record, ErrPermission)
		return
	}
	ctx = database.WithDepartmentScope(ctx, database.DepartmentScope{ProjectID: record.ProjectID, DepartmentID: record.DepartmentID, UserID: record.CreatedBy, AllDepartments: actor.IsSuperuser})
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				lease := time.Now().UTC().Add(leaseTTL)
				updated := s.db.WithContext(ctx).Model(&model.K3sInstallation{}).Where("uuid = ? AND status = ? AND lease_owner = ?", record.ID, "running", s.owner).Update("lease_expires_at", lease)
				if updated.Error != nil || updated.RowsAffected != 1 {
					cancel()
					return
				}
				if err := s.db.WithContext(ctx).Model(&model.K3sInstallationHostLock{}).Where("installation_id = ?", record.ID).Update("expires_at", lease).Error; err != nil {
					cancel()
					return
				}
			}
		}
	}()
	err := s.perform(ctx, &record)
	cancel()
	<-heartbeatDone
	s.finish(context.WithoutCancel(parent), record, err)
}

func (s *Service) finish(ctx context.Context, record model.K3sInstallation, err error) {
	now := time.Now().UTC()
	status, message := "succeeded", ""
	if err != nil {
		status = "failed"
		message = safeFailure(err)
		slog.ErrorContext(ctx, "K3s 安装任务失败", "operation", "k3s_installation", "installation_id", record.ID, "err", err)
		var hosts []Host
		_ = json.Unmarshal(record.Hosts, &hosts)
		for index := range hosts {
			if hosts[index].Status == "succeeded" {
				continue
			}
			if hosts[index].Status == "installing" || hosts[index].Status == "checking" {
				hosts[index].Status = "failed"
				hosts[index].Message = message
			} else {
				hosts[index].Status = "not_started"
				hosts[index].Message = "前序步骤未完成"
			}
		}
		encoded, _ := json.Marshal(hosts)
		record.Hosts = datatypes.JSON(encoded)
		if len(record.Logs)+len(message) < 64*1024 {
			record.Logs += time.Now().Format("15:04:05") + " 安装未完成：" + message + "\n"
		}
	}
	updates := map[string]any{"status": status, "error_message": message, "finished_at": now, "updated_at": now, "lease_owner": "", "lease_expires_at": nil, "cluster_id": record.ClusterID, "logs": record.Logs, "hosts": record.Hosts}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updated := tx.Model(&model.K3sInstallation{}).Where("uuid = ? AND lease_owner = ? AND status = ?", record.ID, s.owner, "running").Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected == 0 {
			return nil
		}
		return tx.Where("installation_id = ?", record.ID).Delete(&model.K3sInstallationHostLock{}).Error
	}); err != nil {
		slog.ErrorContext(ctx, "保存 K3s 安装结果失败", "operation", "k3s_finish", "installation_id", record.ID, "err", err)
	}
}

func safeFailure(err error) string {
	if errors.Is(err, ErrPreflightFailed) {
		return preflightFailureMessage(err)
	}
	for _, safe := range []error{ErrPermission, ErrHostChanged, ErrTargetVersionChanged, ErrPreflightRequired, ErrHostBusy, ErrExistingInstallation, ErrRemote, ErrReadiness, ErrInvalidInput, kube.ErrJoinUnsupported} {
		if errors.Is(err, safe) {
			return safe.Error()
		}
	}
	if errors.Is(err, context.Canceled) {
		return "安装连接已中断，请检查主机上的实际状态。"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "安装超时，请检查主机上的实际状态。"
	}
	return "安装未完成，请检查主机服务、集群连接和安装配置。"
}

func (s *Service) log(record *model.K3sInstallation, message string) {
	line := time.Now().Format("15:04:05") + " " + message + "\n"
	if len(record.Logs)+len(line) > 64*1024 {
		return
	}
	record.Logs += line
	_ = s.db.Model(&model.K3sInstallation{}).Where("uuid = ? AND lease_owner = ? AND status = ?", record.ID, s.owner, "running").Update("logs", record.Logs).Error
}

func (s *Service) hostProgress(record *model.K3sInstallation, hosts []Host, index int, status, message string) {
	hosts[index].Status = status
	hosts[index].Message = message
	encoded, _ := json.Marshal(hosts)
	record.Hosts = datatypes.JSON(encoded)
	_ = s.db.Model(&model.K3sInstallation{}).Where("uuid = ? AND lease_owner = ? AND status = ?", record.ID, s.owner, "running").Update("hosts", record.Hosts).Error
}

func (s *Service) perform(ctx context.Context, record *model.K3sInstallation) error {
	snapshot, err := s.decode(*record)
	if err != nil {
		return err
	}
	if err := s.recheckRuntime(ctx, *record, snapshot); err != nil {
		return err
	}
	input := snapshot.Input
	var hosts []Host
	if json.Unmarshal(record.Hosts, &hosts) != nil {
		return ErrInvalidInput
	}
	// Re-read every selected machine before installing the first one. The
	// preview report is never used as a cache of current machine readiness.
	for index, host := range hosts {
		s.log(record, "检查主机："+host.Name)
		s.hostProgress(record, hosts, index, "checking", "")
	}
	report, err := s.runPreflight(ctx, input, hosts)
	if err != nil {
		return err
	}
	for index, result := range report.Hosts {
		if result.Passed {
			s.hostProgress(record, hosts, index, "ready", "检查通过")
		} else {
			s.hostProgress(record, hosts, index, "failed", "前置检查未通过")
		}
	}
	if err := requirePreflight(report); err != nil {
		return err
	}
	if input.Action == "create" {
		var serverIndex int
		for index, host := range hosts {
			if host.Role == "server" {
				serverIndex = index
				break
			}
		}
		host := hosts[serverIndex]
		installInput, err := s.beforeHostInstall(ctx, *record, snapshot, host)
		if err != nil {
			return err
		}
		s.log(record, "安装控制平面："+host.Name)
		s.hostProgress(record, hosts, serverIndex, "installing", "")
		current, bundle, err := s.loadReviewedHost(ctx, snapshot, host.ID)
		if err != nil {
			return err
		}
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return err
		}
		token := base64.RawURLEncoding.EncodeToString(key)
		script, err := installScript(host, installInput, record.ID, token)
		if err != nil {
			return err
		}
		if err := s.recheckRuntime(ctx, *record, snapshot); err != nil {
			return err
		}
		if err := s.transport.Run(ctx, current, bundle, script, io.Discard); err != nil {
			s.hostProgress(record, hosts, serverIndex, "failed", safeFailure(err))
			return err
		}
		kubeconfig, err := s.transport.Read(ctx, current, bundle, "set -eu\ncat /etc/rancher/k3s/k3s.yaml\n")
		if err != nil {
			return err
		}
		config, err := clientcmd.Load([]byte(kubeconfig))
		if err != nil {
			return ErrRemote
		}
		for _, cluster := range config.Clusters {
			cluster.Server = input.Endpoint
		}
		encoded, err := clientcmd.Write(*config)
		if err != nil {
			return err
		}
		value := string(encoded)
		if err := s.recheckRuntime(ctx, *record, snapshot); err != nil {
			return err
		}
		cluster, err := s.kubernetes.Create(ctx, record.CreatedBy, kube.Input{Name: input.Name, Mode: model.KubernetesKubeconfig, DefaultNamespace: "default", Kubeconfig: &value})
		if err != nil {
			return err
		}
		record.ClusterID = cluster.ID
		_ = s.db.WithContext(ctx).Model(&model.K3sInstallation{}).Where("uuid = ? AND lease_owner = ?", record.ID, s.owner).Update("cluster_id", record.ClusterID).Error
		if disabled, _ := input.ServerOptions["disable-agent"].(bool); !disabled {
			if err := s.waitForNode(ctx, cluster.ID, record.ID, host.ID); err != nil {
				return err
			}
			if err := s.recheckRuntime(ctx, *record, snapshot); err != nil {
				return err
			}
			if err := s.kubernetes.AppendHosts(ctx, cluster.ID, []string{host.ID}); err != nil {
				return err
			}
		}
		s.hostProgress(record, hosts, serverIndex, "succeeded", "控制平面已就绪")
		s.log(record, "集群已接入 EDO")
	}
	// Mint short-lived, CA-bound bootstrap credentials, never read the original
	// server token. Revoke this task's credential even when a node fails to join.
	for index, host := range hosts {
		if host.Role != "agent" {
			continue
		}
		if err := s.recheckRuntime(ctx, *record, snapshot); err != nil {
			return err
		}
		s.log(record, "安装工作节点："+host.Name)
		s.hostProgress(record, hosts, index, "installing", "")
		join, err := s.kubernetes.CreateNodeJoin(ctx, record.ClusterID, kube.NodeJoinInput{Provider: "k3s", Endpoint: input.Endpoint, Minutes: 15})
		if err != nil {
			return err
		}
		err = s.installAgent(ctx, record, snapshot, host, join)
		revokeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		revokeErr := s.kubernetes.RevokeNodeJoin(revokeCtx, record.ClusterID, join.ID, join.ResourceIdentity)
		cancel()
		if err != nil {
			s.hostProgress(record, hosts, index, "failed", safeFailure(err))
			return err
		}
		if revokeErr != nil {
			return revokeErr
		}
		if err := s.recheckRuntime(ctx, *record, snapshot); err != nil {
			return err
		}
		if err := s.kubernetes.AppendHosts(ctx, record.ClusterID, []string{host.ID}); err != nil {
			return err
		}
		s.hostProgress(record, hosts, index, "succeeded", "节点已就绪并关联")
	}
	s.log(record, "K3s 安装完成")
	return nil
}

func (s *Service) installAgent(ctx context.Context, record *model.K3sInstallation, snapshot savedInput, host Host, join *kube.NodeJoin) error {
	if join.Token == "" {
		return kube.ErrJoinUnsupported
	}
	input, err := s.beforeHostInstall(ctx, *record, snapshot, host)
	if err != nil {
		return err
	}
	current, bundle, err := s.loadReviewedHost(ctx, snapshot, host.ID)
	if err != nil {
		return err
	}
	script, err := installScript(host, input, record.ID, join.Token)
	if err != nil {
		return err
	}
	if err := s.recheckRuntime(ctx, *record, snapshot); err != nil {
		return err
	}
	if err := s.transport.Run(ctx, current, bundle, script, io.Discard); err != nil {
		return err
	}
	return s.waitForNode(ctx, record.ClusterID, record.ID, host.ID)
}

func (s *Service) waitForNode(ctx context.Context, clusterID, installationID, hostID string) error {
	client, err := s.kubernetes.Clientset(ctx, clusterID)
	if err != nil {
		return err
	}
	readyCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		list, err := client.CoreV1().Nodes().List(readyCtx, metav1.ListOptions{LabelSelector: "edo.dev/installation=" + installationID + ",edo.dev/host=" + hostID})
		if err == nil {
			for _, node := range list.Items {
				for _, condition := range node.Status.Conditions {
					if condition.Type == "Ready" && condition.Status == "True" {
						return nil
					}
				}
			}
		}
		select {
		case <-readyCtx.Done():
			return fmt.Errorf("%w: %v", ErrReadiness, readyCtx.Err())
		case <-ticker.C:
		}
	}
}
