package messaging

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

type testDeadLetterStream struct {
	info         *jetstream.StreamInfo
	infoErr      error
	purgeErr     error
	purgeSubject string
	purgeCalls   int
}

func (s *testDeadLetterStream) Info(_ context.Context, _ ...jetstream.StreamInfoOpt) (*jetstream.StreamInfo, error) {
	return s.info, s.infoErr
}

func (s *testDeadLetterStream) Purge(_ context.Context, opts ...jetstream.StreamPurgeOpt) error {
	s.purgeCalls++
	request := &jetstream.StreamPurgeRequest{}
	for _, opt := range opts {
		if err := opt(request); err != nil {
			return err
		}
	}
	s.purgeSubject = request.Subject
	return s.purgeErr
}

func TestRetryBackoffIsFinite(t *testing.T) {
	tests := []struct {
		attempts int
		want     []time.Duration
	}{
		{attempts: 1, want: nil},
		{attempts: 2, want: []time.Duration{10 * time.Second}},
		{attempts: 4, want: []time.Duration{10 * time.Second, 30 * time.Second, 2 * time.Minute}},
		{attempts: 20, want: []time.Duration{10 * time.Second, 30 * time.Second, 2 * time.Minute}},
	}
	for _, tt := range tests {
		got := retryBackoff(tt.attempts)
		if len(got) != len(tt.want) {
			t.Fatalf("attempts=%d, got=%v want=%v", tt.attempts, got, tt.want)
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Fatalf("attempts=%d, got=%v want=%v", tt.attempts, got, tt.want)
			}
		}
	}
}

func TestPurgeDeadLetterStreamOnlyPurgesConfiguredSubject(t *testing.T) {
	stream := &testDeadLetterStream{info: &jetstream.StreamInfo{State: jetstream.StreamState{Msgs: 7}}}
	purged, err := purgeDeadLetterStream(context.Background(), stream, "edo.dead.task.v1")
	if err != nil {
		t.Fatalf("清空死信失败: %v", err)
	}
	if purged != 7 {
		t.Fatalf("清空数量错误: %d", purged)
	}
	if stream.purgeCalls != 1 || stream.purgeSubject != "edo.dead.task.v1" {
		t.Fatalf("清空范围错误: calls=%d subject=%q", stream.purgeCalls, stream.purgeSubject)
	}
}

func TestPurgeDeadLetterStreamDoesNotPurgeWhenStatsFail(t *testing.T) {
	stream := &testDeadLetterStream{infoErr: errors.New("stream unavailable")}
	if _, err := purgeDeadLetterStream(context.Background(), stream, "edo.dead.task.v1"); err == nil {
		t.Fatal("读取死信状态失败时应返回错误")
	}
	if stream.purgeCalls != 0 {
		t.Fatalf("读取死信状态失败时不应执行清空: %d", stream.purgeCalls)
	}
}
