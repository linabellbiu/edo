package artifact

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"edo/internal/model"
)

type gatedArtifactReader struct {
	ctx     context.Context
	entered chan struct{}
	resume  chan struct{}
	once    sync.Once
	source  io.Reader
}

func (r *gatedArtifactReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.entered) })
	select {
	case <-r.resume:
		return r.source.Read(p)
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	}
}

func TestLocalArtifactCleanupRejectsActiveUploadWithoutWaiting(t *testing.T) {
	service, _, application := newArtifactTestService(t, 1024)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	item, err := service.Upload(ctx, application.ID, "user-1", "existing.bin", "application/octet-stream", strings.NewReader("existing-content"))
	if err != nil {
		t.Fatalf("准备已有制品失败: %v", err)
	}
	filePath := filepath.Join(service.Root(), filepath.FromSlash(item.StorageKey))
	source := &gatedArtifactReader{ctx: ctx, entered: make(chan struct{}), resume: make(chan struct{}), source: strings.NewReader("pending-content")}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(source.resume) }) }
	defer release()
	uploadResult := make(chan error, 1)
	go func() {
		_, uploadErr := service.Upload(ctx, application.ID, "user-1", "pending.bin", "application/octet-stream", source)
		uploadResult <- uploadErr
	}()
	select {
	case <-source.entered:
	case <-ctx.Done():
		t.Fatal("上传未进入实际制品写入阶段")
	}
	cleanupResult := make(chan error, 1)
	go func() {
		_, cleanupErr := service.ClearLocalArtifacts(ctx)
		cleanupResult <- cleanupErr
	}()
	var cleanupErr error
	returned := false
	select {
	case cleanupErr = <-cleanupResult:
		returned = true
	case <-time.After(500 * time.Millisecond):
		t.Error("上传占用目录时，清理等待传输完成而没有立即拒绝")
	}
	if returned {
		if !errors.Is(cleanupErr, ErrArtifactDirectoryBusy) {
			t.Errorf("上传占用目录时清理应返回忙碌错误: %v", cleanupErr)
		}
		stored, findErr := service.Find(ctx, item.ID)
		if findErr != nil || stored.Status != model.ArtifactStatusAvailable {
			t.Errorf("被拒绝的清理改变了已有制品状态: err=%v", findErr)
		}
		if _, statErr := os.Stat(filePath); statErr != nil {
			t.Errorf("被拒绝的清理删除了已有制品文件: %v", statErr)
		}
	}
	release()
	select {
	case err := <-uploadResult:
		if err != nil {
			t.Errorf("拒绝清理后上传未正常完成: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("释放测试上传后未完成")
	}
	if !returned {
		select {
		case <-cleanupResult:
		case <-ctx.Done():
			t.Fatal("原清理调用未退出")
		}
		return
	}
	report, err := service.ClearLocalArtifacts(ctx)
	if err != nil || report.ArtifactsExpired != 2 || report.FilesDeleted != 2 {
		t.Fatalf("上传结束后清理应恢复可用: report=%+v err=%v", report, err)
	}
}
