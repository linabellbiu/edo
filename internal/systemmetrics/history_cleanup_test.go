package systemmetrics

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

type historyCleanupHook struct {
	mu            sync.Mutex
	scans         [][]any
	queries       [][]any
	scripts       []int
	scan          func(*redis.ScanCmd)
	failOnce      bool
	failTrim      bool
	failAfterOnce bool
	pipelines     int
}

func (h *historyCleanupHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) { return next(ctx, network, addr) }
}

func (h *historyCleanupHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		h.mu.Lock()
		if cmd.Name() == "zrangebyscore" {
			h.queries = append(h.queries, append([]any(nil), cmd.Args()...))
		}
		if cmd.Name() == "evalsha" || cmd.Name() == "eval" {
			h.scripts = append(h.scripts, cmd.Args()[2].(int)-1)
			if h.failTrim {
				h.failTrim = false
				h.mu.Unlock()
				return errors.New("隔离测试的原子清理请求失败")
			}
		}
		h.mu.Unlock()
		if cmd.Name() == "scan" {
			h.mu.Lock()
			h.scans = append(h.scans, append([]any(nil), cmd.Args()...))
			h.mu.Unlock()
			if h.scan != nil {
				h.scan(cmd.(*redis.ScanCmd))
				return nil
			}
		}
		return next(ctx, cmd)
	}
}

func (h *historyCleanupHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		h.mu.Lock()
		h.pipelines++
		fail := h.failOnce
		h.failOnce = false
		failAfter := h.failAfterOnce
		h.failAfterOnce = false
		h.mu.Unlock()
		if fail {
			return errors.New("隔离测试的清理请求失败")
		}
		if err := next(ctx, cmds); err != nil {
			return err
		}
		if failAfter {
			err := errors.New("隔离测试模拟EXEC已完成但回执丢失")
			for _, cmd := range cmds {
				cmd.SetErr(err)
			}
			return err
		}
		return nil
	}
}

func TestHistoryCleanupValidatesExactNamespaceAndSeries(t *testing.T) {
	namespace := `test:metrics[*]?\literal`
	node := strings.Repeat("a", 24)
	for _, suffix := range []string{"5s", "60s"} {
		retention, ok := historyKeyRetention(namespace, namespace+":"+node+":"+suffix)
		want := time.Hour
		if suffix == "60s" {
			want = HistoryRetention
		}
		if !ok || retention != want {
			t.Fatalf("准确序列未识别: %s %v %v", suffix, retention, ok)
		}
	}
	for _, key := range []string{namespace + ":" + node[:23] + ":5s", namespace + ":" + strings.ToUpper(node) + ":5s", namespace + ":" + strings.Repeat("g", 24) + ":5s", namespace + ":" + node + ":5s:extra", namespace + ":" + node + ":15s", namespace + ":nested:" + node + ":5s", "other:" + node + ":5s", namespace + "-other:" + node + ":5s"} {
		if _, ok := historyKeyRetention(namespace, key); ok {
			t.Fatalf("非准确监控键被接受: %q", key)
		}
	}
	if _, ok := historyKeyRetention("", ":"+node+":5s"); ok {
		t.Fatal("空命名空间不允许清理")
	}
}

