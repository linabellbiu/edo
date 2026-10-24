package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"edo/internal/sqlchange"
	"github.com/gin-gonic/gin"
)

type sqlChangeHandler struct {
	service *sqlchange.Service
	logger  *slog.Logger
}

func registerSQLChangeRoutes(protected *gin.RouterGroup, deps Dependencies) {
	h := sqlChangeHandler{service: deps.SQLChanges, logger: deps.Logger}
	routes := protected.Group("/sql-changes")
	routes.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
	})
	permission := func(code string) gin.HandlerFunc {
		return requirePermission(deps.Access, deps.Logger, "sql_change."+code)
	}
	available := func(c *gin.Context) {
		if h.service != nil {
			return
		}
		h.logger.Error("SQL 工单服务未初始化", "operation", "sql_change_unavailable", "request_id", requestIDFrom(c))
		writeError(c, http.StatusServiceUnavailable, "sql_change_unavailable", "SQL 工单服务暂不可用，请稍后重试")
		c.Abort()
	}
	audited := func(action string) gin.HandlerFunc {
		return auditAction(deps.Audits, deps.Logger, "sql_change."+action, "sql_change")
	}
	routes.GET("", permission("read"), available, h.list)
	routes.GET("/options", permission("read"), available, h.options)
	routes.GET("/:id", permission("read"), available, h.detail)
	routes.POST("", audited("create"), permission("read"), permission("create"), available, h.save)
	routes.PUT("/:id", audited("update"), permission("read"), permission("update"), available, h.save)
	routes.DELETE("/:id", audited("delete"), permission("read"), permission("delete"), available, h.remove)
	routes.POST("/:id/reviews", audited("review"), permission("read"), permission("review"), available, h.review)
	routes.POST("/:id/execute", audited("execute"), permission("read"), permission("execute"),
		requirePermission(deps.Access, deps.Logger, "application_database.execute"), available, h.execute)
	routes.POST("/:id/skip", audited("skip"), permission("read"), permission("skip"), available, h.skip)
	routes.POST("/:id/check", audited("check"), permission("read"), permission("check"), available, h.check)
}

func (h sqlChangeHandler) fail(c *gin.Context, operation string, err error) {
	actorID := ""
	if actor, ok := currentUser(c); ok {
		actorID = actor.ID
	}
	h.logger.Error("SQL 工单请求失败", "operation", operation, "resource_id", c.Param("id"), "actor_id", actorID,
		"request_id", requestIDFrom(c), "err", err)
	status, code, message := sqlChangeHTTPError(err)
	writeError(c, status, code, message)
}

func sqlChangeHTTPError(err error) (int, string, string) {
	for _, item := range []struct {
		err    error
		status int
		code   string
	}{
		{sqlchange.ErrScope, http.StatusBadRequest, "sql_change_scope_required"},
		{sqlchange.ErrForbidden, http.StatusForbidden, "sql_change_denied"},
		{sqlchange.ErrNotFound, http.StatusNotFound, "sql_change_not_found"},
		{sqlchange.ErrInvalid, http.StatusBadRequest, "sql_change_invalid"},
		{sqlchange.ErrConflict, http.StatusConflict, "sql_change_conflict"},
		{sqlchange.ErrImmutable, http.StatusConflict, "sql_change_immutable"},
		{sqlchange.ErrState, http.StatusConflict, "sql_change_state"},
		{sqlchange.ErrUnavailable, http.StatusServiceUnavailable, "sql_change_unavailable"},
		{sqlchange.ErrStorage, http.StatusServiceUnavailable, "sql_change_storage_failed"},
		{sqlchange.ErrCheckFailed, http.StatusConflict, "sql_change_check_failed"},
		{sqlchange.ErrExecutionFailed, http.StatusConflict, "sql_change_execution_failed"},
		{sqlchange.ErrExecutionUncertain, http.StatusConflict, "sql_change_execution_uncertain"},
	} {
		if errors.Is(err, item.err) {
			return item.status, item.code, item.err.Error()
		}
	}
	return http.StatusInternalServerError, "sql_change_failed", "SQL 工单操作失败，请稍后重试"
}

