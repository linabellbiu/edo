package artifact

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// OpenBundleMember 只从受控归档中读取一个普通文件，不按归档路径写文件系统。
func (s *Service) OpenBundleMember(ctx context.Context, id, member string) (io.ReadCloser, string, error) {
	stored, source, err := s.OpenDownload(ctx, id)
	if err != nil {
		return nil, "", err
	}
	defer source.Close()
	if stored.MediaType != "application/gzip" || !validBundleMember(member) {
		s.logger.Warn("制品成员参数无效", "operation", "artifact_member_validate", "artifact_id", id)
		return nil, "", ErrInvalidArtifact
	}
	lease, err := s.AcquireBuildDirectory("edo-artifact-member-")
	if err != nil {
		return nil, "", err
	}
	// 释放目录锁前保持文件可读；返回的 reader 负责关闭和清理。
	name := path.Join(lease.Directory, "member")
	file, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		lease.Release()
		return nil, "", err
	}
	digest := sha256.New()
	err = copyBundleMember(ctx, source, member, io.MultiWriter(file, digest), 512*1024*1024)
	if err != nil {
		file.Close()
		lease.Release()
		s.logger.Warn("读取制品成员失败", "operation", "artifact_member_read", "artifact_id", id, "err", err)
		return nil, "", ErrInvalidArtifact
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		lease.Release()
		return nil, "", err
	}
	return &leasedBundleMember{File: file, release: lease.Release}, "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

type leasedBundleMember struct {
	*os.File
	release func()
}

func (f *leasedBundleMember) Close() error { err := f.File.Close(); f.release(); return err }
func validBundleMember(name string) bool {
	return name != "" && name != "." && path.Clean(name) == name && !strings.HasPrefix(name, "/") && !strings.HasPrefix(name, "../") && !strings.ContainsAny(name, "\\\x00")
}
func copyBundleMember(ctx context.Context, source io.Reader, member string, destination io.Writer, limit int64) error {
	if !validBundleMember(member) {
		return ErrInvalidArtifact
	}
	zipped, err := gzip.NewReader(source)
	if err != nil {
		return err
	}
	defer zipped.Close()
	reader := tar.NewReader(io.LimitReader(zipped, limit+1))
	var total int64
	for count := 0; count < 100000; count++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if err != nil {
			return err
		}
		if header.Size < 0 || header.Size > limit-total || (!validBundleMember(strings.TrimSuffix(header.Name, "/")) && header.Name != "./") {
			return ErrInvalidArtifact
		}
		total += header.Size
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir {
			return ErrInvalidArtifact
		}
		if header.Name == member {
			if header.Typeflag != tar.TypeReg {
				return ErrInvalidArtifact
			}
			_, err = io.CopyN(destination, &contextReader{ctx: ctx, source: reader}, header.Size)
			return err
		}
	}
	return fmt.Errorf("%w：制品文件条数超限", ErrInvalidArtifact)
}

// ReadBundleText 供部署读取有限大小的配置正文，不回传归档内其他文件。
func (s *Service) ReadBundleText(ctx context.Context, id, output, member string, maximum int64) (string, error) {
	stored, source, err := s.OpenDownload(ctx, id)
	if err != nil {
		return "", err
	}
	defer source.Close()
	if stored.MediaType != "application/gzip" && output == "" {
		if !validBundleMember(member) {
			return "", ErrInvalidArtifact
		}
		body, err := io.ReadAll(io.LimitReader(source, maximum+1))
		if err != nil || int64(len(body)) > maximum {
			return "", ErrInvalidArtifact
		}
		return string(body), nil
	}
	var input io.Reader = source
	if output != "" {
		nested, _, err := s.OpenBundleMember(ctx, id, output+".tar.gz")
		if err != nil {
			return "", err
		}
		defer nested.Close()
		input = nested
	}
	var text strings.Builder
	if err := copyBundleMember(ctx, input, member, &limitedTextWriter{builder: &text, maximum: maximum}, 512*1024*1024); err != nil {
		s.logger.Warn("读取产物配置失败", "operation", "artifact_bundle_text", "artifact_id", id, "err", err)
		return "", ErrInvalidArtifact
	}
	return text.String(), nil
}

type limitedTextWriter struct {
	builder *strings.Builder
	maximum int64
}

func (w *limitedTextWriter) Write(p []byte) (int, error) {
	if int64(w.builder.Len()+len(p)) > w.maximum {
		return 0, ErrTooLarge
	}
	return w.builder.Write(p)
}
