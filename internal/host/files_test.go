package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"edo/internal/model"
	"edo/internal/terminalfiles"
)

func localFilesFixture(t *testing.T) (terminalfiles.Backend, context.Context, string) {
	t.Helper()
	service, _, ctx, id := hostInteractionFixture(t, model.HostModeLocal)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	t.Cleanup(cancel)
	backend, err := service.OpenFiles(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	return backend, ctx, t.TempDir()
}

func hostTestDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func TestHostFilesLocalRevokedAfterTransferDoesNotCommit(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			backend, ctx, directory := localFilesFixture(t)
			location := filepath.Join(directory, "commit.txt")
			expected := ""
			if existing {
				if err := os.WriteFile(location, []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
				expected = hostTestDigest("original")
			}
			allowed := true
			ctx = terminalfiles.WithCommitAuthorization(ctx, func(context.Context) error {
				if !allowed {
					return os.ErrPermission
				}
				return nil
			})
			input := &hostMutationReader{input: strings.NewReader("candidate"), onRead: func() { allowed = false }}
			if _, err := backend.Put(ctx, location, input, 9, expected); !errors.Is(err, os.ErrPermission) {
				t.Fatalf("传输后撤权仍提交文件: %v", err)
			}
			data, err := os.ReadFile(location)
			if existing && (err != nil || string(data) != "original") || !existing && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("取消后目标发生修改: %q %v", data, err)
			}
			assertNoHostUploadTemps(t, directory)
		})
	}
}

func TestHostFilesLocalAtomicCreateEditAndMetadata(t *testing.T) {
	backend, ctx, directory := localFilesFixture(t)
	location := filepath.Join(directory, "中文 空格\n名称.txt")
	created, err := backend.Put(ctx, location, strings.NewReader("初始值\n"), int64(len("初始值\n")), "")
	if err != nil || created.Kind != "file" || created.Mode != 0600 {
		t.Fatalf("未安全创建普通文件: %+v %v", created, err)
	}
	if _, err = backend.Put(ctx, location, strings.NewReader("不应覆盖"), int64(len("不应覆盖")), ""); !errors.Is(err, terminalfiles.ErrExists) {
		t.Fatalf("未确认覆盖被接受: %v", err)
	}
	if err := os.Chmod(location, 0640); err != nil {
		t.Fatal(err)
	}
	updated, err := backend.Put(ctx, location, strings.NewReader("已更新\n"), int64(len("已更新\n")), hostTestDigest("初始值\n"))
	if err != nil || updated.Mode != 0640 {
		t.Fatalf("更新未保留权限: %+v %v", updated, err)
	}
	reader, metadata, err := backend.Open(ctx, location)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || string(data) != "已更新\n" || metadata.Size != int64(len(data)) {
		t.Fatalf("读取内容错误: %q %+v %v", data, metadata, err)
	}
	entries, err := backend.List(ctx, directory)
	if err != nil || len(entries.Entries) != 1 || entries.Entries[0].Name != filepath.Base(location) || entries.Truncated {
		t.Fatalf("特殊名称或临时文件清理错误: %+v %v", entries, err)
	}
}

type hostMutationReader struct {
	input  io.Reader
	onRead func()
	once   sync.Once
}

func (r *hostMutationReader) Read(buffer []byte) (int, error) {
	r.once.Do(r.onRead)
	return r.input.Read(buffer)
}

