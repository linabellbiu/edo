package task

import (
	"testing"

	"zrt/internal/config"
)

func TestDeploymentDoesNotRetryWithoutIdempotency(t *testing.T) {
	attempts, err := normalizeMaxAttempts(CreateInput{Kind: "deploy.kubernetes", MaxAttempts: 4}, config.DefaultMaxAttempts)
	if err != nil {
		t.Fatalf("计算重试次数失败: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("非幂等发布任务不应自动重试，得到 %d", attempts)
	}
}

func TestPipelineDeploymentDoesNotRetryWithoutIdempotency(t *testing.T) {
	attempts, err := normalizeMaxAttempts(CreateInput{Kind: "pipeline.deploy", MaxAttempts: 4}, config.DefaultMaxAttempts)
	if err != nil {
		t.Fatalf("计算流水线发布重试次数失败: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("包含构建和发布副作用的流水线任务不应自动重试，得到 %d", attempts)
	}
}

func TestIdempotentTaskUsesFiniteRetryLimit(t *testing.T) {
	attempts, err := normalizeMaxAttempts(CreateInput{Kind: "build.image", Idempotent: true}, config.DefaultMaxAttempts)
	if err != nil {
		t.Fatalf("计算重试次数失败: %v", err)
	}
	if attempts != config.DefaultMaxAttempts {
		t.Fatalf("默认最大执行次数错误，得到 %d", attempts)
	}
}

func TestRejectsUnlimitedRetries(t *testing.T) {
	if _, err := normalizeMaxAttempts(CreateInput{Kind: "build.image", MaxAttempts: -1}, config.DefaultMaxAttempts); err == nil {
		t.Fatal("无限重试必须被拒绝")
	}
}

func TestRejectsAttemptsAboveConsumerLimit(t *testing.T) {
	if _, err := normalizeMaxAttempts(CreateInput{Kind: "build.image", MaxAttempts: 5}, config.DefaultMaxAttempts); err == nil {
		t.Fatal("任务执行次数不能超过 Consumer 最大投递次数")
	}
}
