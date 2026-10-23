package account

import (
	"io"
	"log/slog"
	"testing"

	"edo/internal/auth"
)

func TestLoginServiceReusesOnlyDummyHash(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	first, err := NewLoginService(nil, nil, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewLoginService(nil, nil, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	if first.dummyHash == "" || first.dummyHash != second.dummyHash {
		t.Fatal("登录服务重复生成占位摘要")
	}
	if matched, err := auth.ComparePassword("不是占位密码", first.dummyHash); err != nil || matched {
		t.Fatalf("占位摘要未保留真实密码校验: matched=%t err=%v", matched, err)
	}
}
