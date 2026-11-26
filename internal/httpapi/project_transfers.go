package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"edo/internal/access"
	"edo/internal/audit"
	"edo/internal/database"
	"edo/internal/model"
	"edo/internal/projecttransfer"
	"edo/internal/realtime"
)

type projectTransferService interface {
	Catalog(context.Context, string) (projecttransfer.Catalog, error)
	Preview(context.Context, string, projecttransfer.Input) (projecttransfer.Plan, error)
	Move(context.Context, string, projecttransfer.Input) (projecttransfer.Plan, error)
}

type projectTransferHandler struct {
	service projectTransferService
	access  *access.Service
	logger  *slog.Logger
	bus     *realtime.Bus
}

type projectTransferRequest struct {
	TargetProjectID string `json:"target_project_id" binding:"required,max=36"`
	Resources       []struct {
		Kind string `json:"kind" binding:"required,max=64"`
		ID   string `json:"id" binding:"required,max=36"`
	} `json:"resources" binding:"required,min=1,max=100,dive"`
	Confirmation        string `json:"confirmation" binding:"max=512"`
	IncludeDependencies bool   `json:"include_dependencies"`
}

func registerProjectTransferRoutes(group *gin.RouterGroup, deps Dependencies) {
	h := projectTransferHandler{service: deps.ProjectTransfers, access: deps.Access, logger: deps.Logger, bus: deps.Realtime}
	permission := requirePermission(deps.Access, deps.Logger, access.PermissionProjectResourceMove)
	group.GET("/project-resource-transfers/catalog", permission, h.catalog)
	group.POST("/project-resource-transfers/preview", permission, h.preview)
	group.POST("/project-resource-transfers", auditAction(deps.Audits, deps.Logger, access.PermissionProjectResourceMove, "project"), func(c *gin.Context) {
		scope, _ := database.DepartmentScopeFromContext(c.Request.Context())
		setAuditResourceID(c, scope.ProjectID)
		setAuditMetadata(c, map[string]any{"source_project_id": scope.ProjectID})
		c.Next()
	}, permission, h.move)
}

func (h projectTransferHandler) catalog(c *gin.Context) {
	scope, _ := database.DepartmentScopeFromContext(c.Request.Context())
	actor, _ := currentUser(c)
	projects, err := h.access.ProjectResourceMoveTargets(c.Request.Context(), actor)
	if err != nil {
		h.fail(c, "project_transfer_catalog_targets", "", err)
		return
	}
	source, err := h.access.ProjectResourceMoveSource(c.Request.Context(), actor)
	if err != nil {
		h.fail(c, "project_transfer_catalog_source", "", err)
		return
	}
	result, err := h.service.Catalog(c.Request.Context(), scope.ProjectID)
	if err != nil {
		h.fail(c, "project_transfer_catalog", "", err)
		return
	}
	type target struct {
		ID             string `json:"id"`
		Name           string `json:"name"`
		DepartmentName string `json:"department_name"`
	}
	targets := make([]target, 0, len(projects))
	for _, project := range projects {
		targets = append(targets, target{ID: project.ID, Name: project.Name, DepartmentName: project.DepartmentName})
	}
	c.JSON(http.StatusOK, gin.H{"kinds": result.Kinds, "resources": result.Resources, "projects": targets, "source_project": target{ID: source.ID, Name: source.Name, DepartmentName: source.DepartmentName}})
}

func (h projectTransferHandler) preview(c *gin.Context) {
	input, source, ok := h.request(c)
	if !ok {
		return
	}
	plan, err := h.service.Preview(c.Request.Context(), source, input)
	if err != nil {
		h.fail(c, "project_transfer_preview", input.TargetProjectID, err)
		return
	}
	c.JSON(http.StatusOK, plan)
}

func (h projectTransferHandler) move(c *gin.Context) {
	input, source, ok := h.request(c)
	if !ok {
		return
	}
	actor, _ := currentUser(c)
	setAuditResourceID(c, source)
	setAuditMetadata(c, map[string]any{"source_project_id": source, "target_project_id": input.TargetProjectID, "resources": input.Resources})
	// 审计失败回滚移动，成功提交后外层中间件仅保留失败与拒绝的记录。
	input.Audit = func(tx *gorm.DB, plan projecttransfer.Plan) error {
		resources := make([]projecttransfer.Resource, 0, len(plan.Resources))
		for _, item := range plan.Resources {
			resources = append(resources, item.Resource)
		}
		return audit.NewService(tx).Record(c.Request.Context(), audit.RecordInput{
			ActorUserID: actor.ID, Action: access.PermissionProjectResourceMove, ResourceType: "project",
			ResourceID: source, Result: model.AuditSucceeded, RequestID: requestIDFrom(c),
			ClientIP: c.ClientIP(), UserAgent: truncateText(c.Request.UserAgent(), 512),
			Metadata: map[string]any{"source_project_id": source, "target_project_id": input.TargetProjectID, "source_department_id": plan.SourceDepartmentID, "target_department_id": plan.TargetDepartmentID, "resources": resources, "history_count": plan.HistoryCount},
		})
	}
	plan, err := h.service.Move(c.Request.Context(), source, input)
	if err != nil {
		h.fail(c, "project_transfer_move", input.TargetProjectID, err)
		return
	}
	c.Set(auditCommittedKey, true)
	if h.bus != nil {
		scope, _ := database.DepartmentScopeFromContext(c.Request.Context())
		for _, project := range []struct{ id, departmentID string }{
			{plan.SourceProjectID, plan.SourceDepartmentID},
			{plan.TargetProjectID, plan.TargetDepartmentID},
		} {
			scope.ProjectID = project.id
			scope.DepartmentID = project.departmentID
			ctx := database.WithDepartmentScope(c.Request.Context(), scope)
			for _, resource := range []string{"applications", "pipeline_runs", "release_plans", "deployments", "deployment_plans"} {
				h.bus.Changed(ctx, resource, "")
			}
		}
	}
	c.JSON(http.StatusOK, plan)
}

