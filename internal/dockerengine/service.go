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
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/moby/moby/client"
	"gorm.io/gorm"

	"zrt/internal/config"
	"zrt/internal/model"
	"zrt/internal/secret"
)

var (
	ErrInvalidEndpoint  = errors.New("Docker 连接配置无效")
	ErrEndpointExists   = errors.New("Docker 连接名称已存在")
	ErrEndpointNotFound = errors.New("Docker 连接不存在")
	ErrTLSRequired      = errors.New("远程 Docker API 必须启用双向 TLS")
	ErrInvalidTLS       = errors.New("Docker TLS 证书配置无效")
)

var endpointNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_. -]{1,127}$`)

type TLSBundle struct {
	CA         string `json:"ca"`
	ClientCert string `json:"client_cert"`
	ClientKey  string `json:"client_key"`
}

type Input struct {
	Name string
	Host string
	TLS  *TLSBundle
}

type Container struct {
	ID      string   `json:"id"`
	Names   []string `json:"names"`
	Image   string   `json:"image"`
	ImageID string   `json:"image_id"`
	Command string   `json:"command"`
	Created int64    `json:"created"`
	State   string   `json:"state"`
	Status  string   `json:"status"`
}

type Service struct {
	db      *gorm.DB
	secrets *secret.Manager
	config  config.Runtime
}

func NewService(db *gorm.DB, secrets *secret.Manager, cfg config.Runtime) *Service {
	return &Service{db: db, secrets: secrets, config: cfg}
}

func (s *Service) List(ctx context.Context) ([]model.DockerEndpoint, error) {
	var endpoints []model.DockerEndpoint
	if err := s.db.WithContext(ctx).Order("name ASC").Find(&endpoints).Error; err != nil {
		return nil, fmt.Errorf("查询 Docker 连接失败: %w", err)
	}
	return endpoints, nil
}

func (s *Service) Find(ctx context.Context, id string) (*model.DockerEndpoint, error) {
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
	name, host, encryptedTLS, err := s.normalize(id, nil, input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	endpoint := &model.DockerEndpoint{
		ID: id, Name: name, Host: host, TLSCiphertext: encryptedTLS,
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
	existing, err := s.Find(ctx, id)
	if err != nil {
		return nil, err
	}
	name, host, encryptedTLS, err := s.normalize(id, existing, input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(existing).Updates(map[string]any{
		"name": name, "host": host, "tls_ciphertext": encryptedTLS, "updated_at": now,
	}).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrEndpointExists
		}
		return nil, fmt.Errorf("更新 Docker 连接失败: %w", err)
	}
	existing.Name, existing.Host, existing.TLSCiphertext, existing.UpdatedAt = name, host, encryptedTLS, now
	return existing, nil
}

func (s *Service) SetActive(ctx context.Context, id string, active bool) error {
	result := s.db.WithContext(ctx).Model(&model.DockerEndpoint{}).Where("id = ?", id).
		Updates(map[string]any{"is_active": active, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return fmt.Errorf("修改 Docker 连接状态失败: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrEndpointNotFound
	}
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
		containers = append(containers, Container{
			ID: item.ID, Names: item.Names, Image: item.Image, ImageID: item.ImageID,
			Command: item.Command, Created: item.Created, State: string(item.State), Status: item.Status,
		})
	}
	return containers, nil
}

func (s *Service) Client(ctx context.Context, id string) (*client.Client, error) {
	endpoint, err := s.Find(ctx, id)
	if err != nil {
		return nil, err
	}
	if !endpoint.IsActive {
		return nil, ErrEndpointNotFound
	}
	transport := &http.Transport{
		DialContext:  (&net.Dialer{Timeout: s.config.ConnectTimeout, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns: 6, IdleConnTimeout: 30 * time.Second,
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
	apiClient, err := client.New(
		client.WithHTTPClient(httpClient), client.WithHost(endpoint.Host),
		client.WithUserAgent("zrt"), client.WithTimeout(s.config.RequestTimeout),
	)
	if err != nil {
		return nil, fmt.Errorf("初始化 Docker API 客户端失败: %w", err)
	}
	return apiClient, nil
}

func (s *Service) normalize(id string, existing *model.DockerEndpoint, input Input) (string, string, string, error) {
	name := strings.TrimSpace(input.Name)
	host := strings.TrimSpace(input.Host)
	if !endpointNamePattern.MatchString(name) || utf8.RuneCountInString(host) > 1024 {
		return "", "", "", ErrInvalidEndpoint
	}
	parsed, err := url.Parse(host)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", "", ErrInvalidEndpoint
	}
	switch parsed.Scheme {
	case "unix":
		if parsed.Host != "" || !filepath.IsAbs(parsed.Path) || parsed.Path == "/" {
			return "", "", "", ErrInvalidEndpoint
		}
	case "tcp":
		if parsed.Host == "" || parsed.Path != "" {
			return "", "", "", ErrInvalidEndpoint
		}
	default:
		return "", "", "", ErrInvalidEndpoint
	}

	encryptedTLS := ""
	if existing != nil {
		encryptedTLS = existing.TLSCiphertext
	}
	if input.TLS != nil {
		if _, err := makeTLSConfig(host, *input.TLS); err != nil {
			return "", "", "", err
		}
		payload, err := json.Marshal(input.TLS)
		if err != nil {
			return "", "", "", fmt.Errorf("序列化 Docker TLS 配置失败: %w", err)
		}
		encryptedTLS, err = s.secrets.Encrypt(string(payload), tlsAAD(id))
		if err != nil {
			return "", "", "", fmt.Errorf("加密 Docker TLS 配置失败: %w", err)
		}
	}
	if parsed.Scheme == "tcp" && encryptedTLS == "" {
		return "", "", "", ErrTLSRequired
	}
	if parsed.Scheme == "unix" {
		encryptedTLS = ""
	}
	return name, host, encryptedTLS, nil
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
