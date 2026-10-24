package dockerengine

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"path"
	"sort"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

const maximumReportBytes = 16 * 1024 * 1024

// 文件密钥放在源码之外，随匿名卷一起清理，环境变量仅保存容器内路径。
func scriptSecretArchive(files map[string]string) io.Reader {
	var output bytes.Buffer
	tw := tar.NewWriter(&output)
	_ = tw.WriteHeader(&tar.Header{Name: ".secrets", Mode: 0700, Typeflag: tar.TypeDir, Uid: 10001, Gid: 10001})
	keys := make([]string, 0, len(files))
	for name := range files {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		value := files[name]
		_ = tw.WriteHeader(&tar.Header{Name: ".secrets/" + name, Mode: 0400, Size: int64(len(value)), Typeflag: tar.TypeReg, Uid: 10001, Gid: 10001})
		_, _ = tw.Write([]byte(value))
	}
	_ = tw.Close()
	return &output
}

func copyScriptReports(ctx context.Context, api *client.Client, id string, paths []string) (map[string][]byte, error) {
	result := make(map[string][]byte, len(paths))
	remaining := int64(maximumReportBytes)
	for _, name := range paths {
		archive, err := api.CopyFromContainer(ctx, id, client.CopyFromContainerOptions{SourcePath: scriptWorkspace + "/src/" + path.Clean(name)})
		if errdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		reader := tar.NewReader(archive.Content)
		header, readErr := reader.Next()
		if readErr != nil || header.Size < 0 || header.Size > remaining || (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) {
			_ = archive.Content.Close()
			return nil, ErrScriptArtifact
		}
		value, readErr := io.ReadAll(io.LimitReader(reader, remaining+1))
		_ = archive.Content.Close()
		if readErr != nil || int64(len(value)) > remaining {
			return nil, errors.Join(ErrScriptArtifact, readErr)
		}
		remaining -= int64(len(value))
		result[name] = value
	}
	return result, nil
}
