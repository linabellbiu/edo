package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	ErrInvalidStore = errors.New("制品存储配置无效")
	ErrTooLarge     = errors.New("制品文件超过大小限制")
)

var storageKeyPattern = regexp.MustCompile(`^blobs/sha256/[0-9a-f]{2}/[0-9a-f]{64}$`)

type Blob struct {
	Digest     string
	StorageKey string
	SizeBytes  int64
	Created    bool
}

type LocalStore struct {
	root     string
	temp     string
	maxBytes int64
}

func NewLocalStore(directory string, maxBytes int64) (*LocalStore, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" || maxBytes < 1 {
		return nil, ErrInvalidStore
	}
	root, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("解析制品存储目录失败: %w", err)
	}
	temporary := filepath.Join(root, ".tmp")
	for _, path := range []string{temporary, filepath.Join(root, "blobs", "sha256")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, fmt.Errorf("创建制品存储目录失败: %w", err)
		}
	}
	return &LocalStore{root: root, temp: temporary, maxBytes: maxBytes}, nil
}

func (s *LocalStore) Root() string { return s.root }

func (s *LocalStore) MaxBytes() int64 { return s.maxBytes }

// CreateTempDirectory 为构建执行器提供位于制品卷内的临时工作区。
// 调用方完成后必须删除该目录。
func (s *LocalStore) CreateTempDirectory(prefix string) (string, error) {
	if s == nil || s.temp == "" {
		return "", ErrInvalidStore
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || strings.ContainsAny(prefix, `/\\\x00`) {
		prefix = "zrt-artifact-"
	}
	directory, err := os.MkdirTemp(s.temp, prefix)
	if err != nil {
		return "", fmt.Errorf("创建制品临时目录失败: %w", err)
	}
	return directory, nil
}

func (s *LocalStore) Put(ctx context.Context, source io.Reader) (Blob, error) {
	if s == nil || source == nil || s.maxBytes < 1 {
		return Blob{}, ErrInvalidStore
	}
	temporary, err := os.CreateTemp(s.temp, "upload-*.tmp")
	if err != nil {
		return Blob{}, fmt.Errorf("创建制品临时文件失败: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return Blob{}, fmt.Errorf("设置制品临时文件权限失败: %w", err)
	}

	digest := sha256.New()
	limited := &io.LimitedReader{R: &contextReader{ctx: ctx, source: source}, N: s.maxBytes + 1}
	size, err := io.CopyBuffer(io.MultiWriter(temporary, digest), limited, make([]byte, 128*1024))
	if err != nil {
		return Blob{}, fmt.Errorf("写入制品文件失败: %w", err)
	}
	if size > s.maxBytes {
		return Blob{}, ErrTooLarge
	}
	if err := temporary.Sync(); err != nil {
		return Blob{}, fmt.Errorf("同步制品文件失败: %w", err)
	}
	// 内容寻址对象创建后不应再被普通写入误改；在原子重命名前就收紧权限，
	// 避免进程崩溃留下短暂的可写正式对象。
	if err := temporary.Chmod(0o400); err != nil {
		return Blob{}, fmt.Errorf("锁定制品文件权限失败: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return Blob{}, fmt.Errorf("同步制品文件权限失败: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return Blob{}, fmt.Errorf("关闭制品临时文件失败: %w", err)
	}

	hexDigest := hex.EncodeToString(digest.Sum(nil))
	storageKey := filepath.ToSlash(filepath.Join("blobs", "sha256", hexDigest[:2], hexDigest))
	destination, err := s.resolve(storageKey)
	if err != nil {
		return Blob{}, err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return Blob{}, fmt.Errorf("创建制品内容目录失败: %w", err)
	}
	if existing, statErr := os.Stat(destination); statErr == nil {
		if !existing.Mode().IsRegular() || existing.Size() != size {
			return Blob{}, errors.New("制品内容存储发生摘要冲突")
		}
		matches, verifyErr := storedFileMatches(destination, hexDigest)
		if verifyErr != nil {
			return Blob{}, fmt.Errorf("校验已有制品内容失败: %w", verifyErr)
		}
		if !matches {
			return Blob{}, errors.New("制品内容存储发生摘要冲突")
		}
		if err := os.Chmod(destination, 0o400); err != nil {
			return Blob{}, fmt.Errorf("锁定已有制品文件权限失败: %w", err)
		}
		return Blob{Digest: "sha256:" + hexDigest, StorageKey: storageKey, SizeBytes: size}, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Blob{}, fmt.Errorf("检查制品内容是否存在失败: %w", statErr)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		// 同摘要并发写入时，另一条请求可能已经先完成原子重命名。
		if existing, statErr := os.Stat(destination); statErr == nil && existing.Mode().IsRegular() && existing.Size() == size {
			matches, verifyErr := storedFileMatches(destination, hexDigest)
			if verifyErr == nil && matches {
				return Blob{Digest: "sha256:" + hexDigest, StorageKey: storageKey, SizeBytes: size}, nil
			}
		}
		return Blob{}, fmt.Errorf("保存制品内容失败: %w", err)
	}
	removeTemporary = false
	if err := syncArtifactDirectory(filepath.Dir(destination)); err != nil {
		return Blob{}, fmt.Errorf("同步制品内容目录失败: %w", err)
	}
	if err := syncArtifactDirectory(filepath.Dir(filepath.Dir(destination))); err != nil {
		return Blob{}, fmt.Errorf("同步制品摘要目录失败: %w", err)
	}
	return Blob{
		Digest: "sha256:" + hexDigest, StorageKey: storageKey,
		SizeBytes: size, Created: true,
	}, nil
}

func storedFileMatches(path, expectedHexDigest string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.CopyBuffer(digest, file, make([]byte, 128*1024)); err != nil {
		return false, err
	}
	return hex.EncodeToString(digest.Sum(nil)) == expectedHexDigest, nil
}

func (s *LocalStore) Open(storageKey string) (*os.File, error) {
	path, err := s.resolve(storageKey)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开制品文件失败: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		if err != nil {
			return nil, fmt.Errorf("读取制品文件信息失败: %w", err)
		}
		return nil, errors.New("制品存储对象不是普通文件")
	}
	return file, nil
}

func (s *LocalStore) Remove(storageKey string) error {
	path, err := s.resolve(storageKey)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除制品内容失败: %w", err)
	}
	return nil
}

func (s *LocalStore) resolve(storageKey string) (string, error) {
	if s == nil || !storageKeyPattern.MatchString(storageKey) {
		return "", ErrInvalidStore
	}
	path := filepath.Join(s.root, filepath.FromSlash(storageKey))
	relative, err := filepath.Rel(s.root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", ErrInvalidStore
	}
	return path, nil
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.source.Read(buffer)
	}
}
