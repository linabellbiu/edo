package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"edo/internal/access"
	"edo/internal/audit"
	"edo/internal/dockerengine"
	hostmanager "edo/internal/host"
	"edo/internal/kube"
	"edo/internal/model"
	"edo/internal/terminalfiles"
)

type terminalFileHandler struct {
	open   func(context.Context, *gin.Context) (terminalfiles.Backend, error)
	key    func(*gin.Context) string
	checks func(*gin.Context, ...string) func() bool
	logger *slog.Logger
	audits *audit.Service
}

var terminalFileSlots = make(chan struct{}, 8)

type terminalFileResponse struct {
	terminalfiles.Entry
	ETag    string `json:"etag"`
	Content string `json:"content,omitempty"`
}

func registerTerminalFiles(protected *gin.RouterGroup, deps Dependencies) {
	checks := func(c *gin.Context, permissions ...string) func() bool {
		values := make([]func() bool, 0, len(permissions))
		for _, permission := range permissions {
			values = append(values, streamAuthorization(deps, c, permission))
		}
		return func() bool {
			for _, check := range values {
				if !check() {
					return false
				}
			}
			return true
		}
	}
	for _, target := range []struct {
		prefix string
		read   []string
		key    func(*gin.Context) string
		open   func(context.Context, *gin.Context) (terminalfiles.Backend, error)
	}{
		{"/terminals/hosts/:host_id/files", []string{access.PermissionClusterRead, access.PermissionDeploymentRead}, func(c *gin.Context) string { return "host:" + c.Param("host_id") }, func(ctx context.Context, c *gin.Context) (terminalfiles.Backend, error) {
			if deps.Hosts == nil {
				return nil, terminalfiles.ErrUnavailable
			}
			return deps.Hosts.OpenFiles(ctx, c.Param("host_id"))
		}},
		{"/terminals/docker/:endpoint_id/containers/:container_id/files", []string{access.PermissionClusterRead}, func(c *gin.Context) string { return "docker:" + c.Param("endpoint_id") + ":" + c.Param("container_id") }, func(ctx context.Context, c *gin.Context) (terminalfiles.Backend, error) {
			if deps.Terminal == nil {
				return nil, terminalfiles.ErrUnavailable
			}
			return deps.Terminal.OpenDockerFiles(ctx, c.Param("endpoint_id"), c.Param("container_id"))
		}},
		{"/terminals/kubernetes/:cluster_id/namespaces/:namespace/pods/:pod/containers/:container/files", []string{"kubernetes.workload.read"}, func(c *gin.Context) string {
			return "pod:" + c.Param("cluster_id") + ":" + c.Param("namespace") + ":" + c.Param("pod") + ":" + c.Param("container")
		}, func(ctx context.Context, c *gin.Context) (terminalfiles.Backend, error) {
			if deps.Terminal == nil {
				return nil, terminalfiles.ErrUnavailable
			}
			return deps.Terminal.OpenKubernetesFiles(ctx, c.Param("cluster_id"), c.Param("namespace"), c.Param("pod"), c.Param("container"), c.Query("pod_uid"))
		}},
	} {
		h := terminalFileHandler{open: target.open, key: target.key, logger: deps.Logger, audits: deps.Audits}
		h.checks = func(c *gin.Context, permissions ...string) func() bool {
			filesAllowed := checks(c, permissions...)
			resourceAllowed := streamAuthorization(deps, c, target.read...)
			return func() bool { return filesAllowed() && resourceAllowed() }
		}
		resourceRead := requireAnyPermission(deps.Access, deps.Logger, target.read...)
		read := requirePermission(deps.Access, deps.Logger, access.PermissionTerminalFileRead)
		protected.GET(target.prefix, h.audit("terminal.file.list"), resourceRead, read, h.list)
		protected.GET(target.prefix+"/stat", h.audit("terminal.file.stat"), resourceRead, read, h.stat)
		protected.GET(target.prefix+"/content", h.audit("terminal.file.read"), resourceRead, read, h.content)
		protected.GET(target.prefix+"/download", h.audit("terminal.file.download"), resourceRead, read, h.download)
		protected.POST(target.prefix+"/upload", h.audit("terminal.file.upload"), resourceRead, requirePermission(deps.Access, deps.Logger, access.PermissionTerminalFileUpload), h.upload)
		protected.PUT(target.prefix+"/content", h.audit("terminal.file.edit"), resourceRead, read, requirePermission(deps.Access, deps.Logger, access.PermissionTerminalFileEdit), h.edit)
	}
}

