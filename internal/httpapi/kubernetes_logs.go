package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"edo/internal/audit"
	"edo/internal/kube"
	"edo/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

type kubernetesLogHandler struct {
	kube       *kube.Service
	audits     *audit.Service
	logger     *slog.Logger
	streamAuth func(*gin.Context) func() bool
}

func (h kubernetesLogHandler) stream(c *gin.Context) {
	options, err := parseContainerLogOptions(c)
	if err != nil || !containerLogProtocolRequested(c.Request) {
		h.logger.Warn("Pod 日志参数无效", "operation", "kubernetes_logs_validate", "request_id", requestIDFrom(c), "err", err)
		writeError(c, http.StatusBadRequest, "invalid_pod_logs", "日志参数无效")
		return
	}
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	stream, err := h.kube.PodLogs(ctx, c.Param("id"), c.Param("namespace"), c.Param("pod"), c.Param("container"), int64(options.Tail), options.Follow, options.Timestamps)
	if err != nil {
		clusterHandler{logger: h.logger}.resourceError(c, "kubernetes_logs_open", err)
		return
	}
	defer stream.Close()
	upgrader := websocket.Upgrader{HandshakeTimeout: 10 * time.Second, Subprotocols: []string{containerLogSubprotocol}, CheckOrigin: func(*http.Request) bool { return true }}
	connection, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.logger.Warn("Pod 日志握手失败", "operation", "kubernetes_logs_upgrade", "request_id", requestIDFrom(c), "err", err)
		return
	}
	defer connection.Close()
	writer := &containerLogWriter{connection: connection}
	if err := writer.event(containerLogEvent{Type: "ready"}); err != nil {
		return
	}
	if h.audits != nil {
		actor, _ := currentUser(c)
		if actor != nil {
			if err := h.audits.Record(ctx, audit.RecordInput{ActorUserID: actor.ID, Action: "kubernetes.pod.logs", ResourceType: "kubernetes_pod", ResourceID: c.Param("id") + "/" + c.Param("namespace") + "/" + c.Param("pod"), Result: model.AuditSucceeded, RequestID: requestIDFrom(c), ClientIP: c.ClientIP(), Metadata: map[string]any{"container": c.Param("container")}}); err != nil {
				h.logger.Error("记录 Pod 日志审计失败", "operation", "kubernetes_logs_audit", "err", err)
			}
		}
	}
	clientDone, streamDone := make(chan error, 1), make(chan error, 1)
	go readContainerLogClient(connection, clientDone)
	go func() { _, copyErr := io.Copy(writer, stream); streamDone <- copyErr }()
	ping := time.NewTicker(10 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-clientDone:
			return
		case err := <-streamDone:
			if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
				h.logger.Warn("Pod 日志连接中断", "operation", "kubernetes_logs_stream", "cluster_id", c.Param("id"), "pod", c.Param("pod"), "err", err)
				_ = writer.event(containerLogEvent{Type: "error", Message: "Pod 日志连接已中断"})
			} else {
				_ = writer.event(containerLogEvent{Type: "complete", Message: "日志流已结束"})
			}
			return
		case <-ping.C:
			if h.streamAuth != nil && !h.streamAuth(c)() {
				return
			}
			if err := writer.ping(); err != nil {
				return
			}
		}
	}
}
