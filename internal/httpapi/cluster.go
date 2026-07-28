package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"zrt/internal/dockerengine"
	"zrt/internal/kube"
	"zrt/internal/model"
	"zrt/internal/secret"
)

type clusterHandler struct {
	docker *dockerengine.Service
	kube   *kube.Service
	logger *slog.Logger
}

type dockerEndpointRequest struct {
	Name                  string                  `json:"name" binding:"required,max=128"`
	Host                  string                  `json:"host" binding:"required,max=1024"`
	TLS                   *dockerengine.TLSBundle `json:"tls"`
	SSH                   *dockerengine.SSHBundle `json:"ssh"`
	SSHHostKeyFingerprint string                  `json:"ssh_host_key_fingerprint" binding:"max=128"`
}

type runtimeStatusRequest struct {
	Active *bool `json:"active" binding:"required"`
}

type runtimeNameRequest struct {
	Name string `json:"name" binding:"required,max=128"`
}

type dockerEndpointResponse struct {
	ID                    string    `json:"id"`
	Name                  string    `json:"name"`
	Host                  string    `json:"host"`
	Local                 bool      `json:"local"`
	TLSConfigured         bool      `json:"tls_configured"`
	SSHConfigured         bool      `json:"ssh_configured"`
	SSHHostKeyFingerprint string    `json:"ssh_host_key_fingerprint,omitempty"`
	IsActive              bool      `json:"is_active"`
	CreatedBy             string    `json:"created_by"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type kubernetesClusterRequest struct {
	Name             string               `json:"name" binding:"required,max=128"`
	Mode             model.KubernetesMode `json:"mode" binding:"required,max=16"`
	DefaultNamespace string               `json:"default_namespace" binding:"max=63"`
	Kubeconfig       *string              `json:"kubeconfig" binding:"omitempty,max=1048576"`
}

type kubernetesClusterResponse struct {
	ID                   string               `json:"id"`
	Name                 string               `json:"name"`
	Mode                 model.KubernetesMode `json:"mode"`
	APIServer            string               `json:"api_server"`
	DefaultNamespace     string               `json:"default_namespace"`
	KubeconfigConfigured bool                 `json:"kubeconfig_configured"`
	IsActive             bool                 `json:"is_active"`
	CreatedBy            string               `json:"created_by"`
	CreatedAt            time.Time            `json:"created_at"`
	UpdatedAt            time.Time            `json:"updated_at"`
}

func (h clusterHandler) listDockerEndpoints(c *gin.Context) {
	endpoints, err := h.docker.List(c.Request.Context())
	if err != nil {
		h.logger.Error("查询 Docker 连接列表失败", "operation", "docker_endpoint_list", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	response := make([]dockerEndpointResponse, 0, len(endpoints))
	for i := range endpoints {
		response = append(response, toDockerEndpointResponse(&endpoints[i]))
	}
	c.JSON(http.StatusOK, gin.H{"endpoints": response})
}

func (h clusterHandler) createDockerEndpoint(c *gin.Context) {
	var request dockerEndpointRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("创建 Docker 连接参数无效", "operation", "docker_endpoint_create_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", dockerengine.ErrInvalidEndpoint.Error())
		return
	}
	actor, _ := currentUser(c)
	endpoint, err := h.docker.Create(c.Request.Context(), actor.ID, toDockerInput(request))
	if err != nil {
		h.writeDockerError(c, "docker_endpoint_create", err)
		return
	}
	setAuditResourceID(c, endpoint.ID)
	c.JSON(http.StatusCreated, gin.H{"endpoint": toDockerEndpointResponse(endpoint)})
}

func (h clusterHandler) testDockerSSH(c *gin.Context) {
	var request dockerEndpointRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("测试 Docker SSH 参数无效", "operation", "docker_ssh_test_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", dockerengine.ErrInvalidSSH.Error())
		return
	}
	result, err := h.docker.TestSSH(c.Request.Context(), toDockerInput(request))
	if err != nil {
		h.logger.Warn("Docker SSH 连接测试失败", "operation", "docker_ssh_test", "request_id", requestIDFrom(c), "err", err)
		switch {
		case errors.Is(err, dockerengine.ErrInvalidSSH):
			writeError(c, http.StatusBadRequest, "invalid_docker_ssh", dockerengine.ErrInvalidSSH.Error())
		default:
			writeError(c, http.StatusBadGateway, "docker_ssh_unreachable", dockerengine.ErrSSHUnreachable.Error())
		}
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h clusterHandler) updateDockerEndpoint(c *gin.Context) {
	var request dockerEndpointRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("更新 Docker 连接参数无效", "operation", "docker_endpoint_update_bind", "request_id", requestIDFrom(c), "endpoint_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", dockerengine.ErrInvalidEndpoint.Error())
		return
	}
	endpoint, err := h.docker.Update(c.Request.Context(), c.Param("id"), toDockerInput(request))
	if err != nil {
		h.writeDockerError(c, "docker_endpoint_update", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"endpoint": toDockerEndpointResponse(endpoint)})
}

func (h clusterHandler) renameDockerEndpoint(c *gin.Context) {
	var request runtimeNameRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("修改 Docker 连接名称参数无效", "operation", "docker_endpoint_rename_bind", "request_id", requestIDFrom(c), "endpoint_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", dockerengine.ErrInvalidEndpoint.Error())
		return
	}
	endpoint, err := h.docker.Rename(c.Request.Context(), c.Param("id"), request.Name)
	if err != nil {
		h.writeDockerError(c, "docker_endpoint_rename", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"endpoint": toDockerEndpointResponse(endpoint)})
}

func (h clusterHandler) setDockerStatus(c *gin.Context) {
	var request runtimeStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Active == nil {
		h.logger.Warn("修改 Docker 连接状态参数无效", "operation", "docker_endpoint_status_bind", "request_id", requestIDFrom(c), "endpoint_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", "Docker 连接状态格式无效")
		return
	}
	if err := h.docker.SetActive(c.Request.Context(), c.Param("id"), *request.Active); err != nil {
		h.writeDockerError(c, "docker_endpoint_status", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h clusterHandler) pingDocker(c *gin.Context) {
	result, err := h.docker.Ping(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.logger.Error("Docker API 健康检查失败", "operation", "docker_ping", "request_id", requestIDFrom(c), "endpoint_id", c.Param("id"), "err", err)
		if errors.Is(err, dockerengine.ErrEndpointNotFound) {
			writeError(c, http.StatusNotFound, "docker_endpoint_not_found", dockerengine.ErrEndpointNotFound.Error())
		} else {
			writeError(c, http.StatusBadGateway, "docker_unreachable", "无法连接 Docker API，请检查连接配置和网络")
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"api_version": result.APIVersion, "os_type": result.OSType, "experimental": result.Experimental})
}

func (h clusterHandler) listContainers(c *gin.Context) {
	all, _ := strconv.ParseBool(c.DefaultQuery("all", "true"))
	containers, err := h.docker.Containers(c.Request.Context(), c.Param("id"), all)
	if err != nil {
		h.logger.Error("查询 Docker 容器失败", "operation", "docker_container_list", "request_id", requestIDFrom(c), "endpoint_id", c.Param("id"), "err", err)
		if errors.Is(err, dockerengine.ErrEndpointNotFound) {
			writeError(c, http.StatusNotFound, "docker_endpoint_not_found", dockerengine.ErrEndpointNotFound.Error())
		} else {
			writeError(c, http.StatusBadGateway, "docker_unreachable", "无法读取 Docker 容器列表")
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"containers": containers})
}

func (h clusterHandler) listKubernetesClusters(c *gin.Context) {
	clusters, err := h.kube.List(c.Request.Context())
	if err != nil {
		h.logger.Error("查询 Kubernetes 集群列表失败", "operation", "kubernetes_cluster_list", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	response := make([]kubernetesClusterResponse, 0, len(clusters))
	for i := range clusters {
		response = append(response, toKubernetesClusterResponse(&clusters[i]))
	}
	c.JSON(http.StatusOK, gin.H{"clusters": response})
}

func (h clusterHandler) createKubernetesCluster(c *gin.Context) {
	var request kubernetesClusterRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("创建 Kubernetes 集群参数无效", "operation", "kubernetes_cluster_create_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", kube.ErrInvalidCluster.Error())
		return
	}
	actor, _ := currentUser(c)
	cluster, err := h.kube.Create(c.Request.Context(), actor.ID, toKubernetesInput(request))
	if err != nil {
		h.writeKubernetesError(c, "kubernetes_cluster_create", err)
		return
	}
	setAuditResourceID(c, cluster.ID)
	c.JSON(http.StatusCreated, gin.H{"cluster": toKubernetesClusterResponse(cluster)})
}

func (h clusterHandler) updateKubernetesCluster(c *gin.Context) {
	var request kubernetesClusterRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("更新 Kubernetes 集群参数无效", "operation", "kubernetes_cluster_update_bind", "request_id", requestIDFrom(c), "cluster_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", kube.ErrInvalidCluster.Error())
		return
	}
	cluster, err := h.kube.Update(c.Request.Context(), c.Param("id"), toKubernetesInput(request))
	if err != nil {
		h.writeKubernetesError(c, "kubernetes_cluster_update", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"cluster": toKubernetesClusterResponse(cluster)})
}

func (h clusterHandler) setKubernetesStatus(c *gin.Context) {
	var request runtimeStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Active == nil {
		h.logger.Warn("修改 Kubernetes 集群状态参数无效", "operation", "kubernetes_cluster_status_bind", "request_id", requestIDFrom(c), "cluster_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_request", "Kubernetes 集群状态格式无效")
		return
	}
	if err := h.kube.SetActive(c.Request.Context(), c.Param("id"), *request.Active); err != nil {
		h.writeKubernetesError(c, "kubernetes_cluster_status", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h clusterHandler) pingKubernetes(c *gin.Context) {
	version, err := h.kube.Ping(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.logger.Error("Kubernetes API 健康检查失败", "operation", "kubernetes_ping", "request_id", requestIDFrom(c), "cluster_id", c.Param("id"), "err", err)
		if errors.Is(err, kube.ErrClusterNotFound) {
			writeError(c, http.StatusNotFound, "kubernetes_cluster_not_found", kube.ErrClusterNotFound.Error())
		} else {
			writeError(c, http.StatusBadGateway, "kubernetes_unreachable", "无法连接 Kubernetes API，请检查集群凭据和网络")
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"version": version})
}

func (h clusterHandler) listNamespaces(c *gin.Context) {
	namespaces, err := h.kube.Namespaces(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.writeKubernetesReadError(c, "kubernetes_namespace_list", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"namespaces": namespaces})
}

func (h clusterHandler) listPods(c *gin.Context) {
	pods, err := h.kube.Pods(c.Request.Context(), c.Param("id"), c.Query("namespace"))
	if err != nil {
		h.writeKubernetesReadError(c, "kubernetes_pod_list", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"pods": pods})
}

func (h clusterHandler) listDeployments(c *gin.Context) {
	deployments, err := h.kube.Deployments(c.Request.Context(), c.Param("id"), c.Query("namespace"))
	if err != nil {
		h.writeKubernetesReadError(c, "kubernetes_deployment_list", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"deployments": deployments})
}

func (h clusterHandler) writeDockerError(c *gin.Context, operation string, err error) {
	h.logger.Warn("Docker 连接操作失败", "operation", operation, "request_id", requestIDFrom(c), "endpoint_id", c.Param("id"), "err", err)
	switch {
	case errors.Is(err, dockerengine.ErrInvalidEndpoint):
		writeError(c, http.StatusBadRequest, "invalid_docker_endpoint", dockerengine.ErrInvalidEndpoint.Error())
	case errors.Is(err, dockerengine.ErrTLSRequired):
		writeError(c, http.StatusBadRequest, "docker_tls_required", dockerengine.ErrTLSRequired.Error())
	case errors.Is(err, dockerengine.ErrInvalidTLS):
		writeError(c, http.StatusBadRequest, "invalid_docker_tls", dockerengine.ErrInvalidTLS.Error())
	case errors.Is(err, dockerengine.ErrSSHRequired):
		writeError(c, http.StatusBadRequest, "docker_ssh_required", dockerengine.ErrSSHRequired.Error())
	case errors.Is(err, dockerengine.ErrInvalidSSH):
		writeError(c, http.StatusBadRequest, "invalid_docker_ssh", dockerengine.ErrInvalidSSH.Error())
	case errors.Is(err, dockerengine.ErrSSHUnreachable):
		writeError(c, http.StatusBadGateway, "docker_ssh_unreachable", dockerengine.ErrSSHUnreachable.Error())
	case errors.Is(err, dockerengine.ErrEndpointExists):
		writeError(c, http.StatusConflict, "docker_endpoint_exists", dockerengine.ErrEndpointExists.Error())
	case errors.Is(err, dockerengine.ErrEndpointNotFound):
		writeError(c, http.StatusNotFound, "docker_endpoint_not_found", dockerengine.ErrEndpointNotFound.Error())
	case errors.Is(err, secret.ErrUnavailable):
		writeError(c, http.StatusServiceUnavailable, "secrets_unavailable", secret.ErrUnavailable.Error())
	default:
		writeInternalError(c)
	}
}

func (h clusterHandler) writeKubernetesError(c *gin.Context, operation string, err error) {
	h.logger.Warn("Kubernetes 集群操作失败", "operation", operation, "request_id", requestIDFrom(c), "cluster_id", c.Param("id"), "err", err)
	switch {
	case errors.Is(err, kube.ErrInvalidCluster):
		writeError(c, http.StatusBadRequest, "invalid_kubernetes_cluster", kube.ErrInvalidCluster.Error())
	case errors.Is(err, kube.ErrUnsafeKubeconfig):
		writeError(c, http.StatusBadRequest, "unsafe_kubeconfig", kube.ErrUnsafeKubeconfig.Error())
	case errors.Is(err, kube.ErrKubeconfigRequired):
		writeError(c, http.StatusBadRequest, "kubeconfig_required", kube.ErrKubeconfigRequired.Error())
	case errors.Is(err, kube.ErrClusterExists):
		writeError(c, http.StatusConflict, "kubernetes_cluster_exists", kube.ErrClusterExists.Error())
	case errors.Is(err, kube.ErrClusterNotFound):
		writeError(c, http.StatusNotFound, "kubernetes_cluster_not_found", kube.ErrClusterNotFound.Error())
	case errors.Is(err, secret.ErrUnavailable):
		writeError(c, http.StatusServiceUnavailable, "secrets_unavailable", secret.ErrUnavailable.Error())
	default:
		writeInternalError(c)
	}
}

func (h clusterHandler) writeKubernetesReadError(c *gin.Context, operation string, err error) {
	h.logger.Error("读取 Kubernetes 资源失败", "operation", operation, "request_id", requestIDFrom(c), "cluster_id", c.Param("id"), "namespace", c.Query("namespace"), "err", err)
	if errors.Is(err, kube.ErrClusterNotFound) {
		writeError(c, http.StatusNotFound, "kubernetes_cluster_not_found", kube.ErrClusterNotFound.Error())
	} else if errors.Is(err, kube.ErrInvalidCluster) {
		writeError(c, http.StatusBadRequest, "invalid_namespace", "命名空间格式无效")
	} else {
		writeError(c, http.StatusBadGateway, "kubernetes_unreachable", "无法读取 Kubernetes 资源")
	}
}

func toDockerEndpointResponse(endpoint *model.DockerEndpoint) dockerEndpointResponse {
	return dockerEndpointResponse{
		ID: endpoint.ID, Name: endpoint.Name, Host: endpoint.Host,
		Local:         dockerengine.IsLocalEndpointID(endpoint.ID),
		TLSConfigured: endpoint.TLSCiphertext != "", SSHConfigured: endpoint.SSHCredentialCiphertext != "",
		SSHHostKeyFingerprint: endpoint.SSHHostKeyFingerprint, IsActive: endpoint.IsActive,
		CreatedBy: endpoint.CreatedBy, CreatedAt: endpoint.CreatedAt, UpdatedAt: endpoint.UpdatedAt,
	}
}

func toDockerInput(request dockerEndpointRequest) dockerengine.Input {
	return dockerengine.Input{
		Name: request.Name, Host: request.Host, TLS: request.TLS, SSH: request.SSH,
		SSHHostKeyFingerprint: request.SSHHostKeyFingerprint,
	}
}

func toKubernetesInput(request kubernetesClusterRequest) kube.Input {
	return kube.Input{
		Name: request.Name, Mode: request.Mode, DefaultNamespace: request.DefaultNamespace, Kubeconfig: request.Kubeconfig,
	}
}

func toKubernetesClusterResponse(cluster *model.KubernetesCluster) kubernetesClusterResponse {
	return kubernetesClusterResponse{
		ID: cluster.ID, Name: cluster.Name, Mode: cluster.Mode, APIServer: cluster.APIServer,
		DefaultNamespace: cluster.DefaultNamespace, KubeconfigConfigured: cluster.KubeconfigCiphertext != "",
		IsActive: cluster.IsActive, CreatedBy: cluster.CreatedBy,
		CreatedAt: cluster.CreatedAt, UpdatedAt: cluster.UpdatedAt,
	}
}