func TestHostFilesLocalRechecksCASAfterTransfer(t *testing.T) {
	backend, ctx, directory := localFilesFixture(t)
	location := filepath.Join(directory, "cas.txt")
	if err := os.WriteFile(location, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	input := &hostMutationReader{input: strings.NewReader("candidate"), onRead: func() {
		if err := os.WriteFile(location, []byte("external"), 0600); err != nil {
			t.Error(err)
		}
	}}
	if _, err := backend.Put(ctx, location, input, 9, hostTestDigest("original")); !errors.Is(err, terminalfiles.ErrConflict) {
		t.Fatalf("传输中外部修改未被检测: %v", err)
	}
	data, _ := os.ReadFile(location)
	if string(data) != "external" {
		t.Fatalf("覆盖了并发修改: %q", data)
	}
	assertNoHostUploadTemps(t, directory)
}

func TestHostFilesLocalNewFileDoesNotReplaceConcurrentCreator(t *testing.T) {
	backend, ctx, directory := localFilesFixture(t)
	location := filepath.Join(directory, "new.txt")
	input := &hostMutationReader{input: strings.NewReader("candidate"), onRead: func() {
		if err := os.WriteFile(location, []byte("external"), 0600); err != nil {
			t.Error(err)
		}
	}}
	if _, err := backend.Put(ctx, location, input, 9, ""); !errors.Is(err, terminalfiles.ErrExists) {
		t.Fatalf("原子新建覆盖了并发文件: %v", err)
	}
	data, _ := os.ReadFile(location)
	if string(data) != "external" {
		t.Fatalf("新建覆盖现有文件: %q", data)
	}
	assertNoHostUploadTemps(t, directory)
}

func TestHostFilesLocalBoundedDirectoryAndUnsafeTargets(t *testing.T) {
	backend, ctx, directory := localFilesFixture(t)
	for index := 0; index < terminalfiles.MaxEntries+2; index++ {
		if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("entry-%04d", index)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := backend.List(ctx, directory)
	if err != nil || len(entries.Entries) != terminalfiles.MaxEntries || !entries.Truncated {
		t.Fatalf("目录条数未受限: %d %v %v", len(entries.Entries), entries.Truncated, err)
	}
	for _, invalid := range []string{"", "relative", "/tmp/../etc", "/tmp/invalid\x00name"} {
		if _, err := backend.List(ctx, invalid); !errors.Is(err, terminalfiles.ErrInvalidPath) {
			t.Fatalf("无效路径被接受: %q %v", invalid, err)
		}
	}
	link := filepath.Join(directory, "symbolic-link")
	if err := os.Symlink(filepath.Join(directory, "entry-0000"), link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := backend.Open(ctx, link); !errors.Is(err, terminalfiles.ErrNotRegular) {
		t.Fatalf("符号链接可作为普通文件读取: %v", err)
	}
	if _, err := backend.Put(ctx, link, strings.NewReader("overwrite"), 9, hostTestDigest("")); !errors.Is(err, terminalfiles.ErrNotRegular) {
		t.Fatalf("符号链接可作为普通文件覆盖: %v", err)
	}
	if _, _, err := backend.Open(ctx, directory); !errors.Is(err, terminalfiles.ErrNotRegular) {
		t.Fatalf("目录可作为文件下载: %v", err)
	}
}

func TestHostFilesLocalSizeFailureAndCancelPreserveOriginal(t *testing.T) {
	backend, ctx, directory := localFilesFixture(t)
	location := filepath.Join(directory, "original.txt")
	if err := os.WriteFile(location, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, size := range []int64{-1, terminalfiles.MaxTransferBytes + 1} {
		if _, err := backend.Put(ctx, location, strings.NewReader("candidate"), size, hostTestDigest("original")); !errors.Is(err, terminalfiles.ErrTooLarge) {
			t.Fatalf("无效大小仍可传输: %v", err)
		}
	}
	if _, err := backend.Put(ctx, location, strings.NewReader("short"), 10, hostTestDigest("original")); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("未完整收到文件仍提交: %v", err)
	}
	if _, err := backend.Put(ctx, location, strings.NewReader("too-long"), 1, hostTestDigest("original")); !errors.Is(err, terminalfiles.ErrTooLarge) {
		t.Fatalf("超量输入仍提交: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	input := &hostMutationReader{input: strings.NewReader("candidate"), onRead: cancel}
	if _, err := backend.Put(canceled, location, input, 9, hostTestDigest("original")); err == nil {
		t.Fatal("取消后仍提交文件")
	}
	data, _ := os.ReadFile(location)
	if string(data) != "original" {
		t.Fatalf("失败传输改写原文件: %q", data)
	}
	assertNoHostUploadTemps(t, directory)
}

type hostZeroReader struct{}

func (hostZeroReader) Read(buffer []byte) (int, error) {
	clear(buffer)
	return len(buffer), nil
}

func TestHostFilesLocalMaximumUploadIsStreamed(t *testing.T) {
	if testing.Short() {
		t.Skip("短测试跳过 64 MiB 流式传输")
	}
	backend, ctx, directory := localFilesFixture(t)
	location := filepath.Join(directory, "maximum.bin")
	entry, err := backend.Put(ctx, location, io.LimitReader(hostZeroReader{}, terminalfiles.MaxTransferBytes), terminalfiles.MaxTransferBytes, "")
	if err != nil || entry.Size != terminalfiles.MaxTransferBytes {
		t.Fatalf("最大文件未完成有界流式传输: %+v %v", entry, err)
	}
	reader, _, err := backend.Open(ctx, location)
	if err != nil {
		t.Fatal(err)
	}
	count, err := io.Copy(io.Discard, reader)
	_ = reader.Close()
	if err != nil || count != terminalfiles.MaxTransferBytes {
		t.Fatalf("最大文件下载错误: %d %v", count, err)
	}
}

func assertNoHostUploadTemps(t *testing.T, directory string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(directory, ".edo-upload-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("残留上传临时文件: %v %v", matches, err)
	}
}
