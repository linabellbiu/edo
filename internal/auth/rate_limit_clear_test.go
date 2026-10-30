package auth

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"edo/internal/cache"
	"edo/internal/config"
)

func TestLoginRateLimiterClearAllKeepsOtherNamespace(t *testing.T) {
	for _, tc := range []struct{ name, prefix, peer string }{
		{"普通前缀", "edo:c07:", "edo:c07peer:"},
		{"星号", "edo:c07*:", "edo:c07peer:"},
		{"问号", "edo:c07?:", "edo:c07x:"},
		{"方括号", "edo:c07[ab]:", "edo:c07a:"},
		{"反斜杠", `edo:c07\literal:`, "edo:c07literal:"},
		{"混合字符", `edo:c07[*]?\literal:`, "edo:c07*x:literal:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := miniredis.RunT(t)
			ctx := t.Context()
			owned := testClearLoginLimiter(t, server, tc.prefix)
			peer := testClearLoginLimiter(t, server, tc.peer)
			for _, limiter := range []*LoginRateLimiter{owned, peer} {
				if err := limiter.RecordFailure(ctx, "synthetic-user", "127.0.0.1"); err != nil {
					t.Fatal(err)
				}
			}
			namespace := owned.redis.Key("login", "failure") + ":"
			protected := []string{
				owned.redis.Key("session", "synthetic-session"),
				namespace + "pending",
				namespace + strings.Repeat("a", 63),
				namespace + strings.Repeat("a", 65),
				namespace + strings.Repeat("A", 64),
				namespace + strings.Repeat("g", 64),
				namespace + "nested:" + strings.Repeat("a", 64),
				namespace + strings.Repeat("a", 64) + ":extra",
			}
			for _, key := range protected {
				if err := owned.redis.Client().Set(ctx, key, "保留测试值", 2*time.Hour).Err(); err != nil {
					t.Fatal(err)
				}
			}
			if err := owned.ClearAll(ctx); err != nil {
				t.Fatal(err)
			}
			if server.Exists(owned.key("synthetic-user", "127.0.0.1")) {
				t.Error("本命名空间的登录失败计数未清理")
			}
			if !server.Exists(peer.key("synthetic-user", "127.0.0.1")) {
				t.Error("清理本命名空间时删除了另一命名空间的登录计数")
			}
			for _, key := range protected {
				if value, err := server.Get(key); err != nil || value != "保留测试值" || server.TTL(key) != 2*time.Hour {
					t.Errorf("清理影响了非计数键: key=%q value=%q err=%v", key, value, err)
				}
			}
		})
	}
}

func TestLoginRateLimiterClearAllScansEveryPageAndBoundsDeletes(t *testing.T) {
	server := miniredis.RunT(t)
	limiter := testClearLoginLimiter(t, server, "edo:c07:")
	keys := seedClearLoginFailures(t, server, limiter, 237)
	foreign := "edo:peer:login:failure:" + strings.Repeat("a", 64)
	if err := server.Set(foreign, "保留其他实例计数"); err != nil {
		t.Fatal(err)
	}
	pages := []struct {
		cursor, next uint64
		keys         []string
	}{
		{0, 23, append(append([]string(nil), keys[:117]...), foreign)},
		{23, 41, nil},
		{41, 0, keys[117:]},
	}
	var scans int
	var batches []int
	limiter.redis.Client().AddHook(loginClearHook{process: func(ctx context.Context, cmd redis.Cmder, next redis.ProcessHook) error {
		switch cmd.Name() {
		case "scan":
			if scans >= len(pages) {
				t.Fatal("游标完成后不得继续扫描")
			}
			page := pages[scans]
			want := []any{"scan", page.cursor, "match", "edo:c07:login:failure:*", "count", int64(100)}
			if !reflect.DeepEqual(cmd.Args(), want) {
				t.Fatalf("扫描命名空间、游标或批量错误: %v", cmd.Args())
			}
			cmd.(*redis.ScanCmd).SetVal(page.keys, page.next)
			scans++
			return nil
		case "del":
			batches = append(batches, len(cmd.Args())-1)
		default:
			t.Fatalf("清理不得使用额外命令: %s", cmd.Name())
		}
		return next(ctx, cmd)
	}})
	// miniredis 忽略 COUNT；仅替换 SCAN 回执，真实 DEL 验证大页、空页和准确键二次检查。
	if err := limiter.ClearAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if scans != 3 || !reflect.DeepEqual(batches, []int{100, 100, 37}) {
		t.Errorf("扫描遗漏页面或删除超出批次: scans=%d batches=%v", scans, batches)
	}
	for _, key := range keys {
		if server.Exists(key) {
			t.Errorf("本命名空间计数未清理: %s", key)
		}
	}
	if !server.Exists(foreign) {
		t.Error("删除了枚举结果中不属于当前命名空间的计数")
	}
}

