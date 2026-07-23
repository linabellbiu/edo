package httpapi

import (
	"database/sql"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"zrt/internal/access"
	"zrt/internal/account"
	"zrt/internal/audit"
	"zrt/internal/auth"
	"zrt/internal/config"
	"zrt/internal/configuration"
	"zrt/internal/deployment"
	"zrt/internal/dockerengine"
	"zrt/internal/kube"
	"zrt/internal/monitor"
	"zrt/internal/notification"
	"zrt/internal/repository"
	"zrt/internal/scheduler"
	"zrt/internal/task"
	"zrt/internal/terminal"
)

type Dependencies struct {
	Environment    string
	Database       *sql.DB
	Redis          Pinger
	NATS           Pinger
	Logger         *slog.Logger
	Version        string
	WebRoot        string
	AuthConfig     config.Auth
	Accounts       *account.Service
	Login          *account.LoginService
	Sessions       *auth.SessionStore
	Access         *access.Service
	Audits         *audit.Service
	Repositories   *repository.Service
	Docker         *dockerengine.Service
	Kubernetes     *kube.Service
	Deployments    *deployment.Service
	Terminal       *terminal.Service
	Configurations *configuration.Service
	Notifications  *notification.Service
	Monitors       *monitor.Service
	Scheduler      *scheduler.Service
	Tasks          *task.Service
}

type errorResponse struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

