package cicd

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"edo/internal/model"
)

var ErrInvalidCache = errors.New("依赖缓存配置无效，请选择未纳入代码管理的依赖目录和锁文件")

const maxCacheBytes int64 = 256 * 1024 * 1024

func ValidateCache(c *model.WorkflowCacheConfig) error {
	if c == nil {
		return nil
	}
	if c.Key == "" || len(c.Key) > 128 || len(c.Paths) == 0 || len(c.Paths) > 4 || len(c.LockFiles) == 0 || len(c.LockFiles) > 8 {
		return ErrInvalidCache
	}
	for _, name := range c.LockFiles {
		if !ValidRelativePath(name) || path.Clean(name) == "." {
			return ErrInvalidCache
		}
	}
	seen := map[string]bool{}
	for _, name := range c.Paths {
		if !ValidRelativePath(name) {
			return ErrInvalidCache
		}
		clean := path.Clean(name)
		allowed := false
		for _, part := range strings.Split(clean, "/") {
			if part == ".git" || part == ".secrets" || part == ".edo" {
				return ErrInvalidCache
			}
			if part == "node_modules" || part == ".cache" || part == ".gradle" || part == ".m2" {
				allowed = true
			}
		}
		if !allowed || seen[clean] {
			return ErrInvalidCache
		}
		for old := range seen {
			if strings.HasPrefix(clean, old+"/") || strings.HasPrefix(old, clean+"/") {
				return ErrInvalidCache
			}
		}
		seen[clean] = true
	}
	return nil
}
func CacheKey(ctx context.Context, projectID, repoID, platform, image, workspace string, c model.WorkflowCacheConfig) (string, error) {
	if err := ValidateCache(&c); err != nil {
		return "", err
	}
	parts := []string{"v1", projectID, repoID, platform, image, jsonValue(c.Paths), c.Key}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return "", err
	}
	defer root.Close()
	for _, name := range c.LockFiles {
		info, e := root.Lstat(name)
		if e != nil || !info.Mode().IsRegular() || info.Size() > 8*1024*1024 {
			return "", ErrInvalidCache
		}
		file, e := root.Open(name)
		if e != nil {
			return "", e
		}
		data, e := io.ReadAll(io.LimitReader(file, 8*1024*1024+1))
		_ = file.Close()
		if e != nil {
			return "", e
		}
		parts = append(parts, name, digest(string(data)))
	}
	for _, name := range c.Paths {
		if err := cachePathWithoutLinks(root, name); err != nil {
			return "", err
		}
	}
	// 工作区由固定 Commit 直接导出，没有 .git 元数据。源码中已经存在的
	// 目录不能被缓存覆盖；缓存只恢复到这次独立工作区中尚不存在的路径。
	for _, name := range c.Paths {
		if _, err := root.Lstat(path.Clean(name)); err == nil {
			return "", ErrInvalidCache
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}

	return digest(parts...), nil
}
func RestoreCache(directory, key, workspace string, c model.WorkflowCacheConfig) (bool, error) {
	file, err := os.Open(filepath.Join(directory, key+".tar"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() > maxCacheBytes || time.Since(info.ModTime()) > 7*24*time.Hour {
		return false, nil
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return false, err
	}
	defer root.Close()
	reader := tar.NewReader(io.LimitReader(file, maxCacheBytes+1))
	remaining := maxCacheBytes
	count := 0
	for {
		header, e := reader.Next()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return false, e
		}
		count++
		if count > 50000 || header.Size < 0 || header.Size > remaining || !ValidRelativePath(header.Name) {
			return false, ErrInvalidCache
		}
		name := path.Clean(header.Name)
		allowed := false
		for _, p := range c.Paths {
			p = path.Clean(p)
			if name == p || strings.HasPrefix(name, p+"/") {
				allowed = true
			}
		}
		if !allowed {
			return false, ErrInvalidCache
		}
		if err := cachePathWithoutLinks(root, name); err != nil {
			return false, err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if e = root.MkdirAll(name, 0700); e != nil {
				return false, e
			}
		case tar.TypeReg, tar.TypeRegA:
			if e = root.MkdirAll(path.Dir(name), 0700); e != nil {
				return false, e
			}
			if old, e := root.Lstat(name); e == nil && !old.Mode().IsRegular() {
				return false, ErrInvalidCache
			}
			destination, e := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
			if e != nil {
				return false, e
			}
			_, copyErr := io.CopyN(destination, reader, header.Size)
			closeErr := destination.Close()
			if copyErr != nil || closeErr != nil {
				return false, errors.Join(copyErr, closeErr)
			}
			remaining -= header.Size
		default:
			return false, ErrInvalidCache
		}
	}
	return true, nil
}
func SaveCache(directory, key string, paths map[string]string) error {
	if len(paths) == 0 {
		return nil
	}
	temporary, err := os.CreateTemp(directory, "pending-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	writer := tar.NewWriter(temporary)
	remaining := maxCacheBytes
	count := 0
	names := make([]string, 0, len(paths))
	for name := range paths {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		source := paths[name]
		err = filepath.WalkDir(source, func(file string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			count++
			if count > 50000 || entry.Type()&os.ModeSymlink != 0 {
				return ErrInvalidCache
			}
			info, e := entry.Info()
			if e != nil {
				return e
			}
			if !info.IsDir() && !info.Mode().IsRegular() {
				return ErrInvalidCache
			}
			relative, e := filepath.Rel(source, file)
			if e != nil {
				return e
			}
			header, e := tar.FileInfoHeader(info, "")
			if e != nil {
				return e
			}
			header.Name = path.Join(name, filepath.ToSlash(relative))
			header.Mode = 0600
			if info.IsDir() {
				header.Mode = 0700
			}
			if header.Size > remaining {
				return ErrInvalidCache
			}
			remaining -= header.Size
			if e = writer.WriteHeader(header); e != nil {
				return e
			}
			if info.IsDir() {
				return nil
			}
			input, e := os.Open(file)
			if e != nil {
				return e
			}
			_, e = io.Copy(writer, io.LimitReader(input, info.Size()+1))
			_ = input.Close()
			return e
		})
		if err != nil {
			break
		}
	}
	closeErr := writer.Close()
	fileErr := temporary.Close()
	if err != nil || closeErr != nil || fileErr != nil {
		return errors.Join(err, closeErr, fileErr)
	}
	if err = os.Rename(temporary.Name(), filepath.Join(directory, key+".tar")); err != nil {
		return err
	}
	return pruneCaches(directory)
}
func pruneCaches(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	type cached struct {
		name string
		info os.FileInfo
	}
	var files []cached
	var size int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tar") {
			continue
		}
		info, e := entry.Info()
		if e != nil {
			continue
		}
		files = append(files, cached{entry.Name(), info})
		size += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].info.ModTime().Before(files[j].info.ModTime()) })
	for _, file := range files {
		if size <= 2*1024*1024*1024 && time.Since(file.info.ModTime()) <= 7*24*time.Hour {
			continue
		}
		if e := os.Remove(filepath.Join(directory, file.name)); e == nil {
			size -= file.info.Size()
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	return nil
}

func cachePathWithoutLinks(root *os.Root, name string) error {
	for value := path.Clean(name); value != "."; value = path.Dir(value) {
		info, err := root.Lstat(value)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrInvalidCache
		}
	}
	return nil
}
