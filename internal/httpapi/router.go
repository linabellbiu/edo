package httpapi

import (
	"database/sql"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"edo/internal/access"
	"edo/internal/account"
	artifactmanager "edo/internal/artifact"
	"edo/internal/audit"
	"edo/internal/auth"
	"edo/internal/config"
	"edo/internal/configuration"
	"edo/internal/credential"
	"edo/internal/database"
	"edo/internal/department"
	"edo/internal/deployment"
	dnsmanager "edo/internal/dns"
	"edo/internal/dockerengine"
	environmentmanager "edo/internal/environment"
	hostmanager "edo/internal/host"
	"edo/internal/identity"
	"edo/internal/kube"
	"edo/internal/logging"
	"edo/internal/logretention"
	"edo/internal/monitor"
	"edo/internal/notification"
	"edo/internal/pipeline"
	"edo/internal/repository"
	"edo/internal/scheduler"
	"edo/internal/systemmetrics"
	"edo/internal/task"
	"edo/internal/terminal"
	"edo/internal/webui"
)

type Dependencies struct {
	Environment      string
	Database         *sql.DB
	Redis            Pinger
	NATS             Pinger
	Logger           *slog.Logger
	RuntimeLogs      *logging.RuntimeController
	Version          string
	WebRoot          string
	AuthConfig       config.Auth
	Accounts         *account.Service
	Login            *account.LoginService
	LoginLimiter     *auth.LoginRateLimiter
	Sessions         *auth.SessionStore
	Access           *access.Service
	Audits           *audit.Service
	Identities       *identity.Service
	Repositories     *repository.Service
	Pipelines        *pipeline.Service
	Artifacts        *artifactmanager.Service
	Docker           *dockerengine.Service
	Kubernetes       *kube.Service
	Hosts            *hostmanager.Service
	Environments     *environmentmanager.Service
	Deployments      *deployment.Service
	Departments      *department.Service
	Terminal         *terminal.Service
	Configurations   *configuration.Service
	Credentials      *credential.Service
	DNS              *dnsmanager.Service
	Notifications    *notification.Service
	Monitors         *monitor.Service
	Scheduler        *scheduler.Service
	Tasks            *task.Service
	SystemMetrics    *systemmetrics.Service
	LogRetention     *logretention.Service
	DatabaseTransfer *database.TransferService
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
	router.Use(requestID(), securityHeaders(), webUICacheHeaders(), recovery(deps.Logger), accessLog(deps.Logger, deps.RuntimeLogs))

	health := healthHandler{database: deps.Database, redis: deps.Redis, nats: deps.NATS, builder: deps.Docker, logger: deps.Logger}
	api := router.Group("/api/v1")
	api.Use(databaseTransferGuard(deps.DatabaseTransfer, deps.Logger))
	api.GET("/health/live", health.live)
	api.GET("/health/ready", health.ready)
	authAPI := api.Group("/auth")
	authHandler := authHandler{login: deps.Login, accounts: deps.Accounts, sessions: deps.Sessions, access: deps.Access, config: deps.AuthConfig, logger: deps.Logger}
	identityAPI := identityHandler{service: deps.Identities, auth: authHandler, audits: deps.Audits, logger: deps.Logger}
	authAPI.POST("/login", auditAction(deps.Audits, deps.Logger, "auth.login", "session"), authHandler.handleLogin)
	authAPI.POST("/logout", auditAction(deps.Audits, deps.Logger, "auth.logout", "session"), authHandler.handleLogout)
	authAPI.GET("/providers", identityAPI.listPublic)
	authAPI.POST("/ldap/:id/login", auditAction(deps.Audits, deps.Logger, "auth.ldap.login", "session"), identityAPI.loginLDAP)
	authAPI.GET("/oauth/:id/start", identityAPI.startOAuth)
	authAPI.GET("/oauth/:id/callback", identityAPI.callbackOAuth)
	repositoryAPI := repositoryHandler{service: deps.Repositories, access: deps.Access, logger: deps.Logger}
	api.POST("/webhooks/git/:id", repositoryAPI.webhook)

	protected := api.Group("")
	protected.Use(requireAuth(deps.Accounts, deps.Sessions, deps.Logger, deps.AuthConfig.CookieName))
	protected.GET("/auth/me", authHandler.handleMe)
	protected.PUT("/auth/password", auditAction(deps.Audits, deps.Logger, "auth.password.change", "user"), authHandler.handleChangePassword)
	protected.GET("/system/info", requirePermission(deps.Access, deps.Logger, access.PermissionSystemRead), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"name": "EDO", "version": deps.Version})
	})
	systemMetricsAPI := systemMetricsHandler{service: deps.SystemMetrics, logger: deps.Logger}
	protected.GET("/system/metrics", requirePermission(deps.Access, deps.Logger, access.PermissionMonitorRead), systemMetricsAPI.snapshot)
	protected.DELETE("/system/metrics/queue/dead-messages", auditAction(deps.Audits, deps.Logger, "monitor.dead_letter.clear", "message_queue"), requirePermission(deps.Access, deps.Logger, access.PermissionMonitorExecute), systemMetricsAPI.purgeDeadLetters)
	systemLogsAPI := systemLogHandler{service: deps.RuntimeLogs, logger: deps.Logger}
	protected.GET("/logs", requirePermission(deps.Access, deps.Logger, access.PermissionMonitorRead), systemLogsAPI.list)

	accessAPI := accessHandler{accounts: deps.Accounts, access: deps.Access, audits: deps.Audits, departments: deps.Departments, logger: deps.Logger}
	protected.GET("/permissions", requireAnyPermission(deps.Access, deps.Logger,
		access.PermissionRoleRead, access.PermissionRoleCreate, access.PermissionRoleUpdate,
		access.PermissionRoleDelete, access.PermissionUserUpdate,
	), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"permissions": access.Catalog()})
	})
	protected.GET("/roles", requireAnyPermission(deps.Access, deps.Logger,
		access.PermissionRoleRead, access.PermissionRoleCreate, access.PermissionRoleUpdate, access.PermissionRoleDelete,
		access.PermissionUserCreate, access.PermissionUserUpdate,
	), accessAPI.listRoles)
	protected.POST("/roles", auditAction(deps.Audits, deps.Logger, "role.create", "role"), requirePermission(deps.Access, deps.Logger, access.PermissionRoleCreate), accessAPI.createRole)
	protected.PUT("/roles/:id", auditAction(deps.Audits, deps.Logger, "role.update", "role"), requirePermission(deps.Access, deps.Logger, access.PermissionRoleUpdate), accessAPI.updateRole)
	protected.PATCH("/roles/:id/basic", auditAction(deps.Audits, deps.Logger, "role.basic.update", "role"), requirePermission(deps.Access, deps.Logger, access.PermissionRoleUpdate), accessAPI.updateRoleBasic)
	protected.PUT("/roles/:id/permissions", auditAction(deps.Audits, deps.Logger, "role.permissions.update", "role"), requirePermission(deps.Access, deps.Logger, access.PermissionRoleUpdate), accessAPI.updateRolePermissions)
	protected.DELETE("/roles/:id", auditAction(deps.Audits, deps.Logger, "role.delete", "role"), requirePermission(deps.Access, deps.Logger, access.PermissionRoleDelete), accessAPI.deleteRole)

	protected.GET("/users", requireAnyPermission(deps.Access, deps.Logger,
		access.PermissionUserRead, access.PermissionUserCreate, access.PermissionUserUpdate, access.PermissionUserDelete,
	), accessAPI.listUsers)
	protected.POST("/users", auditAction(deps.Audits, deps.Logger, "user.create", "user"), requirePermission(deps.Access, deps.Logger, access.PermissionUserCreate), accessAPI.createUser)
	protected.PATCH("/users/:id/status", auditAction(deps.Audits, deps.Logger, "user.status.update", "user"), requirePermission(deps.Access, deps.Logger, access.PermissionUserUpdate), accessAPI.setUserStatus)
	protected.PUT("/users/:id/department", auditAction(deps.Audits, deps.Logger, "user.department.update", "user"), requirePermission(deps.Access, deps.Logger, access.PermissionUserUpdate), accessAPI.setUserDepartment)
	protected.PUT("/users/:id/roles", auditAction(deps.Audits, deps.Logger, "user.roles.update", "user"), requirePermission(deps.Access, deps.Logger, access.PermissionUserUpdate), accessAPI.setUserRoles)
	protected.PUT("/users/:id/permissions", auditAction(deps.Audits, deps.Logger, "user.permissions.update", "user"), requirePermission(deps.Access, deps.Logger, access.PermissionUserUpdate), accessAPI.setUserPermissions)
	protected.PUT("/users/:id/access", auditAction(deps.Audits, deps.Logger, "user.access.update", "user"), requirePermission(deps.Access, deps.Logger, access.PermissionUserUpdate), accessAPI.setUserAccess)
	protected.DELETE("/users/:id", auditAction(deps.Audits, deps.Logger, "user.delete", "user"), requirePermission(deps.Access, deps.Logger, access.PermissionUserDelete), accessAPI.deleteUser)

	departmentAPI := departmentHandler{service: deps.Departments, logger: deps.Logger}
	protected.GET("/departments", requireAnyPermission(deps.Access, deps.Logger,
		access.PermissionDepartmentRead, access.PermissionDepartmentCreate, access.PermissionDepartmentUpdate,
		access.PermissionDepartmentDelete, access.PermissionUserCreate, access.PermissionUserUpdate,
	), departmentAPI.list)
	protected.POST("/departments", auditAction(deps.Audits, deps.Logger, "department.create", "department"), requirePermission(deps.Access, deps.Logger, access.PermissionDepartmentCreate), departmentAPI.create)
	protected.PUT("/departments/:id", auditAction(deps.Audits, deps.Logger, "department.update", "department"), requirePermission(deps.Access, deps.Logger, access.PermissionDepartmentUpdate), departmentAPI.update)
	protected.DELETE("/departments/:id", auditAction(deps.Audits, deps.Logger, "department.delete", "department"), requirePermission(deps.Access, deps.Logger, access.PermissionDepartmentDelete), departmentAPI.delete)
	protected.GET("/audit-logs", requirePermission(deps.Access, deps.Logger, access.PermissionAuditRead), accessAPI.listAuditLogs)
	credentialAPI := credentialHandler{service: deps.Credentials, logger: deps.Logger}
	protected.GET("/git-credentials", requirePermission(deps.Access, deps.Logger, access.PermissionCredentialRead), credentialAPI.list)
	protected.POST("/git-credentials", auditAction(deps.Audits, deps.Logger, "credential.create", "git_credential"), requirePermission(deps.Access, deps.Logger, access.PermissionCredentialCreate), credentialAPI.create)
	protected.PUT("/git-credentials/:id", auditAction(deps.Audits, deps.Logger, "credential.update", "git_credential"), requirePermission(deps.Access, deps.Logger, access.PermissionCredentialUpdate), credentialAPI.update)
	protected.DELETE("/git-credentials/:id", auditAction(deps.Audits, deps.Logger, "credential.delete", "git_credential"), requirePermission(deps.Access, deps.Logger, access.PermissionCredentialDelete), credentialAPI.remove)
	protected.GET("/git-credentials/:id/secret", auditAction(deps.Audits, deps.Logger, "credential.reveal", "git_credential"), requirePermission(deps.Access, deps.Logger, access.PermissionCredentialRead), credentialAPI.reveal)
	dnsAPI := dnsHandler{service: deps.DNS, logger: deps.Logger}
	protected.GET("/dns/providers", requirePermission(deps.Access, deps.Logger, access.PermissionDNSRead), dnsAPI.listProviders)
	protected.GET("/dns/accounts", requirePermission(deps.Access, deps.Logger, access.PermissionDNSRead), dnsAPI.listAccounts)
	protected.POST("/dns/accounts", auditAction(deps.Audits, deps.Logger, "dns.account.create", "dns_provider_account"), requirePermission(deps.Access, deps.Logger, access.PermissionDNSCreate), dnsAPI.createAccount)
	protected.PUT("/dns/accounts/:id", auditAction(deps.Audits, deps.Logger, "dns.account.update", "dns_provider_account"), requirePermission(deps.Access, deps.Logger, access.PermissionDNSUpdate), dnsAPI.updateAccount)
	protected.PATCH("/dns/accounts/:id/status", auditAction(deps.Audits, deps.Logger, "dns.account.status.update", "dns_provider_account"), requirePermission(deps.Access, deps.Logger, access.PermissionDNSUpdate), dnsAPI.setAccountStatus)
	protected.DELETE("/dns/accounts/:id", auditAction(deps.Audits, deps.Logger, "dns.account.delete", "dns_provider_account"), requirePermission(deps.Access, deps.Logger, access.PermissionDNSDelete), dnsAPI.deleteAccount)
	protected.GET("/dns/domains", requirePermission(deps.Access, deps.Logger, access.PermissionDNSRead), dnsAPI.listDomains)
	protected.POST("/dns/domains", auditAction(deps.Audits, deps.Logger, "dns.domain.create", "dns_domain"), requirePermission(deps.Access, deps.Logger, access.PermissionDNSCreate), dnsAPI.createDomain)
	protected.PUT("/dns/domains/:id", auditAction(deps.Audits, deps.Logger, "dns.domain.update", "dns_domain"), requirePermission(deps.Access, deps.Logger, access.PermissionDNSUpdate), dnsAPI.updateDomain)
	protected.PATCH("/dns/domains/:id/status", auditAction(deps.Audits, deps.Logger, "dns.domain.status.update", "dns_domain"), requirePermission(deps.Access, deps.Logger, access.PermissionDNSUpdate), dnsAPI.setDomainStatus)
	protected.DELETE("/dns/domains/:id", auditAction(deps.Audits, deps.Logger, "dns.domain.delete", "dns_domain"), requirePermission(deps.Access, deps.Logger, access.PermissionDNSDelete), dnsAPI.deleteDomain)
	protected.POST("/dns/domains/:id/test", auditAction(deps.Audits, deps.Logger, "dns.domain.test", "dns_domain"), requirePermission(deps.Access, deps.Logger, access.PermissionDNSExecute), dnsAPI.testDomain)
	protected.GET("/dns/domains/:id/records", requirePermission(deps.Access, deps.Logger, access.PermissionDNSRead), dnsAPI.listRecords)
	protected.POST("/dns/domains/:id/records", auditAction(deps.Audits, deps.Logger, "dns.record.create", "dns_record"), requirePermission(deps.Access, deps.Logger, access.PermissionDNSCreate), dnsAPI.createRecord)
	protected.PUT("/dns/domains/:id/records/:record_id", auditAction(deps.Audits, deps.Logger, "dns.record.update", "dns_record"), requirePermission(deps.Access, deps.Logger, access.PermissionDNSUpdate), dnsAPI.updateRecord)
	protected.DELETE("/dns/domains/:id/records/:record_id", auditAction(deps.Audits, deps.Logger, "dns.record.delete", "dns_record"), requirePermission(deps.Access, deps.Logger, access.PermissionDNSDelete), dnsAPI.deleteRecord)
	protected.GET("/identity-providers", requirePermission(deps.Access, deps.Logger, access.PermissionIdentityRead), identityAPI.list)
	protected.POST("/identity-providers", auditAction(deps.Audits, deps.Logger, "identity_provider.create", "identity_provider"), requirePermission(deps.Access, deps.Logger, access.PermissionIdentityCreate), identityAPI.create)
	protected.PUT("/identity-providers/:id", auditAction(deps.Audits, deps.Logger, "identity_provider.update", "identity_provider"), requirePermission(deps.Access, deps.Logger, access.PermissionIdentityUpdate), identityAPI.update)
	protected.PATCH("/identity-providers/:id/status", auditAction(deps.Audits, deps.Logger, "identity_provider.status.update", "identity_provider"), requirePermission(deps.Access, deps.Logger, access.PermissionIdentityUpdate), identityAPI.setStatus)

	protected.GET("/repositories", requirePermission(deps.Access, deps.Logger, access.PermissionRepositoryRead), repositoryAPI.list)
	protected.POST("/repositories", auditAction(deps.Audits, deps.Logger, "repository.create", "repository"), requirePermission(deps.Access, deps.Logger, access.PermissionRepositoryCreate), repositoryAPI.create)
	protected.POST("/repositories/test", auditAction(deps.Audits, deps.Logger, "repository.test", "repository"), requirePermission(deps.Access, deps.Logger, access.PermissionRepositoryExecute), repositoryAPI.testInput)
	protected.PUT("/repositories/:id", auditAction(deps.Audits, deps.Logger, "repository.update", "repository"), requirePermission(deps.Access, deps.Logger, access.PermissionRepositoryUpdate), repositoryAPI.update)
	protected.DELETE("/repositories/:id", auditAction(deps.Audits, deps.Logger, "repository.delete", "repository"), requirePermission(deps.Access, deps.Logger, access.PermissionRepositoryDelete), repositoryAPI.delete)
	protected.PATCH("/repositories/:id/status", auditAction(deps.Audits, deps.Logger, "repository.status.update", "repository"), requirePermission(deps.Access, deps.Logger, access.PermissionRepositoryUpdate), repositoryAPI.setStatus)
	protected.POST("/repositories/:id/test", auditAction(deps.Audits, deps.Logger, "repository.test", "repository"), requirePermission(deps.Access, deps.Logger, access.PermissionRepositoryExecute), repositoryAPI.testConnection)
	protected.GET("/repositories/:id/webhook-deliveries", requirePermission(deps.Access, deps.Logger, access.PermissionRepositoryRead), repositoryAPI.listDeliveries)
	protected.GET("/repositories/:id/webhook", auditAction(deps.Audits, deps.Logger, "repository.webhook.reveal", "repository"), requirePermission(deps.Access, deps.Logger, access.PermissionRepositorySecretRead), repositoryAPI.webhookConfiguration)

	pipelineAPI := pipelineHandler{service: deps.Pipelines, logger: deps.Logger}
	artifactAPI := artifactHandler{service: deps.Artifacts, logger: deps.Logger}
	protected.GET("/applications", requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryRead), pipelineAPI.listApplications)
	protected.POST("/applications", auditAction(deps.Audits, deps.Logger, "application.create", "application"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryCreate), pipelineAPI.createApplication)
	protected.PUT("/applications/:id", auditAction(deps.Audits, deps.Logger, "application.update", "application"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryUpdate), pipelineAPI.updateApplication)
	protected.PATCH("/applications/:id/status", auditAction(deps.Audits, deps.Logger, "application.status.update", "application"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryUpdate), pipelineAPI.setApplicationStatus)
	protected.POST("/applications/:id/sync", auditAction(deps.Audits, deps.Logger, "application.sync", "application"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryExecute), pipelineAPI.syncApplication)
	protected.GET("/applications/:id/artifacts", requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryRead), artifactAPI.list)
	protected.POST("/applications/:id/artifacts/upload", auditAction(deps.Audits, deps.Logger, "artifact.upload", "artifact"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryCreate), artifactAPI.upload)
	protected.GET("/applications/:id/workflows", requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryRead), pipelineAPI.listWorkflows)
	protected.POST("/applications/:id/workflows", auditAction(deps.Audits, deps.Logger, "workflow.create", "release_workflow"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryCreate), pipelineAPI.createWorkflow)
	protected.GET("/applications/:id/workflows/:workflow_id", requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryRead), pipelineAPI.getWorkflow)
	protected.POST("/applications/:id/workflows/:workflow_id/validate", requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryRead), pipelineAPI.validateWorkflow)
	protected.PUT("/applications/:id/workflows/:workflow_id", auditAction(deps.Audits, deps.Logger, "workflow.update", "release_workflow"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryUpdate), pipelineAPI.saveWorkflow)
	protected.DELETE("/applications/:id/workflows/:workflow_id", auditAction(deps.Audits, deps.Logger, "workflow.delete", "release_workflow"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryDelete), pipelineAPI.deleteWorkflow)
	protected.GET("/applications/:id/workflows/:workflow_id/repository-refs", requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryRead), pipelineAPI.listApplicationRefs)
	protected.POST("/applications/:id/workflows/:workflow_id/pipeline-runs", auditAction(deps.Audits, deps.Logger, "pipeline.prepare", "pipeline_run"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryExecute), pipelineAPI.prepareRun)
	protected.GET("/workflow-templates", requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryRead), pipelineAPI.listWorkflowTemplates)
	protected.GET("/workflow-presets", requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryRead), pipelineAPI.listWorkflowPresets)
	protected.GET("/workflow-runtime-versions", requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryRead), pipelineAPI.listWorkflowRuntimeVersions)
	protected.POST("/workflow-runtime-versions/prepare", auditAction(deps.Audits, deps.Logger, "workflow_runtime.prepare", "workflow_runtime"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryExecute), pipelineAPI.prepareWorkflowRuntimeVersion)
	protected.POST("/workflow-templates", auditAction(deps.Audits, deps.Logger, "workflow_template.create", "release_workflow_template"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryCreate), pipelineAPI.createWorkflowTemplate)
	protected.POST("/workflow-templates/from-preset", auditAction(deps.Audits, deps.Logger, "workflow_template.create_from_preset", "release_workflow_template"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryCreate), pipelineAPI.createWorkflowTemplateFromPreset)
	protected.POST("/workflow-templates/validate", requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryRead), pipelineAPI.validateWorkflowTemplate)
	protected.GET("/workflow-templates/:id", requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryRead), pipelineAPI.getWorkflowTemplate)
	protected.PUT("/workflow-templates/:id", auditAction(deps.Audits, deps.Logger, "workflow_template.update", "release_workflow_template"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryUpdate), pipelineAPI.saveWorkflowTemplate)
	protected.DELETE("/workflow-templates/:id", auditAction(deps.Audits, deps.Logger, "workflow_template.delete", "release_workflow_template"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryDelete), pipelineAPI.deleteWorkflowTemplate)
	protected.GET("/build-plans", requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryRead), pipelineAPI.listBuildPlans)
	protected.POST("/build-plans", auditAction(deps.Audits, deps.Logger, "build_plan.create", "build_plan"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryCreate), pipelineAPI.createBuildPlan)
	protected.GET("/build-plans/:id/artifacts", requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryRead), artifactAPI.listBuildPlan)
	protected.POST("/build-plans/:id/applications/:application_id/artifacts/upload", auditAction(deps.Audits, deps.Logger, "artifact.upload", "artifact"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryCreate), artifactAPI.uploadBuildPlan)
	protected.PUT("/build-plans/:id", auditAction(deps.Audits, deps.Logger, "build_plan.update", "build_plan"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryUpdate), pipelineAPI.updateBuildPlan)
	protected.PATCH("/build-plans/:id/status", auditAction(deps.Audits, deps.Logger, "build_plan.status.update", "build_plan"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryUpdate), pipelineAPI.setBuildPlanStatus)
	protected.DELETE("/build-plans/:id", auditAction(deps.Audits, deps.Logger, "build_plan.delete", "build_plan"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryDelete), pipelineAPI.deleteBuildPlan)
	protected.GET("/artifacts/:id", requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryRead), artifactAPI.get)
	protected.GET("/artifacts/:id/download", requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryRead), artifactAPI.download)
	protected.GET("/image-registries", requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryRead), pipelineAPI.listRegistries)
	protected.POST("/image-registries", auditAction(deps.Audits, deps.Logger, "image_registry.create", "image_registry"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryCreate), pipelineAPI.createRegistry)
	protected.POST("/image-registries/test", auditAction(deps.Audits, deps.Logger, "image_registry.test", "image_registry"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryExecute), pipelineAPI.testRegistry)
	protected.GET("/deployment-plans", requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryRead), pipelineAPI.listDeploymentPlans)
	protected.POST("/deployment-plans", auditAction(deps.Audits, deps.Logger, "deployment_plan.create", "deployment_plan"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryCreate), pipelineAPI.createDeploymentPlan)
	protected.PUT("/deployment-plans/:id", auditAction(deps.Audits, deps.Logger, "deployment_plan.update", "deployment_plan"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryUpdate), pipelineAPI.updateDeploymentPlan)
	protected.PATCH("/deployment-plans/:id/status", auditAction(deps.Audits, deps.Logger, "deployment_plan.status.update", "deployment_plan"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryUpdate), pipelineAPI.setDeploymentPlanStatus)
	protected.DELETE("/deployment-plans/:id", auditAction(deps.Audits, deps.Logger, "deployment_plan.delete", "deployment_plan"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryDelete), pipelineAPI.deleteDeploymentPlan)
	protected.GET("/release-plans", requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryRead), pipelineAPI.listReleasePlans)
	protected.POST("/release-plans", auditAction(deps.Audits, deps.Logger, "release_plan.create", "release_plan"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryCreate), pipelineAPI.createReleasePlan)
	protected.GET("/release-plans/:id", requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryRead), pipelineAPI.getReleasePlan)
	protected.PUT("/release-plans/:id", auditAction(deps.Audits, deps.Logger, "release_plan.update", "release_plan"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryUpdate), pipelineAPI.updateReleasePlan)
	protected.PUT("/release-plans/:id/configuration", auditAction(deps.Audits, deps.Logger, "release_plan.configuration.update", "release_plan"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryUpdate), pipelineAPI.saveReleasePlanConfiguration)
	protected.PATCH("/release-plans/:id/status", auditAction(deps.Audits, deps.Logger, "release_plan.status.update", "release_plan"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryUpdate), pipelineAPI.setReleasePlanStatus)
	protected.DELETE("/release-plans/:id", auditAction(deps.Audits, deps.Logger, "release_plan.delete", "release_plan"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryDelete), pipelineAPI.deleteReleasePlan)
	protected.POST("/release-plans/:id/executions", auditAction(deps.Audits, deps.Logger, "release_plan.execute", "release_plan_execution"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryExecute), pipelineAPI.createReleasePlanExecution)
	protected.PUT("/release-plans/:id/groups/:group_id", auditAction(deps.Audits, deps.Logger, "release_group.update", "release_plan"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryUpdate), pipelineAPI.updateReleaseGroup)
	protected.GET("/pipeline-runs", requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryRead), pipelineAPI.listRuns)
	protected.GET("/pipeline-runs/:id", requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryRead), pipelineAPI.getRun)
	protected.GET("/pipeline-runs/:id/logs", requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryRead), pipelineAPI.listRunLogs)
	protected.GET("/pipeline-runs/:id/logs/ws", requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryRead), pipelineAPI.streamRunLogs)
	protected.POST("/pipeline-runs/:id/execute", auditAction(deps.Audits, deps.Logger, "workflow_run.execute", "pipeline_run"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryExecute), pipelineAPI.executeRun)
	protected.GET("/pipeline-runs/:id/retry-options", requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryRead), pipelineAPI.listRetryRunOptions)
	protected.POST("/pipeline-runs/:id/retry", auditAction(deps.Audits, deps.Logger, "workflow_run.retry", "pipeline_run"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryExecute), pipelineAPI.retryRun)
	protected.POST("/pipeline-runs/:id/advance", auditAction(deps.Audits, deps.Logger, "workflow_run.advance", "pipeline_run"), requirePermission(deps.Access, deps.Logger, access.PermissionDeliveryExecute), pipelineAPI.advanceRun)
	protected.POST("/pipeline-runs/:id/approve", auditAction(deps.Audits, deps.Logger, "workflow_run.approve", "pipeline_run"), requirePermission(deps.Access, deps.Logger, access.PermissionDeploymentReview), pipelineAPI.approveRun)

	clusterAPI := clusterHandler{docker: deps.Docker, kube: deps.Kubernetes, logger: deps.Logger}
	protected.GET("/docker/endpoints", requirePermission(deps.Access, deps.Logger, access.PermissionClusterRead), clusterAPI.listDockerEndpoints)
	protected.POST("/docker/endpoints/test", auditAction(deps.Audits, deps.Logger, "docker.endpoint.test_input", "docker_endpoint"), requirePermission(deps.Access, deps.Logger, access.PermissionClusterExecute), clusterAPI.testDockerSSH)
	protected.POST("/docker/endpoints", auditAction(deps.Audits, deps.Logger, "docker.endpoint.create", "docker_endpoint"), requirePermission(deps.Access, deps.Logger, access.PermissionClusterCreate), clusterAPI.createDockerEndpoint)
	protected.PUT("/docker/endpoints/:id", auditAction(deps.Audits, deps.Logger, "docker.endpoint.update", "docker_endpoint"), requirePermission(deps.Access, deps.Logger, access.PermissionClusterUpdate), clusterAPI.updateDockerEndpoint)
	protected.PATCH("/docker/endpoints/:id/name", auditAction(deps.Audits, deps.Logger, "docker.endpoint.rename", "docker_endpoint"), requirePermission(deps.Access, deps.Logger, access.PermissionClusterUpdate), clusterAPI.renameDockerEndpoint)
	protected.PATCH("/docker/endpoints/:id/status", auditAction(deps.Audits, deps.Logger, "docker.endpoint.status.update", "docker_endpoint"), requirePermission(deps.Access, deps.Logger, access.PermissionClusterUpdate), clusterAPI.setDockerStatus)
	protected.POST("/docker/endpoints/:id/ping", auditAction(deps.Audits, deps.Logger, "docker.endpoint.test", "docker_endpoint"), requirePermission(deps.Access, deps.Logger, access.PermissionClusterExecute), clusterAPI.pingDocker)
	protected.GET("/docker/endpoints/:id/containers", requirePermission(deps.Access, deps.Logger, access.PermissionClusterRead), clusterAPI.listContainers)
	containerLogsAPI := containerLogHandler{docker: deps.Docker, audits: deps.Audits, logger: deps.Logger}
	protected.GET("/docker/endpoints/:id/containers/:container_id/logs/ws", requirePermission(deps.Access, deps.Logger, access.PermissionClusterRead), containerLogsAPI.stream)

	protected.GET("/kubernetes/clusters", requirePermission(deps.Access, deps.Logger, access.PermissionClusterRead), clusterAPI.listKubernetesClusters)
	protected.POST("/kubernetes/clusters/test", auditAction(deps.Audits, deps.Logger, "kubernetes.cluster.test_input", "kubernetes_cluster"), requirePermission(deps.Access, deps.Logger, access.PermissionClusterExecute), clusterAPI.testKubernetesCluster)
	protected.POST("/kubernetes/clusters", auditAction(deps.Audits, deps.Logger, "kubernetes.cluster.create", "kubernetes_cluster"), requirePermission(deps.Access, deps.Logger, access.PermissionClusterCreate), clusterAPI.createKubernetesCluster)
	protected.PUT("/kubernetes/clusters/:id", auditAction(deps.Audits, deps.Logger, "kubernetes.cluster.update", "kubernetes_cluster"), requirePermission(deps.Access, deps.Logger, access.PermissionClusterUpdate), clusterAPI.updateKubernetesCluster)
	protected.PATCH("/kubernetes/clusters/:id/status", auditAction(deps.Audits, deps.Logger, "kubernetes.cluster.status.update", "kubernetes_cluster"), requirePermission(deps.Access, deps.Logger, access.PermissionClusterUpdate), clusterAPI.setKubernetesStatus)
	protected.POST("/kubernetes/clusters/:id/ping", auditAction(deps.Audits, deps.Logger, "kubernetes.cluster.test", "kubernetes_cluster"), requirePermission(deps.Access, deps.Logger, access.PermissionClusterExecute), clusterAPI.pingKubernetes)
	protected.GET("/kubernetes/clusters/:id/namespaces", requirePermission(deps.Access, deps.Logger, access.PermissionClusterRead), clusterAPI.listNamespaces)
	protected.GET("/kubernetes/clusters/:id/pods", requirePermission(deps.Access, deps.Logger, access.PermissionClusterRead), clusterAPI.listPods)
	protected.GET("/kubernetes/clusters/:id/deployments", requirePermission(deps.Access, deps.Logger, access.PermissionClusterRead), clusterAPI.listDeployments)

	hostAPI := hostHandler{service: deps.Hosts, logger: deps.Logger}
	protected.GET("/hosts", requireAnyPermission(deps.Access, deps.Logger, access.PermissionClusterRead, access.PermissionDeploymentRead), hostAPI.list)
	protected.GET("/hosts/statuses", requireAnyPermission(deps.Access, deps.Logger, access.PermissionClusterRead, access.PermissionDeploymentRead), hostAPI.listStatuses)
	protected.GET("/hosts/:id", requireAnyPermission(deps.Access, deps.Logger, access.PermissionClusterRead, access.PermissionDeploymentRead), hostAPI.get)
	protected.POST("/hosts/:id/ping", auditAction(deps.Audits, deps.Logger, "host.ping", "host"), requireAnyPermission(deps.Access, deps.Logger, access.PermissionClusterExecute, access.PermissionDeploymentExecute), hostAPI.ping)
	protected.POST("/hosts/test", auditAction(deps.Audits, deps.Logger, "host.test", "host"), requirePermission(deps.Access, deps.Logger, access.PermissionClusterExecute), hostAPI.test)
	protected.POST("/hosts/:id/test", auditAction(deps.Audits, deps.Logger, "host.test", "host"), requirePermission(deps.Access, deps.Logger, access.PermissionClusterExecute), hostAPI.testExisting)
	protected.POST("/hosts", auditAction(deps.Audits, deps.Logger, "host.create", "host"), requirePermission(deps.Access, deps.Logger, access.PermissionClusterCreate), hostAPI.create)
	protected.PUT("/hosts/:id", auditAction(deps.Audits, deps.Logger, "host.update", "host"), requirePermission(deps.Access, deps.Logger, access.PermissionClusterUpdate), hostAPI.update)
	protected.PATCH("/hosts/:id/status", auditAction(deps.Audits, deps.Logger, "host.status.update", "host"), requirePermission(deps.Access, deps.Logger, access.PermissionClusterUpdate), hostAPI.setStatus)
	protected.DELETE("/hosts/:id", auditAction(deps.Audits, deps.Logger, "host.delete", "host"), requirePermission(deps.Access, deps.Logger, access.PermissionClusterDelete), hostAPI.remove)

	deploymentAPI := deploymentHandler{service: deps.Deployments, logger: deps.Logger}
	environmentAPI := environmentHandler{service: deps.Environments, hosts: deps.Hosts, logger: deps.Logger}
	protected.GET("/environments", requirePermission(deps.Access, deps.Logger, access.PermissionDeploymentRead), environmentAPI.list)
	protected.GET("/environments/:id", requirePermission(deps.Access, deps.Logger, access.PermissionDeploymentRead), environmentAPI.get)
	protected.POST("/environments", auditAction(deps.Audits, deps.Logger, "environment.create", "environment"), requirePermission(deps.Access, deps.Logger, access.PermissionDeploymentCreate), environmentAPI.create)
	protected.PUT("/environments/:id", auditAction(deps.Audits, deps.Logger, "environment.update", "environment"), requirePermission(deps.Access, deps.Logger, access.PermissionDeploymentUpdate), environmentAPI.update)
	protected.PATCH("/environments/:id", auditAction(deps.Audits, deps.Logger, "environment.profile.update", "environment"), requirePermission(deps.Access, deps.Logger, access.PermissionDeploymentUpdate), environmentAPI.updateProfile)
	protected.PUT("/environments/:id/hosts", auditAction(deps.Audits, deps.Logger, "environment.hosts.update", "environment"), requirePermission(deps.Access, deps.Logger, access.PermissionDeploymentUpdate), environmentAPI.replaceHosts)
	protected.PATCH("/environments/:id/status", auditAction(deps.Audits, deps.Logger, "environment.status.update", "environment"), requirePermission(deps.Access, deps.Logger, access.PermissionDeploymentUpdate), environmentAPI.setStatus)
	protected.DELETE("/environments/:id", auditAction(deps.Audits, deps.Logger, "environment.delete", "environment"), requirePermission(deps.Access, deps.Logger, access.PermissionDeploymentDelete), environmentAPI.remove)
	protected.GET("/deployments", requirePermission(deps.Access, deps.Logger, access.PermissionDeploymentRead), deploymentAPI.list)
	protected.POST("/deployments/:id/rollback", auditAction(deps.Audits, deps.Logger, "deployment.rollback", "deployment"), requirePermission(deps.Access, deps.Logger, access.PermissionDeploymentExecute), deploymentAPI.rollback)
	protected.GET("/deployments/:id/runtime", requirePermission(deps.Access, deps.Logger, access.PermissionDeploymentRead), deploymentAPI.runtimeState)
	protected.GET("/deployments/:id/runtime/configuration", requirePermission(deps.Access, deps.Logger, access.PermissionDeploymentRead), deploymentAPI.runtimeConfiguration)
	protected.PUT("/deployments/:id/runtime/configuration", auditAction(deps.Audits, deps.Logger, "deployment.runtime.configuration.update", "deployment"), requirePermission(deps.Access, deps.Logger, access.PermissionDeploymentUpdate), deploymentAPI.saveRuntimeConfiguration)
	protected.POST("/deployments/:id/runtime/restart", auditAction(deps.Audits, deps.Logger, "deployment.runtime.restart", "deployment"), requirePermission(deps.Access, deps.Logger, access.PermissionDeploymentExecute), deploymentAPI.restart)
	protected.POST("/deployments/:id/runtime/stop", auditAction(deps.Audits, deps.Logger, "deployment.runtime.stop", "deployment"), requirePermission(deps.Access, deps.Logger, access.PermissionDeploymentExecute), deploymentAPI.stop)
	protected.POST("/deployments/:id/runtime/scale", auditAction(deps.Audits, deps.Logger, "deployment.runtime.scale", "deployment"), requirePermission(deps.Access, deps.Logger, access.PermissionDeploymentExecute), deploymentAPI.scale)
	protected.DELETE("/deployments/:id/runtime", auditAction(deps.Audits, deps.Logger, "deployment.runtime.remove", "deployment"), requirePermission(deps.Access, deps.Logger, access.PermissionDeploymentExecute), deploymentAPI.removeRuntime)

	terminalAPI := terminalHandler{service: deps.Terminal, audits: deps.Audits, logger: deps.Logger}
	protected.GET("/terminals/docker/:endpoint_id/containers/:container_id/ws", requirePermission(deps.Access, deps.Logger, access.PermissionTerminalOpen), terminalAPI.docker)
	protected.GET("/terminals/kubernetes/:cluster_id/namespaces/:namespace/pods/:pod/containers/:container/ws", requirePermission(deps.Access, deps.Logger, access.PermissionTerminalOpen), terminalAPI.kubernetes)

	configurationAPI := configurationHandler{service: deps.Configurations, logger: deps.Logger}
	settingsAPI := settingsHandler{service: deps.Configurations, loginLimiter: deps.LoginLimiter, authConfig: deps.AuthConfig, retention: deps.LogRetention, migration: deps.DatabaseTransfer, runtimeLogs: deps.RuntimeLogs, repositories: deps.Repositories, artifacts: deps.Artifacts, logger: deps.Logger}
	protected.GET("/settings/builtin-variables", requireAnyPermission(deps.Access, deps.Logger, access.PermissionConfigRead, access.PermissionDeliveryRead), settingsAPI.builtinVariables)
	protected.GET("/settings/external-git-webhook", requirePermission(deps.Access, deps.Logger, access.PermissionConfigRead), settingsAPI.externalGitWebhook)
	protected.PUT("/settings/external-git-webhook", auditAction(deps.Audits, deps.Logger, "settings.git_webhook.update", "settings"), requirePermission(deps.Access, deps.Logger, access.PermissionConfigUpdate), settingsAPI.updateExternalGitWebhook)
	protected.GET("/settings/login-lockout", requirePermission(deps.Access, deps.Logger, access.PermissionConfigRead), settingsAPI.loginLockout)
	protected.PUT("/settings/login-lockout", auditAction(deps.Audits, deps.Logger, "settings.login_lockout.update", "settings"), requirePermission(deps.Access, deps.Logger, access.PermissionConfigUpdate), settingsAPI.updateLoginLockout)
	protected.GET("/settings/runtime-logging", requirePermission(deps.Access, deps.Logger, access.PermissionConfigRead), settingsAPI.runtimeLogging)
	protected.PUT("/settings/runtime-logging", auditAction(deps.Audits, deps.Logger, "settings.runtime_logging.update", "settings"), requirePermission(deps.Access, deps.Logger, access.PermissionConfigUpdate), settingsAPI.updateRuntimeLogging)
	protected.GET("/settings/log-retention", requirePermission(deps.Access, deps.Logger, access.PermissionConfigRead), settingsAPI.logRetention)
	protected.PUT("/settings/log-retention", auditAction(deps.Audits, deps.Logger, "settings.log_retention.update", "settings"), requirePermission(deps.Access, deps.Logger, access.PermissionConfigUpdate), settingsAPI.updateLogRetention)
	protected.POST("/settings/log-retention/cleanup", auditAction(deps.Audits, deps.Logger, "settings.log_retention.cleanup", "settings"), requirePermission(deps.Access, deps.Logger, access.PermissionConfigExecute), settingsAPI.cleanupLogs)
	protected.GET("/settings/runtime-directories", requirePermission(deps.Access, deps.Logger, access.PermissionConfigRead), settingsAPI.runtimeDirectories)
	protected.PUT("/settings/runtime-directories", auditAction(deps.Audits, deps.Logger, "settings.runtime_directories.update", "settings"), requirePermission(deps.Access, deps.Logger, access.PermissionConfigUpdate), settingsAPI.updateRuntimeDirectories)
	protected.POST("/settings/runtime-directories/cleanup-workspaces", auditAction(deps.Audits, deps.Logger, "settings.repository_workspaces.cleanup", "settings"), requirePermission(deps.Access, deps.Logger, access.PermissionConfigExecute), settingsAPI.cleanupRepositoryWorkspaces)
	protected.POST("/settings/runtime-directories/cleanup-builds", auditAction(deps.Audits, deps.Logger, "settings.builds.cleanup", "settings"), requirePermission(deps.Access, deps.Logger, access.PermissionConfigExecute), settingsAPI.cleanupBuilds)
	protected.POST("/settings/runtime-directories/cleanup-cache", auditAction(deps.Audits, deps.Logger, "settings.repository_cache.cleanup", "settings"), requirePermission(deps.Access, deps.Logger, access.PermissionConfigExecute), settingsAPI.cleanupRepositoryCache)
	protected.POST("/settings/runtime-directories/cleanup-artifacts", auditAction(deps.Audits, deps.Logger, "settings.local_artifacts.cleanup", "settings"), requirePermission(deps.Access, deps.Logger, access.PermissionConfigExecute), settingsAPI.cleanupLocalArtifacts)
	protected.GET("/settings/database-migration", requireSuperuser(deps.Logger), settingsAPI.databaseMigrationStatus)
	protected.POST("/settings/database-migration/test", auditAction(deps.Audits, deps.Logger, "settings.database_migration.test", "settings"), requireSuperuser(deps.Logger), settingsAPI.testDatabaseMigration)
	protected.POST("/settings/database-migration", auditAction(deps.Audits, deps.Logger, "settings.database_migration.start", "settings"), requireSuperuser(deps.Logger), settingsAPI.startDatabaseMigration)
	protected.GET("/configurations", requirePermission(deps.Access, deps.Logger, access.PermissionConfigRead), configurationAPI.list)
	protected.POST("/configurations", auditAction(deps.Audits, deps.Logger, "configuration.create", "configuration"), requirePermission(deps.Access, deps.Logger, access.PermissionConfigCreate), configurationAPI.create)
	protected.PUT("/configurations/:id", auditAction(deps.Audits, deps.Logger, "configuration.update", "configuration"), requirePermission(deps.Access, deps.Logger, access.PermissionConfigUpdate), configurationAPI.update)
	protected.PATCH("/configurations/:id/status", auditAction(deps.Audits, deps.Logger, "configuration.status.update", "configuration"), requirePermission(deps.Access, deps.Logger, access.PermissionConfigUpdate), configurationAPI.setStatus)
	protected.GET("/configurations/:id/revisions", requirePermission(deps.Access, deps.Logger, access.PermissionConfigRead), configurationAPI.revisions)

	notificationAPI := notificationHandler{service: deps.Notifications, logger: deps.Logger}
	protected.GET("/notification-channels", requirePermission(deps.Access, deps.Logger, access.PermissionNotificationRead), notificationAPI.listChannels)
	protected.POST("/notification-channels", auditAction(deps.Audits, deps.Logger, "notification.channel.create", "notification_channel"), requirePermission(deps.Access, deps.Logger, access.PermissionNotificationCreate), notificationAPI.createChannel)
	protected.PUT("/notification-channels/:id", auditAction(deps.Audits, deps.Logger, "notification.channel.update", "notification_channel"), requirePermission(deps.Access, deps.Logger, access.PermissionNotificationUpdate), notificationAPI.updateChannel)
	protected.PATCH("/notification-channels/:id/status", auditAction(deps.Audits, deps.Logger, "notification.channel.status.update", "notification_channel"), requirePermission(deps.Access, deps.Logger, access.PermissionNotificationUpdate), notificationAPI.setChannelStatus)
	protected.POST("/notification-channels/:id/test", auditAction(deps.Audits, deps.Logger, "notification.channel.test", "notification"), requirePermission(deps.Access, deps.Logger, access.PermissionNotificationExecute), notificationAPI.testChannel)
	protected.GET("/notifications", requirePermission(deps.Access, deps.Logger, access.PermissionNotificationRead), notificationAPI.list)

	monitorAPI := monitorHandler{service: deps.Monitors, logger: deps.Logger}
	protected.GET("/monitor-rules", requirePermission(deps.Access, deps.Logger, access.PermissionMonitorRead), monitorAPI.list)
	protected.POST("/monitor-rules", auditAction(deps.Audits, deps.Logger, "monitor.rule.create", "monitor_rule"), requirePermission(deps.Access, deps.Logger, access.PermissionMonitorCreate), monitorAPI.create)
	protected.PUT("/monitor-rules/:id", auditAction(deps.Audits, deps.Logger, "monitor.rule.update", "monitor_rule"), requirePermission(deps.Access, deps.Logger, access.PermissionMonitorUpdate), monitorAPI.update)
	protected.PATCH("/monitor-rules/:id/status", auditAction(deps.Audits, deps.Logger, "monitor.rule.status.update", "monitor_rule"), requirePermission(deps.Access, deps.Logger, access.PermissionMonitorUpdate), monitorAPI.setStatus)
	protected.GET("/monitor-rules/:id/checks", requirePermission(deps.Access, deps.Logger, access.PermissionMonitorRead), monitorAPI.checks)

	schedulerAPI := schedulerHandler{service: deps.Scheduler, logger: deps.Logger}
	protected.GET("/schedules", requirePermission(deps.Access, deps.Logger, access.PermissionSchedulerRead), schedulerAPI.list)
	protected.POST("/schedules", auditAction(deps.Audits, deps.Logger, "scheduler.create", "schedule"), requirePermission(deps.Access, deps.Logger, access.PermissionSchedulerCreate), schedulerAPI.create)
	protected.PUT("/schedules/:id", auditAction(deps.Audits, deps.Logger, "scheduler.update", "schedule"), requirePermission(deps.Access, deps.Logger, access.PermissionSchedulerUpdate), schedulerAPI.update)
	protected.PATCH("/schedules/:id/status", auditAction(deps.Audits, deps.Logger, "scheduler.status.update", "schedule"), requirePermission(deps.Access, deps.Logger, access.PermissionSchedulerUpdate), schedulerAPI.setStatus)

	taskAPI := taskHandler{service: deps.Tasks, logger: deps.Logger}
	protected.GET("/tasks", requirePermission(deps.Access, deps.Logger, access.PermissionTaskRead), taskAPI.list)
	protected.POST("/tasks/:id/cancel", auditAction(deps.Audits, deps.Logger, "task.cancel", "task"), requirePermission(deps.Access, deps.Logger, access.PermissionTaskExecute), taskAPI.cancel)
	protected.POST("/tasks/:id/retry", auditAction(deps.Audits, deps.Logger, "task.retry", "task"), requirePermission(deps.Access, deps.Logger, access.PermissionTaskExecute), taskAPI.retry)
	webIndex := installWebUI(router, deps.WebRoot, deps.Logger)
	router.NoRoute(func(c *gin.Context) {
		if webIndex != nil && c.Request.Method == http.MethodGet && !strings.HasPrefix(c.Request.URL.Path, "/api/") {
			webIndex(c)
			return
		}
		c.JSON(http.StatusNotFound, errorResponse{
			Code: "not_found", Message: "请求的资源不存在", RequestID: requestIDFrom(c),
		})
	})
	return router
}

