package messaging

import (
	"testing"
	"time"
)

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
