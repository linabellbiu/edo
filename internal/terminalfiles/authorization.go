package terminalfiles

import "context"

type commitAuthorizationKey struct{}

// WithCommitAuthorization 固定请求的授权检查，文件暂存后、原子提交前必须重新校验。
func WithCommitAuthorization(ctx context.Context, check func(context.Context) error) context.Context {
	return context.WithValue(ctx, commitAuthorizationKey{}, check)
}

// AuthorizeCommit 不把传输完成当作授权；取消和撤权后不得提交暂存文件。
func AuthorizeCommit(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if check, ok := ctx.Value(commitAuthorizationKey{}).(func(context.Context) error); ok && check != nil {
		if err := check(ctx); err != nil {
			return err
		}
	}
	return ctx.Err()
}
