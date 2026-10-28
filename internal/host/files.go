package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/pkg/sftp"

	"edo/internal/model"
	"edo/internal/terminalfiles"
)

type hostReadableFile interface {
	io.ReadCloser
	Stat() (fs.FileInfo, error)
}

type hostWritableFile interface {
	io.WriteCloser
	Chmod(fs.FileMode) error
	Sync() error
}

type hostFileSystem interface {
	Lstat(string) (fs.FileInfo, error)
	Open(string) (hostReadableFile, error)
	CreateExclusive(string) (hostWritableFile, error)
	ReadDir(string, int) ([]fs.FileInfo, bool, error)
	Commit(string, string, bool) error
	Remove(string) error
	SyncSupported() bool
}

type hostFileBackend struct {
	lease *hostInteractionLease
	fs    hostFileSystem
}

// 固定数量条带限制 EDO 内部同一路径并发覆盖，不保留无限增长的路径锁表。
var hostFileWriteLocks = func() [64]chan struct{} {
	var locks [64]chan struct{}
	for index := range locks {
		locks[index] = make(chan struct{}, 1)
	}
	return locks
}()

// OpenFiles 只访问已授权主机当前登录用户可见的文件，不使用 sudo 或递归扫描。
func (s *Service) OpenFiles(ctx context.Context, id string) (terminalfiles.Backend, error) {
	lease, err := s.newInteractionLease(ctx, id, hostFilesSlots)
	if err != nil {
		return nil, err
	}
	backend := &hostFileBackend{lease: lease}
	if lease.target.host.Mode == model.HostModeLocal {
		backend.fs = localHostFiles{}
	} else {
		client, connectErr := s.interactionSSH(lease.ctx, lease.target)
		if connectErr != nil {
			err = connectErr
		} else {
			lease.setCloser(client.Close)
			var files *sftp.Client
			var budget *hostSFTPReadBudget
			files, budget, err = newHostSFTPClient(client)
			if err == nil {
				backend.fs = &sftpHostFiles{client: files, budget: budget}
				lease.setCloser(func() error {
					_ = client.Close()
					_ = files.Close()
					return nil
				})
			}
		}
	}
	if err != nil {
		_ = lease.Close()
		slog.WarnContext(ctx, "打开主机文件连接失败", "operation", "host_files_open", "host_id", id, "err", err)
		return nil, terminalfiles.ErrUnavailable
	}
	if err = lease.check(ctx); err != nil {
		_ = lease.Close()
		return nil, err
	}
	return backend, nil
}

func (b *hostFileBackend) Close() error { return b.lease.Close() }

func hostFilePath(value string) (string, error) {
	if value == "" || len(value) > 4096 || !utf8.ValidString(value) || !strings.HasPrefix(value, "/") || strings.ContainsRune(value, 0) {
		return "", terminalfiles.ErrInvalidPath
	}
	// 明确拒绝遍历片段，不依赖浏览器清理路径。
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return "", terminalfiles.ErrInvalidPath
		}
	}
	return path.Clean(value), nil
}

func hostFileEntry(location string, info fs.FileInfo) terminalfiles.Entry {
	kind := "other"
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		kind = "symlink"
	case info.IsDir():
		kind = "directory"
	case info.Mode().IsRegular():
		kind = "file"
	}
	return terminalfiles.Entry{Name: info.Name(), Path: location, Kind: kind, Size: info.Size(), ModifiedAt: info.ModTime().UTC(), Mode: uint32(info.Mode().Perm())}
}

func (b *hostFileBackend) operation(ctx context.Context, operation string, run func(context.Context) error) error {
	if err := b.lease.check(ctx); err != nil {
		_ = b.Close()
		return err
	}
	// SFTP 无每个请求的 context；取消时关闭本次专用连接，中断阻塞读写。
	stop := context.AfterFunc(ctx, func() { _ = b.Close() })
	defer stop()
	err := run(ctx)
	if err == nil {
		err = b.lease.check(ctx)
	}
	if err != nil {
		slog.WarnContext(ctx, "主机文件操作失败", "operation", operation, "host_id", b.lease.target.host.ID, "err", err)
	}
	return err
}

