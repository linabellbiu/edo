package cicd

import (
	"testing"
	"time"

	"edo/internal/model"
)

func TestPolicyWindowUsesShanghaiMinuteBoundaries(t *testing.T) {
	tests := []struct {
		name, start, end, instant string
		allowed                   bool
	}{
		{"overnight_before_start", "22:00", "06:00", "2026-10-31T13:59:59.999999999Z", false},
		{"overnight_start", "22:00", "06:00", "2026-10-31T14:00:00Z", true},
		{"overnight_before_midnight", "22:00", "06:00", "2026-10-31T15:59:59.999999999Z", true},
		{"overnight_midnight", "22:00", "06:00", "2026-10-31T16:00:00Z", true},
		{"overnight_after_midnight", "22:00", "06:00", "2026-10-31T16:00:00.000000001Z", true},
		{"overnight_before_end", "22:00", "06:00", "2026-10-31T21:59:59.999999999Z", true},
		{"overnight_end", "22:00", "06:00", "2026-10-31T22:00:00Z", true},
		{"overnight_end_minute_last_nanosecond", "22:00", "06:00", "2026-10-31T22:00:59.999999999Z", true},
		{"overnight_after_end_minute", "22:00", "06:00", "2026-10-31T22:01:00Z", false},
		{"overnight_daytime", "22:00", "06:00", "2026-11-01T07:00:00Z", false},
		{"overnight_next_start", "22:00", "06:00", "2026-11-01T14:00:00Z", true},
		{"overnight_offset_input_allowed", "22:00", "06:00", "2026-10-31T12:00:00-04:00", true},
		{"overnight_offset_input_denied", "22:00", "06:00", "2026-11-01T03:00:00-04:00", false},
		{"daytime_before_start", "09:00", "18:00", "2026-11-01T00:59:59.999999999Z", false},
		{"daytime_start", "09:00", "18:00", "2026-11-01T01:00:00Z", true},
		{"daytime_end", "09:00", "18:00", "2026-11-01T10:00:00Z", true},
		{"daytime_end_minute_last_nanosecond", "09:00", "18:00", "2026-11-01T10:00:59.999999999Z", true},
		{"daytime_after_end_minute", "09:00", "18:00", "2026-11-01T10:01:00Z", false},
		{"daytime_does_not_use_input_clock", "09:00", "18:00", "2026-10-31T21:00:00-04:00", true},
		{"one_minute_before", "09:00", "09:00", "2026-11-01T00:59:59.999999999Z", false},
		{"one_minute_start", "09:00", "09:00", "2026-11-01T01:00:00Z", true},
		{"one_minute_last_nanosecond", "09:00", "09:00", "2026-11-01T01:00:59.999999999Z", true},
		{"one_minute_after", "09:00", "09:00", "2026-11-01T01:01:00Z", false},
		{"no_window_at_midnight", "", "", "2026-10-31T16:00:00Z", true},
		{"no_window_at_daytime", "", "", "2026-11-01T07:00:00Z", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instant, err := time.Parse(time.RFC3339Nano, tt.instant)
			if err != nil {
				t.Fatal("测试时间无效：", err)
			}
			// 规则按上海本地分钟判断，结束分钟内的秒和纳秒仍属于窗口。
			policy := model.EnvironmentReleasePolicy{WindowStart: tt.start, WindowEnd: tt.end}
			if got := policyWindow(policy, instant); got != tt.allowed {
				t.Fatalf("窗口 %s 至 %s 在时刻 %s 的判断错误：得到 %t，期望 %t", tt.start, tt.end, tt.instant, got, tt.allowed)
			}
		})
	}
}