func TestLoginRateLimiterClearAllReturnsStorageAndCancellationErrors(t *testing.T) {
	storageErr := errors.New("隔离测试存储故障")
	for _, tc := range []struct {
		name, prefix string
		count        int
		want         error
	}{
		{"扫描失败", "扫描登录失败计数失败:", 3, storageErr},
		{"后续页失败", "扫描登录失败计数失败:", 3, storageErr},
		{"整批删除失败", "清理登录失败计数失败:", 101, storageErr},
		{"尾批删除失败", "清理登录失败计数失败:", 3, storageErr},
		{"扫描前取消", "扫描登录失败计数失败:", 3, context.Canceled},
		{"扫描后取消", "扫描登录失败计数失败:", 3, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := miniredis.RunT(t)
			limiter := testClearLoginLimiter(t, server, "edo:c07:")
			keys := seedClearLoginFailures(t, server, limiter, tc.count)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.name == "扫描前取消" {
				cancel()
			}
			var scans, deletes int
			limiter.redis.Client().AddHook(loginClearHook{process: func(ctx context.Context, cmd redis.Cmder, next redis.ProcessHook) error {
				if err := ctx.Err(); err != nil {
					cmd.SetErr(err)
					return err
				}
				if cmd.Name() == "scan" {
					scans++
					if tc.name == "扫描失败" || tc.name == "后续页失败" && scans > 1 {
						cmd.SetErr(storageErr)
						return storageErr
					}
					var cursor uint64
					if tc.name == "后续页失败" || tc.name == "扫描后取消" {
						cursor = 1
					}
					cmd.(*redis.ScanCmd).SetVal(keys, cursor)
					if tc.name == "扫描后取消" {
						cancel()
					}
					return nil
				}
				if cmd.Name() == "del" {
					deletes++
					cmd.SetErr(storageErr)
					return storageErr
				}
				return next(ctx, cmd)
			}})
			if err := limiter.ClearAll(ctx); !errors.Is(err, tc.want) || !strings.HasPrefix(err.Error(), tc.prefix) {
				t.Fatalf("错误未按原语义返回: %v", err)
			}
			wantDeletes := 0
			if strings.Contains(tc.name, "删除") {
				wantDeletes = 1
			}
			if deletes != wantDeletes {
				t.Errorf("错误后继续提交了删除: got=%d want=%d", deletes, wantDeletes)
			}
			for _, key := range keys {
				if !server.Exists(key) {
					t.Errorf("失败或取消后仍删除了计数: %s", key)
				}
			}
		})
	}
}

type loginClearHook struct {
	process func(context.Context, redis.Cmder, redis.ProcessHook) error
}

func (h loginClearHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h loginClearHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error { return h.process(ctx, cmd, next) }
}
func (h loginClearHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func seedClearLoginFailures(t *testing.T, server *miniredis.Miniredis, limiter *LoginRateLimiter, count int) []string {
	t.Helper()
	keys := make([]string, count)
	for i := range keys {
		keys[i] = limiter.key(fmt.Sprintf("synthetic-%d", i), "127.0.0.1")
		if err := server.Set(keys[i], "1"); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

func testClearLoginLimiter(t *testing.T, server *miniredis.Miniredis, prefix string) *LoginRateLimiter {
	t.Helper()
	redisClient, err := cache.Open(context.Background(), config.Redis{
		URL: "redis://" + server.Addr() + "/0", KeyPrefix: prefix, Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("连接隔离测试 Redis 失败: %v", err)
	}
	t.Cleanup(func() { _ = redisClient.Close() })
	return NewLoginRateLimiter(redisClient, 2, time.Minute, staticLoginLockoutGate(true))
}