func (h terminalFileHandler) prepare(c *gin.Context, permissions ...string) (terminalfiles.Backend, func(), bool) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	body := c.Request.Body
	c.Request = c.Request.WithContext(ctx)
	select {
	case terminalFileSlots <- struct{}{}:
	default:
		cancel()
		h.fail(c, terminalfiles.ErrUnavailable)
		return nil, func() {}, false
	}
	cleanup := func() { cancel(); <-terminalFileSlots }
	if h.checks != nil {
		check := h.checks(c, permissions...)
		if !check() {
			cleanup()
			h.fail(c, fs.ErrPermission)
			return nil, func() {}, false
		}
		ctx = terminalfiles.WithCommitAuthorization(ctx, func(commitContext context.Context) error {
			if err := commitContext.Err(); err != nil {
				return err
			}
			if !check() {
				return fs.ErrPermission
			}
			return nil
		})
		c.Request = c.Request.WithContext(ctx)
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if !check() {
						cancel()
						return
					}
				}
			}
		}()
	}
	// 取消上传时关闭请求流，避免授权撤销后仍阻塞读取上传内容。
	stop := context.AfterFunc(ctx, func() {
		if body != nil {
			_ = body.Close()
		}
	})
	backend, err := h.open(ctx, c)
	if err != nil {
		stop()
		cleanup()
		h.fail(c, err)
		return nil, func() {}, false
	}
	return backend, func() {
		stop()
		cancel()
		if err := backend.Close(); err != nil {
			h.logger.Warn("关闭终端文件资源失败", "operation", "terminal_file_close", "target", h.key(c), "err", err)
		}
		<-terminalFileSlots
	}, true
}

func (h terminalFileHandler) filePath(c *gin.Context, raw string) (string, bool) {
	value, err := terminalfiles.CleanPath(raw)
	if err != nil {
		h.fail(c, err)
		return "", false
	}
	c.Set("terminal_file_path", value)
	return value, true
}

func (h terminalFileHandler) list(c *gin.Context) {
	path, ok := h.filePath(c, c.DefaultQuery("path", "/"))
	if !ok {
		return
	}
	backend, done, ok := h.prepare(c, access.PermissionTerminalFileRead)
	if !ok {
		return
	}
	defer done()
	value, err := backend.List(c.Request.Context(), path)
	if err != nil {
		h.fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"directory": value})
}

func (h terminalFileHandler) read(c *gin.Context, text bool) (terminalFileResponse, bool) {
	path, ok := h.filePath(c, c.Query("path"))
	if !ok {
		return terminalFileResponse{}, false
	}
	backend, done, ok := h.prepare(c, access.PermissionTerminalFileRead)
	if !ok {
		return terminalFileResponse{}, false
	}
	defer done()
	reader, entry, err := backend.Open(c.Request.Context(), path)
	if err != nil {
		h.fail(c, err)
		return terminalFileResponse{}, false
	}
	defer reader.Close()
	limit := terminalfiles.MaxTransferBytes
	if text {
		limit = terminalfiles.MaxTextBytes
	}
	if entry.Size < 0 || entry.Size > limit {
		h.fail(c, terminalfiles.ErrTooLarge)
		return terminalFileResponse{}, false
	}
	hash := sha256.New()
	var content bytes.Buffer
	var destination io.Writer = hash
	if text {
		destination = io.MultiWriter(hash, &content)
	}
	n, err := io.Copy(destination, io.LimitReader(reader, limit+1))
	if err != nil {
		h.fail(c, err)
		return terminalFileResponse{}, false
	}
	if n > limit {
		h.fail(c, terminalfiles.ErrTooLarge)
		return terminalFileResponse{}, false
	}
	if n != entry.Size {
		h.fail(c, terminalfiles.ErrConflict)
		return terminalFileResponse{}, false
	}
	if text && (!utf8.Valid(content.Bytes()) || bytes.IndexByte(content.Bytes(), 0) >= 0) {
		h.fail(c, terminalfiles.ErrNotRegular)
		return terminalFileResponse{}, false
	}
	return terminalFileResponse{Entry: entry, ETag: hex.EncodeToString(hash.Sum(nil)), Content: content.String()}, true
}

