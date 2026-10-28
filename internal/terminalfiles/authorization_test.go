package terminalfiles

import (
	"context"
	"errors"
	"io/fs"
	"testing"
)

func TestCommitAuthorizationChecksCurrentPermissionAndCancellation(t *testing.T) {
	allowed := true
	checks := 0
	ctx, cancel := context.WithCancel(WithCommitAuthorization(context.Background(), func(context.Context) error {
		checks++
		if !allowed {
			return fs.ErrPermission
		}
		return nil
	}))
	if err := AuthorizeCommit(ctx); err != nil || checks != 1 {
		t.Fatalf("初始授权未检查: %v %d", err, checks)
	}
	allowed = false
	if err := AuthorizeCommit(ctx); !errors.Is(err, fs.ErrPermission) || checks != 2 {
		t.Fatalf("未拒绝已撤销授权: %v %d", err, checks)
	}
	cancel()
	if err := AuthorizeCommit(ctx); !errors.Is(err, context.Canceled) || checks != 2 {
		t.Fatalf("取消后仍调用授权或允许提交: %v %d", err, checks)
	}
}

func TestCommitAuthorizationRechecksCancellationAfterGuard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ctx = WithCommitAuthorization(ctx, func(context.Context) error { cancel(); return nil })
	if err := AuthorizeCommit(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("检查期间取消仍可提交: %v", err)
	}
}