func TestHistoryCleanupEscapesGlobAndKeepsRecentSamplesAndTTL(t *testing.T) {
	service, server := testHistoryService(t)
	client, ctx := service.history.client, context.Background()
	namespace := `test:metrics[*]?\literal`
	service.ConfigureHistory(client, namespace, ":18080")
	now := time.Now().UTC().Truncate(time.Minute)
	seed := func(key string, age time.Duration) {
		t.Helper()
		if err := client.ZAdd(ctx, key, redis.Z{Score: float64(now.Add(-age).UnixMilli()), Member: "old"}, redis.Z{Score: float64(now.Add(-time.Minute).UnixMilli()), Member: "recent"}).Err(); err != nil {
			t.Fatal(err)
		}
		if err := client.Expire(ctx, key, 7*time.Hour).Err(); err != nil {
			t.Fatal(err)
		}
	}
	node := strings.Repeat("b", 24)
	valid := []string{namespace + ":" + node + ":5s", namespace + ":" + node + ":60s"}
	for _, key := range valid {
		seed(key, 73*time.Hour)
	}
	foreign := []string{"test:metrics-other:" + node + ":60s", namespace + ":" + node + ":5s:extra", namespace + ":nested:" + node + ":60s", namespace + ":short:60s"}
	for _, key := range foreign {
		seed(key, 73*time.Hour)
	}
	textKey := namespace + ":" + strings.Repeat("c", 24) + ":60s"
	if err := client.Set(ctx, textKey, "不属于监控有序集合", time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	if err := service.history.trimInactive(ctx, now); err != nil {
		t.Fatal(err)
	}
	for _, key := range valid {
		if n := client.ZCard(ctx, key).Val(); n != 1 {
			t.Fatalf("没有精确保留有效采样: key=%q count=%d", key, n)
		}
		if ttl := server.TTL(key); ttl != 7*time.Hour {
			t.Fatalf("清理不得刷新旧键TTL: %q %v", key, ttl)
		}
	}
	for _, key := range foreign {
		if n := client.ZCard(ctx, key).Val(); n != 2 {
			t.Fatalf("清理触及其他用途键: %q count=%d", key, n)
		}
	}
	if value := client.Get(ctx, textKey).Val(); value != "不属于监控有序集合" {
		t.Fatal("同名字符串不能被修改")
	}
}

func TestHistoryCleanupIndexesEntireDiscoveryPageAndBoundsDueBatches(t *testing.T) {
	service, _ := testHistoryService(t)
	client, ctx := service.history.client, context.Background()
	namespace := "test:oversized"
	service.ConfigureHistory(client, namespace, ":18080")
	now := time.Now().UTC().Truncate(time.Minute)
	keys := make([]string, 35)
	for i := range keys {
		keys[i] = fmt.Sprintf("%s:%024x:60s", namespace, i)
		if err := client.ZAdd(ctx, keys[i], redis.Z{Score: float64(now.Add(-73 * time.Hour).UnixMilli()), Member: "old"}, redis.Z{Score: float64(now.Add(-time.Hour).UnixMilli()), Member: "recent"}).Err(); err != nil {
			t.Fatal(err)
		}
	}
	// miniredis 明确忽略 COUNT 返回整页；完整登记后，仅到期清理候选硬限16。
	hook := &historyCleanupHook{}
	service.history.cleanup.client.AddHook(hook)
	for range 3 {
		if err := service.history.trimInactive(ctx, now); err != nil {
			t.Fatal(err)
		}
	}
	if len(hook.scans) != 1 || len(hook.queries) != 3 {
		t.Fatalf("完整发现后不得继续SCAN: scans=%d queries=%d", len(hook.scans), len(hook.queries))
	}
	for _, args := range hook.scans {
		if !reflect.DeepEqual(args, []any{"scan", uint64(0), "match", namespace + ":*", "count", int64(16), "type", "zset"}) {
			t.Fatalf("扫描缺少命名空间、COUNT或类型限制: %v", args)
		}
	}
	for _, args := range hook.queries {
		if !reflect.DeepEqual(args, []any{"zrangebyscore", service.history.retentionIndex(), "-inf", fmt.Sprint(now.UnixMilli()), "limit", int64(0), int64(16)}) {
			t.Fatalf("候选查询必须有精确LIMIT: %v", args)
		}
	}
	for _, size := range hook.scripts {
		if size > historyCleanupBatchSize {
			t.Fatalf("原子清理超过候选上限: %d", size)
		}
	}
	for _, key := range keys {
		if n := client.ZCard(ctx, key).Val(); n != 1 {
			t.Fatalf("超额返回页的后半采样漏清或误删有效数据: %q %d", key, n)
		}
	}
	if !service.history.cleanup.discovered || service.history.cleanup.cursor != 0 {
		t.Fatal("本轮完整发现后应结束游标循环")
	}
	if n := client.ZCount(ctx, service.history.retentionIndex(), "-inf", fmt.Sprint(now.UnixMilli())).Val(); n != 0 {
		t.Fatalf("旧节点的过期采样不得因新节点写入而饿死: %d", n)
	}
}

func TestHistoryCleanupRemovesEmptySeriesFromExpiryIndex(t *testing.T) {
	service, _ := testHistoryService(t)
	client, ctx := service.history.client, context.Background()
	service.ConfigureHistory(client, "test:shrinking", ":18080")
	now := time.Now().UTC()
	for i := range 50 {
		if err := client.ZAdd(ctx, fmt.Sprintf("test:shrinking:%024x:60s", i), redis.Z{Score: float64(now.Add(-73 * time.Hour).UnixMilli()), Member: "old"}).Err(); err != nil {
			t.Fatal(err)
		}
	}
	hook := &historyCleanupHook{}
	service.history.cleanup.client.AddHook(hook)
	for range 4 {
		if err := service.history.trimInactive(ctx, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, size := range hook.scripts {
		if size > historyCleanupBatchSize {
			t.Fatalf("单轮超出清理上限: %d", size)
		}
	}
	for i := range 50 {
		if n := client.Exists(ctx, fmt.Sprintf("test:shrinking:%024x:60s", i)).Val(); n != 0 {
			t.Fatal("到期索引必须完整清理所有空序列，不能依赖变化的扫描页offset")
		}
	}
	if n := client.Exists(ctx, service.history.retentionIndex()).Val(); n != 0 {
		t.Fatal("所有序列为空后，索引成员也必须全部移除")
	}
	if len(hook.scans) != 1 {
		t.Fatal("发现完成后不再扫描Redis键空间")
	}
}

func TestHistoryCleanupKeepsCursorOnFailureAndAdvancesRealPages(t *testing.T) {
	service, _ := testHistoryService(t)
	client, ctx := service.history.client, context.Background()
	service.ConfigureHistory(client, "test:paging", ":18080")
	now := time.Now().UTC()
	keys := []string{"test:paging:" + strings.Repeat("d", 24) + ":60s", "test:paging:" + strings.Repeat("e", 24) + ":60s"}
	for _, key := range keys {
		if err := client.ZAdd(ctx, key, redis.Z{Score: float64(now.Add(-73 * time.Hour).UnixMilli()), Member: "old"}, redis.Z{Score: float64(now.UnixMilli()), Member: "recent"}).Err(); err != nil {
			t.Fatal(err)
		}
	}
	hook := &historyCleanupHook{failOnce: true}
	hook.scan = func(cmd *redis.ScanCmd) {
		cursor := cmd.Args()[1].(uint64)
		if cursor == 0 {
			cmd.SetVal(keys[:1], 41)
		} else if cursor == 41 {
			cmd.SetVal(keys[1:], 0)
		} else {
			t.Fatalf("错误的下一页游标: %d", cursor)
		}
	}
	service.history.cleanup.client.AddHook(hook)
	if err := service.history.trimInactive(ctx, now); err == nil || !strings.Contains(err.Error(), "登记已有监控历史失败") {
		t.Fatalf("发现登记失败必须返回内部诊断错误: %v", err)
	}
	if service.history.cleanup.cursor != 0 || service.history.cleanup.discovered {
		t.Fatal("失败不能跳过尚未确认的页")
	}
	for _, cursor := range []uint64{41, 0} {
		if err := service.history.trimInactive(ctx, now); err != nil {
			t.Fatal(err)
		}
		if service.history.cleanup.cursor != cursor {
			t.Fatalf("没有保存Redis返回的真实游标: got=%d want=%d", service.history.cleanup.cursor, cursor)
		}
	}
	for _, key := range keys {
		if n := client.ZCard(ctx, key).Val(); n != 1 {
			t.Fatalf("分页历史未正确清理: %q %d", key, n)
		}
	}
	if !service.history.cleanup.discovered || len(hook.scans) != 3 {
		t.Fatal("只有完整登记所有发现页后才能结束发现")
	}
	if err := service.history.trimInactive(ctx, now); err != nil || len(hook.scans) != 3 {
		t.Fatalf("完整发现后仍重新扫描: err=%v scans=%d", err, len(hook.scans))
	}
}

func TestHistoryCleanupRetriesIndexedDueSamplesAfterAtomicFailure(t *testing.T) {
	service, _ := testHistoryService(t)
	client, ctx := service.history.client, context.Background()
	service.ConfigureHistory(client, "test:trim-failure", ":18080")
	now := time.Now().UTC()
	key := "test:trim-failure:" + strings.Repeat("e", 24) + ":60s"
	if err := client.ZAdd(ctx, key, redis.Z{Score: float64(now.Add(-73 * time.Hour).UnixMilli()), Member: "old"}, redis.Z{Score: float64(now.UnixMilli()), Member: "recent"}).Err(); err != nil {
		t.Fatal(err)
	}
	hook := &historyCleanupHook{failTrim: true}
	service.history.cleanup.client.AddHook(hook)
	if err := service.history.trimInactive(ctx, now); err == nil || !strings.Contains(err.Error(), "清理过期监控历史失败") {
		t.Fatalf("原子清理失败必须保留内部错误: %v", err)
	}
	if n := client.ZCard(ctx, key).Val(); n != 2 {
		t.Fatal("失败请求未执行时不得宣称采样已清理")
	}
	if score := client.ZScore(ctx, service.history.retentionIndex(), key).Val(); score != 0 {
		t.Fatal("失败不能推迟或丢弃到期索引")
	}
	if err := service.history.trimInactive(ctx, now); err != nil {
		t.Fatal(err)
	}
	if n := client.ZCard(ctx, key).Val(); n != 1 || len(hook.scans) != 1 {
		t.Fatal("已登记到期项应直接重试，不能依赖再次扫描发现")
	}
}

func TestHistoryCleanupAppendDoesNotPostponeOldestDueSample(t *testing.T) {
	service, server := testHistoryService(t)
	client, ctx := service.history.client, context.Background()
	readTimeout, writeTimeout := client.Options().ReadTimeout, client.Options().WriteTimeout
	service.ConfigureHistory(client, "test:earliest-due", ":18080")
	now := time.Now().UTC()
	old := now.Add(-71 * time.Hour)
	for _, at := range []time.Time{old, now} {
		if err := service.history.append(ctx, Snapshot{CollectedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	coarse := service.history.key + ":60s"
	if score := client.ZScore(ctx, service.history.retentionIndex(), coarse).Val(); score != float64(old.Add(HistoryRetention).UnixMilli()) {
		t.Fatalf("新采样推迟了旧采样的到期时间: %v", score)
	}
	if ttl := server.TTL(service.history.retentionIndex()); ttl != HistoryRetention {
		t.Fatalf("索引必须有与保留期匹配的TTL兜底: %v", ttl)
	}
	if client.Options().ReadTimeout != readTimeout || client.Options().WriteTimeout != writeTimeout {
		t.Fatal("清理不能修改共享Redis客户端的读写配置")
	}
	if service.history.cleanup.client == client || service.history.cleanup.client.Options().ReadTimeout != time.Second || service.history.cleanup.client.Options().WriteTimeout != time.Second {
		t.Fatal("清理应复用连接池的独立超时配置，不依赖共享客户端的context选项")
	}
}

func TestHistoryCleanupUsesSeparateFineAndCoarseRetention(t *testing.T) {
	service, _ := testHistoryService(t)
	client, ctx := service.history.client, context.Background()
	service.ConfigureHistory(client, "test:resolutions", ":18080")
	now := time.Now().UTC()
	for _, suffix := range []string{"5s", "60s"} {
		key := "test:resolutions:" + strings.Repeat("c", 24) + ":" + suffix
		if err := client.ZAdd(ctx, key, redis.Z{Score: float64(now.Add(-2 * time.Hour).UnixMilli()), Member: "two-hours"}, redis.Z{Score: float64(now.Add(-30 * time.Minute).UnixMilli()), Member: "recent"}).Err(); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.history.trimInactive(ctx, now); err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		suffix string
		count  int64
	}{{"5s", 1}, {"60s", 2}} {
		if n := client.ZCard(ctx, "test:resolutions:"+strings.Repeat("c", 24)+":"+sample.suffix).Val(); n != sample.count {
			t.Fatalf("分辨率保留期混淆: %s %d", sample.suffix, n)
		}
	}
}

func TestHistoryCleanupRejectsCorruptIndexMembersWithoutTouchingForeignKeys(t *testing.T) {
	service, _ := testHistoryService(t)
	client, ctx := service.history.client, context.Background()
	service.ConfigureHistory(client, "test:owned-index", ":18080")
	now := time.Now().UTC()
	foreign := "other:" + strings.Repeat("f", 24) + ":60s"
	invalid := "test:owned-index:" + strings.Repeat("a", 24) + ":60s:extra"
	for _, key := range []string{foreign, invalid} {
		if err := client.ZAdd(ctx, key, redis.Z{Score: 0, Member: "不属于该监控序列"}).Err(); err != nil {
			t.Fatal(err)
		}
	}
	wrongType := "test:owned-index:" + strings.Repeat("b", 24) + ":60s"
	if err := client.Set(ctx, wrongType, "不得修改", time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{foreign, invalid, wrongType} {
		if err := client.ZAdd(ctx, service.history.retentionIndex(), redis.Z{Score: 0, Member: key}).Err(); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.history.trimInactive(ctx, now); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{foreign, invalid} {
		if n := client.ZCard(ctx, key).Val(); n != 1 {
			t.Fatalf("脚本越界修改了非监控序列: %q", key)
		}
	}
	if value := client.Get(ctx, wrongType).Val(); value != "不得修改" {
		t.Fatal("同名非zset键不得修改")
	}
	if n := client.ZCard(ctx, service.history.retentionIndex()).Val(); n != 0 {
		t.Fatal("无效索引成员必须移出索引，不能使其他到期项饿死")
	}
}

func TestHistoryCleanupScriptRejectsInvalidIndexAndOversizedTargets(t *testing.T) {
	service, _ := testHistoryService(t)
	client, ctx := service.history.client, context.Background()
	service.ConfigureHistory(client, "test:script-boundary", ":18080")
	now := time.Now().UTC()
	key := "test:script-boundary:" + strings.Repeat("a", 24) + ":60s"
	if err := client.ZAdd(ctx, key, redis.Z{Score: 0, Member: "old"}).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.ZAdd(ctx, service.history.retentionIndex(), redis.Z{Score: 0, Member: key}).Err(); err != nil {
		t.Fatal(err)
	}
	keys := []string{service.history.retentionIndex()}
	for range historyCleanupBatchSize + 1 {
		keys = append(keys, key)
	}
	if err := trimHistoryScript.Run(ctx, client, keys, service.history.namespace, now.UnixMilli()).Err(); err == nil {
		t.Fatal("脚本自身必须拒绝超过16个候选，不能只依赖调用方LIMIT")
	}
	if err := trimHistoryScript.Run(ctx, client, []string{"other:retention", key}, service.history.namespace, now.UnixMilli()).Err(); err == nil {
		t.Fatal("脚本自身必须拒绝错误索引")
	}
	if n := client.ZCard(ctx, key).Val(); n != 1 {
		t.Fatal("脚本前提失败时不得修改任何序列")
	}
}

func TestHistoryCleanupConcurrentAppendKeepsNodeAndRecentHistory(t *testing.T) {
	service, _ := testHistoryService(t)
	service.ConfigureHistory(service.history.client, "test:concurrent-cleanup", ":18080")
	now := time.Now().UTC()
	var workers sync.WaitGroup
	for range 12 {
		workers.Go(func() {
			if err := service.history.append(context.Background(), Snapshot{CollectedAt: now}); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	for _, suffix := range []string{":5s", ":60s"} {
		if n := service.history.client.ZCard(context.Background(), service.history.key+suffix).Val(); n != 1 {
			t.Fatalf("并发清理破坏当前节点有效采样: %s %d", suffix, n)
		}
	}
}

func TestHistoryCleanupConcurrentCollectorsShareIndexAndKeepPrefixIsolation(t *testing.T) {
	service, _ := testHistoryService(t)
	client, ctx := service.history.client, context.Background()
	service.ConfigureHistory(client, "test:shared-cleanup", ":18080")
	other := &Service{logger: service.logger}
	other.ConfigureHistory(client, "test:shared-cleanup", ":18081")
	now := time.Now().UTC()
	key := "test:shared-cleanup:" + strings.Repeat("d", 24) + ":60s"
	foreign := "test:shared-cleanup-other:" + strings.Repeat("d", 24) + ":60s"
	for _, name := range []string{key, foreign} {
		if err := client.ZAdd(ctx, name, redis.Z{Score: float64(now.Add(-73 * time.Hour).UnixMilli()), Member: "old"}, redis.Z{Score: float64(now.Add(-2 * time.Hour).UnixMilli()), Member: "recent"}).Err(); err != nil {
			t.Fatal(err)
		}
	}
	var workers sync.WaitGroup
	for range 4 {
		for _, collector := range []*Service{service, other} {
			workers.Go(func() {
				if err := collector.history.append(ctx, Snapshot{CollectedAt: now}); err != nil {
					t.Error(err)
				}
			})
		}
	}
	workers.Wait()
	if n := client.ZCard(ctx, key).Val(); n != 1 {
		t.Fatalf("并发原子清理丢失或保留错误采样: %d", n)
	}
	if n := client.ZCard(ctx, foreign).Val(); n != 2 {
		t.Fatalf("并发清理越过命名空间: %d", n)
	}
	for _, collector := range []*Service{service, other} {
		if n := client.ZCard(ctx, collector.history.key+":60s").Val(); n != 1 {
			t.Fatalf("节点历史发生混合或当前采样丢失: %d", n)
		}
	}
	if score := client.ZScore(ctx, service.history.retentionIndex(), key).Val(); score != float64(now.Add(-2*time.Hour).Add(HistoryRetention).UnixMilli()) {
		t.Fatalf("原子清理未按真实最早剩余采样重排: %v", score)
	}
}

func TestHistoryCleanupRediscoversInactiveSeriesAfterIndexLoss(t *testing.T) {
	service, _ := testHistoryService(t)
	client, ctx := service.history.client, context.Background()
	service.ConfigureHistory(client, "test:index-loss", ":18080")
	now := time.Now().UTC()
	oldKey := "test:index-loss:" + strings.Repeat("e", 24) + ":60s"
	if err := client.ZAdd(ctx, oldKey, redis.Z{Score: float64(now.Add(-73 * time.Hour).UnixMilli()), Member: "expired-later"}, redis.Z{Score: float64(now.Add(-3 * time.Hour).UnixMilli()), Member: "recent"}).Err(); err != nil {
		t.Fatal(err)
	}
	hook := &historyCleanupHook{}
	service.history.cleanup.client.AddHook(hook)
	if err := service.history.append(ctx, Snapshot{CollectedAt: now.Add(-2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if !service.history.cleanup.discovered || len(hook.scans) != 1 || client.ZCard(ctx, oldKey).Val() != 2 {
		t.Fatal("首轮必须完成发现并保留当时尚未过期的两条采样")
	}
	// 仅删除这个隔离测试的索引，模拟索引被单独淘汰；旧节点数据保持不动。
	if err := client.Del(ctx, service.history.retentionIndex()).Err(); err != nil {
		t.Fatal(err)
	}
	if err := service.history.append(ctx, Snapshot{CollectedAt: now}); err != nil {
		t.Fatal(err)
	}
	if n := client.ZCard(ctx, oldKey).Val(); n != 1 || client.ZScore(ctx, oldKey, "recent").Err() != nil {
		t.Fatalf("索引重建必须清理失活节点过期采样并保留最近采样: %d", n)
	}
	if len(hook.scans) != 2 || !service.history.cleanup.discovered {
		t.Fatal("活跃序列重新登记后必须重新完成namespace发现")
	}
	for step := 1; step <= 3; step++ {
		if err := service.history.append(ctx, Snapshot{CollectedAt: now.Add(time.Duration(step) * SampleInterval)}); err != nil {
			t.Fatal(err)
		}
	}
	if len(hook.scans) != 2 || service.history.cleanup.rediscover.Load() {
		t.Fatal("正常重复写入不得反复扫描Redis键空间")
	}
}

func TestHistoryCleanupRediscoveryRequestSurvivesBusyCleanup(t *testing.T) {
	service, _ := testHistoryService(t)
	client, ctx := service.history.client, context.Background()
	service.ConfigureHistory(client, "test:rediscover-busy", ":18080")
	now := time.Now().UTC()
	hook := &historyCleanupHook{}
	service.history.cleanup.client.AddHook(hook)
	if err := service.history.append(ctx, Snapshot{CollectedAt: now}); err != nil {
		t.Fatal(err)
	}
	if len(hook.scans) != 1 || !service.history.cleanup.discovered {
		t.Fatal("前置发现尚未完成")
	}
	if err := client.Del(ctx, service.history.retentionIndex()).Err(); err != nil {
		t.Fatal(err)
	}
	service.history.cleanup.mu.Lock()
	err := service.history.append(ctx, Snapshot{CollectedAt: now.Add(SampleInterval)})
	pending := service.history.cleanup.rediscover.Load()
	service.history.cleanup.mu.Unlock()
	if err != nil || !pending || len(hook.scans) != 1 {
		t.Fatalf("TryLock失败不能消费重发现请求: pending=%v scans=%d err=%v", pending, len(hook.scans), err)
	}
	if err := service.history.append(ctx, Snapshot{CollectedAt: now.Add(2 * SampleInterval)}); err != nil {
		t.Fatal(err)
	}
	if len(hook.scans) != 2 || service.history.cleanup.rediscover.Load() || !service.history.cleanup.discovered {
		t.Fatal("后续append即使不再新增member，也必须消费之前保留的重发现请求")
	}
}

func TestHistoryCleanupUnknownAppendReceiptPreservesRediscoveryWithoutReplay(t *testing.T) {
	service, _ := testHistoryService(t)
	client, ctx := service.history.client, context.Background()
	service.ConfigureHistory(client, "test:unknown-append", ":18080")
	now := time.Now().UTC()
	oldKey := "test:unknown-append:" + strings.Repeat("f", 24) + ":60s"
	if err := client.ZAdd(ctx, oldKey, redis.Z{Score: float64(now.Add(-73 * time.Hour).UnixMilli()), Member: "expired-later"}, redis.Z{Score: float64(now.Add(-3 * time.Hour).UnixMilli()), Member: "recent"}).Err(); err != nil {
		t.Fatal(err)
	}
	reads := &historyCleanupHook{}
	service.history.cleanup.client.AddHook(reads)
	if err := service.history.append(ctx, Snapshot{CollectedAt: now.Add(-2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := client.Del(ctx, service.history.retentionIndex()).Err(); err != nil {
		t.Fatal(err)
	}
	writes := &historyCleanupHook{failAfterOnce: true}
	client.AddHook(writes)
	if err := service.history.append(ctx, Snapshot{CollectedAt: now}); err == nil || !strings.Contains(err.Error(), "保存监控历史失败") {
		t.Fatalf("未知回执必须保持失败，不能伪称成功或重放: %v", err)
	}
	if writes.pipelines != 1 || !service.history.cleanup.rediscover.Load() || len(reads.scans) != 1 {
		t.Fatal("事务回执未知不能重放写入，也不能丢失重发现请求")
	}
	if n := client.ZCard(ctx, service.history.retentionIndex()).Val(); n != 3 {
		t.Fatalf("故障必须真实模拟已完成登记但丢失回执: %d", n)
	}
	if n := client.ZCard(ctx, oldKey).Val(); n != 2 {
		t.Fatal("失败不能宣称已经清掉未确认的旧节点采样")
	}
	if err := service.history.append(ctx, Snapshot{CollectedAt: now.Add(SampleInterval)}); err != nil {
		t.Fatal(err)
	}
	if writes.pipelines != 2 || len(reads.scans) != 2 || service.history.cleanup.rediscover.Load() || client.ZCard(ctx, oldKey).Val() != 1 || client.ZScore(ctx, oldKey, "recent").Err() != nil {
		t.Fatal("下一次正常append登记数为0时也必须重发现并准确清理，不重放前一次写入")
	}
}

func TestHistoryCleanupCollectorRegistrationSurvivesAnotherDiscovererExitingEarly(t *testing.T) {
	service, _ := testHistoryService(t)
	client, ctx := service.history.client, context.Background()
	namespace := "test:collector-recovery"
	service.ConfigureHistory(client, namespace, ":18080")
	other := &Service{logger: service.logger}
	other.ConfigureHistory(client, namespace, ":18080")
	if service.history.key != other.history.key || service.history.cleanup.registration == other.history.cleanup.registration {
		t.Fatal("相同logical node的不同collector也必须有独立注册标记")
	}
	now := time.Now().UTC().Truncate(time.Minute)
	reads := &historyCleanupHook{}
	service.history.cleanup.client.AddHook(reads)
	if err := service.history.append(ctx, Snapshot{CollectedAt: now}); err != nil {
		t.Fatal(err)
	}
	oldKey := namespace + ":" + strings.Repeat("0", 24) + ":60s"
	if err := client.ZAdd(ctx, oldKey, redis.Z{Score: float64(now.Add(-73 * time.Hour).UnixMilli()), Member: "expired"}, redis.Z{Score: float64(now.Add(-2 * time.Hour).UnixMilli()), Member: "recent"}).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.ZAdd(ctx, service.history.retentionIndex(), redis.Z{Score: 0, Member: oldKey}).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.Del(ctx, service.history.retentionIndex()).Err(); err != nil {
		t.Fatal(err)
	}
	// A只完成含B当前序列的第一页，之后不再运行；不能依赖A接续发现C。
	partial := &historyCleanupHook{scan: func(cmd *redis.ScanCmd) {
		cmd.SetVal([]string{service.history.key + ":5s", service.history.key + ":60s"}, 41)
	}}
	other.history.cleanup.client.AddHook(partial)
	if err := other.history.append(ctx, Snapshot{CollectedAt: now}); err != nil {
		t.Fatal(err)
	}
	if other.history.cleanup.discovered || other.history.cleanup.cursor != 41 || len(partial.scans) != 1 || !service.history.cleanup.discovered {
		t.Fatal("测试必须停在A部分发现、B此前已完成发现的准确状态")
	}
	for _, suffix := range []string{":5s", ":60s"} {
		if err := client.ZScore(ctx, service.history.retentionIndex(), service.history.key+suffix).Err(); err != nil {
			t.Fatal("A首屏必须已经替B重新登记当前series")
		}
	}
	if err := client.ZScore(ctx, service.history.retentionIndex(), service.history.cleanup.registration).Err(); !errors.Is(err, redis.Nil) {
		t.Fatalf("A发现不能替B创建私有注册标记: %v", err)
	}
	if err := service.history.append(ctx, Snapshot{CollectedAt: now.Add(SampleInterval)}); err != nil {
		t.Fatal(err)
	}
	if len(reads.scans) != 2 || !service.history.cleanup.discovered || client.ZCard(ctx, oldKey).Val() != 1 || client.ZScore(ctx, oldKey, "recent").Err() != nil {
		t.Fatal("A退出后B必须依靠私有注册完成发现，准确清理失活C并保留最近采样")
	}
}

func TestHistoryCleanupRegistrationExpiresWithoutTouchingSameNamedEntity(t *testing.T) {
	service, _ := testHistoryService(t)
	client, ctx := service.history.client, context.Background()
	service.ConfigureHistory(client, "test:registration-expiry", ":18080")
	now := time.Now().UTC()
	reads := &historyCleanupHook{}
	service.history.cleanup.client.AddHook(reads)
	if err := service.history.append(ctx, Snapshot{CollectedAt: now}); err != nil {
		t.Fatal(err)
	}
	marker := service.history.cleanup.registration
	if err := client.Set(ctx, marker, "索引member不是实体key", time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.ZAdd(ctx, service.history.retentionIndex(), redis.Z{Score: float64(now.UnixMilli() - 1), Member: marker}).Err(); err != nil {
		t.Fatal(err)
	}
	if err := service.history.trimInactive(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := client.ZScore(ctx, service.history.retentionIndex(), marker).Err(); !errors.Is(err, redis.Nil) || client.Get(ctx, marker).Val() != "索引member不是实体key" {
		t.Fatal("注册标记到期只能移除索引member，不能触碰同名实体key")
	}
	if err := service.history.append(ctx, Snapshot{CollectedAt: now.Add(SampleInterval)}); err != nil {
		t.Fatal(err)
	}
	if len(reads.scans) != 2 || !service.history.cleanup.discovered || client.ZScore(ctx, service.history.retentionIndex(), marker).Val() != float64(now.Add(SampleInterval+HistoryRetention).UnixMilli()) {
		t.Fatal("活跃collector的周期性注册回收后必须可重新登记和完成发现")
	}
}
