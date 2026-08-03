package dockerengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/containerd/errdefs"
	"github.com/distribution/reference"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"go.yaml.in/yaml/v3"

	"edo/internal/model"
)

const MaximumComposeYAMLBytes = 512 * 1024

var (
	ErrInvalidComposeYAML        = errors.New("Docker Compose 配置无效")
	ErrComposePluginUnavailable  = errors.New("Docker Compose 插件不可用")
	ErrComposeRollbackFailed     = errors.New("恢复旧 Docker Compose 服务失败")
	ErrComposeRuntimeUnavailable = errors.New("Docker Compose 运行时不可用")
	ErrComposeImageUnavailable   = errors.New("Docker Compose 镜像不可用")
	ErrComposeExecutionFailed    = errors.New("Docker Compose 命令执行失败")
	ErrComposeVerificationFailed = errors.New("Docker Compose 服务结果校验失败")
	composeServicePattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
)

type composeDocument struct {
	Include  yaml.Node                 `yaml:"include"`
	Services map[string]composeService `yaml:"services"`
	Configs  map[string]yaml.Node      `yaml:"configs"`
	Secrets  map[string]yaml.Node      `yaml:"secrets"`
	Volumes  map[string]yaml.Node      `yaml:"volumes"`
}

type composeService struct {
	Image             string    `yaml:"image"`
	Build             yaml.Node `yaml:"build"`
	Extends           yaml.Node `yaml:"extends"`
	EnvFile           yaml.Node `yaml:"env_file"`
	LabelFile         yaml.Node `yaml:"label_file"`
	Privileged        yaml.Node `yaml:"privileged"`
	NetworkMode       yaml.Node `yaml:"network_mode"`
	PID               yaml.Node `yaml:"pid"`
	IPC               yaml.Node `yaml:"ipc"`
	UserNSMode        yaml.Node `yaml:"userns_mode"`
	Devices           yaml.Node `yaml:"devices"`
	DeviceCgroupRules yaml.Node `yaml:"device_cgroup_rules"`
	CapAdd            yaml.Node `yaml:"cap_add"`
	SecurityOpt       yaml.Node `yaml:"security_opt"`
	VolumesFrom       yaml.Node `yaml:"volumes_from"`
	Volumes           yaml.Node `yaml:"volumes"`
}

var composeTopLevelKeys = map[string]struct{}{
	"name": {}, "services": {}, "version": {}, "volumes": {},
}

// Compose 的完整规范包含能够读取宿主机文件、复用 Engine API Socket、加入其他
// 容器命名空间或调用运行时插件的入口。EDO 只开放发布常用且不突破主机边界的子集；
// 未知字段必须拒绝，不能在解析结构体时丢弃后再把原始 YAML 交给 Compose。
var composeServiceKeys = map[string]struct{}{
	"cap_drop": {}, "command": {}, "cpus": {}, "depends_on": {}, "dns": {}, "dns_opt": {},
	"dns_search": {}, "domainname": {}, "entrypoint": {}, "environment": {}, "expose": {},
	"healthcheck": {}, "hostname": {}, "image": {}, "init": {}, "labels": {}, "mem_limit": {},
	"mem_reservation": {}, "oom_kill_disable": {}, "oom_score_adj": {}, "pids_limit": {},
	"platform": {}, "ports": {}, "pull_policy": {}, "read_only": {}, "restart": {}, "shm_size": {},
	"stdin_open": {}, "stop_grace_period": {}, "stop_signal": {}, "tmpfs": {}, "tty": {}, "user": {},
	"volumes": {}, "working_dir": {},
}

// ValidateComposeYAML 只接受能够独立执行的内联 Compose 配置。目标服务的镜像由
// EDO 在运行时注入；旧方案中的 ${EDO_IMAGE} 占位符继续兼容，固定镜像仍会被拒绝。
func ValidateComposeYAML(value, serviceName string) error {
	_, err := parseComposeYAML(value, serviceName)
	return err
}

