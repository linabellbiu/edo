package artifact

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"edo/internal/model"
)

func TestUploadAuthorizationStopsBeforePublishingContent(t *testing.T) {
	for _, failure := range []error{fs.ErrPermission, errors.New("测试授权存储不可用")} {
		t.Run(failure.Error(), func(t *testing.T) {
			root := t.TempDir()
			store, err := NewLocalStore(root, 1024)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			ctx := WithUploadAuthorization(context.Background(), func(context.Context) error {
				calls++
				return failure
			})
			if _, err := store.Put(ctx, strings.NewReader("public fixture")); !errors.Is(err, failure) || calls != 1 {
				t.Fatalf("内容提交未保留授权结果: err=%v calls=%d", err, calls)
			}
			assertNoUploadFiles(t, root)
		})
	}
}

type uploadCancelAtEOF struct{ cancel context.CancelFunc }

func (r uploadCancelAtEOF) Read(p []byte) (int, error) {
	r.cancel()
	return copy(p, "public fixture"), io.EOF
}

func TestLocalStoreRejectsCancellationOnFinalRead(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocalStore(root, 1024)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := store.Put(ctx, uploadCancelAtEOF{cancel}); !errors.Is(err, context.Canceled) {
		t.Fatalf("最后一次读取取消后仍发布内容: %v", err)
	}
	assertNoUploadFiles(t, root)
}

func assertNoUploadFiles(t *testing.T, root string) {
	t.Helper()
	if err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			t.Errorf("被拒绝的上传遗留文件: %s", entry.Name())
		}
		return nil
	}); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

type callerOwnedUploadReader struct {
	io.Reader
	closed int
}

func (r *callerOwnedUploadReader) Close() error { r.closed++; return nil }

func TestUploadFailureDoesNotDrainOrCloseCallerReader(t *testing.T) {
	service, db, application := newArtifactTestService(t, 4)
	input := &callerOwnedUploadReader{Reader: strings.NewReader("public oversized input")}
	if _, err := service.Upload(context.Background(), application.ID, "user-1", "public.txt", "text/plain", input); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("超量上传未拒绝: %v", err)
	}
	if input.closed != 0 {
		t.Fatal("服务擅自关闭调用方 Reader，multipart.Close 可能继续排空请求")
	}
	for _, entity := range []any{&model.Artifact{}, &model.BuildRun{}} {
		var count int64
		if err := db.Model(entity).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("失败上传仍登记记录: count=%d err=%v", count, err)
		}
	}
	entries, err := os.ReadDir(service.store.temp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("失败上传遗留暂存文件: count=%d err=%v", len(entries), err)
	}
}
