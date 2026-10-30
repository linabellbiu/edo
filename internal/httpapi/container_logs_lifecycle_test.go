package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"

	"edo/internal/audit"
	"edo/internal/config"
	"edo/internal/database"
	"edo/internal/dockerengine"
	"edo/internal/model"
)

type silentLogCloseSource struct {
	reader     *io.PipeReader
	closed     chan struct{}
	readDone   chan struct{}
	readOnce   sync.Once
	closeOnce  sync.Once
	closeCount atomic.Int32
}

func (s *silentLogCloseSource) Read(p []byte) (int, error) {
	n, err := s.reader.Read(p)
	if err != nil {
		s.readOnce.Do(func() { close(s.readDone) })
	}
	return n, err
}

func (s *silentLogCloseSource) Close() error {
	s.closeCount.Add(1)
	var result error
	s.closeOnce.Do(func() { result = s.reader.Close(); close(s.closed) })
	return result
}

type silentLogCloseService struct {
	source *silentLogCloseSource
	opened chan context.Context
}

func (s *silentLogCloseService) ContainerLogs(ctx context.Context, _, _ string, _ dockerengine.ContainerLogOptions) (*dockerengine.ContainerLogStream, error) {
	s.opened <- ctx
	return &dockerengine.ContainerLogStream{ReadCloser: s.source}, nil
}

// 静默 Follow 没有后续输出，关闭必须主动释放连接和读取协程，不能等待下一条日志。
func TestContainerLogSilentFollowClientCloseProtocol(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, audited := range []bool{false, true} {
		t.Run(fmt.Sprintf("sqlite_audit_%t", audited), func(t *testing.T) {
			var audits *audit.Service
			var db *gorm.DB
			actor := &model.User{ID: uuid.NewString(), DepartmentID: "00000000-0000-0000-0000-000000000001"}
			scopeValues := database.DepartmentScope{UserID: actor.ID, DepartmentID: actor.DepartmentID, ProjectID: uuid.NewString()}
			scope := database.WithDepartmentScope(context.Background(), scopeValues)
			if audited {
				var err error
				db, err = database.Open(context.Background(), config.Database{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "audit.db"), MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Hour}, logger)
				if err != nil {
					t.Fatalf("无法打开独立审计测试库: %v", err)
				}
				t.Cleanup(func() { _ = database.Close(db) })
				if err := db.AutoMigrate(&model.AuditLog{}); err != nil {
					t.Fatalf("无法迁移独立审计测试表: %v", err)
				}
				audits = audit.NewService(db)
			}
			for round := 0; round < 20; round++ {
				t.Run(fmt.Sprintf("round_%02d", round), func(t *testing.T) {
					reader, input := io.Pipe()
					source := &silentLogCloseSource{reader: reader, closed: make(chan struct{}), readDone: make(chan struct{})}
					service := &silentLogCloseService{source: source, opened: make(chan context.Context, 1)}
					handler := containerLogHandler{docker: service, audits: audits, logger: logger}
					done := make(chan struct{})
					containerID := fmt.Sprintf("public-fixture-%02d", round)
					router := gin.New()
					router.GET("/docker/endpoints/:id/containers/:container_id/logs/ws", func(c *gin.Context) {
						defer close(done)
						c.Request = c.Request.WithContext(database.WithDepartmentScope(c.Request.Context(), scopeValues))
						c.Set(currentUserKey, actor)
						handler.stream(c)
					})
					server := httptest.NewServer(router)
					var connection *websocket.Conn
					t.Cleanup(func() {
						if connection != nil {
							_ = connection.Close()
						}
						_ = reader.Close()
						_ = input.Close()
						server.CloseClientConnections()
						server.Close()
					})
					dialer := websocket.Dialer{Subprotocols: []string{containerLogSubprotocol}, HandshakeTimeout: time.Second}
					var err error
					connection, _, err = dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/docker/endpoints/fixture/containers/"+containerID+"/logs/ws?follow=true", nil)
					if err != nil {
						t.Fatal("独立协议握手未成功")
					}
					_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
					messageType, body, err := connection.ReadMessage()
					if err != nil || messageType != websocket.TextMessage || string(body) != `{"type":"ready"}` {
						t.Fatal("未取得协议 ready 事件")
					}
					streamContext := <-service.opened
					started := time.Now()
					deadline := started.Add(2 * time.Second)
					if err := connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "测试正常关闭"), deadline); err != nil {
						t.Fatal("无法发送正常关闭控制帧")
					}
					_ = connection.SetReadDeadline(deadline)
					if _, _, err = connection.ReadMessage(); !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
						t.Fatal("未在预算内收到正常关闭应答")
					}
					_ = connection.UnderlyingConn().SetReadDeadline(deadline)
					var single [1]byte
					if n, err := connection.UnderlyingConn().Read(single[:]); n != 0 || !errors.Is(err, io.EOF) {
						t.Fatal("关闭应答后未在预算内收到服务端 TCP EOF")
					}
					for name, ch := range map[string]<-chan struct{}{"日志源关闭": source.closed, "日志源读取退出": source.readDone, "会话取消": streamContext.Done(), "处理器结束": done} {
						remaining := time.Until(deadline)
						if remaining <= 0 {
							t.Fatal("整体关闭预算已用尽")
						}
						select {
						case <-ch:
						case <-time.After(remaining):
							t.Fatalf("%s未在整体预算内完成", name)
						}
					}
					if source.closeCount.Load() != 1 {
						t.Fatal("日志源没有精确关闭一次")
					}
					if audited {
						var count int64
						if err := db.WithContext(scope).Model(&model.AuditLog{}).Where("resource_id = ?", containerID).Count(&count).Error; err != nil || count != 2 {
							t.Fatal("正常打开和关闭审计未完整保留")
						}
					}
					t.Logf("正常 Close/ACK/TCP EOF、日志源/读取/会话/处理器均完成，耗时=%s", time.Since(started))
				})
			}
		})
	}
}
