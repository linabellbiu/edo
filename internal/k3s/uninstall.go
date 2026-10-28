package k3s

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.yaml.in/yaml/v3"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"edo/internal/access"
	"edo/internal/database"
	"edo/internal/model"
)

const uninstallTimeout = 10 * time.Minute

type savedUninstall struct {
	TaskID       string           `json:"task_id"`
	Summary      UninstallSummary `json:"summary"`
	SourceDigest string           `json:"source_digest"`
	Proof        uninstallProof   `json:"proof"`
	ConfigDigest string           `json:"config_digest"`
}

func uninstallDestructiveStage(value datatypes.JSON) bool {
	var hosts []Host
	if json.Unmarshal(value, &hosts) != nil || len(hosts) != 1 {
		return true
	}
	return hosts[0].Stage == "uninstall" || hosts[0].Stage == "uninstall_verify"
}

func uninstallConfigDigest(input Input, summary UninstallSummary) (string, error) {
	options := input.AgentOptions
	if summary.Role == "server" {
		options = input.ServerOptions
	}
	config := runtimeOptions(options, Host{ID: summary.HostID, Role: summary.Role}, summary.SourceInstallationID, input.Endpoint, "EDO_RUNTIME_TOKEN")
	if isServerJoin(input) {
		config["server"] = input.Endpoint
	}
	delete(config, "token")
	encoded, err := yaml.Marshal(config)
	if err != nil {
		return "", ErrUninstallUnsupported
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Service) authorizeUninstall(ctx context.Context, actorID string) (*model.User, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	scope, ok := database.DepartmentScopeFromContext(ctx)
	if !ok || scope.UserID != actorID || actorID == "" || s.authorizer == nil {
		return nil, ErrUninstallPermission
	}
	current, err := access.FreshProjectPermissions(s.db, ctx, actorID, scope.ProjectID)
	if err != nil {
		slog.WarnContext(ctx, "复核 K3s 卸载项目权限失败", "operation", "k3s_uninstall_authorize", "actor_id", actorID, "err", err)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrUninstallPermission
	}
	for _, permission := range []string{access.PermissionK3sUninstall, "cluster.read"} {
		allowed, err := s.authorizer.HasPermission(ctx, &current.User, permission)
		if err != nil || !allowed || !current.Permissions[permission] {
			slog.WarnContext(ctx, "K3s 卸载权限已失效", "operation", "k3s_uninstall_authorize", "actor_id", actorID, "permission", permission, "err", err)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, ErrUninstallPermission
		}
	}
	return &current.User, nil
}

func uninstallSourceDigest(record model.K3sInstallation) string {
	value, _ := json.Marshal([]any{record.ID, record.Action, record.Status, record.ProjectID, record.DepartmentID, record.InputCiphertext, string(record.Hosts)})
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func uninstallDataDir(options map[string]any) (string, error) {
	directory := "/var/lib/rancher/k3s"
	if value, ok := options["data-dir"]; ok {
		var valid bool
		directory, valid = value.(string)
		if !valid {
			return "", ErrUninstallUnsupported
		}
	}
	// 官方脚本存在字段分割、glob 和 BRE 路径匹配，只接受不会改变这些语义的路径。
	if !regexp.MustCompile(`^/[A-Za-z0-9_/-]+$`).MatchString(directory) || !path.IsAbs(directory) || path.Clean(directory) != directory || len(strings.Split(strings.Trim(directory, "/"), "/")) < 3 {
		return "", ErrUninstallUnsupported
	}
	for _, reserved := range []string{"/usr", "/etc", "/bin", "/sbin", "/lib", "/lib64", "/boot", "/dev", "/proc", "/sys", "/run", "/home", "/root", "/tmp", "/var/lib/docker", "/var/lib/containerd", "/var/lib/kubelet", "/var/lib/cni"} {
		if directory == reserved || strings.HasPrefix(directory, reserved+"/") || strings.HasPrefix(reserved, directory+"/") {
			return "", ErrUninstallUnsupported
		}
	}
	if directory == "/var/lib/rancher" || strings.HasPrefix("/var/lib/rancher/k3s", directory+"/") {
		return "", ErrUninstallUnsupported
	}
	return directory, nil
}

func (s *Service) uninstallSource(ctx context.Context, input UninstallInput) (model.K3sInstallation, UninstallSummary, error) {
	var record model.K3sInstallation
	if _, err := uuid.Parse(input.InstallationID); err != nil {
		return record, UninstallSummary{}, ErrUninstallUnsupported
	}
	if _, err := uuid.Parse(input.HostID); err != nil {
		return record, UninstallSummary{}, ErrUninstallUnsupported
	}
	scope, _ := database.DepartmentScopeFromContext(ctx)
	err := s.db.WithContext(ctx).Where("uuid = ? AND project_id = ? AND department_id = ?", input.InstallationID, scope.ProjectID, scope.DepartmentID).First(&record).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return record, UninstallSummary{}, ErrNotFound
		}
		return record, UninstallSummary{}, err
	}
	if (record.Action != "create" && record.Action != "join") || (record.Status != "failed" && record.Status != "succeeded") {
		return record, UninstallSummary{}, ErrUninstallUnsupported
	}
	snapshot, err := s.decode(record)
	if err != nil {
		return record, UninstallSummary{}, ErrUninstallUnsupported
	}
	if snapshot.Input.Mode != "native" || snapshot.Input.K3d != nil || snapshot.Input.Action != record.Action {
		return record, UninstallSummary{}, ErrUninstallUnsupported
	}
	if snapshot.Input.Installer["INSTALL_K3S_BIN_DIR_READ_ONLY"] == true {
		return record, UninstallSummary{}, ErrUninstallUnsupported
	}
	for _, key := range []string{"INSTALL_K3S_BIN_DIR", "INSTALL_K3S_NAME", "INSTALL_K3S_SYSTEMD_DIR", "INSTALL_K3S_EXEC", "INSTALL_K3S_TYPE"} {
		if value, ok := snapshot.Input.Installer[key]; ok && value != "" {
			return record, UninstallSummary{}, ErrUninstallUnsupported
		}
	}
	var hosts []Host
	if json.Unmarshal(record.Hosts, &hosts) != nil {
		return record, UninstallSummary{}, ErrUninstallUnsupported
	}
	var selected *Host
	for index := range hosts {
		if hosts[index].ID == input.HostID {
			if selected != nil {
				return record, UninstallSummary{}, ErrUninstallUnsupported
			}
			selected = &hosts[index]
		}
	}
	if selected == nil || (selected.Role != "server" && selected.Role != "agent") {
		return record, UninstallSummary{}, ErrUninstallUnsupported
	}
	if _, ok := snapshot.HostDigests[input.HostID]; !ok {
		return record, UninstallSummary{}, ErrUninstallUnsupported
	}
	expectedRole := ""
	if record.Action == "create" {
		if snapshot.Input.ServerHostID == input.HostID {
			expectedRole = "server"
		}
		if slices.Contains(snapshot.Input.AgentHostIDs, input.HostID) {
			if expectedRole != "" {
				return record, UninstallSummary{}, ErrUninstallUnsupported
			}
			expectedRole = "agent"
		}
	} else if slices.Contains(snapshot.Input.JoinHostIDs, input.HostID) {
		expectedRole = snapshot.Input.JoinRole
	}
	if expectedRole == "" || selected.Role != expectedRole {
		return record, UninstallSummary{}, ErrUninstallUnsupported
	}
	options, service := snapshot.Input.AgentOptions, "k3s-agent"
	if selected.Role == "server" {
		options, service = snapshot.Input.ServerOptions, "k3s"
	}
	if rootless, _ := options["rootless"].(bool); rootless {
		return record, UninstallSummary{}, ErrUninstallUnsupported
	}
	directory, err := uninstallDataDir(options)
	if err != nil {
		return record, UninstallSummary{}, err
	}
	return record, UninstallSummary{SourceInstallationID: record.ID, HostID: input.HostID, Role: selected.Role, ScriptPath: "/usr/local/bin/" + service + "-uninstall.sh", ServiceName: service, DataDir: directory}, nil
}