func parseComposeYAML(value, serviceName string) (*composeDocument, error) {
	serviceName = strings.TrimSpace(serviceName)
	if value == "" || len(value) > MaximumComposeYAMLBytes || !utf8.ValidString(value) ||
		strings.ContainsRune(value, '\x00') || !composeServicePattern.MatchString(serviceName) {
		return nil, ErrInvalidComposeYAML
	}
	decoder := yaml.NewDecoder(strings.NewReader(value))
	var documentNode yaml.Node
	if err := decoder.Decode(&documentNode); err != nil {
		return nil, fmt.Errorf("%w: YAML 无法解析", ErrInvalidComposeYAML)
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("%w: 只允许一个 YAML 文档", ErrInvalidComposeYAML)
	}
	if !validComposeInterpolation(&documentNode) {
		return nil, fmt.Errorf("%w: 只允许引用 EDO_IMAGE，容器内变量请使用 $$ 转义", ErrInvalidComposeYAML)
	}
	if !safeComposeDocumentSchema(&documentNode) {
		return nil, fmt.Errorf("%w: 包含未允许的 Compose 字段或宿主机变量引用", ErrInvalidComposeYAML)
	}
	var document composeDocument
	if err := documentNode.Decode(&document); err != nil || len(document.Services) == 0 {
		return nil, fmt.Errorf("%w: services 不能为空", ErrInvalidComposeYAML)
	}
	if document.Include.Kind != 0 {
		return nil, fmt.Errorf("%w: 内联配置不能使用 include", ErrInvalidComposeYAML)
	}
	for _, service := range document.Services {
		if service.Build.Kind != 0 || service.Extends.Kind != 0 || service.EnvFile.Kind != 0 || service.LabelFile.Kind != 0 {
			return nil, fmt.Errorf("%w: 所有服务都不能现场构建或读取外部配置文件", ErrInvalidComposeYAML)
		}
		if composeServiceEscalatesPrivileges(service) {
			return nil, fmt.Errorf("%w: 服务包含不允许的提权或宿主机共享配置", ErrInvalidComposeYAML)
		}
		if !safeComposeServiceVolumes(&service.Volumes) {
			return nil, fmt.Errorf("%w: 服务卷只允许匿名卷、命名卷或 tmpfs，不能挂载宿主机路径", ErrInvalidComposeYAML)
		}
	}
	if composeResourcesReadFiles(document.Configs) || composeResourcesReadFiles(document.Secrets) {
		return nil, fmt.Errorf("%w: 内联配置不能读取主机上的配置或密钥文件", ErrInvalidComposeYAML)
	}
	if !safeComposeVolumeDefinitions(document.Volumes) {
		return nil, fmt.Errorf("%w: 命名卷不能配置自定义驱动或宿主机路径", ErrInvalidComposeYAML)
	}
	service, exists := document.Services[serviceName]
	image := strings.TrimSpace(service.Image)
	if !exists || (image != "" && image != "${EDO_IMAGE}") || service.Build.Kind != 0 {
		return nil, fmt.Errorf("%w: 指定服务的镜像由 EDO 管理，不能填写固定 image 或 build", ErrInvalidComposeYAML)
	}
	return &document, nil
}

func composeYAMLWithManagedImage(value, serviceName string) (string, error) {
	document, err := parseComposeYAML(value, serviceName)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(document.Services[strings.TrimSpace(serviceName)].Image) == "${EDO_IMAGE}" {
		return value, nil
	}

	decoder := yaml.NewDecoder(strings.NewReader(value))
	var documentNode yaml.Node
	if err := decoder.Decode(&documentNode); err != nil {
		return "", fmt.Errorf("%w: YAML 无法解析", ErrInvalidComposeYAML)
	}
	root := composeNode(&documentNode)
	services, exists := yamlMappingValue(root, "services")
	if !exists {
		return "", ErrInvalidComposeYAML
	}
	service, exists := yamlMappingValue(services, strings.TrimSpace(serviceName))
	service = composeNode(service)
	if !exists || service == nil || service.Kind != yaml.MappingNode {
		return "", ErrInvalidComposeYAML
	}
	service.Content = append(service.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "image"},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "${EDO_IMAGE}"},
	)
	var rendered strings.Builder
	encoder := yaml.NewEncoder(&rendered)
	encoder.SetIndent(2)
	if err := encoder.Encode(&documentNode); err != nil {
		return "", fmt.Errorf("%w: 生成执行配置失败", ErrInvalidComposeYAML)
	}
	if err := encoder.Close(); err != nil {
		return "", fmt.Errorf("%w: 生成执行配置失败", ErrInvalidComposeYAML)
	}
	return rendered.String(), nil
}

func safeComposeDocumentSchema(document *yaml.Node) bool {
	root := composeNode(document)
	if root == nil || root.Kind != yaml.MappingNode || !composeMappingKeysAllowed(root, composeTopLevelKeys, true) {
		return false
	}
	services, exists := yamlMappingValue(root, "services")
	services = composeNode(services)
	if !exists || services == nil || services.Kind != yaml.MappingNode || len(services.Content) == 0 {
		return false
	}
	for index := 0; index+1 < len(services.Content); index += 2 {
		key, service := services.Content[index], services.Content[index+1]
		if key.Kind != yaml.ScalarNode || key.Value == "<<" || !composeServicePattern.MatchString(key.Value) ||
			!composeMappingKeysAllowed(service, composeServiceKeys, false) {
			return false
		}
		environment, found := yamlMappingValue(service, "environment")
		if found && !safeComposeEnvironment(environment) {
			return false
		}
	}
	return true
}

