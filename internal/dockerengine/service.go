package dockerengine

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/moby/moby/client"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"edo/internal/config"
	"edo/internal/hostcredential"
	"edo/internal/model"
	"edo/internal/secret"
)

var (
	ErrInvalidEndpoint         = errors.New("Docker 连接配置无效")
	ErrEndpointExists          = errors.New("Docker 连接名称已存在")
	ErrEndpointNotFound        = errors.New("Docker 连接不存在")
	ErrTLSRequired             = errors.New("远程 Docker API 必须启用双向 TLS")
	ErrInvalidTLS              = errors.New("Docker TLS 证书配置无效")
	ErrSSHRequired             = errors.New("SSH Docker 连接必须提供密码或私钥，并先完成连接测试")
	ErrInvalidSSH              = errors.New("SSH Docker 连接配置无效")
	ErrSSHUnreachable          = errors.New("无法通过 SSH 连接 Docker，请检查地址、端口、用户名和凭据")
	ErrSSHDockerDenied         = errors.New("SSH 登录成功，但无法执行 Docker，请检查 sudo 配置和 Docker 权限")
	ErrUnsupportedArchitecture = errors.New("仅支持 AMD64 或 ARM64 主机架构")
)

var endpointNamePattern = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}_. -]{0,127}$`)

const (
	LocalEndpointID          = "edo-local-docker"
	localEndpointHost        = "builder://local"
	defaultLocalEndpointName = "本地 Docker"
	managedImageDisplayLabel = "edo.image.display"
)

type TLSBundle struct {
	CA         string `json:"ca"`
	ClientCert string `json:"client_cert"`
	ClientKey  string `json:"client_key"`
}

type SSHBundle struct {
	PrivateKey   string `json:"private_key"`
	Passphrase   string `json:"passphrase,omitempty"`
	Password     string `json:"password,omitempty"`
	UseSudo      bool   `json:"use_sudo,omitempty"`
	SudoPassword string `json:"sudo_password,omitempty"`
}

type Input struct {
	Name                  string
	Host                  string
	TLS                   *TLSBundle
	SSH                   *SSHBundle
	SSHHostKeyFingerprint string
}

type Container struct {
	ID           string   `json:"id"`
	Names        []string `json:"names"`
	Image        string   `json:"image"`
	ImageDisplay string   `json:"image_display"`
	ImageID      string   `json:"image_id"`
	Command      string   `json:"command"`
	Created      int64    `json:"created"`
	State        string   `json:"state"`
	Status       string   `json:"status"`
}

type Service struct {
	db      *gorm.DB
	secrets *secret.Manager
	config  config.Runtime

	// 事务克隆与主服务共享监控客户端缓存，因此锁也必须共享，不能复制已使用的 Mutex。
	monitorMu      *sync.Mutex
	monitorClients map[string]monitorClientEntry
}

// WithTransaction 让 Docker 连接校验与上层聚合资源共享同一个数据库事务。
// 返回浅拷贝，避免修改并发请求正在使用的 Service。
func (s *Service) WithTransaction(tx *gorm.DB) *Service {
	if s == nil || tx == nil {
		return s
	}
	clone := *s
	clone.db = tx
	return &clone
}

type monitorClientEntry struct {
	client   *client.Client
	revision monitorClientRevision
}

type monitorClientRevision struct {
	endpointUpdatedAt time.Time
	hostUpdatedAt     time.Time
}

func NewService(db *gorm.DB, secrets *secret.Manager, cfg config.Runtime) *Service {
	return &Service{
		db: db, secrets: secrets, config: cfg,
		monitorMu:      &sync.Mutex{},
		monitorClients: make(map[string]monitorClientEntry),
	}
}

func (s *Service) List(ctx context.Context) ([]model.DockerEndpoint, error) {
	var endpoints []model.DockerEndpoint
	if err := s.db.WithContext(ctx).Where("id <> ?", LocalEndpointID).Order("name ASC").Find(&endpoints).Error; err != nil {
		return nil, fmt.Errorf("查询 Docker 连接失败: %w", err)
	}
	local, err := s.localEndpoint(ctx)
	if err != nil {
		return nil, err
	}
	return append([]model.DockerEndpoint{local}, endpoints...), nil
}

func (s *Service) Find(ctx context.Context, id string) (*model.DockerEndpoint, error) {
	if IsLocalEndpointID(id) {
		endpoint, err := s.localEndpoint(ctx)
		if err != nil {
			return nil, err
		}
		return &endpoint, nil
	}
	var endpoint model.DockerEndpoint
	if err := s.db.WithContext(ctx).First(&endpoint, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrEndpointNotFound
		}
		return nil, fmt.Errorf("查询 Docker 连接失败: %w", err)
	}
	return &endpoint, nil
}

func (s *Service) Create(ctx context.Context, actorID string, input Input) (*model.DockerEndpoint, error) {
	id := uuid.NewString()
	name, host, encryptedTLS, encryptedSSH, sshFingerprint, err := s.normalize(id, nil, input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	endpoint := &model.DockerEndpoint{
		ID: id, Name: name, Host: host, TLSCiphertext: encryptedTLS,
		SSHCredentialCiphertext: encryptedSSH, SSHHostKeyFingerprint: sshFingerprint,
		IsActive: true, CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Create(endpoint).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrEndpointExists
		}
		return nil, fmt.Errorf("创建 Docker 连接失败: %w", err)
	}
	return endpoint, nil
}

func (s *Service) Update(ctx context.Context, id string, input Input) (*model.DockerEndpoint, error) {
	if IsLocalEndpointID(id) {
		return s.Rename(ctx, id, input.Name)
	}
	existing, err := s.Find(ctx, id)
	if err != nil {
		return nil, err
	}
	name, host, encryptedTLS, encryptedSSH, sshFingerprint, err := s.normalize(id, existing, input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(existing).Updates(map[string]any{
		"name": name, "host": host, "tls_ciphertext": encryptedTLS,
		"ssh_credential_ciphertext": encryptedSSH, "ssh_host_key_fingerprint": sshFingerprint,
		"updated_at": now,
	}).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrEndpointExists
		}
		return nil, fmt.Errorf("更新 Docker 连接失败: %w", err)
	}
	existing.Name, existing.Host, existing.TLSCiphertext = name, host, encryptedTLS
	existing.SSHCredentialCiphertext, existing.SSHHostKeyFingerprint = encryptedSSH, sshFingerprint
	existing.UpdatedAt = now
	s.invalidateMonitorClient(id)
	return existing, nil
}

func (s *Service) Rename(ctx context.Context, id, value string) (*model.DockerEndpoint, error) {
	name := strings.TrimSpace(value)
	if !endpointNamePattern.MatchString(name) {
		return nil, ErrInvalidEndpoint
	}
	if IsLocalEndpointID(id) {
		now := time.Now().UTC()
		endpoint := model.DockerEndpoint{
			ID: LocalEndpointID, Name: name, Host: localEndpointHost, IsActive: true,
			CreatedBy: "system", CreatedAt: now, UpdatedAt: now,
		}
		if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"name", "host", "is_active", "updated_at"}),
		}).Create(&endpoint).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return nil, ErrEndpointExists
			}
			return nil, fmt.Errorf("更新本地 Docker 连接名称失败: %w", err)
		}
		updated, err := s.localEndpoint(ctx)
		if err != nil {
			return nil, err
		}
		s.invalidateMonitorClient(id)
		return &updated, nil
	}

	result := s.db.WithContext(ctx).Model(&model.DockerEndpoint{}).Where("id = ?", id).
		Updates(map[string]any{"name": name, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrDuplicatedKey) {
			return nil, ErrEndpointExists
		}
		return nil, fmt.Errorf("更新 Docker 连接名称失败: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return nil, ErrEndpointNotFound
	}
	s.invalidateMonitorClient(id)
	return s.Find(ctx, id)
}

func (s *Service) SetActive(ctx context.Context, id string, active bool) error {
	if IsLocalEndpointID(id) {
		return ErrInvalidEndpoint
	}
	result := s.db.WithContext(ctx).Model(&model.DockerEndpoint{}).Where("id = ?", id).
		Updates(map[string]any{"is_active": active, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return fmt.Errorf("修改 Docker 连接状态失败: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrEndpointNotFound
	}
	s.invalidateMonitorClient(id)
	return nil
}

func (s *Service) Ping(ctx context.Context, id string) (client.PingResult, error) {
	apiClient, err := s.Client(ctx, id)
	if err != nil {
		return client.PingResult{}, err
	}
	defer apiClient.Close()
	requestContext, cancel := context.WithTimeout(ctx, s.config.RequestTimeout)
	defer cancel()
	result, err := apiClient.Ping(requestContext, client.PingOptions{NegotiateAPIVersion: true})
	if err != nil {
		return client.PingResult{}, fmt.Errorf("Docker API 健康检查失败: %w", err)
	}
	return result, nil
}

// PingForMonitor 为高频健康检查复用同一 Docker API 客户端，
// 连接配置变化或检查失败时会丢弃已缓存的客户端。
func (s *Service) PingForMonitor(ctx context.Context, id string) (client.PingResult, error) {
	id = strings.TrimSpace(id)
	apiClient, err := s.clientForMonitor(ctx, id)
	if err != nil {
		return client.PingResult{}, err
	}
	requestContext, cancel := context.WithTimeout(ctx, s.config.RequestTimeout)
	defer cancel()
	result, err := apiClient.Ping(requestContext, client.PingOptions{NegotiateAPIVersion: true})
	if err != nil {
		s.invalidateMonitorClientIf(id, apiClient)
		return client.PingResult{}, fmt.Errorf("Docker API 健康检查失败: %w", err)
	}
	return result, nil
}

func (s *Service) Containers(ctx context.Context, id string, all bool) ([]Container, error) {
	apiClient, err := s.Client(ctx, id)
	if err != nil {
		return nil, err
	}
	defer apiClient.Close()
	requestContext, cancel := context.WithTimeout(ctx, s.config.RequestTimeout)
	defer cancel()
	result, err := apiClient.ContainerList(requestContext, client.ContainerListOptions{All: all})
	if err != nil {
		return nil, fmt.Errorf("查询 Docker 容器失败: %w", err)
	}
	containers := make([]Container, 0, len(result.Items))
	for _, item := range result.Items {
		imageDisplay := strings.TrimSpace(item.Labels[managedImageDisplayLabel])
		if imageDisplay == "" {
			imageDisplay = compactContainerImageReference(item.Image)
		}
		containers = append(containers, Container{
			ID: item.ID, Names: item.Names, Image: item.Image, ImageDisplay: imageDisplay, ImageID: item.ImageID,
			Command: item.Command, Created: item.Created, State: string(item.State), Status: item.Status,
		})
	}
	return containers, nil
}

func compactContainerImageReference(value string) string {
	value = strings.TrimSpace(value)
	repository, digest, found := strings.Cut(value, "@sha256:")
	if found && len(digest) >= 12 {
		return path.Base(repository) + "@" + digest[:12]
	}
	return path.Base(value)
}

func (s *Service) Client(ctx context.Context, id string) (*client.Client, error) {
	endpoint, err := s.Find(ctx, id)
	if err != nil {
		return nil, err
	}
	if !endpoint.IsActive {
		return nil, ErrEndpointNotFound
	}
	return s.clientForEndpoint(ctx, endpoint)
}

func (s *Service) clientForEndpoint(ctx context.Context, endpoint *model.DockerEndpoint) (*client.Client, error) {
	if endpoint == nil || !endpoint.IsActive {
		return nil, ErrEndpointNotFound
	}
	if IsLocalEndpointID(endpoint.ID) {
		return s.BuilderClient()
	}
	transport := &http.Transport{
		DialContext:  (&net.Dialer{Timeout: s.config.ConnectTimeout, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns: 6, IdleConnTimeout: 30 * time.Second,
	}
	dockerHost := endpoint.Host
	var sshDialer *sshDockerDialer
	parsedHost, err := url.Parse(endpoint.Host)
	if err != nil {
		return nil, ErrInvalidEndpoint
	}
	if parsedHost.Scheme == "ssh" {
		host, bundle, fingerprint, err := s.sshConfiguration(ctx, endpoint)
		if err != nil {
			return nil, err
		}
		sshDialer, err = newSSHDockerDialer(host, bundle, fingerprint, s.config.ConnectTimeout)
		if err != nil {
			return nil, err
		}
		dockerHost = "tcp://docker"
	}
	if endpoint.TLSCiphertext != "" {
		value, err := s.secrets.Decrypt(endpoint.TLSCiphertext, tlsAAD(endpoint.ID))
		if err != nil {
			return nil, fmt.Errorf("解密 Docker TLS 配置失败: %w", err)
		}
		var bundle TLSBundle
		if err := json.Unmarshal([]byte(value), &bundle); err != nil {
			return nil, fmt.Errorf("解析 Docker TLS 配置失败: %w", err)
		}
		tlsConfig, err := makeTLSConfig(endpoint.Host, bundle)
		if err != nil {
			return nil, err
		}
		transport.TLSClientConfig = tlsConfig
	}
	httpClient := &http.Client{Transport: transport, Timeout: s.config.RequestTimeout, CheckRedirect: client.CheckRedirect}
	options := []client.Opt{
		client.WithHTTPClient(httpClient), client.WithHost(dockerHost),
		client.WithUserAgent("edo"), client.WithTimeout(s.config.RequestTimeout),
	}
	if sshDialer != nil {
		options = append(options, client.WithDialContext(sshDialer.DialContext))
	}
	apiClient, err := client.New(options...)
	if err != nil {
		return nil, fmt.Errorf("初始化 Docker API 客户端失败: %w", err)
	}
	return apiClient, nil
}

func (s *Service) clientForMonitor(ctx context.Context, id string) (*client.Client, error) {
	s.monitorMu.Lock()
	defer s.monitorMu.Unlock()
	if s.monitorClients == nil {
		s.monitorClients = make(map[string]monitorClientEntry)
	}
	endpoint, err := s.Find(ctx, id)
	if err != nil {
		s.invalidateMonitorClientLocked(id)
		return nil, err
	}
	if !endpoint.IsActive {
		s.invalidateMonitorClientLocked(id)
		return nil, ErrEndpointNotFound
	}
	revision, err := s.monitorRevision(ctx, endpoint)
	if err != nil {
		s.invalidateMonitorClientLocked(id)
		return nil, err
	}
	if cached, exists := s.monitorClients[id]; exists {
		if cached.revision == revision {
			return cached.client, nil
		}
		s.invalidateMonitorClientLocked(id)
	}
	apiClient, err := s.clientForEndpoint(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	s.monitorClients[id] = monitorClientEntry{client: apiClient, revision: revision}
	return apiClient, nil
}

func (s *Service) monitorRevision(ctx context.Context, endpoint *model.DockerEndpoint) (monitorClientRevision, error) {
	revision := monitorClientRevision{endpointUpdatedAt: endpoint.UpdatedAt}
	if endpoint.HostID == "" {
		return revision, nil
	}
	var assigned model.Host
	if err := s.db.WithContext(ctx).Select("id", "is_active", "updated_at").
		First(&assigned, "id = ?", endpoint.HostID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return monitorClientRevision{}, ErrEndpointNotFound
		}
		return monitorClientRevision{}, fmt.Errorf("查询 Docker 所属主机状态失败: %w", err)
	}
	if !assigned.IsActive {
		return monitorClientRevision{}, ErrEndpointNotFound
	}
	revision.hostUpdatedAt = assigned.UpdatedAt
	return revision, nil
}

func (s *Service) invalidateMonitorClient(id string) {
	s.monitorMu.Lock()
	defer s.monitorMu.Unlock()
	s.invalidateMonitorClientLocked(strings.TrimSpace(id))
}

func (s *Service) invalidateMonitorClientIf(id string, apiClient *client.Client) {
	s.monitorMu.Lock()
	defer s.monitorMu.Unlock()
	cached, exists := s.monitorClients[id]
	if !exists || cached.client != apiClient {
		return
	}
	s.invalidateMonitorClientLocked(id)
}

func (s *Service) invalidateMonitorClientLocked(id string) {
	cached, exists := s.monitorClients[id]
	if !exists {
		return
	}
	_ = cached.client.Close()
	delete(s.monitorClients, id)
}

func IsLocalEndpointID(id string) bool {
	return strings.TrimSpace(id) == LocalEndpointID
}

func (s *Service) localEndpoint(ctx context.Context) (model.DockerEndpoint, error) {
	endpoint := model.DockerEndpoint{
		ID: LocalEndpointID, Name: defaultLocalEndpointName, Host: localEndpointHost,
		HostID:    model.BuiltinLocalHostID,
		CreatedBy: "system",
	}
	var capability model.HostCapability
	if err := s.db.WithContext(ctx).
		First(&capability, "host_id = ? AND kind = ?", model.BuiltinLocalHostID, model.HostCapabilityDocker).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return model.DockerEndpoint{}, fmt.Errorf("读取本地 Docker 主机能力失败: %w", err)
		}
	} else {
		endpoint.IsActive = capability.Status == model.HostCapabilityReady
	}
	var saved model.DockerEndpoint
	if err := s.db.WithContext(ctx).First(&saved, "id = ?", LocalEndpointID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return endpoint, nil
		}
		return model.DockerEndpoint{}, fmt.Errorf("读取本地 Docker 连接名称失败: %w", err)
	}
	endpoint.Name = saved.Name
	endpoint.CreatedAt, endpoint.UpdatedAt = saved.CreatedAt, saved.UpdatedAt
	return endpoint, nil
}

// BuilderClient 连接当前 EDO 实例的构建运行时。本地二进制使用宿主机 Docker，
// Compose 通过独立的 mTLS 客户端证书连接隔离的 Docker-in-Docker。
func (s *Service) BuilderClient() (*client.Client, error) {
	return s.builderClient(s.config.RequestTimeout)
}

// builderExecutionClient 只使用调用方上下文控制长时间的镜像拉取、文件复制和容器等待。
// 常规 BuilderClient 的 HTTP 总超时不适用于持续数十分钟的构建输出流。
func (s *Service) builderExecutionClient() (*client.Client, error) {
	return s.builderClient(0)
}

func (s *Service) builderClient(requestTimeout time.Duration) (*client.Client, error) {
	host := strings.TrimSpace(s.config.DockerBuilderHost)
	certPath := strings.TrimSpace(s.config.DockerBuilderTLSCertPath)
	options := []client.Opt{
		client.WithUserAgent("edo-builder"),
		client.WithAPIVersionNegotiation(),
	}
	if requestTimeout > 0 {
		options = append(options, client.WithTimeout(requestTimeout))
	}
	if host == "" {
		if certPath != "" {
			return nil, ErrInvalidEndpoint
		}
		options = append([]client.Opt{client.FromEnv}, options...)
	} else {
		parsed, err := url.Parse(host)
		if err != nil {
			return nil, ErrInvalidEndpoint
		}
		connectionOptions := []client.Opt{client.WithHost(host)}
		switch parsed.Scheme {
		case "tcp":
			if certPath == "" {
				return nil, ErrTLSRequired
			}
			connectionOptions = append(connectionOptions, client.WithTLSClientConfig(
				filepath.Join(certPath, "ca.pem"),
				filepath.Join(certPath, "cert.pem"),
				filepath.Join(certPath, "key.pem"),
			))
		case "unix":
			if certPath != "" {
				return nil, ErrInvalidEndpoint
			}
		default:
			return nil, ErrInvalidEndpoint
		}
		options = append(connectionOptions, options...)
	}
	apiClient, err := client.New(options...)
	if err != nil {
		return nil, fmt.Errorf("初始化 Docker 构建客户端失败: %w", err)
	}
	return apiClient, nil
}

// PingBuilder 检查当前构建运行时，供就绪探针和故障诊断使用。
func (s *Service) PingBuilder(ctx context.Context) error {
	apiClient, err := s.BuilderClient()
	if err != nil {
		return err
	}
	defer apiClient.Close()
	if _, err := apiClient.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true}); err != nil {
		return fmt.Errorf("Docker 构建运行时健康检查失败: %w", err)
	}
	return nil
}

// BuilderArchitecture 返回实际执行构建的 Docker daemon 架构。Compose 模式下它
// 来自独立 Docker-in-Docker，二进制模式下来自当前连接的宿主机 Docker。
func (s *Service) BuilderArchitecture(ctx context.Context) (model.HostArchitecture, error) {
	apiClient, err := s.BuilderClient()
	if err != nil {
		return "", err
	}
	defer apiClient.Close()
	info, err := apiClient.Info(ctx, client.InfoOptions{})
	if err != nil {
		return "", fmt.Errorf("读取 Docker 构建运行时架构失败: %w", err)
	}
	architecture, valid := model.NormalizeHostArchitecture(info.Info.Architecture)
	if !valid {
		return "", ErrUnsupportedArchitecture
	}
	return architecture, nil
}

func (s *Service) normalize(id string, existing *model.DockerEndpoint, input Input) (string, string, string, string, string, error) {
	name := strings.TrimSpace(input.Name)
	host := strings.TrimSpace(input.Host)
	if !endpointNamePattern.MatchString(name) || utf8.RuneCountInString(host) > 1024 {
		return "", "", "", "", "", ErrInvalidEndpoint
	}
	parsed, err := url.Parse(host)
	if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", "", "", "", ErrInvalidEndpoint
	}
	switch parsed.Scheme {
	case "unix":
		if parsed.User != nil || parsed.Host != "" || !filepath.IsAbs(parsed.Path) || parsed.Path == "/" {
			return "", "", "", "", "", ErrInvalidEndpoint
		}
	case "tcp":
		if parsed.User != nil || parsed.Host == "" || parsed.Path != "" {
			return "", "", "", "", "", ErrInvalidEndpoint
		}
	case "ssh":
		if parsed.User == nil || parsed.User.Username() == "" || parsed.Hostname() == "" || (parsed.Path != "" && parsed.Path != "/") {
			return "", "", "", "", "", ErrInvalidSSH
		}
		if _, hasPassword := parsed.User.Password(); hasPassword {
			return "", "", "", "", "", ErrInvalidSSH
		}
		if port := parsed.Port(); port != "" {
			value, err := strconv.ParseUint(port, 10, 16)
			if err != nil || value == 0 {
				return "", "", "", "", "", ErrInvalidSSH
			}
		}
	default:
		return "", "", "", "", "", ErrInvalidEndpoint
	}

	encryptedTLS := ""
	encryptedSSH := ""
	sshFingerprint := strings.TrimSpace(input.SSHHostKeyFingerprint)
	if existing != nil {
		encryptedTLS = existing.TLSCiphertext
		encryptedSSH = existing.SSHCredentialCiphertext
		if sshFingerprint == "" {
			sshFingerprint = existing.SSHHostKeyFingerprint
		}
	}
	if input.TLS != nil {
		if _, err := makeTLSConfig(host, *input.TLS); err != nil {
			return "", "", "", "", "", err
		}
		payload, err := json.Marshal(input.TLS)
		if err != nil {
			return "", "", "", "", "", fmt.Errorf("序列化 Docker TLS 配置失败: %w", err)
		}
		encryptedTLS, err = s.secrets.Encrypt(string(payload), tlsAAD(id))
		if err != nil {
			return "", "", "", "", "", fmt.Errorf("加密 Docker TLS 配置失败: %w", err)
		}
	}
	if input.SSH != nil {
		bundle := *input.SSH
		if !bundle.UseSudo {
			bundle.SudoPassword = ""
		}
		if _, err := sshAuthMethods(bundle); err != nil {
			return "", "", "", "", "", err
		}
		payload, err := json.Marshal(&bundle)
		if err != nil {
			return "", "", "", "", "", fmt.Errorf("序列化 Docker SSH 配置失败: %w", err)
		}
		encryptedSSH, err = s.secrets.Encrypt(string(payload), sshAAD(id))
		if err != nil {
			return "", "", "", "", "", fmt.Errorf("加密 Docker SSH 配置失败: %w", err)
		}
	}
	if parsed.Scheme == "tcp" && encryptedTLS == "" {
		return "", "", "", "", "", ErrTLSRequired
	}
	if parsed.Scheme == "ssh" {
		encryptedTLS = ""
		if encryptedSSH == "" || !validSSHFingerprint(sshFingerprint) {
			return "", "", "", "", "", ErrSSHRequired
		}
	} else {
		encryptedSSH, sshFingerprint = "", ""
	}
	if parsed.Scheme == "unix" || parsed.Scheme == "ssh" {
		encryptedTLS = ""
	}
	return name, host, encryptedTLS, encryptedSSH, sshFingerprint, nil
}

func makeTLSConfig(host string, bundle TLSBundle) (*tls.Config, error) {
	if strings.TrimSpace(bundle.CA) == "" || strings.TrimSpace(bundle.ClientCert) == "" || strings.TrimSpace(bundle.ClientKey) == "" {
		return nil, ErrInvalidTLS
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(bundle.CA)) {
		return nil, ErrInvalidTLS
	}
	certificate, err := tls.X509KeyPair([]byte(bundle.ClientCert), []byte(bundle.ClientKey))
	if err != nil {
		return nil, ErrInvalidTLS
	}
	parsed, _ := url.Parse(host)
	serverName := parsed.Hostname()
	return &tls.Config{
		MinVersion: tls.VersionTLS12, RootCAs: pool, Certificates: []tls.Certificate{certificate}, ServerName: serverName,
	}, nil
}

func tlsAAD(id string) []byte { return []byte("docker_endpoint:" + id + ":tls") }

func sshAAD(id string) []byte { return []byte("docker_endpoint:" + id + ":ssh") }

func (s *Service) sshConfiguration(
	ctx context.Context,
	endpoint *model.DockerEndpoint,
) (string, SSHBundle, string, error) {
	if endpoint == nil {
		return "", SSHBundle{}, "", ErrEndpointNotFound
	}
	hostURL := endpoint.Host
	fingerprint := endpoint.SSHHostKeyFingerprint
	ciphertext := endpoint.SSHCredentialCiphertext
	aad := sshAAD(endpoint.ID)
	if endpoint.HostID != "" {
		var assigned model.Host
		if err := s.db.WithContext(ctx).First(&assigned, "id = ? AND is_active = ?", endpoint.HostID, true).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return "", SSHBundle{}, "", ErrEndpointNotFound
			}
			return "", SSHBundle{}, "", fmt.Errorf("查询 Docker 所属主机失败: %w", err)
		}
		if assigned.Mode != model.HostModeSSH {
			return "", SSHBundle{}, "", ErrInvalidSSH
		}
		if assigned.Address != "" && assigned.SSHPort > 0 && assigned.SSHUsername != "" {
			hostURL = (&url.URL{
				Scheme: "ssh", User: url.User(assigned.SSHUsername),
				Host: net.JoinHostPort(assigned.Address, strconv.Itoa(assigned.SSHPort)),
			}).String()
		}
		if assigned.SSHHostKeyFingerprint != "" {
			fingerprint = assigned.SSHHostKeyFingerprint
		}
		if assigned.SSHCredentialCiphertext != "" {
			ciphertext = assigned.SSHCredentialCiphertext
			aad = hostcredential.AAD(assigned.ID)
		}
	}
	if ciphertext == "" {
		return "", SSHBundle{}, "", ErrSSHRequired
	}
	value, err := s.secrets.Decrypt(ciphertext, aad)
	if err != nil {
		return "", SSHBundle{}, "", fmt.Errorf("解密 Docker SSH 凭据失败: %w", err)
	}
	var bundle SSHBundle
	if err := json.Unmarshal([]byte(value), &bundle); err != nil {
		return "", SSHBundle{}, "", fmt.Errorf("解析 Docker SSH 凭据失败: %w", err)
	}
	return hostURL, bundle, fingerprint, nil
}
