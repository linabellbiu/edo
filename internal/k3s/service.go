package k3s

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.yaml.in/yaml/v3"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/hostcredential"
	"edo/internal/kube"
	"edo/internal/model"
	"edo/internal/secret"
	"edo/internal/sshclient"
)

const planTTL = 10 * time.Minute
const leaseTTL = 2 * time.Minute
const executionTimeout = 45 * time.Minute

var validName = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}_. -]{1,127}$`)

type savedInput struct {
	Input                  Input                   `json:"input"`
	HostDigests            map[string]string       `json:"host_digests"`
	AuthVersion            uint64                  `json:"auth_version"`
	ClusterDigest          string                  `json:"cluster_digest,omitempty"`
	TargetVersion          string                  `json:"target_version,omitempty"`
	PreflightVersion       int                     `json:"preflight_version,omitempty"`
	ServerCriticalDigest   string                  `json:"server_critical_digest,omitempty"`
	ServerMembershipDigest string                  `json:"server_membership_digest,omitempty"`
	ServerMembers          []kube.ServerJoinMember `json:"server_members,omitempty"`
	K3dVersion             string                  `json:"k3d_version,omitempty"`
	K3dK3sVersion          string                  `json:"k3d_k3s_version,omitempty"`
}

type clusterService interface {
	Find(context.Context, string) (*model.KubernetesCluster, error)
	Create(context.Context, string, kube.Input) (*model.KubernetesCluster, error)
	AppendHosts(context.Context, string, []string) error
	Ping(context.Context, string) (string, error)
	CreateNodeJoin(context.Context, string, kube.NodeJoinInput) (*kube.NodeJoin, error)
	RevokeNodeJoin(context.Context, string, string, kube.ResourceIdentity) error
	Clientset(context.Context, string) (*kubernetes.Clientset, error)
	RESTConfig(context.Context, string) (*rest.Config, error)
	RequireClusterPermission(context.Context, string, string, string) error
	InspectServerJoin(context.Context, string, string, string) (*kube.ServerJoinInfo, error)
	Test(context.Context, kube.Input) (*kube.ConnectionTest, error)
}

type Service struct {
	db              *gorm.DB
	secrets         *secret.Manager
	kubernetes      clusterService
	authorizer      Authorizer
	transport       Transport
	owner           string
	k3dWaitInterval time.Duration
}

func NewService(db *gorm.DB, secrets *secret.Manager, kubernetes *kube.Service, authorizer Authorizer, cfg config.Runtime) *Service {
	return &Service{db: db, secrets: secrets, kubernetes: kubernetes, authorizer: authorizer, transport: &sshTransport{timeout: cfg.ConnectTimeout}, owner: uuid.NewString()}
}

func (s *Service) WithTransport(transport Transport) *Service {
	if transport != nil {
		s.transport = transport
	}
	return s
}

func (s *Service) authorize(ctx context.Context, actorID, action string) (*model.User, error) {
	scope, ok := database.DepartmentScopeFromContext(ctx)
	if !ok || scope.ProjectID == "" || scope.DepartmentID == "" || (scope.UserID != "" && scope.UserID != actorID) || s.authorizer == nil {
		return nil, ErrPermission
	}
	var actor model.User
	if err := s.db.WithContext(ctx).First(&actor, "uuid = ?", actorID).Error; err != nil || !actor.IsActive {
		return nil, ErrPermission
	}
	var project model.Project
	if err := s.db.WithContext(ctx).First(&project, "uuid = ?", scope.ProjectID).Error; err != nil || !project.IsActive || project.DepartmentID != scope.DepartmentID {
		return nil, ErrPermission
	}
	if !actor.IsSuperuser {
		if actor.DepartmentID != scope.DepartmentID {
			return nil, ErrPermission
		}
		var count int64
		if err := s.db.WithContext(ctx).Model(&model.ProjectMember{}).Where("project_id = ? AND user_id = ?", scope.ProjectID, actor.ID).Count(&count).Error; err != nil || count == 0 {
			return nil, ErrPermission
		}
	}
	permissions := []string{"cluster.execute", "cluster.update"}
	if action == "create" {
		permissions = append(permissions, "cluster.create")
	} else if action == "join" {
		permissions = append(permissions, "kubernetes.node.create")
	} else {
		return nil, ErrInvalidInput
	}
	for _, permission := range permissions {
		allowed, err := s.authorizer.HasPermission(ctx, &actor, permission)
		if err != nil || !allowed {
			return nil, ErrPermission
		}
	}
	canRead := false
	for _, permission := range []string{"cluster.read", "deployment.read"} {
		allowed, err := s.authorizer.HasPermission(ctx, &actor, permission)
		if err != nil {
			return nil, ErrPermission
		}
		canRead = canRead || allowed
	}
	if !canRead {
		return nil, ErrPermission
	}
	return &actor, nil
}

func hostDigest(host model.Host) string {
	value, _ := json.Marshal(host)
	// Host credentials have json:"-" on the public model; include their identity
	// explicitly in the private review snapshot without storing plaintext secrets.
	sum := sha256.Sum256(append(value, []byte(host.SSHCredentialCiphertext+host.SSHHostKeyFingerprint)...))
	return hex.EncodeToString(sum[:])
}

func clusterDigest(cluster *model.KubernetesCluster) string {
	sum := sha256.Sum256([]byte(cluster.ID + "\x00" + cluster.APIServer + "\x00" + cluster.KubeconfigCiphertext + "\x00" + cluster.UpdatedAt.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:])
}

func (s *Service) loadHost(ctx context.Context, id string) (model.Host, sshclient.Bundle, error) {
	var host model.Host
	if err := s.db.WithContext(ctx).First(&host, "uuid = ?", id).Error; err != nil {
		return host, sshclient.Bundle{}, ErrHostChanged
	}
	scope, _ := database.DepartmentScopeFromContext(ctx)
	if host.IsBuiltin || !host.IsActive || host.Mode != model.HostModeSSH || host.ProjectID != scope.ProjectID || host.DepartmentID != scope.DepartmentID || !sshclient.ValidFingerprint(host.SSHHostKeyFingerprint) {
		return host, sshclient.Bundle{}, ErrHostChanged
	}
	var capability model.HostCapability
	if err := s.db.WithContext(ctx).First(&capability, "host_id = ? AND kind = ? AND status = ?", host.ID, model.HostCapabilitySSH, model.HostCapabilityReady).Error; err != nil {
		return host, sshclient.Bundle{}, ErrHostChanged
	}
	plaintext, err := s.secrets.Decrypt(host.SSHCredentialCiphertext, hostcredential.AAD(host.ID))
	if err != nil {
		return host, sshclient.Bundle{}, ErrHostChanged
	}
	var bundle sshclient.Bundle
	if json.Unmarshal([]byte(plaintext), &bundle) != nil {
		return host, bundle, ErrHostChanged
	}
	if _, err := sshclient.AuthMethods(bundle); err != nil {
		return host, bundle, ErrHostChanged
	}
	if host.SSHUsername != "root" && !bundle.UseSudo {
		return host, bundle, fmt.Errorf("%w：主机 %s 需要启用 sudo", ErrInvalidInput, host.Name)
	}
	return host, bundle, nil
}

func (s *Service) loadReviewedHost(ctx context.Context, snapshot savedInput, id string) (model.Host, sshclient.Bundle, error) {
	host, bundle, err := s.loadHost(ctx, id)
	if err != nil {
		return host, bundle, err
	}
	if digest, ok := snapshot.HostDigests[id]; !ok || hostDigest(host) != digest {
		return host, bundle, ErrHostChanged
	}
	return host, bundle, nil
}

func (s *Service) normalize(ctx context.Context, input Input) (Input, []Host, map[string]string, *model.KubernetesCluster, []string, error) {
	input.Action = strings.TrimSpace(input.Action)
	input.Profile = strings.TrimSpace(input.Profile)
	input.Mode = strings.TrimSpace(input.Mode)
	if input.Mode == "" {
		input.Mode = "native"
	}
	if input.Mode == "k3d" {
		return s.normalizeK3d(ctx, input)
	}
	if input.Mode != "native" || input.K3d != nil {
		return input, nil, nil, nil, nil, &ValidationError{Message: "请选择原生 K3s 或 k3d 安装模式"}
	}
	input.Action = strings.TrimSpace(input.Action)
	input.Profile = strings.TrimSpace(input.Profile)
	input.Name = strings.TrimSpace(input.Name)
	if input.Profile != "default" && input.Profile != "custom" {
		return input, nil, nil, nil, nil, ErrInvalidInput
	}
	if input.Action != "create" && input.Action != "join" {
		return input, nil, nil, nil, nil, ErrInvalidInput
	}
	if err := normalizeJoinSelection(&input); err != nil {
		return input, nil, nil, nil, nil, err
	}
	if len(input.Overrides) > 64*1024 || len(input.AgentHostIDs) > 20 || len(input.JoinHostIDs) > 20 {
		return input, nil, nil, nil, nil, ErrInvalidInput
	}
	if input.Profile == "default" {
		input.ServerOptions = map[string]any{}
		input.AgentOptions = map[string]any{}
		input.Installer = map[string]any{}
		input.Overrides = ""
	}
	overrides := map[string]yaml.Node{}
	if strings.TrimSpace(input.Overrides) != "" {
		if err := yaml.Unmarshal([]byte(input.Overrides), &overrides); err != nil {
			return input, nil, nil, nil, nil, fmt.Errorf("%w：覆盖配置必须是 YAML 对象", ErrInvalidInput)
		}
		for key := range overrides {
			if key != "server" && key != "agent" && key != "installer" {
				return input, nil, nil, nil, nil, fmt.Errorf("%w：覆盖配置只接受 server、agent、installer", ErrInvalidInput)
			}
		}
	}
	roleOverride := func(role string) (string, error) {
		node, ok := overrides[role]
		if !ok {
			return "", nil
		}
		encoded, err := yaml.Marshal(&node)
		return string(encoded), err
	}
	serverOverride, err := roleOverride("server")
	if err != nil {
		return input, nil, nil, nil, nil, ErrInvalidInput
	}
	agentOverride, err := roleOverride("agent")
	if err != nil {
		return input, nil, nil, nil, nil, ErrInvalidInput
	}
	input.ServerOptions, err = ValidateAndMergeOptions("server", input.ServerOptions, serverOverride)
	if err != nil {
		return input, nil, nil, nil, nil, &ValidationError{Message: err.Error()}
	}
	input.AgentOptions, err = ValidateAndMergeOptions("agent", input.AgentOptions, agentOverride)
	if err != nil {
		return input, nil, nil, nil, nil, &ValidationError{Message: err.Error()}
	}
	installerOverride, err := roleOverride("installer")
	if err != nil {
		return input, nil, nil, nil, nil, ErrInvalidInput
	}
	input.Installer, err = ValidateAndMergeInstallerOptions(input.Installer, installerOverride)
	if err != nil {
		return input, nil, nil, nil, nil, &ValidationError{Message: err.Error()}
	}
	input.Overrides = ""
	warnings := append(OptionWarnings("server", input.ServerOptions), OptionWarnings("agent", input.AgentOptions)...)
	ids := append([]string{}, input.AgentHostIDs...)
	if input.Action == "join" {
		ids = append([]string{}, input.JoinHostIDs...)
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if input.Action == "join" {
		input.JoinHostIDs = ids
		input.AgentHostIDs = nil
	} else {
		input.AgentHostIDs = ids
	}
	if len(ids) > 1 {
		options := input.AgentOptions
		if isServerJoin(input) {
			options = input.ServerOptions
		}
		if name, _ := options["node-name"].(string); name != "" {
			return input, nil, nil, nil, nil, fmt.Errorf("%w：多台节点不能使用同一个自定义 node-name", ErrInvalidInput)
		}
	}
	var cluster *model.KubernetesCluster
	var hosts []Host
	digests := map[string]string{}
	if input.Action == "create" {
		if !validName.MatchString(input.Name) || input.ServerHostID == "" || input.ClusterID != "" {
			return input, nil, nil, nil, nil, ErrInvalidInput
		}
		if slices.Contains(ids, input.ServerHostID) {
			return input, nil, nil, nil, nil, ErrInvalidInput
		}
		host, _, err := s.loadHost(ctx, input.ServerHostID)
		if err != nil {
			return input, nil, nil, nil, nil, err
		}
		hosts = append(hosts, Host{ID: host.ID, Name: host.Name, Address: host.Address, Role: "server"})
		digests[host.ID] = hostDigest(host)
		if input.Endpoint == "" {
			port := 6443
			if number, ok := numberOption(input.ServerOptions["https-listen-port"]); ok {
				port = number
			}
			input.Endpoint = "https://" + net.JoinHostPort(host.Address, strconv.Itoa(port))
		}
		var count int64
		if err := s.db.WithContext(ctx).Model(&model.KubernetesCluster{}).Where("name = ?", input.Name).Count(&count).Error; err != nil {
			return input, nil, nil, nil, nil, err
		} else if count > 0 {
			return input, nil, nil, nil, nil, kube.ErrClusterExists
		}
	} else {
		if input.ClusterID == "" || input.ServerHostID != "" || len(ids) == 0 {
			return input, nil, nil, nil, nil, ErrInvalidInput
		}
		cluster, err = s.kubernetes.Find(ctx, input.ClusterID)
		if err != nil {
			return input, nil, nil, nil, nil, err
		}
		if !cluster.IsActive {
			return input, nil, nil, nil, nil, ErrInvalidInput
		}
		version, err := s.liveTargetVersion(ctx, input.ClusterID)
		if err != nil {
			return input, nil, nil, nil, nil, err
		}
		input.targetVersion = version
		input.Name = cluster.Name
		if input.Endpoint == "" {
			input.Endpoint = cluster.APIServer
		}
	}
	if err := selectInstallerVersion(&input); err != nil {
		return input, nil, nil, nil, nil, err
	}
	if _, channel := input.Installer["INSTALL_K3S_CHANNEL"]; channel {
		warnings = append(warnings, "使用安装频道时实际版本可能变化，请核对最终配置与该版本的兼容性。")
	}
	if version, _ := input.Installer["INSTALL_K3S_VERSION"].(string); version != "" && version != OptionsCatalog().Version {
		warnings = append(warnings, "安装版本与选项目录版本不同，请核对该版本支持的自定义配置。")
	}
	input.Endpoint = strings.TrimRight(strings.TrimSpace(input.Endpoint), "/")
	endpoint, err := url.Parse(input.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != "" || strings.ContainsAny(input.Endpoint, "\r\n\x00") {
		return input, nil, nil, nil, nil, ErrInvalidInput
	}
	if ip := net.ParseIP(endpoint.Hostname()); ip != nil && (ip.IsUnspecified() || ip.IsLoopback()) {
		return input, nil, nil, nil, nil, fmt.Errorf("%w：API 地址必须能被 EDO 和新节点访问", ErrInvalidInput)
	}
	if port := endpoint.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return input, nil, nil, nil, nil, ErrInvalidInput
		}
	}
	for _, id := range ids {
		host, _, err := s.loadHost(ctx, id)
		if err != nil {
			return input, nil, nil, nil, nil, err
		}
		role := "agent"
		if isServerJoin(input) {
			role = "server"
		}
		hosts = append(hosts, Host{ID: host.ID, Name: host.Name, Address: host.Address, Role: role})
		digests[host.ID] = hostDigest(host)
	}
	if len(hosts) == 0 {
		return input, nil, nil, nil, nil, ErrInvalidInput
	}
	if isServerJoin(input) {
		if err := s.prepareServerJoin(ctx, &input); err != nil {
			return input, nil, nil, nil, nil, serverJoinPreflightError(hosts, err)
		}
	}
	return input, hosts, digests, cluster, warnings, nil
}

func (s *Service) Preview(ctx context.Context, actorID string, input Input) (*Plan, error) {
	// Include authorization, live version reads and the final review in the
	// same budget as SSH checks, so HTTP can always finish within its deadline.
	ctx, cancel := context.WithTimeout(ctx, PreflightTimeout)
	defer cancel()
	actor, err := s.authorize(ctx, actorID, input.Action)
	if err != nil {
		return nil, err
	}
	if !s.secrets.Available() {
		return nil, secret.ErrUnavailable
	}
	input, hosts, digests, cluster, warnings, err := s.normalize(ctx, input)
	if err != nil {
		return nil, err
	}
	preflight, err := s.runPreflight(ctx, input, hosts)
	if err != nil {
		return nil, err
	}
	if err := requirePreflight(preflight); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	id := uuid.NewString()
	server := redactedYAML(input.ServerOptions)
	agent := redactedYAML(input.AgentOptions)
	for index, host := range hosts {
		if input.Mode == "k3d" {
			continue
		}
		base := input.AgentOptions
		if host.Role == "server" {
			base = input.ServerOptions
		}
		if _, err := installScript(host, input, id, "<后台生成的短期凭据>"); err != nil {
			return nil, err
		}
		finalConfig := runtimeOptions(base, host, id, input.Endpoint, "<后台生成的凭据>")
		if isServerJoin(input) {
			finalConfig["server"] = input.Endpoint
		}
		hosts[index].Config = redactedYAML(finalConfig)
		if host.Role == "server" {
			server = hosts[index].Config
		} else {
			agent = hosts[index].Config
		}
	}
	version, _ := input.Installer["INSTALL_K3S_VERSION"].(string)
	plan := &Plan{ID: id, Action: input.Action, Mode: input.Mode, K3d: input.K3d, Name: input.Name, Status: "planned", ClusterID: input.ClusterID, Endpoint: input.Endpoint, Version: version, TargetVersion: input.targetVersion, ExpiresAt: now.Add(planTTL), Hosts: hosts, ServerConfig: server, AgentConfig: agent, Warnings: warnings, Commands: []string{}, Preflight: preflight}
	if input.Action == "join" {
		plan.JoinRole = input.JoinRole
	}
	if isServerJoin(input) {
		plan.ServerJoin = &ServerJoinSummary{Datastore: input.serverJoin.Datastore, EtcdMembers: len(input.serverJoin.Members)}
	}
	for _, host := range hosts {
		plan.Commands = append(plan.Commands, previewCommand(host, input.Installer))
	}
	plan.Warnings = append(plan.Warnings, "将在所选主机安装并启动 K3s 系统服务。已有 Kubernetes 安装的主机会被拒绝，请先检查主机用途和网络。")
	if input.Mode == "k3d" {
		plan.Version = k3dK3sVersion
		plan.ServerConfig, plan.AgentConfig = "", ""
		plan.Commands = []string{k3dCreateCommand(input, id)}
		plan.Warnings = k3dWarnings(id)
	}
	if isServerJoin(input) {
		plan.Warnings = append(plan.Warnings, "Master 使用经过 SHA256 与版本校验的暂存制品安装，安装阶段不再下载二进制或 SELinux RPM；启用 SELinux 的主机须预装 k3s-selinux 策略包。")
	}
	if disabled, _ := input.ServerOptions["disable-agent"].(bool); disabled {
		plan.Warnings = append(plan.Warnings, "禁用 Server Agent 后不会注册该控制平面节点，该主机暂不自动关联。")
	}
	snapshot := savedInput{Input: input, HostDigests: digests, AuthVersion: actor.AuthVersion, TargetVersion: input.targetVersion, PreflightVersion: preflightVersion}
	if input.Mode == "k3d" {
		snapshot.K3dVersion, snapshot.K3dK3sVersion = k3dVersion, k3dK3sVersion
	}
	if isServerJoin(input) {
		snapshot.ServerCriticalDigest = input.serverJoin.CriticalDigest
		snapshot.ServerMembershipDigest = input.serverJoin.MembershipDigest
		snapshot.ServerMembers = input.serverJoin.Members
	}
	if cluster != nil {
		snapshot.ClusterDigest = clusterDigest(cluster)
	}
	// SSH checks can take time; scope, credentials and target version must still
	// match the review before persisting an executable plan.
	review := model.K3sInstallation{CreatedBy: actorID, Action: input.Action, Name: input.Name}
	if err := s.recheckRuntime(ctx, review, snapshot); err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(snapshot)
	ciphertext, err := s.secrets.Encrypt(string(encoded), []byte("k3s-installation:"+id))
	if err != nil {
		return nil, err
	}
	preview, _ := json.Marshal(plan)
	hostJSON, _ := json.Marshal(hosts)
	scope, _ := database.DepartmentScopeFromContext(ctx)
	record := model.K3sInstallation{ID: id, ProjectID: scope.ProjectID, DepartmentID: scope.DepartmentID, CreatedBy: actorID, Action: input.Action, Name: input.Name, ClusterID: input.ClusterID, Status: "planned", InputCiphertext: ciphertext, Preview: datatypes.JSON(preview), Hosts: datatypes.JSON(hostJSON), Logs: "", ExpiresAt: plan.ExpiresAt, CreatedAt: now, UpdatedAt: now}
	if err := s.db.WithContext(ctx).Create(&record).Error; err != nil {
		return nil, fmt.Errorf("保存安装计划失败: %w", err)
	}
	return plan, nil
}

func (s *Service) decode(record model.K3sInstallation) (savedInput, error) {
	plaintext, err := s.secrets.Decrypt(record.InputCiphertext, []byte("k3s-installation:"+record.ID))
	if err != nil {
		return savedInput{}, err
	}
	var snapshot savedInput
	if err := json.Unmarshal([]byte(plaintext), &snapshot); err != nil {
		return snapshot, ErrInvalidInput
	}
	snapshot.Input.targetVersion = snapshot.TargetVersion
	return snapshot, nil
}

func (s *Service) recheck(ctx context.Context, record model.K3sInstallation, snapshot savedInput) error {
	actor, err := s.authorize(ctx, record.CreatedBy, record.Action)
	if err != nil {
		return err
	}
	if actor.AuthVersion != snapshot.AuthVersion {
		return ErrPermission
	}
	for id, digest := range snapshot.HostDigests {
		host, _, err := s.loadHost(ctx, id)
		if err != nil {
			return err
		}
		if hostDigest(host) != digest {
			return ErrHostChanged
		}
	}
	if snapshot.Input.Action == "join" {
		var current model.KubernetesCluster
		if err := s.db.WithContext(ctx).First(&current, "uuid = ?", snapshot.Input.ClusterID).Error; err != nil {
			return kube.ErrClusterNotFound
		}
		cluster := &current
		if !cluster.IsActive || clusterDigest(cluster) != snapshot.ClusterDigest {
			return ErrHostChanged
		}
	} else {
		query := s.db.WithContext(ctx).Model(&model.KubernetesCluster{}).Where("name = ?", record.Name)
		if record.ClusterID != "" {
			query = query.Where("uuid <> ?", record.ClusterID)
		}
		var count int64
		if err := query.Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return kube.ErrClusterExists
		}
	}
	return nil
}

func (s *Service) Execute(ctx context.Context, actorID, planID string) (*Installation, error) {
	// Remote checks must run without a SQL transaction: Ping uses the shared
	// Kubernetes service's database connection, which may have a one-slot pool.
	var reviewed model.K3sInstallation
	if err := s.db.WithContext(ctx).First(&reviewed, "uuid = ? AND created_by = ?", planID, actorID).Error; err != nil {
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
	checked, err := s.decode(reviewed)
	if err != nil {
		return nil, err
	}
	if err := s.recheckRuntime(ctx, reviewed, checked); err != nil {
		return nil, err
	}
	var record model.K3sInstallation
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scope, _ := database.DepartmentScopeFromContext(ctx)
		var project model.Project
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&project, "uuid = ?", scope.ProjectID).Error; err != nil {
			return ErrPermission
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&record, "uuid = ? AND created_by = ?", planID, actorID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if record.Status != "planned" {
			return ErrPlanConsumed
		}
		if record.InputCiphertext != reviewed.InputCiphertext {
			return ErrTargetVersionChanged
		}
		now := time.Now().UTC()
		if !record.ExpiresAt.After(now) {
			return ErrPlanExpired
		}
		snapshot, err := s.decode(record)
		if err != nil {
			return err
		}
		validation := *s
		validation.db = tx
		if err := validation.recheck(ctx, record, snapshot); err != nil {
			return err
		}
		if record.Action == "create" {
			var active int64
			if err := tx.Model(&model.K3sInstallation{}).Where("action = ? AND name = ? AND status IN ?", "create", record.Name, []string{"pending", "running"}).Count(&active).Error; err != nil {
				return err
			}
			if active > 0 {
				return kube.ErrClusterExists
			}
		}
		if record.Action == "join" {
			var active int64
			if err := tx.Model(&model.K3sInstallation{}).Where("action = ? AND cluster_id = ? AND status IN ?", "join", record.ClusterID, []string{"pending", "running"}).Count(&active).Error; err != nil {
				return err
			}
			if active > 0 {
				return ErrHostBusy
			}
		}
		hostIDs := make([]string, 0, len(snapshot.HostDigests))
		for id := range snapshot.HostDigests {
			hostIDs = append(hostIDs, id)
		}
		slices.Sort(hostIDs)
		for _, hostID := range hostIDs {
			lock := model.K3sInstallationHostLock{HostID: hostID, InstallationID: record.ID, ProjectID: record.ProjectID, DepartmentID: record.DepartmentID, ExpiresAt: now.Add(executionTimeout)}
			if err := tx.Create(&lock).Error; err != nil {
				if errors.Is(err, gorm.ErrDuplicatedKey) {
					return ErrHostBusy
				}
				return fmt.Errorf("%w: %v", ErrHostBusy, err)
			}
		}
		result := tx.Model(&model.K3sInstallation{}).Where("uuid = ? AND status = ?", record.ID, "planned").Updates(map[string]any{"status": "pending", "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrPlanConsumed
		}
		record.Status = "pending"
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.present(record), nil
}

func (s *Service) Get(ctx context.Context, id string) (*Installation, error) {
	var record model.K3sInstallation
	if err := s.db.WithContext(ctx).First(&record, "uuid = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	scope, ok := database.DepartmentScopeFromContext(ctx)
	if !ok || scope.ProjectID != record.ProjectID || scope.DepartmentID != record.DepartmentID || scope.UserID != record.CreatedBy {
		return nil, ErrNotFound
	}
	if _, err := s.authorize(ctx, record.CreatedBy, record.Action); err != nil {
		return nil, err
	}
	return s.present(record), nil
}

func (s *Service) List(ctx context.Context, clusterID string, limit int, actions ...string) ([]Installation, error) {
	scope, ok := database.DepartmentScopeFromContext(ctx)
	if !ok || scope.ProjectID == "" || scope.DepartmentID == "" || scope.UserID == "" {
		return nil, ErrPermission
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	query := s.db.WithContext(ctx).Where("created_by = ? AND project_id = ? AND department_id = ? AND status <> ?", scope.UserID, scope.ProjectID, scope.DepartmentID, "planned")
	if len(actions) > 0 && actions[0] != "" {
		if actions[0] != "create" && actions[0] != "join" {
			return nil, ErrInvalidInput
		}
		query = query.Where("action = ?", actions[0])
	}
	if clusterID != "" {
		query = query.Where("cluster_id = ?", clusterID)
	}
	var records []model.K3sInstallation
	if err := query.Order("created_at DESC").Limit(limit).Find(&records).Error; err != nil {
		return nil, err
	}
	result := make([]Installation, 0, len(records))
	allowed := map[string]bool{}
	for _, record := range records {
		if _, checked := allowed[record.Action]; !checked {
			_, err := s.authorize(ctx, record.CreatedBy, record.Action)
			allowed[record.Action] = err == nil
		}
		if allowed[record.Action] {
			result = append(result, *s.present(record))
		}
	}
	return result, nil
}

func (s *Service) present(record model.K3sInstallation) *Installation {
	var hosts []Host
	_ = json.Unmarshal(record.Hosts, &hosts)
	result := &Installation{ID: record.ID, Action: record.Action, Mode: "native", Name: record.Name, Status: record.Status, ClusterID: record.ClusterID, ExpiresAt: record.ExpiresAt, CreatedAt: record.CreatedAt, StartedAt: record.StartedAt, FinishedAt: record.FinishedAt, Hosts: hosts, Logs: record.Logs, Error: record.ErrorMessage}
	var preview Plan
	if json.Unmarshal(record.Preview, &preview) == nil && preview.Mode == "k3d" {
		result.Mode, result.K3d = "k3d", preview.K3d
	}
	if record.ClusterID != "" {
		result.Cluster = &Cluster{ID: record.ClusterID, Name: record.Name}
	}
	return result
}

func redactedYAML(values map[string]any) string {
	encoded, _ := yaml.Marshal(RedactOptions(values))
	return string(encoded)
}
func numberOption(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, true
	case int64:
		return int(number), true
	case float64:
		return int(number), true
	case json.Number:
		n, err := strconv.Atoi(string(number))
		return n, err == nil
	case string:
		n, err := strconv.Atoi(number)
		return n, err == nil
	}
	return 0, false
}