func composeMappingKeysAllowed(node *yaml.Node, allowed map[string]struct{}, allowExtensions bool) bool {
	node = composeNode(node)
	if node == nil {
		return false
	}
	if node.Kind == yaml.SequenceNode {
		for _, item := range node.Content {
			if !composeMappingKeysAllowed(item, allowed, allowExtensions) {
				return false
			}
		}
		return true
	}
	if node.Kind != yaml.MappingNode {
		return false
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		key, value := node.Content[index], node.Content[index+1]
		if key.Kind != yaml.ScalarNode {
			return false
		}
		if key.Value == "<<" {
			if !composeMappingKeysAllowed(value, allowed, allowExtensions) {
				return false
			}
			continue
		}
		if _, exists := allowed[key.Value]; !exists && (!allowExtensions || !strings.HasPrefix(key.Value, "x-")) {
			return false
		}
	}
	return true
}

func yamlMappingValue(node *yaml.Node, expected string) (*yaml.Node, bool) {
	node = composeNode(node)
	if node == nil {
		return nil, false
	}
	if node.Kind == yaml.SequenceNode {
		for _, item := range node.Content {
			if value, found := yamlMappingValue(item, expected); found {
				return value, true
			}
		}
		return nil, false
	}
	if node.Kind != yaml.MappingNode {
		return nil, false
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == expected {
			return node.Content[index+1], true
		}
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == "<<" {
			if value, found := yamlMappingValue(node.Content[index+1], expected); found {
				return value, true
			}
		}
	}
	return nil, false
}