func (h terminalFileHandler) stat(c *gin.Context) {
	if file, ok := h.read(c, false); ok {
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"file": file})
	}
}
func (h terminalFileHandler) content(c *gin.Context) {
	if file, ok := h.read(c, true); ok {
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{"file": file})
	}
}

func (h terminalFileHandler) download(c *gin.Context) {
	path, ok := h.filePath(c, c.Query("path"))
	if !ok {
		return
	}
	backend, done, ok := h.prepare(c, access.PermissionTerminalFileRead)
	if !ok {
		return
	}
	defer done()
	reader, entry, err := backend.Open(c.Request.Context(), path)
	if err != nil {
		h.fail(c, err)
		return
	}
	defer reader.Close()
	if entry.Size < 0 || entry.Size > terminalfiles.MaxTransferBytes {
		h.fail(c, terminalfiles.ErrTooLarge)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": entry.Name}))
	// 直接流式传输，不把整个文件读入 EDO 内存。
	n, err := io.CopyBuffer(c.Writer, io.LimitReader(reader, entry.Size), make([]byte, 32*1024))
	if err != nil || n != entry.Size {
		c.Set("terminal_file_failed", true)
		h.logger.Warn("终端文件下载未完成", "operation", "terminal_file_download", "target", h.key(c), "err", err)
	}
}

func terminalFileETag(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	raw = strings.Trim(raw, "\"")
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != sha256.Size {
		return "", terminalfiles.ErrInvalidPath
	}
	return strings.ToLower(raw), nil
}

func (h terminalFileHandler) upload(c *gin.Context) {
	path, ok := h.filePath(c, c.Query("path"))
	if !ok {
		return
	}
	expected, err := terminalFileETag(c.GetHeader("If-Match"))
	if err != nil {
		h.fail(c, err)
		return
	}
	permissions := []string{access.PermissionTerminalFileUpload}
	if expected != "" {
		permissions = append(permissions, access.PermissionTerminalFileRead, access.PermissionTerminalFileEdit)
	}
	backend, done, ok := h.prepare(c, permissions...)
	if !ok {
		return
	}
	defer done()
	spool, err := os.CreateTemp("", "edo-terminal-upload-*")
	if err != nil {
		h.fail(c, err)
		return
	}
	defer os.Remove(spool.Name())
	defer spool.Close()
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, terminalfiles.MaxTransferBytes)
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(spool, hash), c.Request.Body)
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			err = terminalfiles.ErrTooLarge
		}
		h.fail(c, err)
		return
	}
	if _, err = spool.Seek(0, io.SeekStart); err != nil {
		h.fail(c, err)
		return
	}
	if h.checks != nil && !h.checks(c, permissions...)() {
		h.fail(c, fs.ErrPermission)
		return
	}
	entry, err := backend.Put(c.Request.Context(), path, spool, size, expected)
	if err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"file": terminalFileResponse{Entry: entry, ETag: hex.EncodeToString(hash.Sum(nil))}})
}