func (b *hostFileBackend) List(ctx context.Context, location string) (terminalfiles.Directory, error) {
	location, err := hostFilePath(location)
	if err != nil {
		slog.WarnContext(ctx, "主机目录路径无效", "operation", "host_files_list_validate", "host_id", b.lease.target.host.ID, "err", err)
		return terminalfiles.Directory{}, err
	}
	result := terminalfiles.Directory{Path: location, Entries: []terminalfiles.Entry{}}
	err = b.operation(ctx, "host_files_list", func(context.Context) error {
		info, statErr := b.fs.Lstat(location)
		if statErr != nil {
			return statErr
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return terminalfiles.ErrInvalidPath
		}
		entries, truncated, readErr := b.fs.ReadDir(location, terminalfiles.MaxEntries)
		if readErr != nil {
			return readErr
		}
		result.Truncated = truncated
		for _, entry := range entries {
			if entry.Name() == "." || entry.Name() == ".." || strings.ContainsAny(entry.Name(), "/\x00") || !utf8.ValidString(entry.Name()) {
				continue
			}
			result.Entries = append(result.Entries, hostFileEntry(path.Join(location, entry.Name()), entry))
		}
		sort.Slice(result.Entries, func(i, j int) bool {
			if (result.Entries[i].Kind == "directory") != (result.Entries[j].Kind == "directory") {
				return result.Entries[i].Kind == "directory"
			}
			return result.Entries[i].Name < result.Entries[j].Name
		})
		return nil
	})
	return result, err
}

func (b *hostFileBackend) openRegular(location string) (hostReadableFile, fs.FileInfo, error) {
	before, err := b.fs.Lstat(location)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, nil, terminalfiles.ErrNotRegular
	}
	if before.Size() < 0 || before.Size() > terminalfiles.MaxTransferBytes {
		return nil, nil, terminalfiles.ErrTooLarge
	}
	file, err := b.fs.Open(location)
	if err != nil {
		return nil, nil, err
	}
	current, err := file.Stat()
	if err == nil && (!current.Mode().IsRegular() || current.Size() < 0 || current.Size() > terminalfiles.MaxTransferBytes) {
		err = terminalfiles.ErrNotRegular
	}
	if err == nil {
		after, statErr := b.fs.Lstat(location)
		if statErr != nil {
			err = statErr
		} else if !after.Mode().IsRegular() || before.Size() != current.Size() || !before.ModTime().Equal(current.ModTime()) || after.Size() != current.Size() || !after.ModTime().Equal(current.ModTime()) {
			err = terminalfiles.ErrConflict
		} else if _, local := b.fs.(localHostFiles); local && (!os.SameFile(before, current) || !os.SameFile(after, current)) {
			err = terminalfiles.ErrConflict
		}
	}
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, current, nil
}

type hostFileReadCloser struct {
	io.ReadCloser
	ctx   context.Context
	lease context.Context
	stop  func() bool
	left  int64
}

func (r *hostFileReadCloser) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if err := r.lease.Err(); err != nil {
		return 0, err
	}
	if r.left == 0 {
		var probe [1]byte
		count, err := r.ReadCloser.Read(probe[:])
		if count != 0 {
			return 0, terminalfiles.ErrTooLarge
		}
		return 0, err
	}
	if int64(len(buffer)) > r.left {
		buffer = buffer[:r.left]
	}
	count, err := r.ReadCloser.Read(buffer)
	r.left -= int64(count)
	return count, err
}
func (r *hostFileReadCloser) Close() error {
	if r.stop != nil {
		r.stop()
	}
	return r.ReadCloser.Close()
}

func (b *hostFileBackend) Open(ctx context.Context, location string) (io.ReadCloser, terminalfiles.Entry, error) {
	location, err := hostFilePath(location)
	if err != nil {
		slog.WarnContext(ctx, "主机文件路径无效", "operation", "host_files_read_validate", "host_id", b.lease.target.host.ID, "err", err)
		return nil, terminalfiles.Entry{}, err
	}
	var opened hostReadableFile
	var entry terminalfiles.Entry
	err = b.operation(ctx, "host_files_read", func(context.Context) error {
		var info fs.FileInfo
		var openErr error
		opened, info, openErr = b.openRegular(location)
		if openErr == nil {
			entry = hostFileEntry(location, info)
		}
		return openErr
	})
	if err != nil {
		if opened != nil {
			_ = opened.Close()
		}
		return nil, terminalfiles.Entry{}, err
	}
	result := &hostFileReadCloser{ReadCloser: opened, ctx: ctx, lease: b.lease.ctx, left: terminalfiles.MaxTransferBytes}
	stopRequest := context.AfterFunc(ctx, func() { _ = opened.Close() })
	stopLease := context.AfterFunc(b.lease.ctx, func() { _ = opened.Close() })
	result.stop = func() bool { a, c := stopRequest(), stopLease(); return a || c }
	return result, entry, nil
}

func (b *hostFileBackend) currentDigest(ctx context.Context, location string) (fs.FileInfo, string, error) {
	file, info, err := b.openRegular(location)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	digest := sha256.New()
	count, err := io.CopyBuffer(digest, &hostContextReader{ctx: ctx, input: io.LimitReader(file, terminalfiles.MaxTransferBytes+1)}, make([]byte, 32*1024))
	if err != nil {
		return nil, "", err
	}
	if count > terminalfiles.MaxTransferBytes {
		return nil, "", terminalfiles.ErrTooLarge
	}
	return info, hex.EncodeToString(digest.Sum(nil)), nil
}