// 候选列表只是 EDO 来源记录；必须再次预览现场，不能将失败任务等同于安全可卸载。
func (s *Service) ListUninstallTargets(ctx context.Context, actorID, hostID string) (targets []UninstallTarget, err error) {
	defer func() {
		if err != nil {
			slog.WarnContext(ctx, "读取 K3s 卸载候选失败", "operation", "k3s_uninstall_targets", "host_id", hostID, "err", err)
		}
	}()
	if _, err = s.authorizeUninstall(ctx, actorID); err != nil {
		return nil, err
	}
	if _, _, err = s.loadHost(ctx, hostID); err != nil {
		return nil, err
	}
	scope, _ := database.DepartmentScopeFromContext(ctx)
	targets = []UninstallTarget{}
	var cursor uint64
	for scanned := 0; ; scanned += 50 {
		query := s.db.WithContext(ctx).Where("project_id = ? AND department_id = ? AND action IN ? AND status IN ?", scope.ProjectID, scope.DepartmentID, []string{"create", "join"}, []string{"failed", "succeeded"})
		if cursor != 0 {
			query = query.Where("id < ?", cursor)
		}
		var records []model.K3sInstallation
		if err = query.Select("id", "uuid", "project_id", "department_id", "action", "name", "status", "created_at", "hosts").Order("id DESC").Limit(50).Find(&records).Error; err != nil {
			return nil, err
		}
		if scanned >= 1000 && len(records) != 0 {
			// 超出有界扫描时不能把不完整结果伪装成“没有候选”。
			return nil, ErrUninstallUnsupported
		}
		for _, record := range records {
			cursor = record.RowID
			var hosts []Host
			if json.Unmarshal(record.Hosts, &hosts) != nil {
				return nil, ErrUninstallUnsupported
			}
			selected := false
			for _, host := range hosts {
				selected = selected || host.ID == hostID
			}
			if !selected {
				continue
			}
			_, summary, sourceErr := s.uninstallSource(ctx, UninstallInput{InstallationID: record.ID, HostID: hostID})
			if sourceErr != nil {
				continue
			}
			targets = append(targets, UninstallTarget{UninstallSummary: summary, InstallationName: record.Name, Status: record.Status, CreatedAt: record.CreatedAt})
		}
		if len(records) < 50 {
			break
		}
	}
	_, err = s.authorizeUninstall(ctx, actorID)
	return targets, err
}

