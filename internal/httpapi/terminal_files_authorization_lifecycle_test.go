package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"edo/internal/access"
	"edo/internal/auth"
	"edo/internal/model"
	"edo/internal/terminalfiles"
)

type joinedRequestContext struct {
	context.Context
	deadlineReads atomic.Int32
}

func (c *joinedRequestContext) Deadline() (time.Time, bool) {
	c.deadlineReads.Add(1)
	return c.Context.Deadline()
}

func TestStreamAuthorizationRetainsOriginalRequestContextAfterGinReuse(t *testing.T) {
	original := &joinedRequestContext{Context: context.Background()}
	reused := &joinedRequestContext{Context: context.Background()}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/files", nil).WithContext(original)
	c.Set(currentUserKey, &model.User{ID: "lifecycle-public-actor"})
	deps := Dependencies{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Sessions: &auth.SessionStore{}}
	authorize := streamAuthorization(deps, c, access.PermissionTerminalFileRead)
	c.Request = httptest.NewRequest(http.MethodGet, "/next-public-request", nil).WithContext(reused)
	if authorize() {
		t.Fatal("无 Cookie 的真实会话检查不应成功或访问存储")
	}
	if got := reused.deadlineReads.Load(); got != 0 {
		t.Errorf("授权闭包迟读了复用后的 Gin Request context: reads=%d", got)
	}
	if got := original.deadlineReads.Load(); got == 0 {
		t.Error("授权闭包未使用创建时的稳定 request context")
	}
}

type joinedDeadlineRecorder struct{ *httptest.ResponseRecorder }

func (joinedDeadlineRecorder) SetReadDeadline(time.Time) error  { return nil }
func (joinedDeadlineRecorder) SetWriteDeadline(time.Time) error { return nil }

type joinedListBackend struct {
	entered          <-chan struct{}
	checkDone        <-chan struct{}
	sequence         *atomic.Int32
	closed           atomic.Int32
	closedBeforeDone atomic.Bool
	closeOrder       atomic.Int32
}

func (b *joinedListBackend) List(ctx context.Context, path string) (terminalfiles.Directory, error) {
	select {
	case <-b.entered:
		return terminalfiles.Directory{Path: path, Entries: []terminalfiles.Entry{}}, nil
	case <-ctx.Done():
		return terminalfiles.Directory{}, ctx.Err()
	}
}
func (*joinedListBackend) Open(context.Context, string) (io.ReadCloser, terminalfiles.Entry, error) {
	return nil, terminalfiles.Entry{}, terminalfiles.ErrUnavailable
}
func (*joinedListBackend) Put(context.Context, string, io.Reader, int64, string) (terminalfiles.Entry, error) {
	return terminalfiles.Entry{}, terminalfiles.ErrUnavailable
}
func (b *joinedListBackend) Close() error {
	select {
	case <-b.checkDone:
	default:
		b.closedBeforeDone.Store(true)
	}
	b.closeOrder.Store(b.sequence.Add(1))
	b.closed.Add(1)
	return nil
}

func awaitJoinedLifecycle(t *testing.T, done <-chan struct{}, stage string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(7 * time.Second):
		t.Fatalf("取消感知的授权生命周期未完成: stage=%s", stage)
	}
}

