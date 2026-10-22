package httpapi

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"edo/internal/pipeline"
)

func (h pipelineHandler) stopRun(c *gin.Context) {
	user, _ := currentUser(c)
	run, err := h.service.StopRun(c.Request.Context(), c.Param("id"), user.ID)
	if err != nil {
		h.logger.Warn("流水线停止请求失败", "operation", "pipeline_stop_request", "request_id", requestIDFrom(c), "pipeline_run_id", c.Param("id"), "err", err)
		switch {
		case errors.Is(err, pipeline.ErrPipelineRunNotFound):
			writeError(c, http.StatusNotFound, "pipeline_run_not_found", pipeline.ErrPipelineRunNotFound.Error())
		case errors.Is(err, pipeline.ErrPipelineStopState):
			writeError(c, http.StatusConflict, "pipeline_stop_state", pipeline.ErrPipelineStopState.Error())
		default:
			writeError(c, http.StatusInternalServerError, "pipeline_stop_failed", pipeline.ErrPipelineStopFailed.Error())
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"pipeline_run": run})
}
