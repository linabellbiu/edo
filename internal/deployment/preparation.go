package deployment

import (
	"context"

	"edo/internal/sshdeploy"
)

type preparedFilesKey struct{}

// 文件流只活在当前执行上下文，不进入发布记录或任务快照。
func WithPreparedFiles(ctx context.Context, files []sshdeploy.FileInput) context.Context {
	return context.WithValue(ctx, preparedFilesKey{}, files)
}
