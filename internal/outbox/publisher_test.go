package outbox

import (
	"testing"
	"time"
)

func TestRetryDelayIsBounded(t *testing.T) {
	if got := retryDelay(1); got != 10*time.Second {
		t.Fatalf("首次重试延迟错误: %v", got)
	}
	if got := retryDelay(100); got != 10*time.Minute {
		t.Fatalf("重试延迟必须有上限: %v", got)
	}
}