func (s *Service) recheckUninstallSource(ctx context.Context, snapshot savedInput) error {
	if snapshot.Uninstall == nil || snapshot.Input.Action != "uninstall" || len(snapshot.HostDigests) != 1 {
		return ErrUninstallUnsupported
	}
	summary := snapshot.Uninstall.Summary
	if _, ok := snapshot.HostDigests[summary.HostID]; !ok {
		return ErrUninstallUnsupported
	}
	source, current, err := s.uninstallSource(ctx, UninstallInput{InstallationID: summary.SourceInstallationID, HostID: summary.HostID})
	if err != nil {
		return err
	}
	if current != summary || uninstallSourceDigest(source) != snapshot.Uninstall.SourceDigest {
		return ErrUninstallChanged
	}
	return s.requireNoUncertainUninstall(ctx, summary.HostID, snapshot.Uninstall.TaskID)
}

func (s *Service) requireNoUncertainUninstall(ctx context.Context, hostID, taskID string) error {
	var records []model.K3sInstallation
	query := s.db.WithContext(ctx).Where("action = ? AND status IN ?", "uninstall", []string{"pending", "running", "failed"}).Where(datatypes.JSONQuery("preview").Equals(hostID, "uninstall", "host_id"))
	if taskID != "" {
		query = query.Where("uuid <> ?", taskID)
	}
	if err := query.Select("uuid", "status", "hosts").Find(&records).Error; err != nil {
		return err
	}
	for _, record := range records {
		if record.Status != "failed" {
			return ErrHostBusy
		}
		var hosts []Host
		if json.Unmarshal(record.Hosts, &hosts) != nil || len(hosts) != 1 {
			return ErrUninstallIncomplete
		}
		if hosts[0].Stage == "uninstall" || hosts[0].Stage == "uninstall_verify" {
			return ErrUninstallIncomplete
		}
	}
	return nil
}