func NewRouter(deps Dependencies) *gin.Engine {
	if deps.Environment == "production" {
		gin.SetMode(gin.ReleaseMode)
	}
	router := gin.New()
	_ = router.SetTrustedProxies(nil)
	router.Use(requestID(), securityHeaders(), recovery(deps.Logger), accessLog(deps.Logger))

	health := healthHandler{database: deps.Database, redis: deps.Redis, nats: deps.NATS, logger: deps.Logger}
	api := router.Group("/api/v1")
	api.GET("/health/live", health.live)
	api.GET("/health/ready", health.ready)
	authAPI := api.Group("/auth")
	authHandler := authHandler{login: deps.Login, sessions: deps.Sessions, access: deps.Access, config: deps.AuthConfig, logger: deps.Logger}
	authAPI.POST("/login", auditAction(deps.Audits, deps.Logger, "auth.login", "session"), authHandler.handleLogin)
	authAPI.POST("/logout", auditAction(deps.Audits, deps.Logger, "auth.logout", "session"), authHandler.handleLogout)
	repositoryAPI := repositoryHandler{service: deps.Repositories, logger: deps.Logger}
	api.POST("/webhooks/git/:id", repositoryAPI.webhook)

	protected := api.Group("")
	protected.Use(requireAuth(deps.Accounts, deps.Sessions, deps.Logger, deps.AuthConfig.CookieName))
	protected.GET("/auth/me", authHandler.handleMe)
	protected.GET("/system/info", requirePermission(deps.Access, deps.Logger, access.PermissionSystemRead), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"name": "ZRT", "version": deps.Version})
	})

	accessAPI := accessHandler{accounts: deps.Accounts, access: deps.Access, audits: deps.Audits, logger: deps.Logger}
	protected.GET("/permissions", requirePermission(deps.Access, deps.Logger, access.PermissionRoleRead), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"permissions": access.Catalog()})
	})
	protected.GET("/roles", requirePermission(deps.Access, deps.Logger, access.PermissionRoleRead), accessAPI.listRoles)
	protected.POST("/roles", auditAction(deps.Audits, deps.Logger, "role.create", "role"), requirePermission(deps.Access, deps.Logger, access.PermissionRoleManage), accessAPI.createRole)
	protected.PUT("/roles/:id", auditAction(deps.Audits, deps.Logger, "role.update", "role"), requirePermission(deps.Access, deps.Logger, access.PermissionRoleManage), accessAPI.updateRole)
	protected.DELETE("/roles/:id", auditAction(deps.Audits, deps.Logger, "role.delete", "role"), requirePermission(deps.Access, deps.Logger, access.PermissionRoleManage), accessAPI.deleteRole)

	protected.GET("/users", requirePermission(deps.Access, deps.Logger, access.PermissionUserRead), accessAPI.listUsers)
	protected.POST("/users", auditAction(deps.Audits, deps.Logger, "user.create", "user"), requirePermission(deps.Access, deps.Logger, access.PermissionUserManage), accessAPI.createUser)
	protected.PATCH("/users/:id/status", auditAction(deps.Audits, deps.Logger, "user.status.update", "user"), requirePermission(deps.Access, deps.Logger, access.PermissionUserManage), accessAPI.setUserStatus)
	protected.PUT("/users/:id/roles", auditAction(deps.Audits, deps.Logger, "user.roles.update", "user"), requirePermission(deps.Access, deps.Logger, access.PermissionUserManage), accessAPI.setUserRoles)
	protected.GET("/audit-logs", requirePermission(deps.Access, deps.Logger, access.PermissionAuditRead), accessAPI.listAuditLogs)

	protected.GET("/repositories", requirePermission(deps.Access, deps.Logger, access.PermissionRepositoryRead), repositoryAPI.list)
	protected.POST("/repositories", auditAction(deps.Audits, deps.Logger, "repository.create", "repository"), requirePermission(deps.Access, deps.Logger, access.PermissionRepositoryManage), repositoryAPI.create)
	protected.PUT("/repositories/:id", auditAction(deps.Audits, deps.Logger, "repository.update", "repository"), requirePermission(deps.Access, deps.Logger, access.PermissionRepositoryManage), repositoryAPI.update)
	protected.PATCH("/repositories/:id/status", auditAction(deps.Audits, deps.Logger, "repository.status.update", "repository"), requirePermission(deps.Access, deps.Logger, access.PermissionRepositoryManage), repositoryAPI.setStatus)
	protected.POST("/repositories/:id/test", auditAction(deps.Audits, deps.Logger, "repository.test", "repository"), requirePermission(deps.Access, deps.Logger, access.PermissionRepositoryManage), repositoryAPI.testConnection)
	protected.GET("/repositories/:id/webhook-deliveries", requirePermission(deps.Access, deps.Logger, access.PermissionRepositoryRead), repositoryAPI.listDeliveries)

	clusterAPI := clusterHandler{docker: deps.Docker, kube: deps.Kubernetes, logger: deps.Logger}
	protected.GET("/docker/endpoints", requirePermission(deps.Access, deps.Logger, access.PermissionClusterRead), clusterAPI.listDockerEndpoints)
	protected.POST("/docker/endpoints", auditAction(deps.Audits, deps.Logger, "docker.endpoint.create", "docker_endpoint"), requirePermission(deps.Access, deps.Logger, access.PermissionClusterManage), clusterAPI.createDockerEndpoint)
	protected.PUT("/docker/endpoints/:id", auditAction(deps.Audits, deps.Logger, "docker.endpoint.update", "docker_endpoint"), requirePermission(deps.Access, deps.Logger, access.PermissionClusterManage), clusterAPI.updateDockerEndpoint)
	protected.PATCH("/docker/endpoints/:id/status", auditAction(deps.Audits, deps.Logger, "docker.endpoint.status.update", "docker_endpoint"), requirePermission(deps.Access, deps.Logger, access.PermissionClusterManage), clusterAPI.setDockerStatus)
	protected.POST("/docker/endpoints/:id/ping", auditAction(deps.Audits, deps.Logger, "docker.endpoint.test", "docker_endpoint"), requirePermission(deps.Access, deps.Logger, access.PermissionClusterManage), clusterAPI.pingDocker)
	protected.GET("/docker/endpoints/:id/containers", requirePermission(deps.Access, deps.Logger, access.PermissionClusterRead), clusterAPI.listContainers)

	protected.GET("/kubernetes/clusters", requirePermission(deps.Access, deps.Logger, access.PermissionClusterRead), clusterAPI.listKubernetesClusters)
	protected.POST("/kubernetes/clusters", auditAction(deps.Audits, deps.Logger, "kubernetes.cluster.create", "kubernetes_cluster"), requirePermission(deps.Access, deps.Logger, access.PermissionClusterManage), clusterAPI.createKubernetesCluster)
	protected.PUT("/kubernetes/clusters/:id", auditAction(deps.Audits, deps.Logger, "kubernetes.cluster.update", "kubernetes_cluster"), requirePermission(deps.Access, deps.Logger, access.PermissionClusterManage), clusterAPI.updateKubernetesCluster)
	protected.PATCH("/kubernetes/clusters/:id/status", auditAction(deps.Audits, deps.Logger, "kubernetes.cluster.status.update", "kubernetes_cluster"), requirePermission(deps.Access, deps.Logger, access.PermissionClusterManage), clusterAPI.setKubernetesStatus)
	protected.POST("/kubernetes/clusters/:id/ping", auditAction(deps.Audits, deps.Logger, "kubernetes.cluster.test", "kubernetes_cluster"), requirePermission(deps.Access, deps.Logger, access.PermissionClusterManage), clusterAPI.pingKubernetes)
	protected.GET("/kubernetes/clusters/:id/namespaces", requirePermission(deps.Access, deps.Logger, access.PermissionClusterRead), clusterAPI.listNamespaces)
	protected.GET("/kubernetes/clusters/:id/pods", requirePermission(deps.Access, deps.Logger, access.PermissionClusterRead), clusterAPI.listPods)
	protected.GET("/kubernetes/clusters/:id/deployments", requirePermission(deps.Access, deps.Logger, access.PermissionClusterRead), clusterAPI.listDeployments)

	deploymentAPI := deploymentHandler{service: deps.Deployments, logger: deps.Logger}
	protected.GET("/deployment-targets", requirePermission(deps.Access, deps.Logger, access.PermissionDeploymentRead), deploymentAPI.listTargets)
	protected.POST("/deployment-targets", auditAction(deps.Audits, deps.Logger, "deployment.target.create", "deployment_target"), requirePermission(deps.Access, deps.Logger, access.PermissionDeploymentManage), deploymentAPI.createTarget)
	protected.PUT("/deployment-targets/:id", auditAction(deps.Audits, deps.Logger, "deployment.target.update", "deployment_target"), requirePermission(deps.Access, deps.Logger, access.PermissionDeploymentManage), deploymentAPI.updateTarget)
	protected.PATCH("/deployment-targets/:id/status", auditAction(deps.Audits, deps.Logger, "deployment.target.status.update", "deployment_target"), requirePermission(deps.Access, deps.Logger, access.PermissionDeploymentManage), deploymentAPI.setTargetStatus)
	protected.GET("/deployments", requirePermission(deps.Access, deps.Logger, access.PermissionDeploymentRead), deploymentAPI.list)
	protected.POST("/deployments", auditAction(deps.Audits, deps.Logger, "deployment.request", "deployment"), requirePermission(deps.Access, deps.Logger, access.PermissionDeploymentRun), deploymentAPI.request)
	protected.POST("/deployments/:id/approve", auditAction(deps.Audits, deps.Logger, "deployment.approve", "deployment"), requirePermission(deps.Access, deps.Logger, access.PermissionDeploymentReview), deploymentAPI.approve)
	protected.POST("/deployments/:id/rollback", auditAction(deps.Audits, deps.Logger, "deployment.rollback", "deployment"), requirePermission(deps.Access, deps.Logger, access.PermissionDeploymentReview), deploymentAPI.rollback)

	terminalAPI := terminalHandler{service: deps.Terminal, audits: deps.Audits, logger: deps.Logger}
	protected.GET("/terminals/docker/:endpoint_id/containers/:container_id/ws", requirePermission(deps.Access, deps.Logger, access.PermissionTerminalOpen), terminalAPI.docker)
	protected.GET("/terminals/kubernetes/:cluster_id/namespaces/:namespace/pods/:pod/containers/:container/ws", requirePermission(deps.Access, deps.Logger, access.PermissionTerminalOpen), terminalAPI.kubernetes)

	configurationAPI := configurationHandler{service: deps.Configurations, logger: deps.Logger}
	protected.GET("/configurations", requirePermission(deps.Access, deps.Logger, access.PermissionConfigRead), configurationAPI.list)
	protected.POST("/configurations", auditAction(deps.Audits, deps.Logger, "configuration.create", "configuration"), requirePermission(deps.Access, deps.Logger, access.PermissionConfigManage), configurationAPI.create)
	protected.PUT("/configurations/:id", auditAction(deps.Audits, deps.Logger, "configuration.update", "configuration"), requirePermission(deps.Access, deps.Logger, access.PermissionConfigManage), configurationAPI.update)
	protected.PATCH("/configurations/:id/status", auditAction(deps.Audits, deps.Logger, "configuration.status.update", "configuration"), requirePermission(deps.Access, deps.Logger, access.PermissionConfigManage), configurationAPI.setStatus)
	protected.GET("/configurations/:id/revisions", requirePermission(deps.Access, deps.Logger, access.PermissionConfigRead), configurationAPI.revisions)

	notificationAPI := notificationHandler{service: deps.Notifications, logger: deps.Logger}
	protected.GET("/notification-channels", requirePermission(deps.Access, deps.Logger, access.PermissionNotificationRead), notificationAPI.listChannels)
	protected.POST("/notification-channels", auditAction(deps.Audits, deps.Logger, "notification.channel.create", "notification_channel"), requirePermission(deps.Access, deps.Logger, access.PermissionNotificationManage), notificationAPI.createChannel)
	protected.PUT("/notification-channels/:id", auditAction(deps.Audits, deps.Logger, "notification.channel.update", "notification_channel"), requirePermission(deps.Access, deps.Logger, access.PermissionNotificationManage), notificationAPI.updateChannel)
	protected.PATCH("/notification-channels/:id/status", auditAction(deps.Audits, deps.Logger, "notification.channel.status.update", "notification_channel"), requirePermission(deps.Access, deps.Logger, access.PermissionNotificationManage), notificationAPI.setChannelStatus)
	protected.POST("/notification-channels/:id/test", auditAction(deps.Audits, deps.Logger, "notification.channel.test", "notification"), requirePermission(deps.Access, deps.Logger, access.PermissionNotificationManage), notificationAPI.testChannel)
	protected.GET("/notifications", requirePermission(deps.Access, deps.Logger, access.PermissionNotificationRead), notificationAPI.list)

	monitorAPI := monitorHandler{service: deps.Monitors, logger: deps.Logger}
	protected.GET("/monitor-rules", requirePermission(deps.Access, deps.Logger, access.PermissionMonitorRead), monitorAPI.list)
	protected.POST("/monitor-rules", auditAction(deps.Audits, deps.Logger, "monitor.rule.create", "monitor_rule"), requirePermission(deps.Access, deps.Logger, access.PermissionMonitorManage), monitorAPI.create)
	protected.PUT("/monitor-rules/:id", auditAction(deps.Audits, deps.Logger, "monitor.rule.update", "monitor_rule"), requirePermission(deps.Access, deps.Logger, access.PermissionMonitorManage), monitorAPI.update)
	protected.PATCH("/monitor-rules/:id/status", auditAction(deps.Audits, deps.Logger, "monitor.rule.status.update", "monitor_rule"), requirePermission(deps.Access, deps.Logger, access.PermissionMonitorManage), monitorAPI.setStatus)
	protected.GET("/monitor-rules/:id/checks", requirePermission(deps.Access, deps.Logger, access.PermissionMonitorRead), monitorAPI.checks)

	schedulerAPI := schedulerHandler{service: deps.Scheduler, logger: deps.Logger}
	protected.GET("/schedules", requirePermission(deps.Access, deps.Logger, access.PermissionSchedulerRead), schedulerAPI.list)
	protected.POST("/schedules", auditAction(deps.Audits, deps.Logger, "scheduler.create", "schedule"), requirePermission(deps.Access, deps.Logger, access.PermissionSchedulerManage), schedulerAPI.create)
	protected.PUT("/schedules/:id", auditAction(deps.Audits, deps.Logger, "scheduler.update", "schedule"), requirePermission(deps.Access, deps.Logger, access.PermissionSchedulerManage), schedulerAPI.update)
	protected.PATCH("/schedules/:id/status", auditAction(deps.Audits, deps.Logger, "scheduler.status.update", "schedule"), requirePermission(deps.Access, deps.Logger, access.PermissionSchedulerManage), schedulerAPI.setStatus)

	taskAPI := taskHandler{service: deps.Tasks, logger: deps.Logger}
	protected.GET("/tasks", requirePermission(deps.Access, deps.Logger, access.PermissionTaskRead), taskAPI.list)
	protected.POST("/tasks/:id/cancel", auditAction(deps.Audits, deps.Logger, "task.cancel", "task"), requirePermission(deps.Access, deps.Logger, access.PermissionTaskManage), taskAPI.cancel)
	protected.POST("/tasks/:id/retry", auditAction(deps.Audits, deps.Logger, "task.retry", "task"), requirePermission(deps.Access, deps.Logger, access.PermissionTaskManage), taskAPI.retry)
	webIndex := installWebUI(router, deps.WebRoot, deps.Logger)
	router.NoRoute(func(c *gin.Context) {
		if webIndex != "" && c.Request.Method == http.MethodGet && !strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.File(webIndex)
			return
		}
		c.JSON(http.StatusNotFound, errorResponse{
			Code: "not_found", Message: "请求的资源不存在", RequestID: requestIDFrom(c),
		})
	})
	return router
}

func installWebUI(router *gin.Engine, webRoot string, logger *slog.Logger) string {
	if strings.TrimSpace(webRoot) == "" {
		return ""
	}
	root, err := filepath.Abs(webRoot)
	if err != nil {
		logger.Warn("Web 前端目录无效", "operation", "webui_path", "err", err)
		return ""
	}
	index := filepath.Join(root, "index.html")
	if info, err := os.Stat(index); err != nil || !info.Mode().IsRegular() {
		logger.Info("未发现已构建的 Web 前端，API 将独立运行", "operation", "webui_disabled", "web_root", root)
		return ""
	}
	assets := filepath.Join(root, "assets")
	if info, err := os.Stat(assets); err == nil && info.IsDir() {
		router.StaticFS("/assets", http.Dir(assets))
	}
	router.GET("/", func(c *gin.Context) { c.File(index) })
	logger.Info("ZRT Web 前端已启用", "operation", "webui_enabled", "web_root", root)
	return index
}
