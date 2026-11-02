package artifact

import "context"

type uploadAuthorizationKey struct{}

// WithUploadAuthorization 只约束当前手动上传；后台构建沿用自身的运行快照授权。
func WithUploadAuthorization(ctx context.Context, check func(context.Context) error) context.Context {
	return context.WithValue(ctx, uploadAuthorizationKey{}, check)
}

func authorizeUpload(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if check, ok := ctx.Value(uploadAuthorizationKey{}).(func(context.Context) error); ok && check != nil {
		if err := check(ctx); err != nil {
			return err
		}
	}
	return ctx.Err()
}