func (h projectTransferHandler) request(c *gin.Context) (projecttransfer.Input, string, bool) {
	var request projectTransferRequest
	scope, _ := database.DepartmentScopeFromContext(c.Request.Context())
	input := projecttransfer.Input{}
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("项目资源移动参数无效", "operation", "project_transfer_bind", "request_id", requestIDFrom(c), "source_project_id", scope.ProjectID, "err", err)
		writeError(c, http.StatusBadRequest, "invalid_project_transfer", projecttransfer.ErrInvalid.Error())
		return input, scope.ProjectID, false
	}
	input.TargetProjectID = request.TargetProjectID
	input.Confirmation = request.Confirmation
	input.IncludeDependencies = request.IncludeDependencies
	setAuditMetadata(c, map[string]any{"target_project_id": input.TargetProjectID})
	for _, resource := range request.Resources {
		input.Resources = append(input.Resources, projecttransfer.Resource{Kind: resource.Kind, ID: resource.ID})
	}
	if input.IncludeDependencies && (len(input.Resources) != 1 || input.Resources[0].Kind != "application") {
		h.fail(c, "project_transfer_application_selection", input.TargetProjectID, projecttransfer.ErrInvalid)
		return input, scope.ProjectID, false
	}
	actor, _ := currentUser(c)
	target, err := h.access.ResolveProject(c.Request.Context(), actor, input.TargetProjectID)
	if err != nil {
		h.fail(c, "project_transfer_target", input.TargetProjectID, err)
		return input, scope.ProjectID, false
	}
	if target.ID == scope.ProjectID || (!actor.IsSuperuser && target.DepartmentID != scope.DepartmentID) {
		h.fail(c, "project_transfer_target", input.TargetProjectID, projecttransfer.ErrInvalid)
		return input, scope.ProjectID, false
	}
	targetScope := scope
	targetScope.ProjectID = target.ID
	targetScope.DepartmentID = target.DepartmentID
	targetContext := database.WithDepartmentScope(c.Request.Context(), targetScope)
	allowed, err := h.access.HasPermission(targetContext, actor, access.PermissionProjectResourceMove)
	if err == nil && allowed {
		allowed, err = h.access.HasCurrentProjectPermission(targetContext, actor, access.PermissionProjectResourceMove)
	}
	if err != nil || !allowed {
		if err == nil {
			err = projecttransfer.ErrForbidden
		}
		h.fail(c, "project_transfer_target_permission", target.ID, err)
		return input, scope.ProjectID, false
	}
	input.Authorize = h.authorize(c.Request.Context(), *actor, scope, targetScope)
	return input, scope.ProjectID, true
}

func (h projectTransferHandler) authorize(ctx context.Context, actor model.User, source, target database.DepartmentScope) func(*gorm.DB) error {
	return func(tx *gorm.DB) error {
		for _, scope := range []database.DepartmentScope{source, target} {
			projectContext := database.WithDepartmentScope(ctx, scope)
			current, err := access.FreshProjectPermissions(tx, projectContext, actor.ID, scope.ProjectID)
			if err == nil && (current.User.AuthVersion != actor.AuthVersion || current.User.DepartmentID != actor.DepartmentID || current.User.IsSuperuser != actor.IsSuperuser || !current.Permissions[access.PermissionProjectResourceMove]) {
				err = access.ErrAccessDelegationDenied
			}
			if err == nil {
				var allowed bool
				allowed, err = h.access.HasPermission(projectContext, &current.User, access.PermissionProjectResourceMove)
				if err == nil && !allowed {
					err = access.ErrAccessDelegationDenied
				}
			}
			if err != nil {
				h.logger.WarnContext(ctx, "事务内资源移动权限复核失败", "operation", "project_transfer_authorize", "user_id", actor.ID, "project_id", scope.ProjectID, "department_id", scope.DepartmentID, "err", err)
				if errors.Is(err, access.ErrAccessDelegationDenied) {
					return projecttransfer.ErrForbidden
				}
				return err
			}
		}
		return nil
	}
}

func (h projectTransferHandler) fail(c *gin.Context, operation, targetID string, err error) {
	scope, _ := database.DepartmentScopeFromContext(c.Request.Context())
	h.logger.Warn("项目资源移动失败", "operation", operation, "request_id", requestIDFrom(c), "source_project_id", scope.ProjectID, "target_project_id", targetID, "err", err)
	switch {
	case errors.Is(err, projecttransfer.ErrInvalid):
		writeError(c, http.StatusBadRequest, "invalid_project_transfer", projecttransfer.ErrInvalid.Error())
	case errors.Is(err, projecttransfer.ErrNotFound):
		writeError(c, http.StatusNotFound, "project_transfer_not_found", projecttransfer.ErrNotFound.Error())
	case errors.Is(err, projecttransfer.ErrConflict):
		writeError(c, http.StatusConflict, "project_transfer_conflict", projecttransfer.ErrConflict.Error())
	case errors.Is(err, projecttransfer.ErrConfirmation):
		writeError(c, http.StatusConflict, "project_transfer_confirmation", projecttransfer.ErrConfirmation.Error())
	case errors.Is(err, projecttransfer.ErrForbidden), errors.Is(err, access.ErrAccessDelegationDenied), errors.Is(err, access.ErrProjectNotFound):
		writeError(c, http.StatusForbidden, "project_transfer_forbidden", projecttransfer.ErrForbidden.Error())
	default:
		writeInternalError(c)
	}
}
