package systemmetrics

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

const historyCleanupBatchSize = 16

type historyCleanup struct {
	client       *redis.Client
	registration string
	mu           sync.Mutex
	cursor       uint64
	discovered   bool
	rediscover   atomic.Bool
}

// 所有目标在脚本内再次校验，trim 与最早剩余采样的重新排期保持原子性。
var trimHistoryScript = redis.NewScript(fmt.Sprintf(`
local index, namespace, now = KEYS[1], ARGV[1], tonumber(ARGV[2])
if index ~= namespace .. ':retention' or #KEYS > %d then
  return redis.error_reply('INVALID_METRIC_INDEX')
end
local prefix = namespace .. ':'
for i = 2, #KEYS do
  local key = KEYS[i]
  local due = redis.call('ZSCORE', index, key)
  if due and tonumber(due) <= now then
    local node, suffix = string.match(string.sub(key, #prefix + 1), '^([0-9a-f]+):([0-9]+s)$')
    local retention = suffix == '5s' and 3600000 or suffix == '60s' and 259200000 or nil
    if string.sub(key, 1, #prefix) ~= prefix or not node or #node ~= 24 or not retention then
      redis.call('ZREM', index, key)
    elseif redis.call('TYPE', key).ok ~= 'zset' then
      redis.call('ZREM', index, key)
    else
      redis.call('ZREMRANGEBYSCORE', key, '-inf', now - retention)
      local first = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
      if #first == 0 then
        redis.call('ZREM', index, key)
      else
        redis.call('ZADD', index, tonumber(first[2]) + retention, key)
      end
    end
  end
end
return #KEYS - 1
`, historyCleanupBatchSize+1))

func (h *historyStore) retentionIndex() string { return h.namespace + ":retention" }

// trimInactive 随后台采样清理已停止节点，不依赖浏览器，也不延长旧键的存活期。
func (h *historyStore) trimInactive(ctx context.Context, now time.Time) error {
	if h.namespace == "" || !h.cleanup.mu.TryLock() {
		return nil
	}
	defer h.cleanup.mu.Unlock()
	if h.cleanup.rediscover.Swap(false) {
		h.cleanup.cursor, h.cleanup.discovered = 0, false
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	// 共享客户端未必启用 context I/O 截止；此客户端复用连接池，仅收窄清理读写超时。
	client := h.cleanup.client

	if !h.cleanup.discovered {
		if err := h.discoverHistory(ctx, client); err != nil {
			return err
		}
	}
	keys, err := client.ZRangeByScore(ctx, h.retentionIndex(), &redis.ZRangeBy{Min: "-inf", Max: strconv.FormatInt(now.UnixMilli(), 10), Count: historyCleanupBatchSize}).Result()
	if err != nil {
		return fmt.Errorf("读取待清监控历史失败: %w", err)
	}
	if len(keys) == 0 {
		return nil
	}
	if err := trimHistoryScript.Run(ctx, client, append([]string{h.retentionIndex()}, keys...), h.namespace, now.UnixMilli()).Err(); err != nil {
		return fmt.Errorf("清理过期监控历史失败: %w", err)
	}
	return nil
}

func (h *historyStore) discoverHistory(ctx context.Context, client *redis.Client) error {
	// 每次注册恢复执行一轮完整游标发现；常态只查到期索引，注册标记到期后可再发现。
	// COUNT 只是提示，完整登记返回页，不截断或以 offset 猜测下页，也不跨轮缓存返回页。
	pattern := strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`, `]`, `\]`).Replace(h.namespace) + ":*"
	keys, next, err := client.ScanType(ctx, h.cleanup.cursor, pattern, historyCleanupBatchSize, "zset").Result()
	if err != nil {
		return fmt.Errorf("发现已有监控历史失败: %w", err)
	}
	members := make([]redis.Z, 0, len(keys))
	for _, key := range keys {
		if _, ok := historyKeyRetention(h.namespace, key); ok {
			members = append(members, redis.Z{Score: 0, Member: key})
		}
	}
	if len(members) > 0 {
		if _, err := client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.ZAddNX(ctx, h.retentionIndex(), members...)
			pipe.Expire(ctx, h.retentionIndex(), HistoryRetention)
			return nil
		}); err != nil {
			return fmt.Errorf("登记已有监控历史失败: %w", err)
		}
	}
	h.cleanup.cursor, h.cleanup.discovered = next, next == 0
	return nil
}

func historyKeyRetention(namespace, key string) (time.Duration, bool) {
	if namespace == "" {
		return 0, false
	}
	value, ok := strings.CutPrefix(key, namespace+":")
	if !ok {
		return 0, false
	}
	node, suffix, ok := strings.Cut(value, ":")
	if !ok || len(node) != 24 {
		return 0, false
	}
	for _, char := range node {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return 0, false
		}
	}
	switch suffix {
	case "5s":
		return time.Hour, true
	case "60s":
		return HistoryRetention, true
	default:
		return 0, false
	}
}