func TestTerminalFileAuthorizationWatcherJoinsBeforeCleanupReturns(t *testing.T) {
	for _, openFailure := range []bool{false, true} {
		name := "正常结束"
		if openFailure {
			name = "打开失败"
		}
		t.Run(name, func(t *testing.T) {
			entered, checkDone, handlerDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var calls, sequence, finishedOrder, returnOrder atomic.Int32
			var canceled atomic.Bool
			backend := &joinedListBackend{entered: entered, checkDone: checkDone, sequence: &sequence}
			parent, cancel := context.WithCancel(context.Background())
			h := terminalFileHandler{
				key:    func(*gin.Context) string { return "host:joined-lifecycle-fixture" },
				logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
				checks: func(c *gin.Context, _ ...string) func() bool {
					operation := c.Request.Context()
					return func() bool {
						if calls.Add(1) == 1 {
							return true
						}
						defer close(checkDone)
						close(entered)
						// 真实取消立即唤醒检查，不使用忽略 ctx 的门来伪造 join 成功。
						<-operation.Done()
						canceled.Store(operation.Err() == context.Canceled)
						finishedOrder.Store(sequence.Add(1))
						return false
					}
				},
				open: func(ctx context.Context, _ *gin.Context) (terminalfiles.Backend, error) {
					if openFailure {
						select {
						case <-entered:
							return nil, terminalfiles.ErrUnavailable
						case <-ctx.Done():
							return nil, ctx.Err()
						}
					}
					return backend, nil
				},
			}
			router := gin.New()
			router.GET("/files", h.list)
			response := httptest.NewRecorder()
			t.Cleanup(func() {
				cancel()
				awaitJoinedLifecycle(t, handlerDone, "清理handler")
				if calls.Load() >= 2 {
					awaitJoinedLifecycle(t, checkDone, "清理checker")
				}
			})
			go func() {
				router.ServeHTTP(joinedDeadlineRecorder{response}, httptest.NewRequest(http.MethodGet, "/files?path=/", nil).WithContext(parent))
				returnOrder.Store(sequence.Add(1))
				close(handlerDone)
			}()
			awaitJoinedLifecycle(t, handlerDone, "handler正常返回")
			awaitJoinedLifecycle(t, checkDone, "checker因cleanup取消而结束")
			wantStatus, wantClosed := http.StatusOK, int32(1)
			if openFailure {
				wantStatus, wantClosed = http.StatusServiceUnavailable, 0
			}
			if response.Code != wantStatus || calls.Load() != 2 || !canceled.Load() || backend.closed.Load() != wantClosed {
				t.Fatalf("授权清理前提错误: status=%d checks=%d canceled=%v backendClose=%d", response.Code, calls.Load(), canceled.Load(), backend.closed.Load())
			}
			if backend.closedBeforeDone.Load() || finishedOrder.Load() >= returnOrder.Load() {
				t.Errorf("handler或backend先于在途check完成: check=%d backend=%d handler=%d", finishedOrder.Load(), backend.closeOrder.Load(), returnOrder.Load())
			}
			if !openFailure && (finishedOrder.Load() >= backend.closeOrder.Load() || backend.closeOrder.Load() >= returnOrder.Load()) {
				t.Error("正常路径未遵守check结束→backend Close→handler返回顺序")
			}
			if got := len(terminalFileSlots); got != 0 {
				t.Errorf("授权清理后仍占用操作槽: slots=%d", got)
			}
		})
	}
}

func TestTerminalFileAuthorizationEarlyExitReleasesSlotWithoutWatcher(t *testing.T) {
	for _, unsupported := range []bool{false, true} {
		name := "初检拒绝"
		if unsupported {
			name = "不支持deadline"
		}
		t.Run(name, func(t *testing.T) {
			var checks, opens atomic.Int32
			h := terminalFileHandler{
				key:    func(*gin.Context) string { return "host:early-exit-fixture" },
				logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
				checks: func(*gin.Context, ...string) func() bool { return func() bool { checks.Add(1); return false } },
				open: func(context.Context, *gin.Context) (terminalfiles.Backend, error) {
					opens.Add(1)
					return nil, terminalfiles.ErrUnavailable
				},
			}
			router := gin.New()
			router.GET("/files", h.list)
			response := httptest.NewRecorder()
			var writer http.ResponseWriter = joinedDeadlineRecorder{response}
			wantStatus, wantChecks := http.StatusForbidden, int32(1)
			if unsupported {
				writer = response
				wantStatus, wantChecks = http.StatusServiceUnavailable, 0
			}
			router.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/files?path=/", nil))
			if response.Code != wantStatus || checks.Load() != wantChecks || opens.Load() != 0 || len(terminalFileSlots) != 0 {
				t.Fatalf("无watcher早退收口错误: status=%d checks=%d opens=%d slots=%d", response.Code, checks.Load(), opens.Load(), len(terminalFileSlots))
			}
		})
	}
}
