package systemmetrics

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const SampleInterval = 5 * time.Second
const HistoryRetention = 3 * 24 * time.Hour

var ErrInvalidHistoryRange = errors.New("请选择支持的监控时间范围")
var ErrHistoryUnavailable = errors.New("历史监控数据暂不可用，请稍后重试")

type History struct {
	NodeID          string     `json:"node_id"`
	From            time.Time  `json:"from"`
	To              time.Time  `json:"to"`
	IntervalSeconds int        `json:"interval_seconds"`
	Points          []Snapshot `json:"points"`
	Latest          Snapshot   `json:"latest"`
}

type historyStore struct {
	client *redis.Client
	key    string
	nodeID string
}

func (s *Service) ConfigureHistory(client *redis.Client, prefix, listener string) {
	hostname, err := os.Hostname()
	if err != nil {
		s.logger.Error("初始化监控历史节点失败", "operation", "system_metrics_history_init", "err", err)
		return
	}
	digest := sha256.Sum256([]byte(hostname + "\x00" + listener))
	nodeID := fmt.Sprintf("%x", digest[:12])
	s.history = &historyStore{client: client, key: prefix + ":" + nodeID, nodeID: nodeID}
}

// Run 在后台独立采样，浏览器查询只读取缓存，不参与 CPU 差值计算。
func (s *Service) Run(ctx context.Context) error {
	ticker := time.NewTicker(SampleInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		collectCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		snapshot := s.Snapshot(collectCtx)
		s.latestMu.Lock()
		s.latest = &snapshot
		s.latestMu.Unlock()
		if s.history != nil {
			if err := s.history.append(collectCtx, snapshot); err != nil && ctx.Err() == nil {
				s.markUnavailable(&Snapshot{}, "history", "保存监控历史失败", err)
			}
		}
		cancel()
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *Service) Current(ctx context.Context) Snapshot {
	s.latestMu.RLock()
	latest := s.latest
	s.latestMu.RUnlock()
	if latest != nil {
		return *latest
	}
	return s.Snapshot(ctx)
}

func (h *historyStore) append(ctx context.Context, snapshot Snapshot) error {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("编码监控采样失败: %w", err)
	}
	now := snapshot.CollectedAt
	score := float64(now.UnixMilli())
	minute := now.Truncate(time.Minute).UnixMilli()
	_, err = h.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		fine, coarse := h.key+":5s", h.key+":60s"
		pipe.ZAdd(ctx, fine, redis.Z{Score: score, Member: data})
		pipe.ZRemRangeByScore(ctx, fine, "-inf", strconv.FormatInt(now.Add(-time.Hour).UnixMilli(), 10))
		pipe.ZRemRangeByRank(ctx, fine, 0, -722)
		pipe.Expire(ctx, fine, time.Hour+time.Minute)
		// 分钟历史取该分钟的最后一个采样值。
		pipe.ZRemRangeByScore(ctx, coarse, strconv.FormatInt(minute, 10), "("+strconv.FormatInt(minute+60_000, 10))
		pipe.ZAdd(ctx, coarse, redis.Z{Score: score, Member: data})
		pipe.ZRemRangeByScore(ctx, coarse, "-inf", strconv.FormatInt(now.Add(-HistoryRetention).UnixMilli(), 10))
		pipe.ZRemRangeByRank(ctx, coarse, 0, -4322)
		pipe.Expire(ctx, coarse, HistoryRetention)
		return nil
	})
	if err != nil {
		return fmt.Errorf("保存监控历史失败: %w", err)
	}
	return nil
}

func (s *Service) History(ctx context.Context, window time.Duration, after int64) (History, error) {
	return s.historyAt(ctx, window, after, time.Now().UTC())
}

func (s *Service) historyAt(ctx context.Context, window time.Duration, after int64, now time.Time) (History, error) {
	if (window != 15*time.Minute && window != time.Hour && window != 6*time.Hour && window != 24*time.Hour && window != HistoryRetention) || after < 0 || after > now.Add(SampleInterval).UnixMilli() {
		return History{}, ErrInvalidHistoryRange
	}
	if s.history == nil {
		return History{}, ErrHistoryUnavailable
	}
	result := History{NodeID: s.history.nodeID, From: now.Add(-window), To: now, IntervalSeconds: 5, Points: []Snapshot{}}
	key := s.history.key + ":5s"
	if window > time.Hour {
		key = s.history.key + ":60s"
		result.IntervalSeconds = 60
	}
	minimum := strconv.FormatInt(result.From.UnixMilli(), 10)
	if after >= result.From.UnixMilli() {
		minimum = "(" + strconv.FormatInt(after, 10)
	}
	values, err := s.history.client.ZRangeByScore(ctx, key, &redis.ZRangeBy{Min: minimum, Max: strconv.FormatInt(now.UnixMilli(), 10), Count: 4321}).Result()
	if err != nil {
		return History{}, fmt.Errorf("读取监控历史失败: %w", err)
	}
	for _, value := range values {
		var point Snapshot
		if err := json.Unmarshal([]byte(value), &point); err != nil {
			return History{}, fmt.Errorf("读取监控采样失败: %w", err)
		}
		result.Points = append(result.Points, point)
	}
	result.Latest = s.Current(ctx)
	return result, nil
}