func (h sqlChangeHandler) list(c *gin.Context) {
	input := sqlchange.ListInput{Search: c.Query("search"), Status: c.Query("status"),
		ApplicationID: c.Query("application_id"), PipelineRunID: c.Query("pipeline_run_id"), Page: 1, PageSize: 10}
	for _, field := range []struct {
		key string
		out *int
	}{
		{"page", &input.Page}, {"page_size", &input.PageSize},
	} {
		if raw, exists := c.GetQuery(field.key); exists {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 || (field.key == "page_size" && value > 100) {
				h.fail(c, "sql_change_list_bind", sqlchange.ErrInvalid)
				return
			}
			*field.out = value
		}
	}
	result, err := h.service.List(c.Request.Context(), input)
	if err != nil {
		h.fail(c, "sql_change_list", err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h sqlChangeHandler) options(c *gin.Context) {
	actor, _ := currentUser(c)
	result, err := h.service.Options(c.Request.Context(), actor.ID)
	if err != nil {
		h.fail(c, "sql_change_options", err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h sqlChangeHandler) detail(c *gin.Context) {
	result, err := h.service.Detail(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.fail(c, "sql_change_detail", err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h sqlChangeHandler) bind(c *gin.Context, input any, operation string) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, sqlchange.MaxSQLBytes*6+(16<<10))
	if err := c.ShouldBindJSON(input); err != nil {
		h.logger.Warn("SQL 工单请求格式无效", "operation", operation, "resource_id", c.Param("id"), "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "sql_change_invalid", sqlchange.ErrInvalid.Error())
		return false
	}
	return true
}

func (h sqlChangeHandler) save(c *gin.Context) {
	var input sqlchange.Input
	if !h.bind(c, &input, "sql_change_save_bind") {
		return
	}
	actor, _ := currentUser(c)
	result, err := h.service.Save(c.Request.Context(), actor.ID, c.Param("id"), input)
	if err != nil {
		h.fail(c, "sql_change_save", err)
		return
	}
	setAuditResourceID(c, result.Change.ID)
	c.JSON(http.StatusOK, result)
}

func (h sqlChangeHandler) remove(c *gin.Context) {
	version, err := strconv.ParseUint(c.Query("version"), 10, 64)
	if err != nil || version == 0 {
		h.fail(c, "sql_change_delete_bind", sqlchange.ErrInvalid)
		return
	}
	actor, _ := currentUser(c)
	if err := h.service.Delete(c.Request.Context(), actor.ID, c.Param("id"), version); err != nil {
		h.fail(c, "sql_change_delete", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h sqlChangeHandler) action(c *gin.Context, action string) {
	var input sqlchange.ActionInput
	if !h.bind(c, &input, "sql_change_"+action+"_bind") {
		return
	}
	actor, _ := currentUser(c)
	var result *sqlchange.DetailResult
	var err error
	switch action {
	case "review":
		result, err = h.service.Review(c.Request.Context(), actor.ID, c.Param("id"), input)
	case "execute":
		result, err = h.service.Execute(c.Request.Context(), actor.ID, c.Param("id"), input)
	case "skip":
		result, err = h.service.Skip(c.Request.Context(), actor.ID, c.Param("id"), input)
	case "check":
		result, err = h.service.Recheck(c.Request.Context(), actor.ID, c.Param("id"), input)
	}
	if err != nil {
		h.fail(c, "sql_change_"+action, err)
		return
	}
	status := http.StatusOK
	if action == "execute" || action == "check" {
		status = http.StatusAccepted
	}
	c.JSON(status, result)
}

func (h sqlChangeHandler) review(c *gin.Context)  { h.action(c, "review") }
func (h sqlChangeHandler) execute(c *gin.Context) { h.action(c, "execute") }
func (h sqlChangeHandler) skip(c *gin.Context)    { h.action(c, "skip") }
func (h sqlChangeHandler) check(c *gin.Context)   { h.action(c, "check") }
