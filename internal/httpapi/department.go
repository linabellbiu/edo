package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"edo/internal/department"
)

type departmentHandler struct {
	service *department.Service
	logger  *slog.Logger
}

type departmentRequest struct {
	Name        string `json:"name" binding:"required,max=64"`
	Description string `json:"description" binding:"max=255"`
}

func (h departmentHandler) list(c *gin.Context) {
	items, err := h.service.List(c.Request.Context())
	if err != nil {
		h.logger.Error("查询部门列表失败", "operation", "department_list", "request_id", requestIDFrom(c), "err", err)
		writeInternalError(c)
		return
	}
	c.JSON(http.StatusOK, gin.H{"departments": items})
}

func (h departmentHandler) create(c *gin.Context) {
	var request departmentRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("创建部门请求参数无效", "operation", "department_create_bind", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_department", department.ErrInvalidDepartment.Error())
		return
	}
	item, err := h.service.Create(c.Request.Context(), department.Input{Name: request.Name, Description: request.Description})
	if err != nil {
		h.writeError(c, "department_create", err)
		return
	}
	setAuditResourceID(c, item.ID)
	c.JSON(http.StatusCreated, gin.H{"department": item})
}

func (h departmentHandler) update(c *gin.Context) {
	var request departmentRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.logger.Warn("更新部门请求参数无效", "operation", "department_update_bind", "request_id", requestIDFrom(c), "department_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_department", department.ErrInvalidDepartment.Error())
		return
	}
	item, err := h.service.Update(c.Request.Context(), c.Param("id"), department.Input{Name: request.Name, Description: request.Description})
	if err != nil {
		h.writeError(c, "department_update", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"department": item})
}

func (h departmentHandler) delete(c *gin.Context) {
	if err := h.service.Delete(c.Request.Context(), c.Param("id")); err != nil {
		h.writeError(c, "department_delete", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h departmentHandler) writeError(c *gin.Context, operation string, err error) {
	h.logger.Warn("部门操作失败", "operation", operation, "request_id", requestIDFrom(c), "department_id", c.Param("id"), "err", err)
	switch {
	case errors.Is(err, department.ErrInvalidDepartment):
		writeError(c, http.StatusBadRequest, "invalid_department", department.ErrInvalidDepartment.Error())
	case errors.Is(err, department.ErrDepartmentNameExist):
		writeError(c, http.StatusConflict, "department_name_exists", department.ErrDepartmentNameExist.Error())
	case errors.Is(err, department.ErrDepartmentNotFound):
		writeError(c, http.StatusNotFound, "department_not_found", department.ErrDepartmentNotFound.Error())
	case errors.Is(err, department.ErrDepartmentInUse), errors.Is(err, department.ErrDefaultDepartment):
		writeError(c, http.StatusConflict, "department_in_use", err.Error())
	default:
		writeInternalError(c)
	}
}
