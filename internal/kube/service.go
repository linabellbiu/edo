package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"zrt/internal/config"
	"zrt/internal/model"
	"zrt/internal/secret"
)

var (
	ErrInvalidCluster     = errors.New("Kubernetes 集群配置无效")
	ErrClusterExists      = errors.New("Kubernetes 集群名称已存在")
	ErrClusterNotFound    = errors.New("Kubernetes 集群不存在")
	ErrUnsafeKubeconfig   = errors.New("kubeconfig 包含不允许的外部命令、文件引用或身份模拟配置")
	ErrKubeconfigRequired = errors.New("请提供 kubeconfig")
	ErrClusterUnreachable = errors.New("无法连接 Kubernetes API，请检查集群凭据和网络")
)

var clusterNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_. -]{1,127}$`)

type Input struct {
	Name             string
	Mode             model.KubernetesMode
	DefaultNamespace string
	Kubeconfig       *string
}

type ConnectionTest struct {
	APIServer string `json:"api_server"`
	Version   string `json:"version"`
}

type Pod struct {
	Namespace  string            `json:"namespace"`
	Name       string            `json:"name"`
	Phase      string            `json:"phase"`
	NodeName   string            `json:"node_name"`
	PodIP      string            `json:"pod_ip"`
	Labels     map[string]string `json:"labels"`
	CreatedAt  time.Time         `json:"created_at"`
	Containers []string          `json:"containers"`
}

type Deployment struct {
	Namespace          string            `json:"namespace"`
	Name               string            `json:"name"`
	Replicas           int32             `json:"replicas"`
	ReadyReplicas      int32             `json:"ready_replicas"`
	AvailableReplicas  int32             `json:"available_replicas"`
	UpdatedReplicas    int32             `json:"updated_replicas"`
	Generation         int64             `json:"generation"`
	ObservedGeneration int64             `json:"observed_generation"`
	Labels             map[string]string `json:"labels"`
}

type Service struct {
	db      *gorm.DB
	secrets *secret.Manager
	config  config.Runtime
}

// WithTransaction 让集群连接校验与上层聚合资源共享同一个数据库事务。
// 返回浅拷贝，避免修改并发请求正在使用的 Service。
func (s *Service) WithTransaction(tx *gorm.DB) *Service {
	if s == nil || tx == nil {
		return s
	}
	clone := *s
	clone.db = tx
	return &clone
}

func NewService(db *gorm.DB, secrets *secret.Manager, cfg config.Runtime) *Service {
	return &Service{db: db, secrets: secrets, config: cfg}
}

func (s *Service) List(ctx context.Context) ([]model.KubernetesCluster, error) {
	var clusters []model.KubernetesCluster
	if err := s.db.WithContext(ctx).Order("name ASC").Find(&clusters).Error; err != nil {
		return nil, fmt.Errorf("查询 Kubernetes 集群失败: %w", err)
	}
	return clusters, nil
}

func (s *Service) Find(ctx context.Context, id string) (*model.KubernetesCluster, error) {
	var cluster model.KubernetesCluster
	if err := s.db.WithContext(ctx).First(&cluster, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrClusterNotFound
		}
		return nil, fmt.Errorf("查询 Kubernetes 集群失败: %w", err)
	}
	return &cluster, nil
}

func (s *Service) Create(ctx context.Context, actorID string, input Input) (*model.KubernetesCluster, error) {
	id := uuid.NewString()
	name, namespace, apiServer, encrypted, err := s.normalize(id, nil, input)
	if err != nil {
		return nil, err
	}
	if _, err := s.Test(ctx, input); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	cluster := &model.KubernetesCluster{
		ID: id, Name: name, Mode: input.Mode, APIServer: apiServer,
		DefaultNamespace: namespace, KubeconfigCiphertext: encrypted,
		IsActive: true, CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.WithContext(ctx).Create(cluster).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrClusterExists
		}
		return nil, fmt.Errorf("创建 Kubernetes 集群失败: %w", err)
	}
	return cluster, nil
}

// Test 使用尚未保存的连接配置访问 Kubernetes API，不写入 kubeconfig 或集群记录。
func (s *Service) Test(ctx context.Context, input Input) (*ConnectionTest, error) {
	name := strings.TrimSpace(input.Name)
	if !clusterNamePattern.MatchString(name) || utf8.RuneCountInString(name) > 128 {
		return nil, ErrInvalidCluster
	}
	if _, err := normalizeNamespace(input.DefaultNamespace); err != nil {
		return nil, err
	}
	restConfig, err := restConfigFromInput(input)
	if err != nil {
		return nil, err
	}
	s.configureRESTConfig(restConfig)

	requestContext := ctx
	cancel := func() {}
	if s.config.ConnectTimeout > 0 {
		requestContext, cancel = context.WithTimeout(ctx, s.config.ConnectTimeout)
	}
	defer cancel()

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("%w: 初始化 Kubernetes 客户端失败: %w", ErrClusterUnreachable, err)
	}
	body, err := clientset.Discovery().RESTClient().Get().AbsPath("/version").Do(requestContext).Raw()
	if err != nil {
		return nil, fmt.Errorf("%w: 请求 Kubernetes API %q 失败: %w", ErrClusterUnreachable, restConfig.Host, err)
	}
	var info version.Info
	if err := json.Unmarshal(body, &info); err != nil || strings.TrimSpace(info.GitVersion) == "" {
		if err == nil {
			err = errors.New("版本字段为空")
		}
		return nil, fmt.Errorf("%w: 解析 Kubernetes API 版本失败: %w", ErrClusterUnreachable, err)
	}
	return &ConnectionTest{APIServer: restConfig.Host, Version: info.GitVersion}, nil
}

func (s *Service) Update(ctx context.Context, id string, input Input) (*model.KubernetesCluster, error) {
	existing, err := s.Find(ctx, id)
	if err != nil {
		return nil, err
	}
	name, namespace, apiServer, encrypted, err := s.normalize(id, existing, input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(existing).Updates(map[string]any{
		"name": name, "mode": input.Mode, "api_server": apiServer,
		"default_namespace": namespace, "kubeconfig_ciphertext": encrypted, "updated_at": now,
	}).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, ErrClusterExists
		}
		return nil, fmt.Errorf("更新 Kubernetes 集群失败: %w", err)
	}
	existing.Name, existing.Mode, existing.APIServer = name, input.Mode, apiServer
	existing.DefaultNamespace, existing.KubeconfigCiphertext, existing.UpdatedAt = namespace, encrypted, now
	return existing, nil
}

func (s *Service) SetActive(ctx context.Context, id string, active bool) error {
	result := s.db.WithContext(ctx).Model(&model.KubernetesCluster{}).Where("id = ?", id).
		Updates(map[string]any{"is_active": active, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return fmt.Errorf("修改 Kubernetes 集群状态失败: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrClusterNotFound
	}
	return nil
}

func (s *Service) Ping(ctx context.Context, id string) (string, error) {
	clientset, err := s.Clientset(ctx, id)
	if err != nil {
		return "", err
	}
	body, err := clientset.Discovery().RESTClient().Get().AbsPath("/version").Do(ctx).Raw()
	if err != nil {
		return "", fmt.Errorf("Kubernetes API 健康检查失败: %w", err)
	}
	var info version.Info
	if err := json.Unmarshal(body, &info); err != nil {
		return "", fmt.Errorf("解析 Kubernetes API 版本失败: %w", err)
	}
	return info.GitVersion, nil
}

func (s *Service) Namespaces(ctx context.Context, id string) ([]string, error) {
	clientset, err := s.Clientset(ctx, id)
	if err != nil {
		return nil, err
	}
	requestContext, cancel := context.WithTimeout(ctx, s.config.RequestTimeout)
	defer cancel()
	result, err := clientset.CoreV1().Namespaces().List(requestContext, metav1.ListOptions{Limit: 500})
	if err != nil {
		return nil, fmt.Errorf("查询 Kubernetes 命名空间失败: %w", err)
	}
	names := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		names = append(names, item.Name)
	}
	return names, nil
}

func (s *Service) Pods(ctx context.Context, id, namespace string) ([]Pod, error) {
	namespace, err := normalizeNamespace(namespace)
	if err != nil {
		return nil, err
	}
	clientset, err := s.Clientset(ctx, id)
	if err != nil {
		return nil, err
	}
	requestContext, cancel := context.WithTimeout(ctx, s.config.RequestTimeout)
	defer cancel()
	result, err := clientset.CoreV1().Pods(namespace).List(requestContext, metav1.ListOptions{Limit: 500})
	if err != nil {
		return nil, fmt.Errorf("查询 Kubernetes Pod 失败: %w", err)
	}
	pods := make([]Pod, 0, len(result.Items))
	for _, item := range result.Items {
		containers := make([]string, 0, len(item.Spec.Containers))
		for _, container := range item.Spec.Containers {
			containers = append(containers, container.Name)
		}
		pods = append(pods, Pod{
			Namespace: item.Namespace, Name: item.Name, Phase: string(item.Status.Phase),
			NodeName: item.Spec.NodeName, PodIP: item.Status.PodIP, Labels: item.Labels,
			CreatedAt: item.CreationTimestamp.Time, Containers: containers,
		})
	}
	return pods, nil
}

func (s *Service) Deployments(ctx context.Context, id, namespace string) ([]Deployment, error) {
	namespace, err := normalizeNamespace(namespace)
	if err != nil {
		return nil, err
	}
	clientset, err := s.Clientset(ctx, id)
	if err != nil {
		return nil, err
	}
	requestContext, cancel := context.WithTimeout(ctx, s.config.RequestTimeout)
	defer cancel()
	result, err := clientset.AppsV1().Deployments(namespace).List(requestContext, metav1.ListOptions{Limit: 500})
	if err != nil {
		return nil, fmt.Errorf("查询 Kubernetes Deployment 失败: %w", err)
	}
	deployments := make([]Deployment, 0, len(result.Items))
	for _, item := range result.Items {
		replicas := int32(0)
		if item.Spec.Replicas != nil {
			replicas = *item.Spec.Replicas
		}
		deployments = append(deployments, Deployment{
			Namespace: item.Namespace, Name: item.Name, Replicas: replicas,
			ReadyReplicas: item.Status.ReadyReplicas, AvailableReplicas: item.Status.AvailableReplicas,
			UpdatedReplicas: item.Status.UpdatedReplicas, Generation: item.Generation,
			ObservedGeneration: item.Status.ObservedGeneration, Labels: item.Labels,
		})
	}
	return deployments, nil
}

func (s *Service) Clientset(ctx context.Context, id string) (*kubernetes.Clientset, error) {
	restConfig, err := s.RESTConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("初始化 Kubernetes 客户端失败: %w", err)
	}
	return clientset, nil
}

func (s *Service) RESTConfig(ctx context.Context, id string) (*rest.Config, error) {
	cluster, err := s.Find(ctx, id)
	if err != nil {
		return nil, err
	}
	if !cluster.IsActive {
		return nil, ErrClusterNotFound
	}
	var result *rest.Config
	switch cluster.Mode {
	case model.KubernetesInCluster:
		result, err = rest.InClusterConfig()
	case model.KubernetesKubeconfig:
		var raw string
		raw, err = s.secrets.Decrypt(cluster.KubeconfigCiphertext, kubeconfigAAD(cluster.ID))
		if err == nil {
			result, err = safeRESTConfig([]byte(raw))
		}
	default:
		err = ErrInvalidCluster
	}
	if err != nil {
		return nil, fmt.Errorf("加载 Kubernetes 连接配置失败: %w", err)
	}
	s.configureRESTConfig(result)
	return result, nil
}

func (s *Service) configureRESTConfig(result *rest.Config) {
	result.Timeout = s.config.RequestTimeout
	result.QPS = 20
	result.Burst = 40
	result.UserAgent = "zrt"
}

func (s *Service) normalize(
	id string,
	existing *model.KubernetesCluster,
	input Input,
) (name, namespace, apiServer, encrypted string, err error) {
	name = strings.TrimSpace(input.Name)
	if !clusterNamePattern.MatchString(name) || utf8.RuneCountInString(name) > 128 {
		err = ErrInvalidCluster
		return
	}
	namespace, err = normalizeNamespace(input.DefaultNamespace)
	if err != nil {
		return
	}
	if existing != nil {
		encrypted = existing.KubeconfigCiphertext
		apiServer = existing.APIServer
	}
	switch input.Mode {
	case model.KubernetesInCluster:
		encrypted = ""
		apiServer = "in-cluster"
	case model.KubernetesKubeconfig:
		if input.Kubeconfig != nil {
			value := strings.TrimSpace(*input.Kubeconfig)
			if value == "" || len(value) > 1024*1024 {
				err = ErrKubeconfigRequired
				return
			}
			var restConfig *rest.Config
			restConfig, err = safeRESTConfig([]byte(value))
			if err != nil {
				return
			}
			apiServer = restConfig.Host
			encrypted, err = s.secrets.Encrypt(value, kubeconfigAAD(id))
			if err != nil {
				err = fmt.Errorf("加密 kubeconfig 失败: %w", err)
				return
			}
		} else if existing == nil || existing.Mode != model.KubernetesKubeconfig || encrypted == "" {
			err = ErrKubeconfigRequired
			return
		}
	default:
		err = ErrInvalidCluster
	}
	return
}

func safeRESTConfig(data []byte) (*rest.Config, error) {
	rawConfig, err := clientcmd.Load(data)
	if err != nil {
		return nil, ErrInvalidCluster
	}
	for _, cluster := range rawConfig.Clusters {
		if cluster == nil || cluster.CertificateAuthority != "" || cluster.ProxyURL != "" || cluster.InsecureSkipTLSVerify {
			return nil, ErrUnsafeKubeconfig
		}
		parsed, err := url.Parse(cluster.Server)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, ErrInvalidCluster
		}
	}
	for _, authInfo := range rawConfig.AuthInfos {
		if authInfo == nil || authInfo.Exec != nil || authInfo.AuthProvider != nil ||
			authInfo.ClientCertificate != "" || authInfo.ClientKey != "" || authInfo.TokenFile != "" ||
			authInfo.Impersonate != "" || len(authInfo.ImpersonateGroups) > 0 || len(authInfo.ImpersonateUserExtra) > 0 {
			return nil, ErrUnsafeKubeconfig
		}
	}
	result, err := clientcmd.RESTConfigFromKubeConfig(data)
	if err != nil {
		return nil, ErrInvalidCluster
	}
	return result, nil
}

func restConfigFromInput(input Input) (*rest.Config, error) {
	switch input.Mode {
	case model.KubernetesInCluster:
		result, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("%w: 读取集群内连接配置失败: %w", ErrClusterUnreachable, err)
		}
		return result, nil
	case model.KubernetesKubeconfig:
		if input.Kubeconfig == nil {
			return nil, ErrKubeconfigRequired
		}
		value := strings.TrimSpace(*input.Kubeconfig)
		if value == "" || len(value) > 1024*1024 {
			return nil, ErrKubeconfigRequired
		}
		return safeRESTConfig([]byte(value))
	default:
		return nil, ErrInvalidCluster
	}
}

func normalizeNamespace(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "default"
	}
	if messages := validation.IsDNS1123Label(value); len(messages) > 0 {
		return "", ErrInvalidCluster
	}
	return value, nil
}

func kubeconfigAAD(id string) []byte { return []byte("kubernetes_cluster:" + id + ":kubeconfig") }