func (s *Service) PreviewUninstall(ctx context.Context, actorID string, input UninstallInput) (plan *Plan, err error) {
	defer func() {
		if err != nil {
			slog.WarnContext(ctx, "准备 K3s 卸载计划失败", "operation", "k3s_uninstall_preview", "source_id", input.InstallationID, "host_id", input.HostID, "err", err)
		}
	}()
	actor, err := s.authorizeUninstall(ctx, actorID)
	if err != nil {
		return nil, err
	}
	source, summary, err := s.uninstallSource(ctx, input)
	if err != nil {
		return nil, err
	}
	host, bundle, err := s.loadHost(ctx, input.HostID)
	if err != nil {
		return nil, err
	}
	// 必须仍是原任务审阅的连接身份；不能凭同名主机推测另一台机器。
	original, err := s.decode(source)
	if err != nil || original.HostDigests[host.ID] != hostDigest(host) {
		return nil, ErrHostChanged
	}
	if err = s.requireNoUncertainUninstall(ctx, host.ID, ""); err != nil {
		return nil, err
	}
	probeCtx, stop := context.WithTimeout(ctx, 45*time.Second)
	defer stop()
	configDigest, err := uninstallConfigDigest(original.Input, summary)
	if err != nil {
		return nil, err
	}
	proof, err := s.probeUninstall(probeCtx, host, bundle, summary, configDigest)
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	snapshot := savedInput{Input: Input{Action: "uninstall", Mode: "native", Name: source.Name}, HostDigests: map[string]string{host.ID: hostDigest(host)}, AuthVersion: actor.AuthVersion, Uninstall: &savedUninstall{TaskID: id, Summary: summary, SourceDigest: uninstallSourceDigest(source), Proof: proof, ConfigDigest: configDigest}}
	if err = s.recheckInstallationAuthority(ctx, actorID, "uninstall", snapshot); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	plan = &Plan{ID: id, Action: "uninstall", Mode: "native", Name: source.Name, Status: "planned", ClusterID: source.ClusterID, ExpiresAt: now.Add(planTTL), Uninstall: &summary, Hosts: []Host{{ID: host.ID, Name: host.Name, Address: host.Address, Role: summary.Role, Status: "ready"}}, Commands: []string{summary.ScriptPath}, Warnings: []string{"官方卸载将停止此主机的 K3s 和工作负载，删除本地集群数据、本地持久卷及 K3s 网络配置；此操作不可撤销。", "不会删除 EDO 安装历史或集群连接；远端服务、进程、安装标记及锁全部验证完成后，任务才显示成功。"}}
	encoded, _ := json.Marshal(snapshot)
	ciphertext, err := s.secrets.Encrypt(string(encoded), []byte("k3s-installation:"+plan.ID))
	if err != nil {
		return nil, err
	}
	preview, _ := json.Marshal(plan)
	hosts, _ := json.Marshal(plan.Hosts)
	scope, _ := database.DepartmentScopeFromContext(ctx)
	record := model.K3sInstallation{ID: plan.ID, ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID, CreatedBy: actorID, Action: "uninstall", Name: source.Name, ClusterID: source.ClusterID, Status: "planned", InputCiphertext: ciphertext, Preview: datatypes.JSON(preview), Hosts: datatypes.JSON(hosts), ExpiresAt: plan.ExpiresAt, CreatedAt: now, UpdatedAt: now}
	if err = s.db.WithContext(ctx).Create(&record).Error; err != nil {
		return nil, err
	}
	return plan, nil
}

func (s *Service) ExecuteUninstall(ctx context.Context, actorID, planID, confirmation string) (result *Installation, err error) {
	defer func() {
		if err != nil {
			slog.WarnContext(ctx, "提交 K3s 卸载任务失败", "operation", "k3s_uninstall_submit", "plan_id", planID, "err", err)
		}
	}()
	if confirmation != "确认" {
		return nil, ErrUninstallConfirmation
	}
	if _, err = s.authorizeUninstall(ctx, actorID); err != nil {
		return nil, err
	}
	var reviewed model.K3sInstallation
	if err = s.db.WithContext(ctx).First(&reviewed, "uuid = ? AND created_by = ? AND action = ?", planID, actorID, "uninstall").Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if reviewed.Status != "planned" {
		return nil, ErrPlanConsumed
	}
	if !reviewed.ExpiresAt.After(time.Now().UTC()) {
		return nil, ErrPlanExpired
	}
	snapshot, err := s.decode(reviewed)
	if err != nil {
		return nil, err
	}
	if err = s.recheck(ctx, reviewed, snapshot); err != nil {
		return nil, err
	}
	if err = s.recheckUninstallRemote(ctx, snapshot); err != nil {
		return nil, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scope, _ := database.DepartmentScopeFromContext(ctx)
		var project model.Project
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&project, "uuid = ?", scope.ProjectID).Error; err != nil {
			return ErrUninstallPermission
		}
		var record model.K3sInstallation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&record, "uuid = ? AND created_by = ? AND action = ?", planID, actorID, "uninstall").Error; err != nil {
			return ErrNotFound
		}
		if record.Status != "planned" {
			return ErrPlanConsumed
		}
		if record.InputCiphertext != reviewed.InputCiphertext {
			return ErrUninstallChanged
		}
		now := time.Now().UTC()
		if !record.ExpiresAt.After(now) {
			return ErrPlanExpired
		}
		validation := *s
		validation.db = tx
		if err := validation.recheck(ctx, record, snapshot); err != nil {
			return err
		}
		lock := model.K3sInstallationHostLock{HostID: snapshot.Uninstall.Summary.HostID, InstallationID: record.ID, ProjectID: record.ProjectID, DepartmentID: record.DepartmentID, ExpiresAt: now.Add(uninstallTimeout)}
		if err := tx.Create(&lock).Error; err != nil {
			slog.WarnContext(ctx, "获取 K3s 卸载主机互斥失败", "operation", "k3s_uninstall_lock", "host_id", lock.HostID, "installation_id", record.ID, "err", err)
			return ErrHostBusy
		}
		updated := tx.Model(&model.K3sInstallation{}).Where("uuid = ? AND status = ?", record.ID, "planned").Updates(map[string]any{"status": "pending", "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrPlanConsumed
		}
		record.Status = "pending"
		result = s.present(record)
		return nil
	})
	return result, err
}

