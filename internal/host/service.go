package host

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	mobyclient "github.com/moby/moby/client"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"zrt/internal/dockerengine"
	"zrt/internal/hostcredential"
	"zrt/internal/model"
	"zrt/internal/secret"
)

var (
	ErrInvalidHost           = errors.New("主机配置无效")
	ErrHostExists            = errors.New("主机名称已存在")
	ErrHostNotFound          = errors.New("主机不存在")
	ErrHostTestRequired      = errors.New("请先完成主机及所选能力测试")
	ErrHostReferenced        = errors.New("主机或能力仍被部署配置引用，无法删除或移除")
	ErrCapabilityUnavailable = errors.New("当前运行环境不支持所选主机能力")
	ErrBuiltinHost           = errors.New("内置主机不允许执行此操作")
)

const (
	testTTL                 = 10 * time.Minute
	localProbeTTL           = 15 * time.Second
	localProbeTimeout       = 3 * time.Second
	runtimeProbeTimeout     = 30 * time.Second
	runtimeProbeConcurrency = 8
)

var (
	hostNamePattern = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}_. -]{0,127}$`)
	dnsNamePattern  = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
)

type dockerProbe interface {
	TestSSH(context.Context, dockerengine.Input) (dockerengine.SSHTestResult, error)
	TestSSHConnection(context.Context, dockerengine.Input) (dockerengine.SSHConnectionTestResult, error)
	PingForMonitor(context.Context, string) (mobyclient.PingResult, error)
	PingBuilder(context.Context) error
}

type kubernetesProbe interface {
	Ping(context.Context, string) (string, error)
}

type Input struct {
	Name                string
	Address             string
	SSHPort             int
	SSHUsername         string
	SSHAuthType         model.SSHAuthType
	SSH                 *dockerengine.SSHBundle
	CapabilityKinds     []model.HostCapabilityKind
	KubernetesClusterID string
	TestToken           string
	ReuseCredential     bool
	UseSudo             *bool
}

type TestResult struct {
	Token             string    `json:"test_token"`
	ExpiresAt         time.Time `json:"expires_at"`
	Fingerprint       string    `json:"fingerprint"`
	DockerVersion     string    `json:"docker_version,omitempty"`
	KubernetesVersion string    `json:"kubernetes_version,omitempty"`
}

type PingResult struct {
	Fingerprint       string `json:"fingerprint"`
	DockerVersion     string `json:"docker_version,omitempty"`
	KubernetesVersion string `json:"kubernetes_version,omitempty"`
}

type RuntimeStatusChange struct {
	HostID   string
	Kind     model.HostCapabilityKind
	Previous model.HostCapabilityStatus
	Status   model.HostCapabilityStatus
	Err      error
}

type runtimeProbeResult struct {
	hostID          string
	kind            model.HostCapabilityKind
	runtimeID       string
	previous        model.HostCapabilityStatus
	previousVersion string
	status          model.HostCapabilityStatus
	version         string
	err             error
}

type Detail struct {
	Host              model.Host
	Capabilities      []model.HostCapability
	CapabilityOptions []CapabilityOption
}

type CapabilityOption struct {
	Kind      model.HostCapabilityKind `json:"kind"`
	Available bool                     `json:"available"`
	Reason    string                   `json:"reason,omitempty"`
	Version   string                   `json:"version,omitempty"`
	probeErr  error
}

type testedInput struct {
	digest    [32]byte
	result    TestResult
	expiresAt time.Time
	hostID    string
}

type Service struct {
	db         *gorm.DB
	secrets    *secret.Manager
	docker     dockerProbe
	kubernetes kubernetesProbe

	mu                  sync.Mutex
	tests               map[string]testedInput
	localOptions        []CapabilityOption
	localOptionsExpires time.Time
	runtimeMu           sync.Mutex
	runtimeProbes       map[string]struct{}
	runtimeProbeSlots   chan struct{}
	localRuntimeProbe   bool
}

func NewService(db *gorm.DB, secrets *secret.Manager, docker dockerProbe, kubernetes kubernetesProbe) *Service {
	return &Service{
		db: db, secrets: secrets, docker: docker, kubernetes: kubernetes,
		tests: make(map[string]testedInput), runtimeProbes: make(map[string]struct{}),
		runtimeProbeSlots: make(chan struct{}, runtimeProbeConcurrency),
	}
}

func (s *Service) List(ctx context.Context) ([]Detail, error) {
	if err := s.refreshLocalCapabilities(ctx, false); err != nil {
		return nil, err
	}
	var hosts []model.Host
	if err := s.db.WithContext(ctx).Order("is_builtin DESC, name ASC").Find(&hosts).Error; err != nil {
		return nil, fmt.Errorf("查询主机列表失败: %w", err)
	}
	capabilities, err := s.listCapabilities(ctx, hostIDs(hosts))
	if err != nil {
		return nil, err
	}
	result := make([]Detail, 0, len(hosts))
	for i := range hosts {
		detail := Detail{Host: hosts[i], Capabilities: capabilities[hosts[i].ID]}
		if hosts[i].ID == model.BuiltinLocalHostID {
			s.decorateLocalDetail(ctx, &detail)
		}
		result = append(result, detail)
	}
	return result, nil
}

func (s *Service) Get(ctx context.Context, id string) (*Detail, error) {
	id = strings.TrimSpace(id)
	if id == model.BuiltinLocalHostID {
		if err := s.refreshLocalCapabilities(ctx, false); err != nil {
			return nil, err
		}
	}
	var current model.Host
	if err := s.db.WithContext(ctx).First(&current, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrHostNotFound
		}
		return nil, fmt.Errorf("查询主机失败: %w", err)
	}
	capabilities, err := s.listCapabilities(ctx, []string{current.ID})
	if err != nil {
		return nil, err
	}
	detail := &Detail{Host: current, Capabilities: capabilities[current.ID]}
	if current.ID == model.BuiltinLocalHostID {
		s.decorateLocalDetail(ctx, detail)
	}
	return detail, nil
}

// RefreshLocalCapabilities 只刷新用户已经启用的本地能力状态，不会替用户自动启用能力。
func (s *Service) RefreshLocalCapabilities(ctx context.Context) error {
	return s.refreshLocalCapabilities(ctx, true)
}

// RefreshRuntimeStatuses 检查已关联运行时的真实连接状态。探测并行执行，
// 单台不可达主机不会拖慢其余主机的状态刷新。
func (s *Service) RefreshRuntimeStatuses(ctx context.Context) ([]RuntimeStatusChange, error) {
	var hosts []model.Host
	if err := s.db.WithContext(ctx).Where("is_builtin = ?", false).Find(&hosts).Error; err != nil {
		return nil, fmt.Errorf("查询远程主机状态失败: %w", err)
	}
	capabilities, err := s.listCapabilities(ctx, hostIDs(hosts))
	if err != nil {
		return nil, err
	}

	probes := make([]struct {
		host       model.Host
		capability model.HostCapability
	}, 0)
	for i := range hosts {
		for _, capability := range capabilities[hosts[i].ID] {
			if capability.RuntimeID == "" ||
				(capability.Kind != model.HostCapabilityDocker && capability.Kind != model.HostCapabilityKubernetes) {
				continue
			}
			probes = append(probes, struct {
				host       model.Host
				capability model.HostCapability
			}{host: hosts[i], capability: capability})
		}
	}

	type probeOutcome struct {
		result runtimeProbeResult
		local  bool
		err    error
	}
	results := make(chan probeOutcome, len(probes)+1)
	scheduled := 0
	if s.beginLocalRuntimeProbe() {
		scheduled++
		go func() {
			defer s.endLocalRuntimeProbe()
			results <- probeOutcome{local: true, err: s.refreshLocalCapabilities(ctx, true)}
		}()
	}
	for i := range probes {
		probeKey := probes[i].host.ID + "\x00" + string(probes[i].capability.Kind) + "\x00" + probes[i].capability.RuntimeID
		if !s.beginRuntimeProbe(probeKey) {
			continue
		}
		scheduled++
		go func(current struct {
			host       model.Host
			capability model.HostCapability
		}, key string) {
			defer s.endRuntimeProbe(key)
			probeCtx, cancel := context.WithTimeout(ctx, runtimeProbeTimeout)
			defer cancel()
			results <- probeOutcome{result: s.probeRuntime(probeCtx, current.host, current.capability)}
		}(probes[i], probeKey)
	}

	changes := make([]RuntimeStatusChange, 0, scheduled)
	var firstErr error
	for range scheduled {
		outcome := <-results
		if outcome.local {
			if outcome.err != nil && firstErr == nil {
				firstErr = outcome.err
			}
			continue
		}
		change, err := s.persistRuntimeProbeResult(ctx, outcome.result)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if change != nil {
			changes = append(changes, *change)
		}
	}
	return changes, firstErr
}

func (s *Service) beginRuntimeProbe(key string) bool {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtimeProbes == nil {
		s.runtimeProbes = make(map[string]struct{})
	}
	if s.runtimeProbeSlots == nil {
		s.runtimeProbeSlots = make(chan struct{}, runtimeProbeConcurrency)
	}
	if _, exists := s.runtimeProbes[key]; exists {
		return false
	}
	select {
	case s.runtimeProbeSlots <- struct{}{}:
		s.runtimeProbes[key] = struct{}{}
		return true
	default:
		return false
	}
}

func (s *Service) endRuntimeProbe(key string) {
	s.runtimeMu.Lock()
	delete(s.runtimeProbes, key)
	<-s.runtimeProbeSlots
	s.runtimeMu.Unlock()
}

func (s *Service) beginLocalRuntimeProbe() bool {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.localRuntimeProbe {
		return false
	}
	s.localRuntimeProbe = true
	return true
}

func (s *Service) endLocalRuntimeProbe() {
	s.runtimeMu.Lock()
	s.localRuntimeProbe = false
	s.runtimeMu.Unlock()
}

func (s *Service) persistRuntimeProbeResult(ctx context.Context, result runtimeProbeResult) (*RuntimeStatusChange, error) {
	if result.previous == result.status && (result.version == "" || result.version == result.previousVersion) {
		return nil, nil
	}
	updates := map[string]any{"status": result.status, "updated_at": time.Now().UTC()}
	if result.version != "" {
		updates["version"] = result.version
	}
	update := s.db.WithContext(ctx).Model(&model.HostCapability{}).
		Where("host_id = ? AND kind = ? AND runtime_id = ? AND status = ?", result.hostID, result.kind, result.runtimeID, result.previous).
		Updates(updates)
	if update.Error != nil {
		return nil, fmt.Errorf("更新主机运行时状态失败: %w", update.Error)
	}
	if update.RowsAffected == 0 || result.previous == result.status {
		return nil, nil
	}
	return &RuntimeStatusChange{
		HostID: result.hostID, Kind: result.kind, Previous: result.previous,
		Status: result.status, Err: result.err,
	}, nil
}

func (s *Service) probeRuntime(ctx context.Context, host model.Host, capability model.HostCapability) runtimeProbeResult {
	result := runtimeProbeResult{
		hostID: host.ID, kind: capability.Kind, runtimeID: capability.RuntimeID,
		previous: capability.Status, previousVersion: capability.Version,
		status: model.HostCapabilityUnreachable,
	}
	if !host.IsActive || host.Mode != model.HostModeSSH {
		result.err = ErrInvalidHost
		return result
	}
	switch capability.Kind {
	case model.HostCapabilityDocker:
		if s.docker == nil {
			result.err = ErrCapabilityUnavailable
			return result
		}
		ping, err := s.docker.PingForMonitor(ctx, capability.RuntimeID)
		if err != nil {
			result.err = err
			return result
		}
		result.status, result.version = model.HostCapabilityReady, ping.APIVersion
	case model.HostCapabilityKubernetes:
		if s.kubernetes == nil {
			result.err = ErrCapabilityUnavailable
			return result
		}
		version, err := s.kubernetes.Ping(ctx, capability.RuntimeID)
		if err != nil {
			result.err = err
			return result
		}
		result.status, result.version = model.HostCapabilityReady, version
	}
	return result
}

func (s *Service) refreshLocalCapabilities(ctx context.Context, force bool) error {
	options := s.probeLocalCapabilityOptions(ctx, force)
	available := make(map[model.HostCapabilityKind]CapabilityOption, len(options))
	for i := range options {
		available[options[i].Kind] = options[i]
	}
	var capabilities []model.HostCapability
	if err := s.db.WithContext(ctx).Where("host_id = ?", model.BuiltinLocalHostID).Find(&capabilities).Error; err != nil {
		return fmt.Errorf("查询本地主机能力失败: %w", err)
	}
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for i := range capabilities {
			option, exists := available[capabilities[i].Kind]
			if !exists {
				continue
			}
			status := model.HostCapabilityUnreachable
			if option.Available {
				status = model.HostCapabilityReady
			}
			if capabilities[i].Status == status && capabilities[i].Version == option.Version {
				continue
			}
			if err := tx.Model(&model.HostCapability{}).
				Where("host_id = ? AND kind = ?", model.BuiltinLocalHostID, capabilities[i].Kind).
				Updates(map[string]any{"status": status, "version": option.Version, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) decorateLocalDetail(ctx context.Context, detail *Detail) {
	if detail == nil {
		return
	}
	detail.CapabilityOptions = s.probeLocalCapabilityOptions(ctx, false)
	options := make(map[model.HostCapabilityKind]CapabilityOption, len(detail.CapabilityOptions))
	for i := range detail.CapabilityOptions {
		options[detail.CapabilityOptions[i].Kind] = detail.CapabilityOptions[i]
	}
	for i := range detail.Capabilities {
		option, exists := options[detail.Capabilities[i].Kind]
		if !exists {
			continue
		}
		detail.Capabilities[i].Status = model.HostCapabilityUnreachable
		if option.Available {
			detail.Capabilities[i].Status = model.HostCapabilityReady
		}
		detail.Capabilities[i].Version = option.Version
	}
}

func (s *Service) probeLocalCapabilityOptions(ctx context.Context, force bool) []CapabilityOption {
	now := time.Now().UTC()
	s.mu.Lock()
	if !force && len(s.localOptions) > 0 && s.localOptionsExpires.After(now) {
		cached := append([]CapabilityOption(nil), s.localOptions...)
		s.mu.Unlock()
		return cached
	}
	s.mu.Unlock()

	dockerOption := CapabilityOption{Kind: model.HostCapabilityDocker}
	if s.docker == nil {
		dockerOption.Reason = "当前运行环境未配置 Docker 客户端"
	} else {
		probeContext, cancel := context.WithTimeout(ctx, localProbeTimeout)
		err := s.docker.PingBuilder(probeContext)
		cancel()
		if err == nil {
			dockerOption.Available = true
		} else {
			dockerOption.Reason = "当前运行环境无法连接 Docker 服务"
			dockerOption.probeErr = err
		}
	}
	localExecOption := detectLocalExecCapability(runtime.GOOS, exec.LookPath)
	options := []CapabilityOption{dockerOption, localExecOption}

	s.mu.Lock()
	s.localOptions = append([]CapabilityOption(nil), options...)
	s.localOptionsExpires = now.Add(localProbeTTL)
	s.mu.Unlock()
	return options
}

func detectLocalExecCapability(goos string, lookPath func(string) (string, error)) CapabilityOption {
	option := CapabilityOption{Kind: model.HostCapabilityLocalExec}
	if goos == "windows" {
		option.Reason = "Windows 原生运行暂不支持直接终端执行"
		option.probeErr = errors.New("native Windows local execution is unsupported")
		return option
	}
	if _, err := lookPath("sh"); err != nil {
		option.Reason = "当前运行环境缺少 sh，无法直接终端执行"
		option.probeErr = err
		return option
	}
	option.Available = true
	return option
}

func (s *Service) Test(ctx context.Context, input Input) (TestResult, error) {
	if input.ReuseCredential {
		return TestResult{}, ErrInvalidHost
	}
	return s.testRemoteInput(ctx, input, "")
}

// TestExisting 使用服务端已加密保存的凭据重新测试远程主机，前端无需再次取得或提交原密码。
func (s *Service) TestExisting(ctx context.Context, id string, input Input) (TestResult, error) {
	var existing model.Host
	if err := s.db.WithContext(ctx).First(&existing, "id = ?", strings.TrimSpace(id)).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return TestResult{}, ErrHostNotFound
		}
		return TestResult{}, fmt.Errorf("查询待测试主机失败: %w", err)
	}
	if existing.IsBuiltin || existing.Mode != model.HostModeSSH {
		return TestResult{}, ErrInvalidHost
	}
	prepared, err := s.prepareUpdateInput(&existing, input)
	if err != nil {
		return TestResult{}, err
	}
	return s.testRemoteInput(ctx, prepared, existing.ID)
}

func (s *Service) testRemoteInput(ctx context.Context, input Input, hostID string) (TestResult, error) {
	normalized, err := normalizeInput(input)
	if err != nil {
		return TestResult{}, err
	}
	if s.docker == nil {
		return TestResult{}, ErrInvalidHost
	}
	hostURL := dockerHostURL(normalized)
	result := TestResult{}
	if slices.Contains(normalized.CapabilityKinds, model.HostCapabilityDocker) {
		tested, err := s.docker.TestSSH(ctx, dockerengine.Input{Host: hostURL, SSH: normalized.SSH})
		if err != nil {
			return TestResult{}, err
		}
		result.Fingerprint, result.DockerVersion = tested.Fingerprint, tested.DockerVersion
	} else {
		tested, err := s.docker.TestSSHConnection(ctx, dockerengine.Input{Host: hostURL, SSH: normalized.SSH})
		if err != nil {
			return TestResult{}, err
		}
		result.Fingerprint = tested.Fingerprint
	}
	if slices.Contains(normalized.CapabilityKinds, model.HostCapabilityKubernetes) {
		if s.kubernetes == nil {
			return TestResult{}, ErrInvalidHost
		}
		version, err := s.kubernetes.Ping(ctx, normalized.KubernetesClusterID)
		if err != nil {
			return TestResult{}, err
		}
		result.KubernetesVersion = version
	}
	now := time.Now().UTC()
	result.Token, result.ExpiresAt = uuid.NewString(), now.Add(testTTL)
	s.mu.Lock()
	for token, tested := range s.tests {
		if tested.expiresAt.Before(now) {
			delete(s.tests, token)
		}
	}
	s.tests[result.Token] = testedInput{
		digest: inputDigest(normalized), result: result, expiresAt: result.ExpiresAt, hostID: hostID,
	}
	s.mu.Unlock()
	return result, nil
}

func (s *Service) Ping(ctx context.Context, id string, selected ...model.HostCapabilityKind) (PingResult, error) {
	capability := model.HostCapabilityKind("")
	if len(selected) > 0 {
		capability = selected[0]
	}
	if capability != "" && capability != model.HostCapabilitySSH && capability != model.HostCapabilityDocker &&
		capability != model.HostCapabilityKubernetes && capability != model.HostCapabilityLocalExec {
		return PingResult{}, ErrInvalidHost
	}
	detail, err := s.Get(ctx, id)
	if err != nil {
		return PingResult{}, err
	}
	current := detail.Host
	if current.IsBuiltin {
		return s.pingLocal(ctx, detail, capability)
	}
	if current.Mode != model.HostModeSSH || !current.IsActive || s.docker == nil {
		return PingResult{}, ErrInvalidHost
	}
	bundle, err := s.decryptCredential(&current)
	if err != nil {
		return PingResult{}, err
	}
	kinds := make([]model.HostCapabilityKind, 0, len(detail.Capabilities))
	clusterID := ""
	for i := range detail.Capabilities {
		kinds = append(kinds, detail.Capabilities[i].Kind)
		if detail.Capabilities[i].Kind == model.HostCapabilityKubernetes {
			clusterID = detail.Capabilities[i].RuntimeID
		}
	}
	hostURL := dockerHostURL(Input{
		Address: current.Address, SSHPort: current.SSHPort, SSHUsername: current.SSHUsername,
	})
	probeInput := dockerengine.Input{
		Host: hostURL, SSH: bundle, SSHHostKeyFingerprint: current.SSHHostKeyFingerprint,
	}
	result := PingResult{Fingerprint: current.SSHHostKeyFingerprint}
	if capability != "" && !slices.Contains(kinds, capability) {
		return PingResult{}, ErrInvalidHost
	}
	if capability == model.HostCapabilitySSH {
		tested, err := s.docker.TestSSHConnection(ctx, probeInput)
		if err != nil {
			return PingResult{}, err
		}
		result.Fingerprint = tested.Fingerprint
		return result, nil
	}
	if capability == model.HostCapabilityDocker || (capability == "" && slices.Contains(kinds, model.HostCapabilityDocker)) {
		tested, err := s.docker.TestSSH(ctx, probeInput)
		if err != nil {
			return PingResult{}, err
		}
		result.Fingerprint, result.DockerVersion = tested.Fingerprint, tested.DockerVersion
	} else if capability == "" {
		tested, err := s.docker.TestSSHConnection(ctx, probeInput)
		if err != nil {
			return PingResult{}, err
		}
		result.Fingerprint = tested.Fingerprint
	}
	if capability == model.HostCapabilityKubernetes || (capability == "" && slices.Contains(kinds, model.HostCapabilityKubernetes)) {
		if s.kubernetes == nil || clusterID == "" {
			return PingResult{}, ErrInvalidHost
		}
		result.KubernetesVersion, err = s.kubernetes.Ping(ctx, clusterID)
		if err != nil {
			return PingResult{}, err
		}
	}
	return result, nil
}

func (s *Service) pingLocal(ctx context.Context, detail *Detail, selected model.HostCapabilityKind) (PingResult, error) {
	if detail == nil || !detail.Host.IsActive || detail.Host.Mode != model.HostModeLocal {
		return PingResult{}, ErrInvalidHost
	}
	if err := s.refreshLocalCapabilities(ctx, true); err != nil {
		return PingResult{}, err
	}
	detail.CapabilityOptions = s.probeLocalCapabilityOptions(ctx, false)
	enabled := make(map[model.HostCapabilityKind]struct{}, len(detail.Capabilities))
	for i := range detail.Capabilities {
		enabled[detail.Capabilities[i].Kind] = struct{}{}
	}
	if selected != "" {
		if selected != model.HostCapabilityDocker && selected != model.HostCapabilityLocalExec {
			return PingResult{}, ErrInvalidHost
		}
		if _, exists := enabled[selected]; !exists {
			return PingResult{}, ErrInvalidHost
		}
	}
	options := make(map[model.HostCapabilityKind]CapabilityOption, len(detail.CapabilityOptions))
	for i := range detail.CapabilityOptions {
		options[detail.CapabilityOptions[i].Kind] = detail.CapabilityOptions[i]
	}
	for kind := range enabled {
		if selected != "" && kind != selected {
			continue
		}
		option, exists := options[kind]
		if !exists || !option.Available {
			if option.probeErr != nil {
				return PingResult{}, fmt.Errorf("本地主机能力测试失败: %w: %v", ErrCapabilityUnavailable, option.probeErr)
			}
			return PingResult{}, ErrCapabilityUnavailable
		}
	}
	return PingResult{}, nil
}

func (s *Service) Create(ctx context.Context, actorID string, input Input) (*Detail, error) {
	normalized, tested, err := s.consumeTest(input, "")
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	ciphertext, err := s.encryptCredential(id, normalized.SSH)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	current := model.Host{
		ID: id, Name: normalized.Name, Mode: model.HostModeSSH,
		Address: normalized.Address, SSHPort: normalized.SSHPort, SSHUsername: normalized.SSHUsername,
		SSHAuthType: normalized.SSHAuthType, SSHCredentialCiphertext: ciphertext,
		SSHHostKeyFingerprint: tested.Fingerprint, IsActive: true,
		CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	var capabilities []model.HostCapability
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&current).Error; err != nil {
			return err
		}
		var err error
		capabilities, err = s.replaceCapabilities(tx, &current, normalized, tested)
		return err
	})
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrHostExists
		}
		if errors.Is(err, ErrHostReferenced) || errors.Is(err, ErrInvalidHost) {
			return nil, err
		}
		return nil, fmt.Errorf("创建主机失败: %w", err)
	}
	return &Detail{Host: current, Capabilities: capabilities}, nil
}

func (s *Service) Update(ctx context.Context, id string, input Input) (*Detail, error) {
	id = strings.TrimSpace(id)
	var existing model.Host
	if err := s.db.WithContext(ctx).First(&existing, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrHostNotFound
		}
		return nil, fmt.Errorf("查询待更新主机失败: %w", err)
	}
	if existing.IsBuiltin {
		return s.updateLocal(ctx, &existing, input)
	}
	prepared, err := s.prepareUpdateInput(&existing, input)
	if err != nil {
		return nil, err
	}
	normalized, tested, err := s.consumeTest(prepared, existing.ID)
	if err != nil {
		return nil, err
	}
	ciphertext := existing.SSHCredentialCiphertext
	if !normalized.ReuseCredential || normalized.UseSudo != nil {
		ciphertext, err = s.encryptCredential(existing.ID, normalized.SSH)
		if err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	var capabilities []model.HostCapability
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Host{}).Where("id = ?", existing.ID).Updates(map[string]any{
			"name": normalized.Name, "address": normalized.Address, "ssh_port": normalized.SSHPort,
			"ssh_username": normalized.SSHUsername, "ssh_auth_type": normalized.SSHAuthType,
			"ssh_credential_ciphertext": ciphertext, "ssh_host_key_fingerprint": tested.Fingerprint,
			"updated_at": now,
		}).Error; err != nil {
			return err
		}
		existing.Name, existing.Address, existing.SSHPort = normalized.Name, normalized.Address, normalized.SSHPort
		existing.SSHUsername, existing.SSHAuthType = normalized.SSHUsername, normalized.SSHAuthType
		existing.SSHCredentialCiphertext, existing.SSHHostKeyFingerprint = ciphertext, tested.Fingerprint
		existing.UpdatedAt = now
		capabilities, err = s.replaceCapabilities(tx, &existing, normalized, tested)
		return err
	})
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrHostExists
		}
		if errors.Is(err, ErrHostReferenced) || errors.Is(err, ErrInvalidHost) {
			return nil, err
		}
		return nil, fmt.Errorf("更新主机失败: %w", err)
	}
	return &Detail{Host: existing, Capabilities: capabilities}, nil
}

func (s *Service) updateLocal(ctx context.Context, existing *model.Host, input Input) (*Detail, error) {
	if existing == nil || existing.ID != model.BuiltinLocalHostID || existing.Mode != model.HostModeLocal {
		return nil, ErrBuiltinHost
	}
	normalized, err := normalizeLocalInput(input)
	if err != nil {
		return nil, err
	}
	options := s.probeLocalCapabilityOptions(ctx, true)
	available := make(map[model.HostCapabilityKind]CapabilityOption, len(options))
	for i := range options {
		available[options[i].Kind] = options[i]
	}
	for _, kind := range normalized.CapabilityKinds {
		if option, exists := available[kind]; !exists || !option.Available {
			if option.probeErr != nil {
				return nil, fmt.Errorf("探测本地主机能力失败: %w: %v", ErrCapabilityUnavailable, option.probeErr)
			}
			return nil, ErrCapabilityUnavailable
		}
	}
	now := time.Now().UTC()
	var capabilities []model.HostCapability
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Host{}).Where("id = ?", existing.ID).
			Updates(map[string]any{"name": normalized.Name, "updated_at": now}).Error; err != nil {
			return err
		}
		var current []model.HostCapability
		if err := tx.Where("host_id = ?", existing.ID).Find(&current).Error; err != nil {
			return err
		}
		currentByKind := make(map[model.HostCapabilityKind]model.HostCapability, len(current))
		for i := range current {
			currentByKind[current[i].Kind] = current[i]
		}
		for _, kind := range normalized.CapabilityKinds {
			capability := currentByKind[kind]
			capability.HostID, capability.Kind = existing.ID, kind
			capability.Status, capability.Version = model.HostCapabilityReady, available[kind].Version
			capability.UseSudo, capability.UpdatedAt = false, now
			if capability.CreatedAt.IsZero() {
				capability.CreatedAt = now
			}
			if kind == model.HostCapabilityDocker {
				capability.RuntimeID = dockerengine.LocalEndpointID
			} else {
				capability.RuntimeID = ""
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "host_id"}, {Name: "kind"}},
				DoUpdates: clause.AssignmentColumns([]string{"runtime_id", "status", "version", "use_sudo", "updated_at"}),
			}).Create(&capability).Error; err != nil {
				return err
			}
			capabilities = append(capabilities, capability)
			delete(currentByKind, kind)
		}
		for _, capability := range currentByKind {
			hostID := ""
			if capability.Kind == model.HostCapabilityLocalExec {
				hostID = existing.ID
			}
			referenced, err := s.runtimeReferenced(ctx, tx, hostID, []string{capability.RuntimeID})
			if err != nil {
				return err
			}
			if referenced {
				return ErrHostReferenced
			}
			if err := tx.Where("host_id = ? AND kind = ?", existing.ID, capability.Kind).
				Delete(&model.HostCapability{}).Error; err != nil {
				return err
			}
		}
		return tx.Model(&model.DockerEndpoint{}).Where("id = ?", dockerengine.LocalEndpointID).
			Updates(map[string]any{"name": normalized.Name, "updated_at": now}).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrHostExists
		}
		if errors.Is(err, ErrHostReferenced) {
			return nil, err
		}
		return nil, fmt.Errorf("更新本地主机失败: %w", err)
	}
	existing.Name, existing.UpdatedAt = normalized.Name, now
	detail := &Detail{Host: *existing, Capabilities: capabilities, CapabilityOptions: options}
	s.decorateLocalDetail(ctx, detail)
	return detail, nil
}

func (s *Service) prepareUpdateInput(existing *model.Host, input Input) (Input, error) {
	if !input.ReuseCredential {
		return input, nil
	}
	if existing == nil || existing.Mode != model.HostModeSSH {
		return Input{}, ErrInvalidHost
	}
	if input.SSH != nil && (input.SSH.Password != "" || strings.TrimSpace(input.SSH.PrivateKey) != "" ||
		input.SSH.Passphrase != "" || input.SSH.SudoPassword != "") {
		return Input{}, ErrInvalidHost
	}
	if input.SSHAuthType != "" && input.SSHAuthType != existing.SSHAuthType {
		return Input{}, ErrInvalidHost
	}
	bundle, err := s.decryptCredential(existing)
	if err != nil {
		return Input{}, err
	}
	if input.UseSudo != nil {
		bundle.UseSudo = *input.UseSudo
	}
	input.SSH, input.SSHAuthType = bundle, existing.SSHAuthType
	return input, nil
}

func (s *Service) SetActive(ctx context.Context, id string, active bool) error {
	id = strings.TrimSpace(id)
	var current model.Host
	if err := s.db.WithContext(ctx).First(&current, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrHostNotFound
		}
		return fmt.Errorf("查询待启停主机失败: %w", err)
	}
	if current.IsBuiltin {
		return ErrBuiltinHost
	}
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.Host{}).Where("id = ?", id).
			Updates(map[string]any{"is_active": active, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrHostNotFound
		}
		return tx.Model(&model.DockerEndpoint{}).Where("host_id = ?", id).
			Updates(map[string]any{"is_active": active, "updated_at": now}).Error
	}); err != nil {
		if errors.Is(err, ErrHostNotFound) {
			return err
		}
		return fmt.Errorf("修改主机状态失败: %w", err)
	}
	return nil
}

func (s *Service) Remove(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	var current model.Host
	if err := s.db.WithContext(ctx).First(&current, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrHostNotFound
		}
		return fmt.Errorf("查询待删除主机失败: %w", err)
	}
	if current.IsBuiltin {
		return ErrBuiltinHost
	}
	var capabilities []model.HostCapability
	if err := s.db.WithContext(ctx).Where("host_id = ?", id).Find(&capabilities).Error; err != nil {
		return fmt.Errorf("查询主机能力失败: %w", err)
	}
	runtimeIDs := make([]string, 0, 1)
	for i := range capabilities {
		// Kubernetes 集群是独立资源，删除主机关系不能把集群发布目标误判为主机引用。
		if capabilities[i].Kind == model.HostCapabilityDocker {
			runtimeIDs = append(runtimeIDs, capabilities[i].RuntimeID)
		}
	}
	referenced, err := s.runtimeReferenced(ctx, s.db, current.ID, runtimeIDs)
	if err != nil {
		return err
	}
	if referenced {
		return ErrHostReferenced
	}
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.DockerEndpoint{}).Where("host_id = ?", id).
			Updates(map[string]any{"host_id": "", "is_active": false, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Where("host_id = ?", id).Delete(&model.HostCapability{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Host{}, "id = ?", id).Error
	}); err != nil {
		return fmt.Errorf("删除主机失败: %w", err)
	}
	return nil
}

func (s *Service) replaceCapabilities(
	tx *gorm.DB,
	current *model.Host,
	input Input,
	tested TestResult,
) ([]model.HostCapability, error) {
	var existing []model.HostCapability
	if err := tx.Where("host_id = ?", current.ID).Find(&existing).Error; err != nil {
		return nil, err
	}
	existingByKind := make(map[model.HostCapabilityKind]model.HostCapability, len(existing))
	for i := range existing {
		existingByKind[existing[i].Kind] = existing[i]
	}
	now := time.Now().UTC()
	desired := make([]model.HostCapability, 0, len(input.CapabilityKinds))
	for _, kind := range input.CapabilityKinds {
		capability := existingByKind[kind]
		capability.HostID, capability.Kind = current.ID, kind
		capability.Status, capability.UpdatedAt = model.HostCapabilityReady, now
		if capability.CreatedAt.IsZero() {
			capability.CreatedAt = now
		}
		switch kind {
		case model.HostCapabilitySSH:
			capability.RuntimeID, capability.Version, capability.UseSudo = "", "", false
		case model.HostCapabilityDocker:
			runtimeID, err := s.upsertDockerEndpoint(tx, current, capability.RuntimeID, input, tested.Fingerprint)
			if err != nil {
				return nil, err
			}
			capability.RuntimeID, capability.Version = runtimeID, tested.DockerVersion
			capability.UseSudo = input.SSH != nil && input.SSH.UseSudo
		case model.HostCapabilityKubernetes:
			capability.RuntimeID, capability.Version = input.KubernetesClusterID, tested.KubernetesVersion
			capability.UseSudo = false
		default:
			return nil, ErrInvalidHost
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "host_id"}, {Name: "kind"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"runtime_id", "status", "version", "use_sudo", "updated_at",
			}),
		}).Create(&capability).Error; err != nil {
			return nil, err
		}
		desired = append(desired, capability)
		delete(existingByKind, kind)
	}
	for _, capability := range existingByKind {
		hostID := ""
		runtimeIDs := []string(nil)
		if capability.Kind == model.HostCapabilitySSH {
			hostID = current.ID
		}
		if capability.Kind == model.HostCapabilityDocker {
			runtimeIDs = []string{capability.RuntimeID}
		}
		referenced, err := s.runtimeReferenced(tx.Statement.Context, tx, hostID, runtimeIDs)
		if err != nil {
			return nil, err
		}
		if referenced {
			return nil, ErrHostReferenced
		}
		if capability.Kind == model.HostCapabilityDocker && capability.RuntimeID != "" {
			if err := tx.Model(&model.DockerEndpoint{}).Where("id = ?", capability.RuntimeID).
				Updates(map[string]any{"is_active": false, "updated_at": now}).Error; err != nil {
				return nil, err
			}
		}
		if err := tx.Where("host_id = ? AND kind = ?", current.ID, capability.Kind).
			Delete(&model.HostCapability{}).Error; err != nil {
			return nil, err
		}
	}
	return desired, nil
}

func (s *Service) upsertDockerEndpoint(
	tx *gorm.DB,
	current *model.Host,
	runtimeID string,
	input Input,
	fingerprint string,
) (string, error) {
	hostURL := dockerHostURL(input)
	now := time.Now().UTC()
	if runtimeID == "" {
		runtimeID = uuid.NewString()
		endpoint := model.DockerEndpoint{
			ID: runtimeID, HostID: current.ID, Name: current.Name, Host: hostURL,
			TLSCiphertext: "", SSHCredentialCiphertext: "", SSHHostKeyFingerprint: fingerprint,
			IsActive: current.IsActive, CreatedBy: current.CreatedBy, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&endpoint).Error; err != nil {
			return "", err
		}
		return runtimeID, nil
	}
	result := tx.Model(&model.DockerEndpoint{}).Where("id = ?", runtimeID).Updates(map[string]any{
		"host_id": current.ID, "name": current.Name, "host": hostURL,
		"ssh_host_key_fingerprint": fingerprint, "is_active": current.IsActive, "updated_at": now,
	})
	if result.Error != nil {
		return "", result.Error
	}
	if result.RowsAffected != 1 {
		return "", ErrInvalidHost
	}
	return runtimeID, nil
}

func (s *Service) consumeTest(input Input, hostID string) (Input, TestResult, error) {
	normalized, err := normalizeInput(input)
	if err != nil {
		return Input{}, TestResult{}, err
	}
	token := strings.TrimSpace(input.TestToken)
	now := time.Now().UTC()
	s.mu.Lock()
	tested, ok := s.tests[token]
	if ok {
		delete(s.tests, token)
	}
	s.mu.Unlock()
	if !ok || tested.expiresAt.Before(now) || tested.hostID != hostID || tested.digest != inputDigest(normalized) {
		return Input{}, TestResult{}, ErrHostTestRequired
	}
	return normalized, tested.result, nil
}

func (s *Service) encryptCredential(hostID string, bundle *dockerengine.SSHBundle) (string, error) {
	if s.secrets == nil || bundle == nil {
		return "", secret.ErrUnavailable
	}
	payload, err := json.Marshal(bundle)
	if err != nil {
		return "", fmt.Errorf("序列化主机 SSH 凭据失败: %w", err)
	}
	value, err := s.secrets.Encrypt(string(payload), hostcredential.AAD(hostID))
	if err != nil {
		return "", fmt.Errorf("加密主机 SSH 凭据失败: %w", err)
	}
	return value, nil
}

func (s *Service) decryptCredential(current *model.Host) (*dockerengine.SSHBundle, error) {
	if s.secrets == nil || current == nil || current.SSHCredentialCiphertext == "" {
		return nil, secret.ErrUnavailable
	}
	value, err := s.secrets.Decrypt(current.SSHCredentialCiphertext, hostcredential.AAD(current.ID))
	if err != nil {
		return nil, fmt.Errorf("解密主机 SSH 凭据失败: %w", err)
	}
	var bundle dockerengine.SSHBundle
	if err := json.Unmarshal([]byte(value), &bundle); err != nil {
		return nil, fmt.Errorf("解析主机 SSH 凭据失败: %w", err)
	}
	return &bundle, nil
}

func (s *Service) listCapabilities(ctx context.Context, ids []string) (map[string][]model.HostCapability, error) {
	result := make(map[string][]model.HostCapability, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var capabilities []model.HostCapability
	if err := s.db.WithContext(ctx).Where("host_id IN ?", ids).
		Order("host_id ASC, kind ASC").Find(&capabilities).Error; err != nil {
		return nil, fmt.Errorf("查询主机能力失败: %w", err)
	}
	for i := range capabilities {
		result[capabilities[i].HostID] = append(result[capabilities[i].HostID], capabilities[i])
	}
	return result, nil
}

func (s *Service) runtimeReferenced(ctx context.Context, db *gorm.DB, hostID string, runtimeIDs []string) (bool, error) {
	hostID = strings.TrimSpace(hostID)
	filteredRuntimeIDs := make([]string, 0, len(runtimeIDs))
	for _, runtimeID := range runtimeIDs {
		if runtimeID = strings.TrimSpace(runtimeID); runtimeID != "" {
			filteredRuntimeIDs = append(filteredRuntimeIDs, runtimeID)
		}
	}
	if hostID == "" && len(filteredRuntimeIDs) == 0 {
		return false, nil
	}
	var count int64
	query := db.WithContext(ctx).Model(&model.DeploymentTarget{})
	if hostID != "" && len(filteredRuntimeIDs) > 0 {
		query = query.Where("host_id = ? OR runtime_id IN ?", hostID, filteredRuntimeIDs)
	} else if hostID != "" {
		query = query.Where("host_id = ?", hostID)
	} else {
		query = query.Where("runtime_id IN ?", filteredRuntimeIDs)
	}
	if err := query.Count(&count).Error; err != nil {
		return false, fmt.Errorf("检查主机发布目标引用失败: %w", err)
	}
	return count > 0, nil
}

func normalizeInput(input Input) (Input, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Address = strings.TrimSpace(input.Address)
	input.SSHUsername = strings.TrimSpace(input.SSHUsername)
	input.KubernetesClusterID = strings.TrimSpace(input.KubernetesClusterID)
	if input.SSHPort == 0 {
		input.SSHPort = 22
	}
	if !hostNamePattern.MatchString(input.Name) || !validAddress(input.Address) ||
		input.SSHPort < 1 || input.SSHPort > 65535 ||
		input.SSHUsername == "" || len(input.SSHUsername) > 255 ||
		strings.ContainsAny(input.SSHUsername, "@:/\\ \t\r\n\x00") || input.SSH == nil {
		return Input{}, ErrInvalidHost
	}
	switch input.SSHAuthType {
	case model.SSHAuthPassword:
		if input.SSH.Password == "" || strings.TrimSpace(input.SSH.PrivateKey) != "" || input.SSH.Passphrase != "" {
			return Input{}, ErrInvalidHost
		}
	case model.SSHAuthPrivateKey:
		if strings.TrimSpace(input.SSH.PrivateKey) == "" || input.SSH.Password != "" {
			return Input{}, ErrInvalidHost
		}
	default:
		return Input{}, ErrInvalidHost
	}
	seen := make(map[model.HostCapabilityKind]struct{}, len(input.CapabilityKinds))
	kinds := make([]model.HostCapabilityKind, 0, len(input.CapabilityKinds))
	for _, kind := range input.CapabilityKinds {
		if kind != model.HostCapabilitySSH && kind != model.HostCapabilityDocker && kind != model.HostCapabilityKubernetes {
			return Input{}, ErrInvalidHost
		}
		if _, exists := seen[kind]; exists {
			continue
		}
		seen[kind] = struct{}{}
		kinds = append(kinds, kind)
	}
	if len(kinds) == 0 {
		return Input{}, ErrInvalidHost
	}
	slices.Sort(kinds)
	input.CapabilityKinds = kinds
	if slices.Contains(kinds, model.HostCapabilityKubernetes) {
		if input.KubernetesClusterID == "" {
			return Input{}, ErrInvalidHost
		}
	} else {
		input.KubernetesClusterID = ""
	}
	return input, nil
}

func normalizeLocalInput(input Input) (Input, error) {
	input.Name = strings.TrimSpace(input.Name)
	if !hostNamePattern.MatchString(input.Name) {
		return Input{}, ErrInvalidHost
	}
	seen := make(map[model.HostCapabilityKind]struct{}, len(input.CapabilityKinds))
	kinds := make([]model.HostCapabilityKind, 0, len(input.CapabilityKinds))
	for _, kind := range input.CapabilityKinds {
		if kind != model.HostCapabilityDocker && kind != model.HostCapabilityLocalExec {
			return Input{}, ErrInvalidHost
		}
		if _, exists := seen[kind]; exists {
			continue
		}
		seen[kind] = struct{}{}
		kinds = append(kinds, kind)
	}
	slices.Sort(kinds)
	input.CapabilityKinds = kinds
	input.Address, input.SSHUsername, input.KubernetesClusterID = "", "", ""
	input.SSHPort, input.SSHAuthType, input.SSH = 0, "", nil
	input.TestToken, input.ReuseCredential, input.UseSudo = "", false, nil
	return input, nil
}

func inputDigest(input Input) [32]byte {
	payload, _ := json.Marshal(struct {
		Name, Address, Username, AuthType, KubernetesClusterID string
		Port                                                   int
		SSH                                                    *dockerengine.SSHBundle
		Capabilities                                           []model.HostCapabilityKind
		ReuseCredential                                        bool
	}{
		Name: input.Name, Address: input.Address, Port: input.SSHPort,
		Username: input.SSHUsername, AuthType: string(input.SSHAuthType), SSH: input.SSH,
		Capabilities: input.CapabilityKinds, KubernetesClusterID: input.KubernetesClusterID,
		ReuseCredential: input.ReuseCredential,
	})
	return sha256.Sum256(payload)
}

func dockerHostURL(input Input) string {
	return (&url.URL{
		Scheme: "ssh", User: url.User(input.SSHUsername),
		Host: net.JoinHostPort(input.Address, fmt.Sprintf("%d", input.SSHPort)),
	}).String()
}

func validAddress(value string) bool {
	if value == "" || len(value) > 253 || strings.ContainsAny(value, " /\\@?#\t\r\n\x00") {
		return false
	}
	if net.ParseIP(value) != nil {
		return true
	}
	return dnsNamePattern.MatchString(value) && !strings.Contains(value, "..")
}

func hostIDs(hosts []model.Host) []string {
	result := make([]string, 0, len(hosts))
	for i := range hosts {
		result = append(result, hosts[i].ID)
	}
	return result
}