type hostContextReader struct {
	ctx   context.Context
	input io.Reader
}

func (r *hostContextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.input.Read(buffer)
}

func (b *hostFileBackend) Put(ctx context.Context, location string, input io.Reader, size int64, expected string) (terminalfiles.Entry, error) {
	location, err := hostFilePath(location)
	if err != nil || location == "/" || input == nil || size < 0 || size > terminalfiles.MaxTransferBytes {
		if err == nil {
			err = terminalfiles.ErrInvalidPath
			if size < 0 || size > terminalfiles.MaxTransferBytes {
				err = terminalfiles.ErrTooLarge
			}
		}
		slog.WarnContext(ctx, "主机文件保存参数无效", "operation", "host_files_write_validate", "host_id", b.lease.target.host.ID, "err", err)
		return terminalfiles.Entry{}, err
	}
	if expected != "" {
		digest, decodeErr := hex.DecodeString(expected)
		if decodeErr != nil || len(digest) != sha256.Size || expected != strings.ToLower(expected) {
			slog.WarnContext(ctx, "主机文件版本无效", "operation", "host_files_write_etag", "host_id", b.lease.target.host.ID)
			return terminalfiles.Entry{}, terminalfiles.ErrConflict
		}
	}
	key := sha256.Sum256([]byte(b.lease.target.host.ID + "\x00" + location))
	lock := hostFileWriteLocks[int(key[0])%len(hostFileWriteLocks)]
	select {
	case lock <- struct{}{}:
		defer func() { <-lock }()
	case <-ctx.Done():
		return terminalfiles.Entry{}, ctx.Err()
	case <-b.lease.ctx.Done():
		return terminalfiles.Entry{}, b.lease.ctx.Err()
	}
	var result terminalfiles.Entry
	err = b.operation(ctx, "host_files_write", func(ctx context.Context) error {
		mode := fs.FileMode(0600)
		var original fs.FileInfo
		if expected == "" {
			if _, statErr := b.fs.Lstat(location); statErr == nil {
				return terminalfiles.ErrExists
			} else if !errors.Is(statErr, fs.ErrNotExist) {
				return statErr
			}
		} else {
			info, digest, statErr := b.currentDigest(ctx, location)
			if statErr != nil {
				return statErr
			}
			if digest != expected {
				return terminalfiles.ErrConflict
			}
			original, mode = info, info.Mode().Perm()
		}
		parent := path.Dir(location)
		if info, parentErr := b.fs.Lstat(parent); parentErr != nil {
			return parentErr
		} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return terminalfiles.ErrInvalidPath
		}
		temporary := path.Join(parent, ".edo-upload-"+uuid.NewString())
		file, createErr := b.fs.CreateExclusive(temporary)
		if createErr != nil {
			return createErr
		}
		defer b.removeTemporary(ctx, temporary)
		closed := false
		defer func() {
			if !closed {
				_ = file.Close()
			}
		}()
		if err := file.Chmod(0600); err != nil {
			return err
		}
		count, copyErr := io.CopyBuffer(file, &hostContextReader{ctx: ctx, input: io.LimitReader(input, size+1)}, make([]byte, 32*1024))
		if copyErr != nil {
			return copyErr
		}
		if count > size {
			return terminalfiles.ErrTooLarge
		}
		if count != size {
			return io.ErrUnexpectedEOF
		}
		if err := file.Chmod(mode); err != nil {
			return err
		}
		if b.fs.SyncSupported() {
			if err := file.Sync(); err != nil {
				return err
			}
		}
		if err := file.Close(); err != nil {
			closed = true
			return err
		}
		closed = true
		if err := b.lease.check(ctx); err != nil {
			return err
		}
		// 传输之后重新核对原文件，不能覆盖期间发生的外部修改。
		if expected != "" {
			info, digest, checkErr := b.currentDigest(ctx, location)
			if checkErr != nil {
				return checkErr
			}
			if digest != expected || info.Mode().Perm() != original.Mode().Perm() {
				return terminalfiles.ErrConflict
			}
		}
		if err := terminalfiles.AuthorizeCommit(ctx); err != nil {
			return err
		}
		if err := b.fs.Commit(temporary, location, expected != ""); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return terminalfiles.ErrExists
			}
			return err
		}
		info, statErr := b.fs.Lstat(location)
		if statErr != nil {
			return statErr
		}
		result = hostFileEntry(location, info)
		return nil
	})
	return result, err
}