func (s *Service) ListUninstallations(ctx context.Context, actorID, hostID string, limit int) (result []Installation, err error) {
	defer func() {
		if err != nil {
			slog.WarnContext(ctx, "读取 K3s 卸载历史失败", "operation", "k3s_uninstall_list", "host_id", hostID, "err", err)
		}
	}()
	if _, err = s.authorizeUninstall(ctx, actorID); err != nil {
		return nil, err
	}
	if hostID != "" {
		if _, _, err = s.loadHost(ctx, hostID); err != nil {
			return nil, err
		}
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	scope, _ := database.DepartmentScopeFromContext(ctx)
	query := s.db.WithContext(ctx).Where("created_by = ? AND project_id = ? AND department_id = ? AND action = ? AND status <> ?", actorID, scope.ProjectID, scope.DepartmentID, "uninstall", "planned")
	if hostID != "" {
		query = query.Where(datatypes.JSONQuery("preview").Equals(hostID, "uninstall", "host_id"))
	}
	var records []model.K3sInstallation
	if err = query.Order("id DESC").Limit(limit).Find(&records).Error; err != nil {
		return nil, err
	}
	result = make([]Installation, 0, len(records))
	for _, record := range records {
		result = append(result, *s.present(record))
	}
	return result, nil
}

func (s *Service) recheckUninstallRemote(ctx context.Context, snapshot savedInput) error {
	if snapshot.Uninstall == nil {
		return ErrUninstallUnsupported
	}
	host, bundle, err := s.loadReviewedHost(ctx, snapshot, snapshot.Uninstall.Summary.HostID)
	if err != nil {
		return err
	}
	probeCtx, stop := context.WithTimeout(ctx, 45*time.Second)
	defer stop()
	proof, err := s.probeUninstall(probeCtx, host, bundle, snapshot.Uninstall.Summary, snapshot.Uninstall.ConfigDigest)
	if err != nil {
		return err
	}
	if proof != snapshot.Uninstall.Proof {
		return ErrUninstallChanged
	}
	return nil
}

func (s *Service) performUninstall(ctx context.Context, record *model.K3sInstallation, snapshot savedInput) error {
	if err := s.recheck(ctx, *record, snapshot); err != nil {
		return err
	}
	var hosts []Host
	if json.Unmarshal(record.Hosts, &hosts) != nil || len(hosts) != 1 || snapshot.Uninstall == nil || hosts[0].ID != snapshot.Uninstall.Summary.HostID {
		return ErrUninstallUnsupported
	}
	if err := s.runHostOperation(ctx, record, hosts, 0, "uninstall_guard", func(ctx context.Context, _ io.Writer) error { return s.recheckUninstallRemote(ctx, snapshot) }); err != nil {
		return installationError("uninstall_guard", err)
	}
	if err := s.recheck(ctx, *record, snapshot); err != nil {
		return err
	}
	host, bundle, err := s.loadReviewedHost(ctx, snapshot, hosts[0].ID)
	if err != nil {
		return err
	}
	if err = s.runHostOperation(ctx, record, hosts, 0, "uninstall", func(ctx context.Context, _ io.Writer) error {
		// 官方脚本会输出 set -x；全部丢弃，进度仅使用已登记阶段和有界心跳。
		return s.transport.Run(ctx, host, bundle, uninstallExecutionScript(*snapshot.Uninstall), io.Discard)
	}); err != nil {
		return installationError("uninstall", err)
	}
	if err = s.runHostOperation(ctx, record, hosts, 0, "uninstall_verify", func(ctx context.Context, _ io.Writer) error {
		output, err := s.transport.Read(ctx, host, bundle, uninstallVerificationScript(snapshot.Uninstall.Summary))
		if err != nil {
			return err
		}
		if output != "EDO_K3S_UNINSTALL_VERIFIED\n" {
			return ErrUninstallIncomplete
		}
		return s.recheck(ctx, *record, snapshot)
	}); err != nil {
		return installationError("uninstall_verify", err)
	}
	if err = s.hostProgress(record, hosts, 0, "succeeded", "官方卸载及现场验证完成"); err != nil {
		return err
	}
	return s.log(record, "官方卸载已完成；EDO 历史与集群连接已保留。")
}