func (h terminalFileHandler) edit(c *gin.Context) {
	var input struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		ETag    string `json:"etag"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, terminalfiles.MaxTextBytes*6+8192)
	if err := c.ShouldBindJSON(&input); err != nil {
		h.fail(c, terminalfiles.ErrInvalidPath)
		return
	}
	path, ok := h.filePath(c, input.Path)
	if !ok {
		return
	}
	expected, err := terminalFileETag(input.ETag)
	if err != nil || expected == "" {
		h.fail(c, terminalfiles.ErrConflict)
		return
	}
	if !utf8.ValidString(input.Content) || strings.ContainsRune(input.Content, 0) {
		h.fail(c, terminalfiles.ErrNotRegular)
		return
	}
	if int64(len(input.Content)) > terminalfiles.MaxTextBytes {
		h.fail(c, terminalfiles.ErrTooLarge)
		return
	}
	backend, done, ok := h.prepare(c, access.PermissionTerminalFileRead, access.PermissionTerminalFileEdit)
	if !ok {
		return
	}
	defer done()
	if h.checks != nil && !h.checks(c, access.PermissionTerminalFileRead, access.PermissionTerminalFileEdit)() {
		h.fail(c, fs.ErrPermission)
		return
	}
	entry, err := backend.Put(c.Request.Context(), path, strings.NewReader(input.Content), int64(len(input.Content)), expected)
	if err != nil {
		h.fail(c, err)
		return
	}
	hash := sha256.Sum256([]byte(input.Content))
	c.JSON(http.StatusOK, gin.H{"file": terminalFileResponse{Entry: entry, ETag: hex.EncodeToString(hash[:])}})
}

func (h terminalFileHandler) fail(c *gin.Context, err error) {
	h.logger.Warn("终端文件操作失败", "operation", "terminal_file_operation", "target", h.key(c), "request_id", requestIDFrom(c), "err", err)
	status, code, message := http.StatusBadGateway, "terminal_file_failed", "文件操作失败，请检查目标状态或权限"
	switch {
	case errors.Is(err, hostmanager.ErrHostNotFound), errors.Is(err, dockerengine.ErrEndpointNotFound), errors.Is(err, kube.ErrClusterNotFound):
		status, code, message = http.StatusNotFound, "file_target_not_found", "操作目标不存在或无访问权限"
	case errors.Is(err, hostmanager.ErrHostInteractiveUnavailable), errors.Is(err, hostmanager.ErrHostInteractiveBusy):
		status, code, message = http.StatusServiceUnavailable, "file_unavailable", "当前主机暂不支持文件操作或连接繁忙"
	case errors.Is(err, hostmanager.ErrHostInteractiveChanged):
		status, code, message = http.StatusConflict, "file_target_changed", "目标身份已变化，请重新打开终端"
	case errors.Is(err, terminalfiles.ErrInvalidPath):
		status, code, message = http.StatusBadRequest, "invalid_file_path", terminalfiles.ErrInvalidPath.Error()
	case errors.Is(err, terminalfiles.ErrNotRegular):
		status, code, message = http.StatusBadRequest, "invalid_file_type", "仅支持普通文本文件预览或普通文件传输"
	case errors.Is(err, terminalfiles.ErrTooLarge):
		status, code, message = http.StatusRequestEntityTooLarge, "file_too_large", terminalfiles.ErrTooLarge.Error()
	case errors.Is(err, terminalfiles.ErrConflict), errors.Is(err, terminalfiles.ErrExists):
		status, code, message = http.StatusConflict, "file_changed", "同名文件已存在或内容已变化，请重新读取并确认"
	case errors.Is(err, fs.ErrPermission), errors.Is(err, context.Canceled):
		status, code, message = http.StatusForbidden, "file_permission_denied", "没有文件操作权限，或操作授权已失效"
	case errors.Is(err, fs.ErrNotExist):
		status, code, message = http.StatusNotFound, "file_not_found", "文件或目录不存在"
	case errors.Is(err, context.DeadlineExceeded):
		status, code, message = http.StatusGatewayTimeout, "file_timeout", "文件操作超时，请稍后重试"
	case errors.Is(err, terminalfiles.ErrUnavailable):
		status, code, message = http.StatusServiceUnavailable, "file_unavailable", "当前目标暂不支持文件操作或服务繁忙"
	}
	writeError(c, status, code, message)
}

func (h terminalFileHandler) audit(action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		if h.audits == nil {
			return
		}
		actor, ok := currentUser(c)
		if !ok {
			return
		}
		result := model.AuditSucceeded
		if c.Writer.Status() == http.StatusForbidden {
			result = model.AuditDenied
		} else if c.Writer.Status() >= 400 || c.GetBool("terminal_file_failed") {
			result = model.AuditFailed
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 5*time.Second)
		defer cancel()
		err := h.audits.Record(ctx, audit.RecordInput{ActorUserID: actor.ID, Action: action, ResourceType: "terminal_file", ResourceID: h.key(c), Result: result, RequestID: requestIDFrom(c), ClientIP: c.ClientIP(), UserAgent: truncateText(c.Request.UserAgent(), 512), Metadata: map[string]any{"path": c.GetString("terminal_file_path"), "status": c.Writer.Status()}})
		if err != nil {
			h.logger.Error("记录终端文件审计失败", "operation", "terminal_file_audit", "target", h.key(c), "err", err)
		}
	}
}
