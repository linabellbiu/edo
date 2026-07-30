package httpapi

import (
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	artifactmanager "zrt/internal/artifact"
)

const multipartArtifactOverhead = int64(1024 * 1024)

type artifactHandler struct {
	service *artifactmanager.Service
	logger  *slog.Logger
}

func (h artifactHandler) list(c *gin.Context) {
	if !h.available(c) {
		return
	}
	items, err := h.service.ListByApplication(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.writeError(c, "artifact_list", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"artifacts": items})
}

func (h artifactHandler) upload(c *gin.Context) {
	if !h.available(c) {
		return
	}
	// MultipartReader 直接把文件流交给内容寻址存储，不调用 ParseMultipartForm，
	// 避免框架先把大文件完整写入另一份临时文件。
	limit := h.service.MaxBytes() + multipartArtifactOverhead
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
	reader, err := c.Request.MultipartReader()
	if err != nil {
		h.log().Warn("上传制品请求格式无效", "operation", "artifact_upload_multipart", "request_id", requestIDFrom(c), "application_id", c.Param("id"), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_artifact_upload", "请选择需要上传的制品文件")
		return
	}
	for {
		part, nextErr := reader.NextPart()
		if nextErr != nil {
			if errors.Is(nextErr, io.EOF) {
				break
			}
			h.writeError(c, "artifact_upload_read", nextErr)
			return
		}
		if part.FormName() != "file" || strings.TrimSpace(part.FileName()) == "" {
			_ = part.Close()
			continue
		}
		actor, _ := currentUser(c)
		item, uploadErr := h.service.Upload(
			c.Request.Context(), c.Param("id"), actor.ID, part.FileName(), part.Header.Get("Content-Type"), part,
		)
		_ = part.Close()
		if uploadErr != nil {
			h.writeError(c, "artifact_upload", uploadErr)
			return
		}
		setAuditResourceID(c, item.ID)
		c.JSON(http.StatusCreated, gin.H{"artifact": item})
		return
	}
	h.log().Warn("上传制品请求缺少文件", "operation", "artifact_upload_file_required", "request_id", requestIDFrom(c), "application_id", c.Param("id"))
	writeError(c, http.StatusBadRequest, "artifact_file_required", "请选择需要上传的制品文件")
}

func (h artifactHandler) get(c *gin.Context) {
	if !h.available(c) {
		return
	}
	item, err := h.service.Find(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.writeError(c, "artifact_get", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"artifact": item})
}

func (h artifactHandler) download(c *gin.Context) {
	if !h.available(c) {
		return
	}
	item, file, err := h.service.OpenDownload(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.writeError(c, "artifact_download", err)
		return
	}
	defer file.Close()
	contentType := item.MediaType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": item.Name})
	if disposition == "" {
		disposition = "attachment"
	}
	c.DataFromReader(http.StatusOK, item.SizeBytes, contentType, file, map[string]string{
		"Content-Disposition": disposition,
		"Digest":              item.Digest,
	})
}

func (h artifactHandler) available(c *gin.Context) bool {
	if h.service != nil {
		return true
	}
	h.log().Error("制品服务未初始化", "operation", "artifact_service_unavailable", "request_id", requestIDFrom(c))
	writeError(c, http.StatusServiceUnavailable, "artifact_service_unavailable", "制品服务暂时不可用")
	return false
}

func (h artifactHandler) writeError(c *gin.Context, operation string, err error) {
	h.log().Error("制品操作失败", "operation", operation, "request_id", requestIDFrom(c), "resource_id", c.Param("id"), "err", err)
	var maxBytesError *http.MaxBytesError
	switch {
	case errors.Is(err, artifactmanager.ErrApplicationNotFound):
		writeError(c, http.StatusNotFound, "application_not_found", artifactmanager.ErrApplicationNotFound.Error())
	case errors.Is(err, artifactmanager.ErrArtifactNotFound):
		writeError(c, http.StatusNotFound, "artifact_not_found", artifactmanager.ErrArtifactNotFound.Error())
	case errors.Is(err, artifactmanager.ErrTooLarge), errors.As(err, &maxBytesError):
		writeError(c, http.StatusRequestEntityTooLarge, "artifact_too_large", "制品文件超过允许的大小")
	case errors.Is(err, artifactmanager.ErrInvalidArtifact):
		writeError(c, http.StatusBadRequest, "invalid_artifact", artifactmanager.ErrInvalidArtifact.Error())
	case errors.Is(err, artifactmanager.ErrArtifactUnavailable):
		writeError(c, http.StatusConflict, "artifact_unavailable", artifactmanager.ErrArtifactUnavailable.Error())
	case errors.Is(err, artifactmanager.ErrArtifactConflict):
		writeError(c, http.StatusConflict, "artifact_conflict", artifactmanager.ErrArtifactConflict.Error())
	default:
		writeInternalError(c)
	}
}

func (h artifactHandler) log() *slog.Logger {
	if h.logger != nil {
		return h.logger
	}
	return slog.Default()
}