func (b *hostFileBackend) removeTemporary(ctx context.Context, location string) {
	err := b.fs.Remove(location)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return
	}
	if b.lease.target.host.Mode == model.HostModeSSH {
		// 取消可能已经中断 SFTP；只在原身份仍有效时为本次随机临时文件
		// 进行一次有界清理，不向新配置的主机发送旧操作，也不扫描其他文件。
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		current, checkErr := b.lease.service.interactionTarget(cleanupCtx, b.lease.target.host.ID)
		if checkErr == nil && current.digest == b.lease.target.digest {
			select {
			case hostFilesSlots <- struct{}{}:
				defer func() { <-hostFilesSlots }()
				client, connectErr := b.lease.service.interactionSSH(cleanupCtx, b.lease.target)
				if connectErr == nil {
					defer client.Close()
					stop := context.AfterFunc(cleanupCtx, func() { _ = client.Close() })
					defer stop()
					files, budget, openErr := newHostSFTPClient(client)
					if openErr == nil {
						cleanup := &sftpHostFiles{client: files, budget: budget}
						err = cleanup.Remove(location)
						_ = client.Close()
						_ = files.Close()
						if err == nil || errors.Is(err, fs.ErrNotExist) {
							return
						}
					}
				}
			default:
			}
		}
	}
	slog.WarnContext(ctx, "清理本次文件传输临时文件失败", "operation", "host_files_temporary_cleanup", "host_id", b.lease.target.host.ID, "temporary_name", path.Base(location), "err", err)
}

type localHostFiles struct{}

func (localHostFiles) Lstat(location string) (fs.FileInfo, error) { return os.Lstat(location) }
func (localHostFiles) Open(location string) (hostReadableFile, error) {
	return os.OpenFile(location, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
func (localHostFiles) CreateExclusive(location string) (hostWritableFile, error) {
	return os.OpenFile(location, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
}
func (localHostFiles) Remove(location string) error {
	info, err := os.Lstat(location)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return terminalfiles.ErrNotRegular
	}
	return os.Remove(location)
}
func (localHostFiles) SyncSupported() bool { return true }
func (localHostFiles) Commit(source, destination string, overwrite bool) error {
	if overwrite {
		return os.Rename(source, destination)
	}
	// link 对不存在的目标原子建名，避免先检查再 rename 覆盖并发新建文件。
	return os.Link(source, destination)
}
func (localHostFiles) ReadDir(location string, maximum int) ([]fs.FileInfo, bool, error) {
	directory, err := os.OpenFile(location, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(maximum + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	truncated := len(entries) > maximum
	if truncated {
		entries = entries[:maximum]
	}
	result := make([]fs.FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, false, err
		}
		result = append(result, info)
	}
	return result, truncated, nil
}

type sftpHostFiles struct {
	client *sftp.Client
	budget *hostSFTPReadBudget
}

func (f *sftpHostFiles) Lstat(location string) (fs.FileInfo, error) { return f.client.Lstat(location) }
func (f *sftpHostFiles) Open(location string) (hostReadableFile, error) {
	return f.client.Open(location)
}
func (f *sftpHostFiles) CreateExclusive(location string) (hostWritableFile, error) {
	return f.client.OpenFile(location, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
}
func (f *sftpHostFiles) Remove(location string) error {
	info, err := f.client.Lstat(location)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return terminalfiles.ErrNotRegular
	}
	return f.client.Remove(location)
}
func (f *sftpHostFiles) SyncSupported() bool {
	_, supported := f.client.HasExtension("fsync@openssh.com")
	return supported
}
func (f *sftpHostFiles) Commit(source, destination string, overwrite bool) error {
	if !overwrite {
		if _, supported := f.client.HasExtension("hardlink@openssh.com"); !supported {
			return fmt.Errorf("SFTP 服务不支持原子新建: %w", terminalfiles.ErrUnavailable)
		}
		err := f.client.Link(source, destination)
		if err != nil {
			if _, statErr := f.client.Lstat(destination); statErr == nil {
				return terminalfiles.ErrExists
			}
		}
		return err
	}
	if _, supported := f.client.HasExtension("posix-rename@openssh.com"); !supported {
		return fmt.Errorf("SFTP 服务不支持原子覆盖: %w", terminalfiles.ErrUnavailable)
	}
	return f.client.PosixRename(source, destination)
}
func (f *sftpHostFiles) ReadDir(location string, maximum int) ([]fs.FileInfo, bool, error) {
	f.budget.start(256 * 1024)
	defer f.budget.stop()
	entries, err := f.client.ReadDirContext(context.Background(), location)
	if len(entries) > maximum {
		return entries[:maximum], true, nil
	}
	if f.budget.exhausted() {
		return nil, false, terminalfiles.ErrTooLarge
	}
	return entries, false, err
}