func safeComposeEnvironment(node *yaml.Node) bool {
	node = composeNode(node)
	if node == nil {
		return false
	}
	switch node.Kind {
	case yaml.MappingNode:
		for index := 0; index+1 < len(node.Content); index += 2 {
			key, value := node.Content[index], composeNode(node.Content[index+1])
			if key.Kind != yaml.ScalarNode || !environmentNamePattern.MatchString(key.Value) || value == nil ||
				value.Kind != yaml.ScalarNode || value.Tag == "!!null" {
				return false
			}
		}
		return true
	case yaml.SequenceNode:
		for _, item := range node.Content {
			item = composeNode(item)
			if item == nil || item.Kind != yaml.ScalarNode {
				return false
			}
			name, _, found := strings.Cut(item.Value, "=")
			if !found || !environmentNamePattern.MatchString(name) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func composeServiceEscalatesPrivileges(service composeService) bool {
	if service.Privileged.Kind != 0 || service.Devices.Kind != 0 || service.DeviceCgroupRules.Kind != 0 ||
		service.CapAdd.Kind != 0 || service.SecurityOpt.Kind != 0 || service.VolumesFrom.Kind != 0 {
		return true
	}
	if dangerousComposeNamespace(&service.NetworkMode, true) {
		return true
	}
	return dangerousComposeNamespace(&service.PID, false) || dangerousComposeNamespace(&service.IPC, false) ||
		dangerousComposeNamespace(&service.UserNSMode, false)
}

func dangerousComposeNamespace(node *yaml.Node, network bool) bool {
	node = composeNode(node)
	if node == nil || node.Kind == 0 {
		return false
	}
	if node.Kind != yaml.ScalarNode {
		return true
	}
	value := strings.ToLower(strings.TrimSpace(node.Value))
	if network {
		return value == "host" || value == "none" || strings.HasPrefix(value, "container:") || strings.HasPrefix(value, "service:")
	}
	return value == "host" || strings.HasPrefix(value, "container:") || strings.HasPrefix(value, "service:")
}

func safeComposeServiceVolumes(node *yaml.Node) bool {
	node = composeNode(node)
	if node == nil || node.Kind == 0 {
		return true
	}
	if node.Kind != yaml.SequenceNode {
		return false
	}
	for _, item := range node.Content {
		item = composeNode(item)
		if item == nil {
			return false
		}
		switch item.Kind {
		case yaml.ScalarNode:
			if !safeComposeShortVolume(item.Value) {
				return false
			}
		case yaml.MappingNode:
			if !safeComposeLongVolume(item) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func safeComposeShortVolume(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\\\x00") || strings.Contains(strings.ToLower(value), "docker.sock") {
		return false
	}
	parts := strings.Split(value, ":")
	if len(parts) == 1 {
		return safeComposeContainerPath(parts[0])
	}
	if len(parts) < 2 || len(parts) > 3 || !composeVolumeNamePattern.MatchString(parts[0]) {
		return false
	}
	return safeComposeContainerPath(parts[1])
}

func safeComposeLongVolume(node *yaml.Node) bool {
	typeValue, typeExists := yamlMappingScalar(node, "type")
	if !typeExists {
		return false
	}
	typeValue = strings.ToLower(strings.TrimSpace(typeValue))
	target, targetExists := yamlMappingScalar(node, "target")
	if !targetExists || !safeComposeContainerPath(target) {
		return false
	}
	source, sourceExists := yamlMappingScalar(node, "source")
	switch typeValue {
	case "volume":
		return !sourceExists || strings.TrimSpace(source) == "" || composeVolumeNamePattern.MatchString(strings.TrimSpace(source))
	case "tmpfs":
		return !sourceExists || strings.TrimSpace(source) == ""
	default:
		// bind、npipe、cluster 与 image 都会绕过 EDO 的制品和宿主机边界。
		return false
	}
}

func safeComposeContainerPath(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "/") && value != "/" && !strings.ContainsAny(value, "\\\x00") &&
		!strings.Contains(strings.ToLower(value), "docker.sock")
}

var composeVolumeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func safeComposeVolumeDefinitions(volumes map[string]yaml.Node) bool {
	for name, definition := range volumes {
		if !composeVolumeNamePattern.MatchString(name) {
			return false
		}
		node := composeNode(&definition)
		if node == nil || node.Kind == 0 || (node.Kind == yaml.ScalarNode && node.Tag == "!!null") {
			continue
		}
		if node.Kind != yaml.MappingNode {
			return false
		}
		for index := 0; index+1 < len(node.Content); index += 2 {
			key := node.Content[index].Value
			// Compose 默认会使用 EDO 项目名给卷加前缀并标记归属。
			// name/external 会绕过该隔离并挂载主机上已有数据，因此不开放。
			if key != "labels" {
				return false
			}
		}
	}
	return true
}

func composeNode(node *yaml.Node) *yaml.Node {
	for node != nil && (node.Kind == yaml.AliasNode || node.Kind == yaml.DocumentNode) {
		if node.Kind == yaml.AliasNode {
			node = node.Alias
			continue
		}
		if len(node.Content) != 1 {
			return nil
		}
		node = node.Content[0]
	}
	return node
}

func yamlMappingScalar(node *yaml.Node, expected string) (string, bool) {
	node = composeNode(node)
	if node == nil || node.Kind != yaml.MappingNode {
		return "", false
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		key, value := node.Content[index].Value, node.Content[index+1]
		if key == expected {
			value = composeNode(value)
			if value == nil || value.Kind != yaml.ScalarNode {
				return "", false
			}
			return value.Value, true
		}
		if key == "<<" {
			if result, found := yamlMappingScalar(value, expected); found {
				return result, true
			}
		}
	}
	return "", false
}

func composeResourcesReadFiles(resources map[string]yaml.Node) bool {
	for _, resource := range resources {
		if yamlMappingHasKey(&resource, "file") {
			return true
		}
	}
	return false
}

func yamlMappingHasKey(node *yaml.Node, expected string) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.AliasNode {
		return yamlMappingHasKey(node.Alias, expected)
	}
	if node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
		return yamlMappingHasKey(node.Content[0], expected)
	}
	if node.Kind != yaml.MappingNode {
		return false
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == expected {
			return true
		}
		if node.Content[index].Value == "<<" && yamlMappingHasKey(node.Content[index+1], expected) {
			return true
		}
	}
	return false
}

func validComposeInterpolation(node *yaml.Node) bool {
	if node == nil {
		return true
	}
	if node.Kind == yaml.ScalarNode && !validComposeScalarInterpolation(node.Value) {
		return false
	}
	for _, child := range node.Content {
		if !validComposeInterpolation(child) {
			return false
		}
	}
	return true
}

func validComposeScalarInterpolation(value string) bool {
	for index := 0; index < len(value); {
		if value[index] != '$' {
			index++
			continue
		}
		if index+1 >= len(value) {
			return false
		}
		switch value[index+1] {
		case '$':
			index += 2
		case '{':
			end := strings.IndexByte(value[index+2:], '}')
			if end < 0 {
				return false
			}
			expression := value[index+2 : index+2+end]
			name := expression
			if separator := strings.IndexAny(name, ":?+-"); separator >= 0 {
				name = name[:separator]
			}
			if name != "EDO_IMAGE" {
				return false
			}
			index += end + 3
		default:
			end := index + 1
			for end < len(value) && ((value[end] >= 'A' && value[end] <= 'Z') ||
				(value[end] >= 'a' && value[end] <= 'z') || (value[end] >= '0' && value[end] <= '9') || value[end] == '_') {
				end++
			}
			if value[index+1:end] != "EDO_IMAGE" {
				return false
			}
			index = end
		}
	}
	return true
}

type ComposeDeployInput struct {
	EndpointID      string
	TargetID        string
	ServiceName     string
	YAML            string
	Image           string
	ExpectedImageID string
	DeploymentID    string
	RegistryAuth    RegistryAuth
	Timeout         time.Duration
	Stdout          io.Writer
	Stderr          io.Writer
}

// DeployCompose 通过官方 Docker Compose CLI 执行固定的 up 命令。配置从标准输入传入，
// 不创建任意命令入口；本地、Unix Socket、mTLS TCP 和 SSH Docker 连接使用同一套语义。
func (s *Service) DeployCompose(ctx context.Context, input ComposeDeployInput) (string, error) {
	if s == nil || strings.TrimSpace(input.EndpointID) == "" || strings.TrimSpace(input.TargetID) == "" ||
		strings.TrimSpace(input.DeploymentID) == "" || input.Timeout < 30*time.Second || input.Timeout > time.Hour {
		return "", ErrInvalidComposeYAML
	}
	managedYAML, err := composeYAMLWithManagedImage(input.YAML, input.ServiceName)
	if err != nil {
		return "", err
	}
	input.YAML = managedYAML
	if input.ExpectedImageID != "" {
		if !IsEDOLocalImage(input.Image) || !IsValidImageID(input.ExpectedImageID) {
			return "", composeDeploymentError(ErrComposeImageUnavailable, errors.New("待部署的本地 Docker 镜像无效"))
		}
	} else if _, err := parseImmutableComposeImage(input.Image); err != nil {
		return "", composeDeploymentError(ErrComposeImageUnavailable, err)
	}

	endpoint, err := s.Find(ctx, input.EndpointID)
	if err != nil {
		return "", composeDeploymentError(ErrComposeRuntimeUnavailable, err)
	}
	apiClient, err := s.executionClient(ctx, input.EndpointID)
	if err != nil {
		return "", composeDeploymentError(ErrComposeRuntimeUnavailable, err)
	}
	defer apiClient.Close()
	deployContext, cancel := context.WithTimeout(ctx, input.Timeout)
	defer cancel()
	if err := s.prepareComposeImage(deployContext, apiClient, endpoint, input.Image, input.ExpectedImageID, input.RegistryAuth); err != nil {
		return "", composeDeploymentError(ErrComposeImageUnavailable, err)
	}
	projectName := composeProjectName(input.TargetID)
	previousImage, err := composeServiceImage(deployContext, apiClient, projectName, input.ServiceName, "")
	if err != nil {
		return "", composeDeploymentError(ErrComposeVerificationFailed, err)
	}
	executionInput := input
	if input.ExpectedImageID != "" {
		// Compose 同样直接消费已经校验的 Image ID，不能在校验标签后继续把
		// 可变标签交给 compose up，否则两次操作之间仍存在镜像替换窗口。
		executionInput.Image = input.ExpectedImageID
	}
	if err := s.runCompose(deployContext, endpoint, projectName, executionInput); err != nil {
		if contextError := deployContext.Err(); contextError != nil {
			return previousImage, fmt.Errorf("Docker Compose 部署超时或被取消: %w", contextError)
		}
		if stateErr := composeServiceStateError(deployContext, apiClient, projectName, input.ServiceName); stateErr != nil {
			deployErr := errors.Join(stateErr, err)
			return previousImage, s.composeDeploymentErrorWithRollback(
				ctx, endpoint, apiClient, projectName, executionInput, previousImage, deployErr,
			)
		}
		return previousImage, composeDeploymentError(ErrComposeExecutionFailed, err)
	}
	if err := waitComposeServiceHealthy(deployContext, apiClient, projectName, input.ServiceName); err != nil {
		return previousImage, s.composeDeploymentErrorWithRollback(
			ctx, endpoint, apiClient, projectName, executionInput, previousImage, err,
		)
	}
	if _, err := composeServiceImage(deployContext, apiClient, projectName, input.ServiceName, executionInput.Image); err != nil {
		return previousImage, s.composeDeploymentErrorWithRollback(
			ctx, endpoint, apiClient, projectName, executionInput, previousImage,
			composeDeploymentError(ErrComposeVerificationFailed, err),
		)
	}
	return previousImage, nil
}

func composeDeploymentError(category error, cause error) error {
	if cause == nil || errors.Is(cause, category) {
		return cause
	}
	return fmt.Errorf("%w: %w", category, cause)
}

func (s *Service) composeDeploymentErrorWithRollback(
	ctx context.Context,
	endpoint *model.DockerEndpoint,
	apiClient *client.Client,
	projectName string,
	input ComposeDeployInput,
	previousImage string,
	deployErr error,
) error {
	if strings.TrimSpace(previousImage) == "" {
		return deployErr
	}
	rollbackTimeout := min(input.Timeout, 2*time.Minute)
	if rollbackTimeout < 30*time.Second {
		rollbackTimeout = 30 * time.Second
	}
	rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()
	rollbackInput := input
	rollbackInput.Image = previousImage
	rollbackInput.ExpectedImageID = ""
	if err := s.runCompose(rollbackContext, endpoint, projectName, rollbackInput); err != nil {
		return fmt.Errorf("%w: deployment_error=%v rollback_error=%v", ErrComposeRollbackFailed, deployErr, err)
	}
	if err := waitComposeServiceHealthy(rollbackContext, apiClient, projectName, input.ServiceName); err != nil {
		return fmt.Errorf("%w: deployment_error=%v rollback_error=%v", ErrComposeRollbackFailed, deployErr, err)
	}
	if _, err := composeServiceImage(rollbackContext, apiClient, projectName, input.ServiceName, previousImage); err != nil {
		return fmt.Errorf("%w: deployment_error=%v rollback_error=%v", ErrComposeRollbackFailed, deployErr, err)
	}
	return deployErr
}

func parseImmutableComposeImage(value string) (string, error) {
	value = strings.TrimSpace(value)
	named, err := reference.ParseNormalizedNamed(value)
	if err != nil {
		return "", errors.New("Docker Compose 镜像地址无效")
	}
	if _, immutable := named.(reference.Digested); !immutable {
		return "", errors.New("Docker Compose 必须使用不可变镜像摘要")
	}
	return reference.FamiliarString(named), nil
}

func (s *Service) prepareComposeImage(
	ctx context.Context,
	apiClient *client.Client,
	endpoint *model.DockerEndpoint,
	image, expectedImageID string, registry RegistryAuth,
) error {
	if expectedImageID != "" {
		inspect, err := apiClient.ImageInspect(ctx, image)
		if err != nil {
			return fmt.Errorf("目标主机上找不到流水线构建的本地镜像: %w", err)
		}
		if inspect.ID != expectedImageID {
			return errors.New("目标主机上的本地镜像与流水线构建结果不一致")
		}
		return nil
	}
	if _, err := apiClient.ImageInspect(ctx, image); err == nil {
		return nil
	} else if !errdefs.IsNotFound(err) {
		return fmt.Errorf("检查目标主机 Docker Compose 镜像失败: %w", err)
	}
	pulledWithSSH, err := s.pullImageWithSSH(ctx, endpoint.ID, image, registry)
	if err != nil {
		return err
	}
	if !pulledWithSSH {
		encodedAuth, encodeErr := encodeRegistryAuth(registryAuthConfig(registry))
		if encodeErr != nil {
			return encodeErr
		}
		pull, err := apiClient.ImagePull(ctx, image, client.ImagePullOptions{RegistryAuth: encodedAuth})
		if err != nil {
			return fmt.Errorf("拉取 Docker Compose 镜像失败: %w", err)
		}
		if err := pull.Wait(ctx); err != nil {
			return fmt.Errorf("等待 Docker Compose 镜像拉取完成失败: %w", err)
		}
	}
	if _, err := apiClient.ImageInspect(ctx, image); err != nil {
		return fmt.Errorf("校验 Docker Compose 镜像失败: %w", err)
	}
	return nil
}

func composeProjectName(targetID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(targetID)))
	return "edo-" + hex.EncodeToString(digest[:6])
}