func installWebUI(router *gin.Engine, webRoot string, logger *slog.Logger) gin.HandlerFunc {
	if strings.TrimSpace(webRoot) != "" {
		root, err := filepath.Abs(webRoot)
		if err != nil {
			logger.Warn("Web 前端目录无效", "operation", "webui_path", "err", err)
		} else {
			index := filepath.Join(root, "index.html")
			if info, err := os.Stat(index); err == nil && info.Mode().IsRegular() {
				assets := filepath.Join(root, "assets")
				if info, err := os.Stat(assets); err == nil && info.IsDir() {
					router.StaticFS("/assets", http.Dir(assets))
				}
				handler := func(c *gin.Context) { c.File(index) }
				router.GET("/", handler)
				logger.Info("EDO Web 前端已启用", "operation", "webui_enabled", "web_root", root)
				return handler
			}
		}
	}

	embedded := webui.Files()
	if embedded == nil {
		logger.Info("未发现已构建的 Web 前端，API 将独立运行", "operation", "webui_disabled")
		return nil
	}
	index, err := fs.ReadFile(embedded, "index.html")
	if err != nil {
		logger.Error("读取内嵌 Web 前端失败", "operation", "webui_embed", "err", err)
		return nil
	}
	if assets, err := fs.Sub(embedded, "assets"); err == nil {
		router.StaticFS("/assets", http.FS(assets))
	}
	handler := func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", index)
	}
	router.GET("/", handler)
	logger.Info("EDO 内嵌 Web 前端已启用", "operation", "webui_enabled", "web_root", "embedded")
	return handler
}