func composeServiceImage(
	ctx context.Context,
	apiClient *client.Client,
	projectName, serviceName, expectedImage string,
) (string, error) {
	result, err := apiClient.ContainerList(ctx, client.ContainerListOptions{
		All: true,
		Filters: client.Filters{
			"label": {
				"com.docker.compose.project=" + projectName: true,
				"com.docker.compose.service=" + serviceName: true,
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("读取 Docker Compose 服务状态失败: %w", err)
	}
	if len(result.Items) == 0 {
		if expectedImage == "" {
			return "", nil
		}
		return "", errors.New("Docker Compose 没有创建指定服务")
	}
	currentImage := ""
	for _, item := range result.Items {
		inspect, err := apiClient.ContainerInspect(ctx, item.ID, client.ContainerInspectOptions{})
		if err != nil {
			return "", fmt.Errorf("检查 Docker Compose 服务容器失败: %w", err)
		}
		if inspect.Container.Config == nil {
			return "", errors.New("Docker Compose 服务容器配置不完整")
		}
		if expectedImage != "" && (inspect.Container.State == nil || !inspect.Container.State.Running) {
			return "", fmt.Errorf("%w: service=%s", ErrContainerNotRunning, serviceName)
		}
		image := strings.TrimSpace(inspect.Container.Config.Image)
		if expectedImage != "" && image != expectedImage {
			return "", errors.New("Docker Compose 服务没有使用流水线上游镜像")
		}
		if IsValidImageID(expectedImage) && inspect.Container.Image != expectedImage {
			return "", errors.New("Docker Compose 服务实际镜像与流水线上游 Image ID 不一致")
		}
		if currentImage == "" {
			currentImage = image
		} else if currentImage != image {
			currentImage = ""
		}
	}
	return currentImage, nil
}

func composeServiceStateError(
	ctx context.Context,
	apiClient *client.Client,
	projectName string,
	serviceName string,
) error {
	containers, err := composeServiceContainers(ctx, apiClient, projectName, serviceName)
	if err != nil {
		return err
	}
	if len(containers) == 0 {
		return fmt.Errorf("%w: service=%s container_count=0", ErrContainerNotRunning, serviceName)
	}
	for _, item := range containers {
		inspect, err := apiClient.ContainerInspect(ctx, item.ID, client.ContainerInspectOptions{})
		if err != nil {
			return fmt.Errorf("检查 Docker Compose 服务容器失败: %w", err)
		}
		state := inspect.Container.State
		if state == nil {
			return fmt.Errorf("%w: service=%s 没有可读取的状态", ErrContainerNotRunning, serviceName)
		}
		if state.Restarting || inspect.Container.RestartCount > 0 {
			return fmt.Errorf("%w: service=%s status=%s restart_count=%d exit_code=%d",
				ErrContainerRestarted, serviceName, state.Status, inspect.Container.RestartCount, state.ExitCode)
		}
		if !state.Running {
			return fmt.Errorf("%w: service=%s status=%s exit_code=%d",
				ErrContainerNotRunning, serviceName, state.Status, state.ExitCode)
		}
		if state.Health != nil && state.Health.Status == container.Unhealthy {
			return fmt.Errorf("%w: service=%s failing_streak=%d",
				ErrContainerUnhealthy, serviceName, state.Health.FailingStreak)
		}
	}
	return nil
}

func waitComposeServiceHealthy(
	ctx context.Context,
	apiClient *client.Client,
	projectName string,
	serviceName string,
) error {
	containers, err := composeServiceContainers(ctx, apiClient, projectName, serviceName)
	if err != nil {
		return err
	}
	if len(containers) == 0 {
		return fmt.Errorf("%w: service=%s container_count=0", ErrContainerNotRunning, serviceName)
	}
	for _, item := range containers {
		if err := waitContainerHealthy(ctx, apiClient, item.ID); err != nil {
			return err
		}
	}
	return nil
}

func composeServiceContainers(
	ctx context.Context,
	apiClient *client.Client,
	projectName string,
	serviceName string,
) ([]container.Summary, error) {
	result, err := apiClient.ContainerList(ctx, client.ContainerListOptions{
		All: true,
		Filters: client.Filters{
			"label": {
				"com.docker.compose.project=" + projectName: true,
				"com.docker.compose.service=" + serviceName: true,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("读取 Docker Compose 服务状态失败: %w", err)
	}
	return result.Items, nil
}

func (s *Service) runCompose(
	ctx context.Context,
	endpoint *model.DockerEndpoint,
	projectName string,
	input ComposeDeployInput,
) error {
	parsed, err := url.Parse(endpoint.Host)
	if err != nil {
		return ErrInvalidEndpoint
	}
	if parsed.Scheme == "ssh" {
		host, bundle, fingerprint, err := s.sshConfiguration(ctx, endpoint)
		if err != nil {
			return err
		}
		connector, err := newSSHConnector(host, bundle, fingerprint, s.config.ConnectTimeout)
		if err != nil {
			return err
		}
		return runComposeWithSSH(ctx, connector, bundle, projectName, input)
	}
	return s.runComposeCLI(ctx, endpoint, projectName, input)
}

func runComposeWithSSH(
	ctx context.Context,
	connector *sshConnector,
	bundle SSHBundle,
	projectName string,
	input ComposeDeployInput,
) error {
	client, err := connector.connectPinned(ctx)
	if err != nil {
		return fmt.Errorf("连接 Docker SSH 主机失败: %w", err)
	}
	defer client.Close()
	mode, err := resolveDockerSSHCommandMode(ctx, client, bundle)
	if err != nil {
		return fmt.Errorf("检查远程 Docker sudo 权限失败: %w", err)
	}
	versionCommand, versionInput := mode.prepare("docker compose version --short", nil)
	if err := runSSHCommandWithStreams(ctx, client, versionCommand, versionInput, io.Discard, io.Discard); err != nil {
		return fmt.Errorf("%w: %v", ErrComposePluginUnavailable, err)
	}
	arguments := composeCommandArguments("/tmp", projectName, input.ServiceName, input.Timeout)
	quotedArguments := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		quotedArguments = append(quotedArguments, shellQuote(argument))
	}
	command := "env EDO_IMAGE=" + shellQuote(input.Image) + " docker " + strings.Join(quotedArguments, " ")
	command, commandInput := mode.prepare(command, strings.NewReader(input.YAML))
	if err := runSSHCommandWithStreams(ctx, client, command, commandInput, outputOrDiscard(input.Stdout), outputOrDiscard(input.Stderr)); err != nil {
		return fmt.Errorf("远程执行 Docker Compose 失败: %w", err)
	}
	return nil
}

func (s *Service) runComposeCLI(
	ctx context.Context,
	endpoint *model.DockerEndpoint,
	projectName string,
	input ComposeDeployInput,
) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return errors.New("Docker CLI 未安装，无法执行 Docker Compose 部署")
	}
	directory, err := os.MkdirTemp("", "edo-compose-*")
	if err != nil {
		return fmt.Errorf("创建 Docker Compose 临时目录失败: %w", err)
	}
	defer os.RemoveAll(directory)
	environment, cleanup, err := s.composeCLIEnvironment(endpoint, input.Image, input.RegistryAuth)
	if err != nil {
		return err
	}
	defer cleanup()
	versionCommand := exec.CommandContext(ctx, "docker", "compose", "version", "--short")
	versionCommand.Env = environment
	versionCommand.Stdout, versionCommand.Stderr = io.Discard, io.Discard
	if err := versionCommand.Run(); err != nil {
		return fmt.Errorf("%w: %v", ErrComposePluginUnavailable, err)
	}
	command := exec.CommandContext(ctx, "docker", composeCommandArguments(directory, projectName, input.ServiceName, input.Timeout)...)
	command.Dir = directory
	command.Env = environment
	command.Stdin = strings.NewReader(input.YAML)
	command.Stdout = outputOrDiscard(input.Stdout)
	command.Stderr = outputOrDiscard(input.Stderr)
	if err := command.Run(); err != nil {
		return fmt.Errorf("执行 Docker Compose 失败: %w", err)
	}
	return nil
}

func composeCommandArguments(projectDirectory, projectName, serviceName string, timeout time.Duration) []string {
	seconds := int(timeout / time.Second)
	return []string{
		"compose", "--ansi", "never", "--project-name", projectName,
		"--project-directory", projectDirectory, "--file", "-",
		"up", "--detach", "--no-build", "--pull", "never", "--wait",
		"--wait-timeout", strconv.Itoa(seconds), serviceName,
	}
}

func (s *Service) composeCLIEnvironment(
	endpoint *model.DockerEndpoint,
	image string,
	registry RegistryAuth,
) ([]string, func(), error) {
	if endpoint == nil {
		return nil, func() {}, ErrEndpointNotFound
	}
	environment := selectedEnvironment("PATH", "HOME", "TMPDIR")
	configDirectory, err := writeDockerCLIConfig(registry)
	if err != nil {
		return nil, func() {}, err
	}
	environment = append(environment, "DOCKER_CONFIG="+configDirectory)
	cleanups := []func(){func() { _ = os.RemoveAll(configDirectory) }}
	cleanup := func() {
		for _, current := range cleanups {
			current()
		}
	}
	if IsLocalEndpointID(endpoint.ID) {
		host := strings.TrimSpace(s.config.DockerBuilderHost)
		certPath := strings.TrimSpace(s.config.DockerBuilderTLSCertPath)
		if host == "" {
			if certPath != "" {
				return nil, cleanup, ErrInvalidEndpoint
			}
			environment = append(environment, selectedEnvironment(
				"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH",
			)...)
		} else {
			environment = append(environment, "DOCKER_HOST="+host)
			parsed, err := url.Parse(host)
			if err != nil {
				return nil, cleanup, ErrInvalidEndpoint
			}
			switch parsed.Scheme {
			case "tcp":
				if certPath == "" {
					return nil, cleanup, ErrTLSRequired
				}
				environment = append(environment, "DOCKER_TLS_VERIFY=1", "DOCKER_CERT_PATH="+certPath)
			case "unix":
				if certPath != "" {
					return nil, cleanup, ErrInvalidEndpoint
				}
			default:
				return nil, cleanup, ErrInvalidEndpoint
			}
		}
	} else {
		parsed, err := url.Parse(endpoint.Host)
		if err != nil || (parsed.Scheme != "unix" && parsed.Scheme != "tcp") {
			return nil, cleanup, ErrInvalidEndpoint
		}
		environment = append(environment, "DOCKER_HOST="+endpoint.Host)
		if parsed.Scheme == "tcp" {
			if s.secrets == nil || endpoint.TLSCiphertext == "" {
				return nil, cleanup, ErrTLSRequired
			}
			plaintext, err := s.secrets.Decrypt(endpoint.TLSCiphertext, tlsAAD(endpoint.ID))
			if err != nil {
				return nil, cleanup, fmt.Errorf("解密 Docker TLS 配置失败: %w", err)
			}
			var bundle TLSBundle
			if err := json.Unmarshal([]byte(plaintext), &bundle); err != nil {
				return nil, cleanup, fmt.Errorf("解析 Docker TLS 配置失败: %w", err)
			}
			directory, err := writeComposeTLSDirectory(bundle)
			if err != nil {
				return nil, cleanup, err
			}
			cleanups = append(cleanups, func() { _ = os.RemoveAll(directory) })
			environment = append(environment, "DOCKER_TLS_VERIFY=1", "DOCKER_CERT_PATH="+directory)
		}
	}
	return append(environment, "EDO_IMAGE="+image, "COMPOSE_IGNORE_ORPHANS=1"), cleanup, nil
}

func selectedEnvironment(names ...string) []string {
	result := make([]string, 0, len(names))
	for _, name := range names {
		if value, exists := os.LookupEnv(name); exists {
			result = append(result, name+"="+value)
		}
	}
	return result
}

func writeComposeTLSDirectory(bundle TLSBundle) (string, error) {
	if _, err := makeTLSConfig("tcp://docker.invalid:2376", bundle); err != nil {
		return "", err
	}
	directory, err := os.MkdirTemp("", "edo-compose-tls-*")
	if err != nil {
		return "", fmt.Errorf("创建 Docker TLS 临时目录失败: %w", err)
	}
	files := map[string]string{"ca.pem": bundle.CA, "cert.pem": bundle.ClientCert, "key.pem": bundle.ClientKey}
	for name, value := range files {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(value), 0o600); err != nil {
			_ = os.RemoveAll(directory)
			return "", fmt.Errorf("写入 Docker TLS 临时配置失败: %w", err)
		}
	}
	return directory, nil
}

func outputOrDiscard(output io.Writer) io.Writer {
	if output == nil {
		return io.Discard
	}
	return output
}
